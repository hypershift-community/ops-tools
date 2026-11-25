package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func TestDeleteReportsUnprovenLeaks(t *testing.T) {
	for _, tc := range []struct {
		name        string
		withProof   bool
		wantDeleted []string
	}{
		{name: "When every leak lacks proof, it should report the skipped leak and succeed"},
		{name: "When only one leak has proof, it should delete that leak and report the skipped one", withProof: true, wantDeleted: []string{"vpc-proven"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Confirm, cfg.OutputDir = true, t.TempDir()
			now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
			created := now.Add(-48 * time.Hour)
			ec2Client := &deleteProofEC2{vpcs: []ec2types.Vpc{makeVPCWithAge("vpc-unproven", "node-pool-unproven-vpc", "node-pool-unproven", created)}}
			if tc.withProof {
				vpc := makeVPCWithAge("vpc-proven", "node-pool-proven-vpc", "node-pool-proven", created)
				vpc.OwnerId = aws.String("test-owner")
				vpc.Tags = append(vpc.Tags, ec2types.Tag{Key: aws.String(TagHypershiftProwJobID), Value: aws.String(fixtureJobID)})
				ec2Client.vpcs = append(ec2Client.vpcs, vpc)
				cfg.ProofFile = filepath.Join(cfg.OutputDir, "proofs.json")
				receipt := InfraSet{InfraID: "node-pool-proven", VPCs: []VPCInfo{{VPCID: "vpc-proven", OwnerID: "test-owner"}}, Prow: &ProwProof{
					Status: "matched", Result: "failure", Method: "tag", Job: fixtureJob, JobID: fixtureJobID,
					BuildID: fixtureBuild, CreatedAt: created, MetadataURL: prowStorageURL + "/" + prowBucket + "/" + runPath(fixtureJob, fixtureBuild) + "prowjob.json",
				}}
				if err := saveFlowJSON(cfg.ProofFile, Report{SchemaVersion: reportSchemaVersion, Command: "prove", Region: cfg.Region, InfraSets: []InfraSet{receipt}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := runDelete(t.Context(), cfg, func(_ context.Context, scanCfg Config) (*Scanner, error) {
				return &Scanner{EC2: ec2Client, Route53: &mockRoute53{}, IAM: &mockIAM{}, S3: &mockS3{}, Config: scanCfg, Now: func() time.Time { return now }}, nil
			}); err != nil {
				t.Fatalf("expected skips to be informational, got %v", err)
			}
			if !slices.Equal(ec2Client.deletedVPCs, tc.wantDeleted) {
				t.Fatalf("deleted VPCs = %v, want %v", ec2Client.deletedVPCs, tc.wantDeleted)
			}
			files, err := filepath.Glob(filepath.Join(cfg.OutputDir, "delete-*.txt"))
			if err != nil || len(files) != 1 {
				t.Fatalf("expected one cleanup report, got %v: %v", files, err)
			}
			output, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			want := "Leaked infra sets not deleted (insufficient Prow proof): 1\n  - node-pool-unproven: no proof supplied for this infra set\n"
			if !strings.HasSuffix(string(output), want) {
				t.Fatalf("cleanup report must end with %q; got:\n%s", want, output)
			}
		})
	}
}

type deleteProofEC2 struct {
	trackingEC2
	vpcs []ec2types.Vpc
}

func (m *deleteProofEC2) DescribeVpcs(context.Context, *ec2.DescribeVpcsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	return &ec2.DescribeVpcsOutput{Vpcs: m.vpcs}, nil
}

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
