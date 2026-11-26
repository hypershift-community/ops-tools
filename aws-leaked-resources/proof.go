package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

// ProwProof records provenance, independently of the AWS verdict.
type ProwProof struct {
	Status       string             `json:"status"`
	Reason       string             `json:"reason,omitempty"`
	Method       string             `json:"method,omitempty"`
	Job          string             `json:"job,omitempty"`
	JobID        string             `json:"jobID,omitempty"`
	BuildID      string             `json:"buildID,omitempty"`
	Result       string             `json:"result,omitempty"`
	CreatedAt    time.Time          `json:"createdAt,omitzero"`
	MatchedHex   string             `json:"matchedHex,omitempty"`
	MetadataURL  string             `json:"metadataURL,omitempty"`
	ArtifactURL  string             `json:"artifactURL,omitempty"`
	ArtifactNote string             `json:"artifactNote,omitempty"`
	Resources    []ResourceEvidence `json:"resources,omitempty"`
}

// ResourceEvidence identifies an AWS resource in a creation log.
type ResourceEvidence struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

var (
	hex20       = regexp.MustCompile(`^[0-9a-f]{20}$`)
	variantHash = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*-([0-9a-f]{10})$`)
)

func infraHash(infraID string) string {
	if hex20.MatchString(infraID) {
		return infraID
	}
	if match := variantHash.FindStringSubmatch(infraID); match != nil {
		return match[1]
	}
	return ""
}

func prowHashMatches(token, id string) bool {
	if token == "" || id == "" {
		return false
	}
	sum := sha256.Sum256([]byte(id))
	return strings.HasPrefix(hex.EncodeToString(sum[:]), token)
}

func eligibleForProof(s InfraSet) bool {
	return s.Verdict == VerdictLeaked || s.Verdict == VerdictUncertain
}

func uniqueProwTag(set InfraSet) (string, bool) {
	var id string
	for _, vpc := range set.VPCs {
		if vpc.ProwJobID != "" {
			if id != "" && id != vpc.ProwJobID {
				return "", false
			}
			id = vpc.ProwJobID
		}
	}
	return id, true
}

func proofMethod(set InfraSet, jobName, jobID string) (string, string) {
	tag, consistent := uniqueProwTag(set)
	if !consistent || jobID == "" {
		return "", ""
	}
	if tag != "" {
		if tag == jobID {
			return "tag", ""
		}
		return "", ""
	}
	token := infraHash(set.InfraID)
	if len(token) == 10 && !strings.Contains(jobName, "-e2e-v2-aws") {
		return "", ""
	}
	if prowHashMatches(token, jobID) {
		return "sha256", token
	}
	return "", ""
}

func mergeProwWindows(windows []prowWindow) []prowWindow {
	slices.SortFunc(windows, func(a, b prowWindow) int { return a.from.Compare(b.from) })
	var merged []prowWindow
	for _, w := range windows {
		if len(merged) == 0 || w.from.After(merged[len(merged)-1].to) {
			merged = append(merged, w)
		} else if w.to.After(merged[len(merged)-1].to) {
			merged[len(merged)-1].to = w.to
		}
	}
	return merged
}

func proofCandidates(sets []InfraSet, target string, window time.Duration) ([]InfraSet, []prowWindow) {
	var candidates []InfraSet
	var windows []prowWindow
	for _, set := range sets {
		if !eligibleForProof(set) || (target != "" && target != set.InfraID) {
			continue
		}
		tag, consistent := uniqueProwTag(set)
		set.Prow = &ProwProof{Status: "unresolved"}
		switch {
		case !consistent:
			set.Prow.Status, set.Prow.Reason = "ambiguous", "conflicting Prow job tags"
		case tag == "" && infraHash(set.InfraID) == "":
			set.Prow.Reason = "no Prow tag or supported infra ID hash"
		case set.AnchorTime.IsZero():
			set.Prow.Reason = "no usable AWS timestamp"
		default:
			windows = append(windows, prowWindow{set.AnchorTime.Add(-window), set.AnchorTime.Add(window)})
		}
		candidates = append(candidates, set)
	}
	return candidates, mergeProwWindows(windows)
}

func (a prowArchive) prove(ctx context.Context, sets []InfraSet, target string, window time.Duration, verifyArtifacts bool) []InfraSet {
	candidates, windows := proofCandidates(sets, target, window)
	if len(windows) == 0 {
		return candidates
	}
	jobs, err := a.runs(ctx, windows)
	for i := range candidates {
		set := &candidates[i]
		if set.Prow.Reason != "" {
			continue
		}
		if err != nil {
			set.Prow = &ProwProof{Status: "unavailable", Reason: err.Error()}
			continue
		}
		a.matchRun(set, jobs, window)
	}
	if verifyArtifacts {
		// Artifact failures do not erase the job match. Missing corroboration is reported separately.
		for i := range candidates {
			if candidates[i].Prow.Status == "matched" {
				a.corroborate(ctx, &candidates[i])
			}
		}
	}
	return candidates
}

func (a prowArchive) matchRun(set *InfraSet, jobs []archivedProwJob, window time.Duration) {
	set.Prow.Reason = "no matching archived run in the search window"
	for _, job := range jobs {
		if job.Metadata.CreatedAt.Before(set.AnchorTime.Add(-window)) || job.Metadata.CreatedAt.After(set.AnchorTime.Add(window)) {
			continue
		}
		method, token := proofMethod(*set, job.Spec.Job, job.Metadata.Name)
		if method == "" {
			continue
		}
		if set.Prow.Status == "matched" {
			set.Prow = &ProwProof{Status: "ambiguous", Reason: "multiple archived runs match"}
			set.ProwLink = ProwSearchURL(set.TestType)
			return
		}
		path := runPath(job.Spec.Job, job.Status.BuildID)
		set.Prow = &ProwProof{
			Status: "matched", Method: method, MatchedHex: token, Job: job.Spec.Job, JobID: job.Metadata.Name,
			BuildID: job.Status.BuildID, Result: job.Status.State, CreatedAt: job.Metadata.CreatedAt,
			MetadataURL: a.objectURL(path + "prowjob.json"),
		}
		set.ProwLink = prowDeckURL + "/view/gs/" + prowBucket + "/" + strings.TrimSuffix(path, "/")
	}
}

func failedProwRun(state string) bool {
	return state == "failure" || state == "aborted" || state == "error"
}

func readReport(path, command string) (Report, error) {
	var report Report
	data, err := os.ReadFile(path)
	if err != nil {
		return report, err
	}
	if err := json.Unmarshal(data, &report); err != nil {
		return report, fmt.Errorf("decode %s report: %w", command, err)
	}
	if report.SchemaVersion != reportSchemaVersion || report.Command != command || report.Region == "" {
		return report, fmt.Errorf("expected a version %d %s report with an AWS region", reportSchemaVersion, command)
	}
	return report, nil
}
