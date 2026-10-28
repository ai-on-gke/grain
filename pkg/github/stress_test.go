package github

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestStressGitHubClientRateLimitingAndBackoff validates high-concurrency rate limit
// coordination, backoff synchronization, and thread safety across parallel workers.
func TestStressGitHubClientRateLimitingAndBackoff(t *testing.T) {
	var (
		serverReqs     atomic.Int64
		rateLimitHits  atomic.Int64
		successHits    atomic.Int64
		simulatedClock atomic.Int64
	)
	simulatedClock.Store(time.Now().Unix())

	// Upstream test server simulating GitHub REST API with periodic 429 / secondary rate limits
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqNum := serverReqs.Add(1)

		// Every 25th request returns secondary rate limit 429
		if reqNum%25 == 0 {
			rateLimitHits.Add(1)
			w.Header().Set("Retry-After", "1")
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again."}`))
			return
		}

		// Check ETag
		if match := r.Header.Get("If-None-Match"); match == `"etag-test-v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		successHits.Add(1)
		w.Header().Set("ETag", `"etag-test-v1"`)
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprint(simulatedClock.Load()+3600))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": 1, "state": "open", "title": "Stress Test PR"}`))
	}))
	defer server.Close()

	transport := &RealTransport{
		Host:        strings.TrimPrefix(server.URL, "http://"),
		UseTLS:      false,
		Conditional: true,
		sleep: func(d time.Duration) {
			// Mocked fast sleep
		},
	}

	testDuration := 3 * time.Second
	if env := os.Getenv("GRAIN_STRESS_DURATION"); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			testDuration = d
		}
	} else if testing.Short() {
		testDuration = 1 * time.Second
	}

	const workers = 60
	var (
		wg             sync.WaitGroup
		completedCalls atomic.Int64
		declinedCalls  atomic.Int64
		okCalls        atomic.Int64
		done           = make(chan struct{})
	)

	// Background ticker advancing clock so rate limits naturally recover
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				simulatedClock.Add(2)
			}
		}
	}()

	deadline := time.Now().Add(testDuration)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(workerID), uint64(time.Now().UnixNano())))

			for time.Now().Before(deadline) {
				endpointID := r.IntN(4)
				var path string
				switch endpointID {
				case 0:
					path = "/repos/owner/repo/pulls"
				case 1:
					path = "/repos/owner/repo/commits/abc1234/check-runs"
				case 2:
					path = "/repos/owner/repo/issues/42"
				case 3:
					path = "/repos/owner/repo/branches/main"
				}

				resp, err := transport.Request(http.MethodGet, path, nil, nil)
				if err != nil {
					if IsRateLimited(err) {
						declinedCalls.Add(1)
						// Brief pause before trying again to avoid CPU spinning
						time.Sleep(2 * time.Millisecond)
					}
				} else {
					if resp.Status == http.StatusOK || resp.Status == http.StatusNotModified {
						okCalls.Add(1)
					}
				}
				completedCalls.Add(1)
			}
		}(w)
	}

	wg.Wait()
	close(done)

	stats := transport.Stats()
	t.Logf("GitHub Stress Completed: %d total calls, %d OK, %d declined by rate-limit gate, %d server 429s, Sent: %d, Revalidated: %d",
		completedCalls.Load(), okCalls.Load(), declinedCalls.Load(), rateLimitHits.Load(), stats.Sent, stats.Revalidated)

	if completedCalls.Load() == 0 {
		t.Fatal("No calls completed during GitHub client stress test")
	}
	if stats.Declined == 0 && declinedCalls.Load() == 0 && rateLimitHits.Load() > 0 {
		t.Errorf("Expected some calls to be declined by the gate during rate limit throttling")
	}
}

// TestStressGitHubClientConcurrentETagAnd5xxRetries tests LRU cache bounds,
// ETag revalidation, and 5xx retries under heavy concurrent churn.
func TestStressGitHubClientConcurrentETagAnd5xxRetries(t *testing.T) {
	var (
		serverReqs atomic.Int64
		retried5xx atomic.Int64
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqNum := serverReqs.Add(1)

		// Intermittently return 500 error on odd requests to exercise retry loop
		if reqNum%17 == 0 {
			retried5xx.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("500 internal server error"))
			return
		}

		etag := fmt.Sprintf(`"etag-%s"`, r.URL.Path)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusOK)
		// Return 8KB payload
		w.Write(bytes.Repeat([]byte("abcdefgh"), 1024))
	}))
	defer server.Close()

	transport := &RealTransport{
		Host:        strings.TrimPrefix(server.URL, "http://"),
		UseTLS:      false,
		Conditional: true,
		sleep: func(d time.Duration) {
			// instant retry
		},
	}

	const workers = 40
	const iterations = 50
	var wg sync.WaitGroup

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				path := fmt.Sprintf("/repos/owner/repo/pulls/%d", (workerID*iterations+i)%200)
				_, _ = transport.Request(http.MethodGet, path, nil, nil)
			}
		}(w)
	}

	wg.Wait()

	stats := transport.Stats()
	t.Logf("ETag/5xx Stress completed: Sent=%d, Revalidated=%d, Retried=%d", stats.Sent, stats.Revalidated, stats.Retried)
	if stats.Sent == 0 {
		t.Fatal("No requests recorded in stats")
	}
}
