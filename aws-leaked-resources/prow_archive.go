package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	prowBucket     = "test-platform-results-public"
	prowJobPrefix  = "logs/periodic-ci-openshift-hypershift-"
	prowStorageURL = "https://storage.googleapis.com"
)

type prowArchive struct {
	baseURL string
	client  *http.Client
}

type archivedProwJob struct {
	Metadata struct {
		Name      string    `json:"name"` // PROW_JOB_ID, not the Kubernetes object UID.
		CreatedAt time.Time `json:"creationTimestamp"`
	} `json:"metadata"`
	Spec struct {
		Job string `json:"job"`
	} `json:"spec"`
	Status struct {
		BuildID string `json:"build_id"`
		State   string `json:"state"`
	} `json:"status"`
}

type prowWindow struct{ from, to time.Time }

type prowObjectPage struct {
	Next     string                  `json:"nextPageToken"`
	Prefixes []string                `json:"prefixes"`
	Items    []struct{ Name string } `json:"items"`
}

func (a prowArchive) get(ctx context.Context, address string) (*http.Response, error) {
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err != nil {
			return nil, err
		}
		res, err := a.client.Do(req)
		if err != nil {
			return nil, err
		}
		if res.StatusCode == http.StatusOK {
			return res, nil
		}
		_ = res.Body.Close()
		if res.StatusCode == http.StatusNotFound {
			return nil, errProwObjectNotFound
		}
		if attempt == 3 || (res.StatusCode != http.StatusTooManyRequests && res.StatusCode < 500) {
			return nil, fmt.Errorf("archive returned HTTP %d for %s", res.StatusCode, address)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt) * 200 * time.Millisecond):
		}
	}
}

var errProwObjectNotFound = errors.New("archive object not found")

func (a prowArchive) objectURL(object string) string {
	return a.baseURL + (&url.URL{Path: "/" + prowBucket + "/" + object}).EscapedPath()
}

func (a prowArchive) listPage(ctx context.Context, query url.Values) (prowObjectPage, error) {
	var page prowObjectPage
	address := a.baseURL + "/storage/v1/b/" + prowBucket + "/o?" + query.Encode()
	res, err := a.get(ctx, address)
	if err != nil {
		return page, err
	}
	defer func() { _ = res.Body.Close() }()
	err = json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&page)
	return page, err
}

func (a prowArchive) listPrefixes(ctx context.Context, prefix, start, end string) ([]string, error) {
	query := url.Values{"prefix": {prefix}, "delimiter": {"/"}, "fields": {"nextPageToken,prefixes"}}
	if start != "" {
		query.Set("startOffset", start)
		query.Set("endOffset", end)
	}
	var prefixes []string
	for {
		page, err := a.listPage(ctx, query)
		if err != nil {
			return nil, err
		}
		prefixes = append(prefixes, page.Prefixes...)
		if page.Next == "" {
			return prefixes, nil
		}
		if page.Next == query.Get("pageToken") {
			return nil, fmt.Errorf("repeated page token while listing %s", prefix)
		}
		query.Set("pageToken", page.Next)
	}
}

// OpenShift Prow build IDs encode UNIX milliseconds since the Snowflake epoch
// in the high bits. Offsets are a search optimization, not proof of a match.
func prowBuildOffset(t time.Time) string {
	const epochMS = 1288834974657
	const bits = 22
	const maxMillis = (1<<63 - 1) >> bits
	ms := max(0, min(maxMillis, t.UnixMilli()-epochMS))
	// Retained OpenShift build IDs are 19 digits; keep offsets in that range so
	// GCS lexicographic ordering is also numeric ordering.
	return strconv.FormatInt(max(1_000_000_000_000_000_000, ms<<bits), 10)
}

func (a prowArchive) fetchRun(ctx context.Context, path string) (archivedProwJob, error) {
	var job archivedProwJob
	res, err := a.get(ctx, a.objectURL(path+"prowjob.json"))
	if err != nil {
		return job, err
	}
	defer func() { _ = res.Body.Close() }()
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&job); err != nil {
		return job, fmt.Errorf("decode %sprowjob.json: %w", path, err)
	}
	if job.Metadata.Name == "" || job.Metadata.CreatedAt.IsZero() || runPath(job.Spec.Job, job.Status.BuildID) != path {
		return job, fmt.Errorf("metadata does not identify archived run %s", path)
	}
	return job, nil
}

func runPath(job, id string) string { return "logs/" + job + "/" + id + "/" }

// runs looks up windows once for the whole batch, not once per infra set.
func (a prowArchive) runs(ctx context.Context, windows []prowWindow) ([]archivedProwJob, error) {
	prefixes, err := a.listPrefixes(ctx, prowJobPrefix, "", "")
	if err != nil {
		return nil, err
	}
	paths := make([][]string, len(prefixes))
	err = prowBatch(ctx, len(prefixes), func(ctx context.Context, i int) error {
		prefix := prefixes[i]
		if !strings.HasPrefix(prefix, prowJobPrefix) || strings.Count(prefix, "/") != 2 || !strings.HasSuffix(prefix, "/") {
			return fmt.Errorf("unexpected Prow job prefix %q", prefix)
		}
		for _, w := range windows {
			builds, err := a.listPrefixes(ctx, prefix, prefix+prowBuildOffset(w.from)+"/", prefix+prowBuildOffset(w.to.Add(time.Millisecond))+"/")
			if err != nil {
				return err
			}
			paths[i] = append(paths[i], builds...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var pathsToRead []string
	seen := map[string]bool{}
	for _, builds := range paths {
		for _, path := range builds {
			if !seen[path] {
				seen[path] = true
				pathsToRead = append(pathsToRead, path)
			}
		}
	}
	jobs := make([]archivedProwJob, len(pathsToRead))
	err = prowBatch(ctx, len(pathsToRead), func(ctx context.Context, i int) error {
		job, err := a.fetchRun(ctx, pathsToRead[i])
		if errors.Is(err, errProwObjectNotFound) {
			return nil // A build can exist before its metadata is published.
		}
		jobs[i] = job
		return err
	})
	return jobs, err
}

func prowBatch(ctx context.Context, count int, fn func(context.Context, int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	work := make(chan int)
	var wg sync.WaitGroup
	var firstErr error
	var once sync.Once
	for range min(16, count) {
		wg.Go(func() {
			for i := range work {
				if err := fn(ctx, i); err != nil {
					once.Do(func() { firstErr = err; cancel() })
					return
				}
			}
		})
	}
send:
	for i := range count {
		select {
		case <-ctx.Done():
			break send
		case work <- i:
		}
	}
	close(work)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}
