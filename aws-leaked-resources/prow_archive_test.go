package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	fixtureJob   = "periodic-ci-openshift-hypershift-release-5.0-periodics-e2e-v2-aws"
	fixtureJobID = "3006ade8-89d1-4437-ad72-999e02cbb980"
	fixtureBuild = "2125492318067757056"
	fixtureTime  = "2026-11-25T07:30:26Z"
)

func TestProofMethod(t *testing.T) {
	for _, tc := range []struct {
		name, infra, id, tag, wantMethod, wantHex string
	}{
		{"When the infra ID is the CLI hash, it should match the full 20 characters", "8e0987463baead01aa68", "1e2793ca-0384-4fea-9a8d-d5e4d119384b", "", "sha256", "8e0987463baead01aa68"},
		{"When the infra ID is an E2E v2 variant, it should match the suffix", "public-rollouts-c412bb98af", fixtureJobID, "", "sha256", "c412bb98af"},
		{"When the string has a newline, it should not match the original job ID", "public-c412bb98af", fixtureJobID + "\n", "", "", ""},
		{"When a tag points to a different job, it should not fall back to the hash", "public-c412bb98af", fixtureJobID, "different-job", "", ""},
		{"When a tag matches, it should support a named infra ID", "node-pool-aaaaa", fixtureJobID, fixtureJobID, "tag", ""},
		{"When a prefix is only 16 hex characters, it should not count as the CLI hash", "8e0987463baead01", "1e2793ca-0384-4fea-9a8d-d5e4d119384b", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set := InfraSet{InfraID: tc.infra, VPCs: []VPCInfo{{ProwJobID: tc.tag}}}
			method, hash := proofMethod(set, fixtureJob, tc.id)
			if method != tc.wantMethod || hash != tc.wantHex {
				t.Fatalf("got method=%q hash=%q; want %q %q", method, hash, tc.wantMethod, tc.wantHex)
			}
		})
	}
}

func TestProwArchiveProve(t *testing.T) {
	anchor, err := time.Parse(time.RFC3339, fixtureTime)
	if err != nil {
		t.Fatal(err)
	}
	set := InfraSet{InfraID: "public-c412bb98af", Verdict: VerdictUncertain, AnchorTime: anchor,
		VPCs: []VPCInfo{{VPCID: "vpc-123", EndpointIDs: []string{"vpce-456"}}}}
	for _, tc := range []struct {
		name, result, wantStatus string
		forbidden, duplicate     bool
	}{
		{name: "When a failed job matches, it should cite the run and the exact AWS resource", result: "failure", wantStatus: "matched"},
		{name: "When a successful job matches, it should report its actual result", result: "success", wantStatus: "matched"},
		{name: "When two builds match, it should not pick one by result", result: "failure", duplicate: true, wantStatus: "ambiguous"},
		{name: "When the archive is inaccessible, it should report unavailable", forbidden: true, wantStatus: "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			build := runPath(fixtureJob, fixtureBuild)
			other := runPath(fixtureJob, "2125492318067757057")
			step := build + "artifacts/e2e/hypershift-aws-create-guests/"
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if tc.forbidden {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				if r.URL.Path == "/storage/v1/b/"+prowBucket+"/o" {
					var page string
					switch q.Get("prefix") {
					case prowJobPrefix:
						if q.Get("pageToken") == "" {
							page = `{"nextPageToken":"jobs-page-2"}`
						} else {
							page = fmt.Sprintf(`{"prefixes":[%q]}`, "logs/"+fixtureJob+"/")
						}
					case "logs/" + fixtureJob + "/":
						prefix := "logs/" + fixtureJob + "/"
						if q.Get("startOffset") != prefix+prowBuildOffset(anchor.Add(-time.Hour))+"/" || q.Get("endOffset") <= build {
							t.Error("missing build time bounds")
						}
						if tc.duplicate {
							page = fmt.Sprintf(`{"prefixes":[%q,%q]}`, build, other)
						} else {
							page = fmt.Sprintf(`{"prefixes":[%q,%q]}`, build, runPath(fixtureJob, "2125492318067757058"))
						}
					case build + "artifacts/":
						if q.Get("matchGlob") == "**/*"+set.InfraID+"*" {
							page = fmt.Sprintf(`{"items":[{"name":%q}]}`, build+"artifacts/e2e/"+set.InfraID+"/creation.log")
						} else {
							page = fmt.Sprintf(`{"prefixes":[%q]}`, build+"artifacts/e2e/")
						}
					case build + "artifacts/e2e/":
						page = fmt.Sprintf(`{"prefixes":[%q]}`, step)
					default:
						t.Errorf("unexpected list request: %s", r.URL)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					_, _ = fmt.Fprint(w, page)
					return
				}
				switch r.URL.Path {
				case "/" + prowBucket + "/" + build + "prowjob.json", "/" + prowBucket + "/" + other + "prowjob.json":
					pieces := strings.Split(r.URL.Path, "/")
					_, _ = fmt.Fprintf(w, `{"metadata":{"name":%q,"uid":"another-id","creationTimestamp":%q},"spec":{"job":%q},"status":{"build_id":%q,"state":%q}}`,
						fixtureJobID, fixtureTime, fixtureJob, pieces[len(pieces)-2], tc.result)
				case "/" + prowBucket + "/" + step + "build-log.txt":
					_, _ = fmt.Fprintln(w, `prefix {"msg":"Created s3 VPC endpoint","id":"vpce-456"}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer s.Close()
			a := prowArchive{baseURL: s.URL, client: s.Client()}
			nonCandidate := set
			nonCandidate.Verdict = VerdictActive
			result := a.prove(t.Context(), []InfraSet{set, nonCandidate}, "", time.Hour, true)
			if len(result) != 1 || result[0].Prow.Status != tc.wantStatus {
				data, _ := json.Marshal(result)
				t.Fatalf("want one candidate with status %q; got %s", tc.wantStatus, data)
			}
			if tc.wantStatus == "matched" {
				proof := result[0].Prow
				if proof.Result != tc.result || proof.BuildID != fixtureBuild || proof.ArtifactURL == "" ||
					len(proof.Resources) != 1 || proof.Resources[0].ID != "vpce-456" || !strings.Contains(result[0].ProwLink, fixtureJob+"/"+fixtureBuild) {
					t.Fatalf("unexpected proof: %+v, deck link: %s", proof, result[0].ProwLink)
				}
			}
		})
	}
	t.Run("When no set is eligible, it should complete without an archive client", func(t *testing.T) {
		set.Verdict = VerdictProtected
		if result := (prowArchive{}).prove(t.Context(), []InfraSet{set}, "", time.Hour, true); len(result) != 0 {
			t.Fatalf("unexpected candidate: %+v", result)
		}
	})
}

func TestProwBuildOffset(t *testing.T) {
	t.Run("When an archived build ID encodes a timestamp, it should fall inside the corresponding offsets", func(t *testing.T) {
		date, err := time.Parse(time.RFC3339, fixtureTime)
		if err != nil {
			t.Fatal(err)
		}
		if got := prowBuildOffset(date); got >= fixtureBuild || prowBuildOffset(date.Add(time.Minute)) <= fixtureBuild {
			t.Fatalf("archived build %s not bracketed by offsets around %v (start %s)", fixtureBuild, date, got)
		}
	})
}

func TestCreatedResources(t *testing.T) {
	t.Run("When logs mention AWS IDs, it should only corroborate exact creation events", func(t *testing.T) {
		input := strings.NewReader(`starting {"msg":"Created VPC","id":"vpc-123"}
{"msg":"Created s3 VPC endpoint","id":"vpce-456-extra"}
{"msg":"Deleted VPC","id":"vpc-456"}
{"msg":"endpoint created","endpointID":"vpce-789"}
{"msg":"endpoint created","endpointID":"vpce-789"}`)
		ids, err := createdResources(input, map[string]bool{"vpc-123": true, "vpc-456": true, "vpce-456": true, "vpce-789": true})
		if err != nil || strings.Join(ids, ",") != "vpc-123,vpce-789" {
			t.Fatalf("confirmed IDs = %v, err = %v", ids, err)
		}
	})
}
