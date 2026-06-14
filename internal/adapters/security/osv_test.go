package security

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOSVQueryBatchPreservesOrderAndDeduplicates(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/querybatch", r.URL.Path)
		requestCount.Add(1)
		var body struct {
			Queries []map[string]any `json:"queries"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Len(t, body.Queries, 2)
		_, _ = w.Write([]byte(`{"results":[{"vulns":[{"id":"GHSA-a","modified":"2026-01-01T00:00:00Z"}]},{"vulns":[{"id":"GHSA-b"}]}]}`))
	}))
	defer server.Close()
	client := NewOSVClient(WithOSVBaseURL(server.URL), WithOSVMaxRetries(0))

	results, err := client.QueryBatch(context.Background(), []OSVQuery{
		{Ecosystem: "npm", Name: "lodash", Version: "4.17.21"},
		{PURL: "pkg:pypi/requests@2.31.0"},
		{Ecosystem: "npm", Name: "lodash", Version: "4.17.21"},
	})

	require.NoError(t, err)
	require.Len(t, results, 3)
	require.Equal(t, "GHSA-a", results[0].Vulnerabilities[0].ID)
	require.Equal(t, "GHSA-b", results[1].Vulnerabilities[0].ID)
	require.Equal(t, "GHSA-a", results[2].Vulnerabilities[0].ID)
	require.EqualValues(t, 1, requestCount.Load())
}

func TestOSVPURLVersionRulesAndCommitShape(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		bodies = append(bodies, body)
		_, _ = w.Write([]byte(`{"results":[{"vulns":[]},{"vulns":[]}]}`))
	}))
	defer server.Close()
	client := NewOSVClient(WithOSVBaseURL(server.URL), WithOSVMaxRetries(0))

	_, err := client.QueryBatch(context.Background(), []OSVQuery{{PURL: "pkg:npm/lodash@4.17.21", Version: "4.17.21"}})
	require.Error(t, err)
	var osvErr *OSVError
	require.True(t, errors.As(err, &osvErr))
	require.False(t, osvErr.Retryable)

	_, err = client.QueryBatch(context.Background(), []OSVQuery{{PURL: "pkg:npm/lodash@4.17.21"}, {Commit: "ABCDEF1234567890"}})
	require.NoError(t, err)
	require.Len(t, bodies, 1)
	queries := bodies[0]["queries"].([]any)
	purlQuery := queries[0].(map[string]any)
	require.NotContains(t, purlQuery, "version")
	pkg := purlQuery["package"].(map[string]any)
	require.Equal(t, "pkg:npm/lodash@4.17.21", pkg["purl"])
	commitQuery := queries[1].(map[string]any)
	require.Equal(t, "abcdef1234567890", commitQuery["commit"])
}

func TestOSVQueryConvenienceMethodsHydrateDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/querybatch":
			_, _ = w.Write([]byte(`{"results":[{"vulns":[{"id":"GHSA-detail","modified":"2026-01-01T00:00:00Z"}]}]}`))
		case r.URL.Path == "/vulns/GHSA-detail":
			_, _ = w.Write([]byte(`{"id":"GHSA-detail","summary":"bad package","details":"full details","aliases":["CVE-2026-0001"],"database_specific":{"severity":"HIGH"},"references":[{"url":"https://example.test/advisory"}],"modified":"2026-01-02T00:00:00Z"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewOSVClient(WithOSVBaseURL(server.URL), WithOSVMaxRetries(0))

	vulns, err := client.QueryPackage(context.Background(), "npm", "lodash", "4.17.21")

	require.NoError(t, err)
	require.Len(t, vulns, 1)
	require.Equal(t, "GHSA-detail", vulns[0].ID)
	require.Equal(t, "CVE-2026-0001", vulns[0].CVE)
	require.Equal(t, "HIGH", vulns[0].Severity)
	require.Equal(t, "bad package", vulns[0].Summary)
	require.Equal(t, []string{"https://example.test/advisory"}, vulns[0].References)
}

func TestOSVHydrationNotFoundIsNonRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer server.Close()
	client := NewOSVClient(WithOSVBaseURL(server.URL), WithOSVMaxRetries(0))

	_, err := client.HydrateVulnerability(context.Background(), "GHSA-missing")

	require.Error(t, err)
	var osvErr *OSVError
	require.True(t, errors.As(err, &osvErr))
	require.Equal(t, http.StatusNotFound, osvErr.StatusCode)
	require.False(t, osvErr.Retryable)
}

func TestOSVRetryClassificationFor429And5xx(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := attempts.Add(1)
		if count == 1 {
			w.Header().Set("Retry-After", "7")
			http.Error(w, "rate", http.StatusTooManyRequests)
			return
		}
		if count == 2 {
			http.Error(w, "boom", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"vulns":[]}]}`))
	}))
	defer server.Close()
	var sawRetryAfter bool
	client := NewOSVClient(
		WithOSVBaseURL(server.URL),
		WithOSVMaxRetries(2),
		WithOSVBackoff(func(ctx context.Context, attempt int, err *OSVError) error {
			if attempt == 1 {
				require.NotNil(t, err.RetryAfter)
				sawRetryAfter = *err.RetryAfter > 0
			}
			return nil
		}),
	)

	_, err := client.QueryBatch(context.Background(), []OSVQuery{{Ecosystem: "npm", Name: "lodash"}})

	require.NoError(t, err)
	require.True(t, sawRetryAfter)
	require.EqualValues(t, 3, attempts.Load())
}

func TestOSVContextCancellationIsNotRetried(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewOSVClient(WithOSVBaseURL("http://127.0.0.1:1"), WithOSVMaxRetries(3), WithOSVBackoff(func(context.Context, int, *OSVError) error {
		t.Fatal("backoff should not run for cancelled context")
		return nil
	}))

	_, err := client.QueryBatch(ctx, []OSVQuery{{Ecosystem: "npm", Name: "lodash"}})

	require.Error(t, err)
	require.True(t, errors.Is(err, context.Canceled))
}

func TestOSVChunkingSplitsBatches(t *testing.T) {
	var batchSizes []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Queries []map[string]any `json:"queries"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		batchSizes = append(batchSizes, len(body.Queries))
		results := make([]string, len(body.Queries))
		for i := range results {
			results[i] = `{"vulns":[]}`
		}
		_, _ = w.Write([]byte(`{"results":[` + strings.Join(results, ",") + `]}`))
	}))
	defer server.Close()
	client := NewOSVClient(WithOSVBaseURL(server.URL), WithOSVMaxBatchSize(2), WithOSVMaxRetries(0))

	_, err := client.QueryBatch(context.Background(), []OSVQuery{
		{Ecosystem: "npm", Name: "a"},
		{Ecosystem: "npm", Name: "b"},
		{Ecosystem: "npm", Name: "c"},
	})

	require.NoError(t, err)
	require.Equal(t, []int{2, 1}, batchSizes)
}
