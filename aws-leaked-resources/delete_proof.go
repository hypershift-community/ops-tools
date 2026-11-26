package main

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var (
	periodicJobName = regexp.MustCompile(`^periodic-ci-openshift-hypershift-[a-z0-9][a-z0-9.-]*$`)
	prowBuildID     = regexp.MustCompile(`^[0-9]{19}$`)
)

func loadDeleteProof(filename, region string) (map[string]InfraSet, error) {
	if filename == "" {
		return nil, nil
	}
	report, err := readReport(filename, "prove")
	if err != nil {
		return nil, fmt.Errorf("read delete proof: %w", err)
	}
	if report.Region != region {
		return nil, fmt.Errorf("proof region %q does not match delete region %q", report.Region, region)
	}
	return indexProofs(report)
}

func indexProofs(report Report) (map[string]InfraSet, error) {
	index := make(map[string]InfraSet, len(report.InfraSets))
	for _, set := range report.InfraSets {
		if _, exists := index[set.InfraID]; set.InfraID == "" || exists {
			return nil, fmt.Errorf("missing or duplicate infra ID %q in proof report", set.InfraID)
		}
		index[set.InfraID] = set
	}
	return index, nil
}

// selectDeletable never promotes a verdict from the new AWS scan. It only
// removes LEAKED sets lacking proof, even in dry-run and interactive modes.
func selectDeletable(sets []InfraSet, proofs map[string]InfraSet, requireProof bool) (selected []InfraSet, skipped int) {
	for i := range sets {
		set := &sets[i]
		if !eligibleForProof(*set) {
			continue
		}
		receipt, exists := proofs[set.InfraID]
		bound := exists && sameVPCs(set.VPCs, receipt.VPCs)
		if bound {
			set.Prow, set.ProwLink = receipt.Prow, receipt.ProwLink
		}
		if set.Verdict != VerdictLeaked {
			continue
		}
		if requireProof {
			if !exists {
				set.DeleteBlock = "no proof supplied for this infra set"
			} else if !bound {
				set.DeleteBlock = "VPC IDs or owners differ from the proof report"
			} else {
				set.DeleteBlock = proofBlockReason(*set)
			}
		}
		if set.DeleteBlock != "" {
			skipped++
			continue
		}
		selected = append(selected, *set)
	}
	return selected, skipped
}

func sameVPCs(current, snapshot []VPCInfo) bool {
	if len(current) == 0 || len(current) != len(snapshot) {
		return false
	}
	owners := make(map[string]string, len(snapshot))
	for _, vpc := range snapshot {
		if vpc.VPCID == "" || vpc.OwnerID == "" || owners[vpc.VPCID] != "" {
			return false
		}
		owners[vpc.VPCID] = vpc.OwnerID
	}
	for _, vpc := range current {
		if vpc.OwnerID == "" || owners[vpc.VPCID] != vpc.OwnerID {
			return false
		}
		delete(owners, vpc.VPCID)
	}
	return true
}

func proofBlockReason(set InfraSet) string {
	p := set.Prow
	if p == nil || p.Status != "matched" {
		return "no uniquely matched archived Prow run"
	}
	if !failedProwRun(p.Result) {
		return "matched Prow run did not end in failure, error, or abort"
	}
	method, token := proofMethod(set, p.Job, p.JobID)
	build := prowStorageURL + "/" + prowBucket + "/" + runPath(p.Job, p.BuildID)
	if !periodicJobName.MatchString(p.Job) || !prowBuildID.MatchString(p.BuildID) || p.CreatedAt.IsZero() ||
		p.MetadataURL != build+"prowjob.json" || method == "" || method != p.Method || token != p.MatchedHex {
		return "archived run does not substantiate the current infra ID or Prow tag"
	}
	if len(token) == 10 {
		for _, vpc := range set.VPCs {
			found := slices.ContainsFunc(p.Resources, func(resource ResourceEvidence) bool {
				return strings.HasPrefix(resource.URL, build) && strings.HasSuffix(resource.URL, "/build-log.txt") &&
					(resource.ID == vpc.VPCID || slices.Contains(vpc.EndpointIDs, resource.ID))
			})
			if !found {
				return "10-character hash needs an exact creation-log resource match for each VPC"
			}
		}
	}
	return ""
}
