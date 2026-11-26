package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"path"
	"slices"
	"strings"
)

func (a prowArchive) corroborate(ctx context.Context, set *InfraSet) {
	build := runPath(set.Prow.Job, set.Prow.BuildID)
	var notes []string
	artifact, err := a.findArtifact(ctx, build, set.InfraID)
	if err != nil {
		notes = append(notes, "artifact listing incomplete")
	} else if artifact != "" {
		set.Prow.ArtifactURL = a.objectURL(artifact)
	}
	ids := map[string]bool{}
	for _, vpc := range set.VPCs {
		ids[vpc.VPCID] = true
		for _, id := range vpc.EndpointIDs {
			ids[id] = true
		}
	}
	logs, err := a.creationLogs(ctx, build)
	if err != nil {
		notes = append(notes, "creation log listing incomplete")
	}
	logs = append(logs, build+"build-log.txt")
	for _, object := range logs {
		res, err := a.get(ctx, a.objectURL(object))
		if errors.Is(err, errProwObjectNotFound) {
			continue
		}
		if err != nil {
			notes = append(notes, "creation log unavailable")
			break
		}
		matches, err := createdResources(io.LimitReader(res.Body, 8<<20), ids)
		_ = res.Body.Close()
		if err != nil {
			notes = append(notes, "creation log could not be fully read")
		}
		for _, id := range matches {
			set.Prow.Resources = append(set.Prow.Resources, ResourceEvidence{ID: id, URL: a.objectURL(object)})
			delete(ids, id)
		}
		if len(ids) == 0 {
			break
		}
	}
	if len(set.Prow.Resources) == 0 {
		notes = append(notes, "no scanned AWS resource ID confirmed in creation logs")
	}
	set.Prow.ArtifactNote = strings.Join(notes, "; ")
}

func (a prowArchive) findArtifact(ctx context.Context, build, infraID string) (string, error) {
	if !variantHash.MatchString(infraID) && !hex20.MatchString(infraID) {
		return "", nil // Do not interpolate arbitrary tag-associated IDs into a glob.
	}
	query := url.Values{
		"prefix": {build + "artifacts/"}, "matchGlob": {"**/*" + infraID + "*"},
		"fields": {"nextPageToken,items(name)"},
	}
	// shortcut: stop after a few pages rather than traversing large must-gather dumps.
	for range 4 {
		page, err := a.listPage(ctx, query)
		if err != nil {
			return "", err
		}
		for _, item := range page.Items {
			if strings.HasPrefix(item.Name, build+"artifacts/") && strings.Contains(item.Name, infraID) {
				return item.Name, nil
			}
		}
		if page.Next == "" || page.Next == query.Get("pageToken") {
			break
		}
		query.Set("pageToken", page.Next)
	}
	return "", nil
}

func (a prowArchive) creationLogs(ctx context.Context, build string) ([]string, error) {
	root := build + "artifacts/"
	tests, err := a.listPrefixes(ctx, root, "", "")
	if err != nil {
		return nil, err
	}
	var logs []string
	for _, test := range tests {
		if !strings.HasPrefix(test, root) || path.Base(test) == "release" || path.Base(test) == "build-resources" {
			continue
		}
		steps, err := a.listPrefixes(ctx, test, "", "")
		if err != nil {
			return logs, err
		}
		for _, dir := range steps {
			step := path.Base(dir)
			if strings.HasPrefix(dir, test) && (strings.Contains(step, "create") || strings.Contains(step, "provision")) &&
				!strings.Contains(step, "deprovision") {
				logs = append(logs, dir+"build-log.txt")
			}
		}
	}
	slices.Sort(logs)
	return logs, nil
}

func createdResources(log io.Reader, wanted map[string]bool) ([]string, error) {
	seen := map[string]bool{}
	var found []string
	s := bufio.NewScanner(log)
	s.Buffer(make([]byte, 4096), 1<<20)
	for s.Scan() {
		line := s.Bytes()
		if at := bytes.IndexByte(line, '{'); at >= 0 {
			var event struct {
				Message    string `json:"msg"`
				ID         string `json:"id"`
				EndpointID string `json:"endpointID"`
			}
			if json.Unmarshal(line[at:], &event) != nil {
				continue
			}
			id := event.ID
			switch event.Message {
			case "Created VPC":
				if !strings.HasPrefix(id, "vpc-") {
					continue
				}
			case "endpoint created":
				id = event.EndpointID
				fallthrough
			case "Created s3 VPC endpoint", "Created VPC endpoint for proxy":
				if !strings.HasPrefix(id, "vpce-") {
					continue
				}
			default:
				continue
			}
			if wanted[id] && !seen[id] {
				found = append(found, id)
				seen[id] = true
			}
		}
	}
	return found, s.Err()
}
