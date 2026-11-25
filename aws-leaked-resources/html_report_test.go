package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteHTMLReport(t *testing.T) {
	archive := prowStorageURL + "/" + prowBucket + "/" + runPath(fixtureJob, fixtureBuild)
	deck := prowDeckURL + "/view/gs/" + prowBucket + "/" + strings.TrimSuffix(runPath(fixtureJob, fixtureBuild), "/")
	for _, tc := range []struct {
		name             string
		change           func(scan, proof *Report)
		include, exclude []string
		wantError        string
	}{
		{
			name: "When the inputs share a candidate, it should link AWS inventory and Prow evidence without trusting markup",
			include: []string{
				`--bg: #1a1b26`, `class="LEAKED">1</strong>`, `class="ACTIVE">0</strong>`,
				`href="https://console.aws.amazon.com/vpc/home?region=us-east-1#VpcDetails:VpcId=vpc-example"`,
				`#VpcEndpoints:VpcEndpointId=vpce-example`, `#ListRecordSets/ZEXAMPLE`, `#/roles/details/example-role`,
				`#/identity_providers/details/arn:aws:iam::000000000000:oidc-provider%2Fexample.test`,
				`href="` + deck + `"`, `href="` + archive + `prowjob.json"`,
				archive + `artifacts/infra.json`, archive + `artifacts/create/build-log.txt`,
				`&lt;script&gt;`, `Prow: matched / failure`,
			},
			exclude: []string{`<script>`, `javascript:`, `https://other.example/not-an-archive`},
		},
		{
			name: "When the proof has different VPCs, it should show a warning and not render its provenance",
			change: func(_, proof *Report) {
				proof.InfraSets[0].VPCs = []VPCInfo{{VPCID: "vpc-other", OwnerID: "000000000000"}}
			},
			include: []string{"VPC IDs or owners differ", "1 proof entries did not bind"},
			exclude: []string{deck, fixtureBuild},
		},
		{
			name:    "When a candidate has no proof entry, it should keep it in the report",
			change:  func(_, proof *Report) { proof.InfraSets = nil },
			include: []string{"No proof report entry", "vpc-example"},
			exclude: []string{deck},
		},
		{
			name:      "When the reports come from different regions, it should refuse to combine them",
			change:    func(_, proof *Report) { proof.Region = "us-east-2" },
			wantError: "does not match scan region",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set := InfraSet{
				InfraID: "autoscaling-example", Verdict: VerdictLeaked, VerdictReason: `<script>alert("invalid")</script>`,
				VPCs:          []VPCInfo{{VPCID: "vpc-example", EndpointIDs: []string{"vpce-example"}, OwnerID: "000000000000"}},
				HostedZones:   []ZoneInfo{{ZoneID: "/hostedzone/ZEXAMPLE"}},
				IAMRoles:      []string{"example-role"},
				OIDCProviders: []string{"arn:aws:iam::000000000000:oidc-provider/example.test"},
			}
			receipt := set
			receipt.ProwLink = deck
			receipt.Prow = &ProwProof{
				Status: "matched", Result: "failure", Job: fixtureJob, BuildID: fixtureBuild, JobID: fixtureJobID,
				MetadataURL: archive + "prowjob.json", ArtifactURL: archive + "artifacts/infra.json",
				Resources: []ResourceEvidence{
					{ID: "vpce-example", URL: archive + "artifacts/create/build-log.txt"},
					{ID: "invalid-url", URL: "javascript:alert(1)"},
					{ID: "other-host", URL: "https://other.example/not-an-archive"},
				},
			}
			scan := Report{Region: "us-east-1", InfraSets: []InfraSet{set}}
			proof := Report{Region: "us-east-1", InfraSets: []InfraSet{receipt}}
			if tc.change != nil {
				tc.change(&scan, &proof)
			}
			var buf bytes.Buffer
			err := writeHTMLReport(&buf, scan, proof)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || buf.Len() != 0 {
					t.Fatalf("wanted error %q and no HTML, got %v (%d bytes)", tc.wantError, err, buf.Len())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range tc.include {
				if !strings.Contains(buf.String(), value) {
					t.Errorf("report does not include %q", value)
				}
			}
			for _, value := range tc.exclude {
				if strings.Contains(buf.String(), value) {
					t.Errorf("report unexpectedly includes %q", value)
				}
			}
		})
	}
	t.Run("When the scan contains multiple verdicts, it should group them in dedicated views with Leaked first", func(t *testing.T) {
		groups := []struct {
			id      string
			verdict Verdict
		}{
			{"leaked", VerdictLeaked}, {"uncertain", VerdictUncertain}, {"active", VerdictActive},
			{"too-young", VerdictTooYoung}, {"protected", VerdictProtected},
		}
		scan, proof := Report{Region: "us-east-1"}, Report{Region: "us-east-1"}
		for i := len(groups) - 1; i >= 0; i-- {
			g := groups[i]
			scan.InfraSets = append(scan.InfraSets, InfraSet{InfraID: "set-" + g.id, Verdict: g.verdict})
		}
		var buf bytes.Buffer
		if err := writeHTMLReport(&buf, scan, proof); err != nil {
			t.Fatal(err)
		}
		html := buf.String()
		if strings.Index(html, `href="#leaked"`) >= strings.Index(html, `href="#uncertain"`) {
			t.Fatal("Leaked should be the first navigation item")
		}
		for i, g := range groups {
			if !strings.Contains(html, `<a href="#`+g.id+`">`) {
				t.Errorf("missing navigation item for %s", g.id)
			}
			class := "verdict-section"
			if i != 0 {
				class += " other-panel"
			}
			_, panel, ok := strings.Cut(html, `<section id="`+g.id+`" class="`+class+`"`)
			if !ok {
				t.Errorf("missing section for %s", g.id)
				continue
			}
			panel, _, _ = strings.Cut(panel, "</details>")
			if !strings.Contains(panel, "<code>set-"+g.id+"</code>") {
				t.Errorf("entry for %s is not in its own view", g.id)
			}
		}
	})
}

func TestRunHTMLReport(t *testing.T) {
	t.Run("When the output already exists, it should preserve the existing file", func(t *testing.T) {
		dir := t.TempDir()
		scan, proof := filepath.Join(dir, "scan.json"), filepath.Join(dir, "proof.json")
		for command, file := range map[string]string{"scan": scan, "prove": proof} {
			if err := os.WriteFile(file, []byte(`{"schemaVersion":1,"command":"`+command+`","region":"us-east-1"}`), 0600); err != nil {
				t.Fatal(err)
			}
		}
		var stdout bytes.Buffer
		if err := runHTMLReport(scan, proof, "-", &stdout); err != nil || !strings.Contains(stdout.String(), "No infrastructure sets") {
			t.Fatalf("expected empty HTML report on stdout; got %d bytes and %v", stdout.Len(), err)
		}
		if err := runHTMLReport(scan, proof, scan, &stdout); !errors.Is(err, os.ErrExist) {
			t.Fatalf("expected existing-file error, got %v", err)
		}
		if _, err := readReport(scan, "scan"); err != nil {
			t.Fatalf("scan was overwritten: %v", err)
		}
	})
}
