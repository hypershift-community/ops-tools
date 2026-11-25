package main

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"
)

func sortSets(sets []InfraSet) {
	order := map[Verdict]int{
		VerdictProtected: 0,
		VerdictActive:    1,
		VerdictTooYoung:  2,
		VerdictUncertain: 3,
		VerdictLeaked:    4,
	}
	sort.Slice(sets, func(i, j int) bool {
		if order[sets[i].Verdict] != order[sets[j].Verdict] {
			return order[sets[i].Verdict] < order[sets[j].Verdict]
		}
		return sets[i].InfraID < sets[j].InfraID
	})
}

func PrintTable(w io.Writer, sets []InfraSet) {
	sortSets(sets)
	_, _ = fmt.Fprintf(w, "%-24s %-12s %-4s %-42s %s\n", "VERDICT", "AGE", "VPCs", "INFRA-ID", "REASON")
	_, _ = fmt.Fprintf(w, "%s\n", strings.Repeat("-", 120))

	for _, s := range sets {
		age := ""
		if s.Age > 0 {
			age = formatDuration(s.Age)
		}

		vpcCount := len(s.VPCs)
		_, _ = fmt.Fprintf(w, "%-24s %-12s %-4d %-42s %s\n",
			s.Verdict, age, vpcCount, s.InfraID, s.VerdictReason)

		if s.Verdict == VerdictLeaked || s.Verdict == VerdictUncertain {
			if s.TestType != "" {
				prowInfo := fmt.Sprintf("test=%s", s.TestType)
				if s.Namespace != "" {
					prowInfo += fmt.Sprintf("  ns=%s", s.Namespace)
				}
				if s.ProwLink != "" && (s.Prow == nil || s.Prow.Status != "matched") {
					prowInfo += fmt.Sprintf("  prow=%s", s.ProwLink)
				}
				_, _ = fmt.Fprintf(w, "  %-22s %s\n", "", prowInfo)
			}
			printProwProof(w, s, strings.Repeat(" ", 24))
			for _, vpc := range s.VPCs {
				subRes := formatSubResources(vpc)
				provenance := formatProvenance(vpc)
				_, _ = fmt.Fprintf(w, "  %-22s vpc: %-25s name: %-30s %s%s\n", "", vpc.VPCID, vpc.Name, subRes, provenance)
			}
			for _, z := range s.HostedZones {
				zType := "public"
				if z.Private {
					zType = "private"
				}
				_, _ = fmt.Fprintf(w, "  %-22s zone: %-25s %s (%s, %d records)\n", "", z.ZoneID, z.Name, zType, z.Records)
			}
			for _, oidc := range s.OIDCProviders {
				_, _ = fmt.Fprintf(w, "  %-22s oidc: %s\n", "", oidc)
			}
			for _, role := range s.IAMRoles {
				_, _ = fmt.Fprintf(w, "  %-22s role: %s\n", "", role)
			}
		}
	}
}

func printProwProof(w io.Writer, s InfraSet, indent string) {
	p := s.Prow
	if p == nil {
		return
	}
	if p.Status != "matched" {
		_, _ = fmt.Fprintf(w, "%sProw: %s — %s\n", indent, p.Status, p.Reason)
		return
	}
	_, _ = fmt.Fprintf(w, "%sProw: matched by %s — %s (build=%s, result=%s, created=%s)\n", indent, p.Method, p.Job, p.BuildID, p.Result, p.CreatedAt.Format(time.RFC3339))
	_, _ = fmt.Fprintf(w, "%s  PROW_JOB_ID=%s  %s\n", indent, p.JobID, s.ProwLink)
	if p.MatchedHex != "" {
		_, _ = fmt.Fprintf(w, "%s  sha256(PROW_JOB_ID)[:%d]=%s\n", indent, len(p.MatchedHex), p.MatchedHex)
	}
	if p.ArtifactURL != "" {
		_, _ = fmt.Fprintf(w, "%s  artifact name: %s\n", indent, p.ArtifactURL)
	}
	for _, r := range p.Resources {
		_, _ = fmt.Fprintf(w, "%s  confirmed %s in creation log: %s\n", indent, r.ID, r.URL)
	}
	if p.ArtifactNote != "" {
		_, _ = fmt.Fprintf(w, "%s  %s\n", indent, p.ArtifactNote)
	}
}

func PrintSummary(w io.Writer, sets []InfraSet) {
	counts := CountByVerdict(sets)
	totalVPCs := 0
	for _, s := range sets {
		totalVPCs += len(s.VPCs)
	}

	_, _ = fmt.Fprintf(w, "\n")
	_, _ = fmt.Fprintf(w, "==============================\n")
	_, _ = fmt.Fprintf(w, "  Summary\n")
	_, _ = fmt.Fprintf(w, "==============================\n")
	_, _ = fmt.Fprintf(w, "  Protected:   %d (%d VPCs)\n", counts[VerdictProtected], countVPCs(sets, VerdictProtected))
	_, _ = fmt.Fprintf(w, "  Active:      %d (%d VPCs)\n", counts[VerdictActive], countVPCs(sets, VerdictActive))
	_, _ = fmt.Fprintf(w, "  Too young:   %d (%d VPCs)\n", counts[VerdictTooYoung], countVPCs(sets, VerdictTooYoung))
	_, _ = fmt.Fprintf(w, "  Uncertain:   %d (%d VPCs)\n", counts[VerdictUncertain], countVPCs(sets, VerdictUncertain))
	_, _ = fmt.Fprintf(w, "  Leaked:      %d (%d VPCs)\n", counts[VerdictLeaked], countVPCs(sets, VerdictLeaked))
	_, _ = fmt.Fprintf(w, "  Total:       %d infra sets (%d VPCs)\n", len(sets), totalVPCs)
	var attempted, failedMatches int
	for _, s := range sets {
		if s.Prow != nil {
			attempted++
			if s.Prow.Status == "matched" && failedProwRun(s.Prow.Result) {
				failedMatches++
			}
		}
	}
	if attempted > 0 {
		_, _ = fmt.Fprintf(w, "  Prow:        %d/%d candidates matched to unsuccessful runs\n", failedMatches, attempted)
	}
	_, _ = fmt.Fprintf(w, "==============================\n")
}

func PrintJSON(w io.Writer, report Report) error {
	report.InfraSets = append([]InfraSet{}, report.InfraSets...)
	sortSets(report.InfraSets)
	for i := range report.InfraSets {
		set := &report.InfraSets[i]
		set.VPCs = slices.Clone(set.VPCs)
		sort.Slice(set.VPCs, func(i, j int) bool { return set.VPCs[i].VPCID < set.VPCs[j].VPCID })
		set.HostedZones = slices.Clone(set.HostedZones)
		sort.Slice(set.HostedZones, func(i, j int) bool { return set.HostedZones[i].ZoneID < set.HostedZones[j].ZoneID })
		set.IAMRoles = slices.Clone(set.IAMRoles)
		slices.Sort(set.IAMRoles)
		set.OIDCProviders = slices.Clone(set.OIDCProviders)
		slices.Sort(set.OIDCProviders)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func formatDuration(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	if hours < 48 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dd%dh", hours/24, hours%24)
}

func formatSubResources(vpc VPCInfo) string {
	parts := []string{}
	if vpc.Endpoints > 0 {
		parts = append(parts, fmt.Sprintf("endpoints=%d", vpc.Endpoints))
	}
	if vpc.NATGateways > 0 {
		parts = append(parts, fmt.Sprintf("nats=%d", vpc.NATGateways))
	}
	if vpc.IGWs > 0 {
		parts = append(parts, fmt.Sprintf("igws=%d", vpc.IGWs))
	}
	if vpc.Subnets > 0 {
		parts = append(parts, fmt.Sprintf("subnets=%d", vpc.Subnets))
	}
	if vpc.SecurityGroups > 0 {
		parts = append(parts, fmt.Sprintf("sgs=%d", vpc.SecurityGroups))
	}
	if vpc.RouteTables > 0 {
		parts = append(parts, fmt.Sprintf("rtbs=%d", vpc.RouteTables))
	}
	if vpc.ENIs > 0 {
		parts = append(parts, fmt.Sprintf("enis=%d", vpc.ENIs))
	}
	if vpc.ELBs > 0 {
		parts = append(parts, fmt.Sprintf("elbs=%d", vpc.ELBs))
	}
	if vpc.EIPs > 0 {
		parts = append(parts, fmt.Sprintf("eips=%d", vpc.EIPs))
	}
	if vpc.EndpointServices > 0 {
		parts = append(parts, fmt.Sprintf("vpce-svcs=%d", vpc.EndpointServices))
	}
	if len(parts) == 0 {
		return "(empty VPC)"
	}
	return strings.Join(parts, " ")
}

func formatProvenance(vpc VPCInfo) string {
	if vpc.Source == "" && vpc.ProwJobID == "" {
		return ""
	}
	parts := []string{}
	if vpc.Source != "" {
		parts = append(parts, "source="+vpc.Source)
	}
	if vpc.ProwJobID != "" {
		parts = append(parts, "prow="+vpc.ProwJobID)
	}
	if vpc.ClusterName != "" {
		parts = append(parts, "cluster="+vpc.ClusterName)
	}
	return " [" + strings.Join(parts, " ") + "]"
}
