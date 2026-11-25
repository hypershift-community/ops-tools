package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSelectDeletable(t *testing.T) {
	for _, tc := range []struct {
		name       string
		change     func(current, receipt *InfraSet)
		noReceipt  bool
		bypass     bool
		wantCount  int
		wantReason string
	}{
		{name: "When the VPC and failed archived run match, it should allow deletion", wantCount: 1},
		{name: "When no report is provided, it should skip by default", noReceipt: true, wantReason: "no proof supplied"},
		{name: "When the operator explicitly disables proof, it should allow current LEAKED sets", noReceipt: true, bypass: true, wantCount: 1},
		{name: "When AWS now says the set is active, proof should not promote it", change: func(s, _ *InfraSet) { s.Verdict = VerdictActive }},
		{name: "When proof is disabled, it should still exclude uncertain resources", bypass: true, change: func(s, _ *InfraSet) { s.Verdict = VerdictUncertain }},
		{name: "When the owner differs, it should reject the receipt", change: func(s, _ *InfraSet) { s.VPCs[0].OwnerID = "different-owner" }, wantReason: "VPC IDs or owners differ"},
		{name: "When the receipt omits a VPC, it should reject the receipt", change: func(s, _ *InfraSet) { s.VPCs = append(s.VPCs, VPCInfo{VPCID: "vpc-789", OwnerID: "test-owner"}) }, wantReason: "VPC IDs or owners differ"},
		{name: "When the matching run succeeded, it should not allow deletion", change: func(_, r *InfraSet) { r.Prow.Result = "success" }, wantReason: "did not end in failure"},
		{name: "When the archive returned no proof, it should not allow deletion", change: func(_, r *InfraSet) { r.Prow.Status = "unavailable" }, wantReason: "no uniquely matched"},
		{name: "When the job ID does not hash to the infra ID, it should not use the receipt", change: func(_, r *InfraSet) { r.Prow.JobID = "other-job" }, wantReason: "does not substantiate"},
		{name: "When an artifact name is the only short-hash evidence, it should not allow deletion", change: func(_, r *InfraSet) { r.Prow.Resources = nil; r.Prow.ArtifactURL = "https://example.test/artifact" }, wantReason: "exact creation-log"},
		{name: "When a previously confirmed endpoint is gone, it should not count it as evidence", change: func(s, _ *InfraSet) { s.VPCs[0].EndpointIDs = nil }, wantReason: "exact creation-log"},
		{name: "When a matching tag identifies the failed run, it should not require short-hash corroboration", change: func(s, r *InfraSet) {
			s.VPCs[0].ProwJobID = fixtureJobID
			r.Prow.Method, r.Prow.MatchedHex, r.Prow.Resources = "tag", "", nil
		}, wantCount: 1},
		{name: "When a 20-character hash identifies the failed run, it should not require short-hash corroboration", change: func(s, r *InfraSet) {
			s.InfraID, r.InfraID = "8e0987463baead01aa68", "8e0987463baead01aa68"
			r.Prow.JobID, r.Prow.MatchedHex, r.Prow.Resources = "1e2793ca-0384-4fea-9a8d-d5e4d119384b", r.InfraID, nil
		}, wantCount: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			build := prowStorageURL + "/" + prowBucket + "/" + runPath(fixtureJob, fixtureBuild)
			current := InfraSet{InfraID: "autoscaling-c412bb98af", Verdict: VerdictLeaked,
				VPCs: []VPCInfo{{VPCID: "vpc-123", OwnerID: "test-owner", EndpointIDs: []string{"vpce-456"}}}}
			receipt := current
			receipt.VPCs = append([]VPCInfo{}, current.VPCs...)
			receipt.Prow = &ProwProof{
				Status: "matched", Result: "failure", Method: "sha256", MatchedHex: "c412bb98af", Job: fixtureJob, JobID: fixtureJobID,
				BuildID: fixtureBuild, CreatedAt: time.Unix(100, 0), MetadataURL: build + "prowjob.json",
				Resources: []ResourceEvidence{{ID: "vpce-456", URL: build + "artifacts/e2e/create/build-log.txt"}},
			}
			if tc.change != nil {
				tc.change(&current, &receipt)
			}
			proofs := map[string]InfraSet{}
			if !tc.noReceipt {
				proofs[receipt.InfraID] = receipt
			}
			sets := []InfraSet{current}
			selected, skipped := selectDeletable(sets, proofs, DefaultConfig().RequireProof && !tc.bypass)
			if len(selected) != tc.wantCount {
				t.Fatalf("eligible = %d, want %d; reason: %q", len(selected), tc.wantCount, sets[0].DeleteBlock)
			}
			if tc.wantReason == "" {
				if skipped != 0 || sets[0].DeleteBlock != "" {
					t.Fatalf("unexpected skip: %q", sets[0].DeleteBlock)
				}
			} else if skipped != 1 || !strings.Contains(sets[0].DeleteBlock, tc.wantReason) {
				t.Fatalf("skipped = %d, reason = %q; want one skip with %q", skipped, sets[0].DeleteBlock, tc.wantReason)
			}
		})
	}
}

func TestLoadDeleteProof(t *testing.T) {
	for _, tc := range []struct{ name, data, want string }{
		{"When the report is from a different region, it should reject it", `{"schemaVersion":1,"command":"prove","region":"us-east-2"}`, "does not match delete region"},
		{"When the input is a scan instead of a proof, it should reject it", `{"schemaVersion":1,"command":"scan","region":"us-east-1"}`, "expected a version 1 prove report"},
		{"When the report has duplicate rows, it should reject the ambiguity", `{"schemaVersion":1,"command":"prove","region":"us-east-1","infraSets":[{"InfraID":"a"},{"InfraID":"a"}]}`, "duplicate infra ID"},
		{"When the report cannot be decoded, it should fail closed", `{"schemaVersion":1`, "decode prove report"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "proofs.json")
			if err := os.WriteFile(filename, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadDeleteProof(filename, "us-east-1"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got error %v, want %q", err, tc.want)
			}
		})
	}
}

func TestVerifyDeleteTargets(t *testing.T) {
	t.Run("When a VPC target omits a sibling, it should refuse to delete shared resources", func(t *testing.T) {
		one, two := VPCInfo{VPCID: "vpc-123", OwnerID: "test-owner"}, VPCInfo{VPCID: "vpc-456", OwnerID: "test-owner"}
		all := map[string][]VPCInfo{"infra": {two, one}}
		if err := verifyDeleteTargets(map[string][]VPCInfo{"infra": {one}}, all); err == nil {
			t.Fatal("partial target should fail verification")
		}
		if err := verifyDeleteTargets(map[string][]VPCInfo{"infra": {one, two}}, all); err != nil {
			t.Fatal(err)
		}
	})
}
