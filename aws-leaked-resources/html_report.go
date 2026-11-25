package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
)

//go:embed report.html.tmpl
var htmlReportSource string

var htmlReportTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"awsURL": awsConsoleURL,
	"count":  func(totals map[Verdict]int, verdict string) int { return totals[Verdict(verdict)] },
	"deck":   func(raw string) string { return httpsURL(raw, "prow.ci.openshift.org") },
	"archive": func(raw string) string {
		if strings.HasPrefix(raw, prowStorageURL+"/"+prowBucket+"/logs/") {
			return httpsURL(raw, "storage.googleapis.com")
		}
		return ""
	},
	"stamp": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return t.UTC().Format("2006-01-02 15:04:05 UTC")
	},
}).Parse(htmlReportSource))

type htmlInventoryRow struct {
	InfraSet
	ProofNote string
}

type htmlVerdictSection struct {
	ID, Title, Region string
	Rows              []htmlInventoryRow
}

func writeHTMLReport(w io.Writer, scan, proof Report) error {
	if scan.Region != proof.Region {
		return fmt.Errorf("proof region %q does not match scan region %q", proof.Region, scan.Region)
	}
	index, err := indexProofs(proof)
	if err != nil {
		return err
	}
	data := struct {
		Scan, Proof Report
		Leaked      htmlVerdictSection
		Other       []htmlVerdictSection
		Totals      map[Verdict]int
		Matched     int
		Ignored     int
	}{Scan: scan, Proof: proof, Totals: CountByVerdict(scan.InfraSets)}
	sets := append([]InfraSet(nil), scan.InfraSets...)
	sortSets(sets)
	rows := make(map[Verdict][]htmlInventoryRow)
	for _, set := range sets {
		// The scan is the source of AWS inventory; only the proof may supply evidence.
		set.Prow = nil
		row := htmlInventoryRow{InfraSet: set}
		receipt, exists := index[set.InfraID]
		if eligibleForProof(set) {
			switch {
			case !exists:
				row.ProofNote = "No proof report entry for this candidate."
			case !sameVPCs(set.VPCs, receipt.VPCs):
				row.ProofNote = "Proof ignored: VPC IDs or owners differ from this scan."
			default:
				delete(index, set.InfraID)
				row.Prow = receipt.Prow
				if row.Prow != nil && row.Prow.Status == "matched" {
					row.ProwLink = receipt.ProwLink
					data.Matched++
				} else if row.Prow == nil {
					row.ProofNote = "No evidence recorded for this candidate."
				}
			}
		}
		rows[set.Verdict] = append(rows[set.Verdict], row)
	}
	data.Ignored = len(index)
	data.Leaked = htmlVerdictSection{"leaked", "Leaked", scan.Region, rows[VerdictLeaked]}
	data.Other = []htmlVerdictSection{
		{"uncertain", "Uncertain", scan.Region, rows[VerdictUncertain]},
		{"active", "Active", scan.Region, rows[VerdictActive]},
		{"too-young", "Too young", scan.Region, rows[VerdictTooYoung]},
		{"protected", "Protected", scan.Region, rows[VerdictProtected]},
	}
	return htmlReportTemplate.Execute(w, data)
}

func httpsURL(raw, host string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != host || u.User != nil {
		return ""
	}
	return raw
}

func awsConsoleURL(region, kind, id string) string {
	if id == "" {
		return ""
	}
	const console = "https://console.aws.amazon.com"
	vpcConsole := console + "/vpc/home?region=" + url.QueryEscape(region)
	switch kind {
	case "vpc":
		return vpcConsole + "#VpcDetails:VpcId=" + url.QueryEscape(id)
	case "endpoint":
		return vpcConsole + "#VpcEndpoints:VpcEndpointId=" + url.QueryEscape(id)
	case "zone":
		return console + "/route53/v2/hostedzones#ListRecordSets/" + url.PathEscape(strings.TrimPrefix(id, "/hostedzone/"))
	case "role":
		return console + "/iam/home#/roles/details/" + url.PathEscape(id)
	case "oidc":
		return console + "/iam/home#/identity_providers/details/" + url.PathEscape(id)
	}
	return ""
}

func runHTMLReport(scanPath, proofPath, output string, stdout io.Writer) error {
	scan, err := readReport(scanPath, "scan")
	if err != nil {
		return fmt.Errorf("read scan report: %w", err)
	}
	proof, err := readReport(proofPath, "prove")
	if err != nil {
		return fmt.Errorf("read proof report: %w", err)
	}
	if output == "-" {
		return writeHTMLReport(stdout, scan, proof)
	}
	return writeHTMLFile(output, scan, proof)
}

func writeHTMLFile(output string, scan, proof Report) error {
	var html bytes.Buffer
	if err := writeHTMLReport(&html, scan, proof); err != nil {
		return err
	}
	f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create HTML report: %w", err)
	}
	if _, err := html.WriteTo(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("write HTML report: %w", err)
	}
	return f.Close()
}
