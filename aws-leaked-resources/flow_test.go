package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunFlow(t *testing.T) {
	for _, tc := range []struct {
		name       string
		proven     bool
		proveFails bool
		verbose    bool
	}{
		{name: "When proof is sufficient, it should save all reports and suggest scoped interactive cleanup", proven: true},
		{name: "When verbose is requested and no candidate is proven, it should show progress without suggesting cleanup", verbose: true},
		{name: "When proof fails, it should retain the scan without suggesting cleanup", proveFails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := flowOptions{scan: DefaultConfig(), proof: defaultProofOptions(""), verbose: tc.verbose}
			cfg.scan.OutputDir = filepath.Join(t.TempDir(), "reports with spaces")
			cfg.scan.Region, cfg.scan.Target, cfg.scan.MinAge = "us-west-2", "test-infra", 48*time.Hour
			set := InfraSet{InfraID: cfg.scan.Target, Verdict: VerdictLeaked, VPCs: []VPCInfo{
				{VPCID: "vpc-example", OwnerID: "test-owner", ProwJobID: fixtureJobID},
			}}
			testErr := errors.New("test archive failure")
			var steps []string
			scan := func(_ context.Context, cfg Config, progress io.Writer) (Report, error) {
				steps = append(steps, "scan")
				if cfg.Delete || cfg.Region != "us-west-2" || cfg.Target != set.InfraID || cfg.MinAge != 48*time.Hour {
					t.Fatalf("wrong scan settings: %+v", cfg)
				}
				if _, err := io.WriteString(progress, "Detailed AWS scan log\n"); err != nil {
					t.Fatal(err)
				}
				return Report{SchemaVersion: 1, Command: "scan", Region: cfg.Region, InfraSets: []InfraSet{set}}, nil
			}
			prove := func(_ context.Context, scan Report, _ proofOptions) (Report, error) {
				steps = append(steps, "prove")
				if len(scan.InfraSets) != 1 || scan.InfraSets[0].InfraID != set.InfraID || scan.Command != "scan" {
					t.Fatalf("prove did not receive the scan: %+v", scan)
				}
				if tc.proveFails {
					return Report{}, testErr
				}
				receipt := scan.InfraSets[0]
				receipt.Prow = &ProwProof{Status: "unresolved"}
				if tc.proven {
					receipt.Prow = &ProwProof{Status: "matched", Result: "failure", Method: "tag", CreatedAt: time.Unix(1, 0),
						Job: fixtureJob, JobID: fixtureJobID, BuildID: fixtureBuild,
						MetadataURL: prowStorageURL + "/" + prowBucket + "/" + runPath(fixtureJob, fixtureBuild) + "prowjob.json"}
				}
				scan.Command, scan.InfraSets = "prove", []InfraSet{receipt}
				return scan, nil
			}
			var output bytes.Buffer
			err := runFlow(t.Context(), cfg, []string{"/test bin/cleanleaked"}, &output, scan, prove)
			if got := strings.Join(steps, ","); got != "scan,prove" {
				t.Fatalf("unexpected steps: %s", got)
			}
			runs, readErr := os.ReadDir(cfg.scan.OutputDir)
			if readErr != nil || len(runs) != 1 || !runs[0].IsDir() {
				t.Fatalf("expected one run directory; got %v: %v", runs, readErr)
			}
			dir := filepath.Join(cfg.scan.OutputDir, runs[0].Name())
			if _, readErr := readReport(filepath.Join(dir, "scan.json"), "scan"); readErr != nil {
				t.Fatal(readErr)
			}
			htmlPath := filepath.Join(dir, "report.html")
			if tc.proveFails {
				if !errors.Is(err, testErr) || strings.Contains(output.String(), "Next step") || strings.Contains(output.String(), "[3/3]") {
					t.Fatalf("expected proof error and no cleanup suggestion; got %v: %s", err, output.String())
				}
				if _, statErr := os.Stat(htmlPath); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("expected no completed HTML report, got %v", statErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			proofPath := filepath.Join(dir, "proofs.json")
			if _, readErr := readReport(proofPath, "prove"); readErr != nil {
				t.Fatal(readErr)
			}
			if html, readErr := os.ReadFile(htmlPath); readErr != nil || !bytes.Contains(html, []byte("Leak investigation report")) {
				t.Fatalf("expected an HTML report: %v", readErr)
			}
			message := output.String()
			if !strings.Contains(message, "HTML report") || !strings.Contains(message, htmlPath) {
				t.Fatalf("report path not printed: %s", message)
			}
			if !strings.Contains(message, "[1/3] Scan AWS inventory") || !strings.Contains(message, "[2/3] Look up Prow evidence") ||
				!strings.Contains(message, "[3/3] Generate HTML report") || strings.Contains(message, "Detailed AWS scan log") != tc.verbose {
				t.Fatalf("wrong scan progress: %s", message)
			}
			if tc.proven {
				want := "'/test bin/cleanleaked' delete --interactive --require-proof=true --region us-west-2 --min-age 48h0m0s --proof '" + proofPath + "' --output-dir '" + dir + "' --target test-infra"
				continuation := " \\\n    "
				if !strings.Contains(strings.ReplaceAll(message, continuation, " "), want) || !strings.Contains(message, continuation+"--proof ") || strings.Contains(message, "--confirm") {
					t.Fatalf("wrong cleanup suggestion: %s", message)
				}
			} else if !strings.Contains(message, "No cleanup command is suggested") || strings.Contains(message, "--interactive") {
				t.Fatalf("unexpected cleanup suggestion: %s", message)
			}
		})
	}
}

func TestShortFlowPath(t *testing.T) {
	t.Run("When the report is inside the working directory, it should print a relative path", func(t *testing.T) {
		dir, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join("output", "flow-example", "report.html")
		if got := shortFlowPath(filepath.Join(dir, path)); got != path {
			t.Fatalf("got %q, want %q", got, path)
		}
		outside := filepath.Join(filepath.Dir(dir), "outside-report.html")
		if got := shortFlowPath(outside); got != outside {
			t.Fatalf("path outside the working directory should stay absolute, got %q", got)
		}
	})
}

func TestShellCommand(t *testing.T) {
	t.Run("When paths contain shell metacharacters, it should quote them as literal arguments", func(t *testing.T) {
		if got, want := shellCommand([]string{"cleanleaked", "a's $file", ""}), `cleanleaked 'a'\''s $file' ''`; got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}
