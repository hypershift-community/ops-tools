package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSetAnchorTime(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		vpcs   []VPCInfo
		want   time.Time
		source string
	}{
		{"When creation times are known, it should use the oldest one instead of an expiry estimate", []VPCInfo{{CreatedAt: now.Add(time.Hour)}, {CreatedAt: now, ExpirationDate: now}}, now, "vpc"},
		{"When only expiry is known, it should estimate the creation time", []VPCInfo{{ExpirationDate: now.Add(4 * time.Hour)}}, now, "expirationDate"},
		{"When there are no timestamps, it should omit the anchor", []VPCInfo{{}}, time.Time{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set := InfraSet{VPCs: tc.vpcs}
			setAnchorTime(&set)
			if !set.AnchorTime.Equal(tc.want) || set.AnchorSource != tc.source {
				t.Fatalf("anchor = %v (%s), want %v (%s)", set.AnchorTime, set.AnchorSource, tc.want, tc.source)
			}
		})
	}
}

func TestPrintJSON(t *testing.T) {
	t.Run("When the inventory is unsorted, it should emit a versioned report in stable order", func(t *testing.T) {
		report := Report{SchemaVersion: 1, Command: "scan", Region: "us-east-1", InfraSets: []InfraSet{
			{InfraID: "z", Verdict: VerdictLeaked, VPCs: []VPCInfo{{VPCID: "vpc-b"}, {VPCID: "vpc-a"}}},
			{InfraID: "a", Verdict: VerdictLeaked}, {InfraID: "l", Verdict: VerdictActive},
		}}
		var buf bytes.Buffer
		if err := PrintJSON(&buf, report); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(buf.String(), "anchorTime") {
			t.Fatal("unknown anchor time should be omitted")
		}
		var got Report
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.SchemaVersion != 1 || got.Region != report.Region || len(got.InfraSets) != 3 {
			t.Fatalf("unexpected report: %+v", got)
		}
		if got.InfraSets[0].InfraID != "l" || got.InfraSets[1].InfraID != "a" || got.InfraSets[2].VPCs[0].VPCID != "vpc-a" {
			t.Fatalf("inventory is not sorted: %+v", got.InfraSets)
		}
	})
}
