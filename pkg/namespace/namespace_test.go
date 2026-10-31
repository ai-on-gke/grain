// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package namespace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/bootstrap/playbooks"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/redact"
)

func newTestServer(t *testing.T, store ci.RunStore, srcDir string, redactor *redact.Redactor) *Server {
	t.Helper()
	return &Server{
		CIStore:            store,
		SrcDir:             srcDir,
		Redactor:           redactor,
		FollowPollInterval: 10 * time.Millisecond,
		FollowMaxDuration:  2 * time.Second,
	}
}

func serveRequest(srv *Server, scope Scope, method, urlStr string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, urlStr, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req, scope)
	return rec
}

func TestRetriedJobAttemptsAndPerTestExtractionOverREST(t *testing.T) {
	t.Parallel()

	store := ci.NewRunStore(10)
	now := time.Now().UTC()
	run := ci.Run{
		ID:         "883a3337aa7850f7",
		Repo:       "bwsalmon/grain",
		Ref:        "grain/task-796",
		Commit:     "deadbeef1234",
		Conclusion: ci.ConclusionCancelled,
		StartedAt:  &now,
		FinishedAt: &now,
		Jobs: []ci.JobRun{
			{
				JobID:       "go",
				Name:        "go",
				Conclusion:  ci.ConclusionCancelled,
				Retried:     true,
				RetryCount:  1,
				RetryReason: `step "Test" failed (exit 1)`,
				Steps: []ci.StepRun{
					{
						Name:       "Test",
						Conclusion: ci.ConclusionCancelled,
						Output:     "attempt 2 cancelled mid-suite\n",
					},
				},
				GoTestLog: "=== PACKAGE github.com/ai-on-gke/grain/pkg/ci\n=== RUN   TestSomething\n",
				Attempts: []ci.JobAttempt{
					{
						Attempt:    1,
						Conclusion: ci.ConclusionFailure,
						Steps: []ci.StepRun{
							{
								Name:       "Test",
								Conclusion: ci.ConclusionFailure,
								ExitCode:   1,
								Output:     "step summary: 1 package failed (pkg/ci)\n",
							},
						},
						GoTestLog: "=== PACKAGE github.com/ai-on-gke/grain/pkg/ci\n" +
							"=== RUN   TestPassOnAttempt1\n" +
							"--- PASS: TestPassOnAttempt1 (0.01s)\n" +
							"=== RUN   TestFailOnAttempt1\n" +
							"    attempts_test.go:42: expected Attempt 1 log to survive, got nil\n" +
							"--- FAIL: TestFailOnAttempt1 (0.03s)\n" +
							"FAIL\tgithub.com/ai-on-gke/grain/pkg/ci\t0.04s\n",
					},
					{
						Attempt:    2,
						Conclusion: ci.ConclusionCancelled,
						Steps: []ci.StepRun{
							{
								Name:       "Test",
								Conclusion: ci.ConclusionCancelled,
								Output:     "attempt 2 cancelled mid-suite\n",
							},
						},
						GoTestLog: "=== PACKAGE github.com/ai-on-gke/grain/pkg/ci\n=== RUN   TestSomething\n",
					},
				},
			},
		},
	}
	store.Put(run)

	srv := newTestServer(t, store, "", nil)
	scope := Scope{TaskRepo: "bwsalmon/grain", MayReadCI: true}

	// 1. Both attempts are listed under .../go/attempts?list
	rec := serveRequest(srv, scope, http.MethodGet, "/fs/ci/bwsalmon/grain/883a3337aa7850f7/go/attempts?list", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("attempts list status = %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "1/\n2/\n" {
		t.Fatalf("attempts list = %q, want %q", got, "1/\n2/\n")
	}

	// 2. Attempt 1 step log is preserved and addressable
	rec = serveRequest(srv, scope, http.MethodGet, "/fs/ci/bwsalmon/grain/883a3337aa7850f7/go/attempts/1/Test.log", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("attempt 1 Test.log status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "1 package failed (pkg/ci)") {
		t.Errorf("attempt 1 Test.log = %q", rec.Body.String())
	}

	// 3. Attempt 1 raw gotest.log is preserved and addressable
	rec = serveRequest(srv, scope, http.MethodGet, "/fs/ci/bwsalmon/grain/883a3337aa7850f7/go/attempts/1/gotest.log", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("attempt 1 gotest.log status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "TestFailOnAttempt1") {
		t.Errorf("attempt 1 gotest.log = %q", rec.Body.String())
	}

	// 4. Attempt 1 per-test output extraction for the failing Go test
	rec = serveRequest(srv, scope, http.MethodGet, "/fs/ci/bwsalmon/grain/883a3337aa7850f7/go/attempts/1/tests/pkg/ci/TestFailOnAttempt1.log", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("attempt 1 per-test log status = %d: %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{
		"=== RUN   TestFailOnAttempt1",
		"attempts_test.go:42: expected Attempt 1 log to survive, got nil",
		"--- FAIL: TestFailOnAttempt1 (0.03s)",
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("per-test output missing %q in:\n%s", want, rec.Body.String())
		}
	}
	if strings.Contains(rec.Body.String(), "TestPassOnAttempt1") {
		t.Errorf("per-test output for TestFailOnAttempt1 leaked TestPassOnAttempt1:\n%s", rec.Body.String())
	}

	// 5. Attempt 2 and unnumbered path both resolve to attempt 2
	for _, p := range []string{
		"/fs/ci/bwsalmon/grain/883a3337aa7850f7/go/attempts/2/Test.log",
		"/fs/ci/bwsalmon/grain/883a3337aa7850f7/go/Test.log",
	} {
		rec = serveRequest(srv, scope, http.MethodGet, p, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d: %s", p, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "attempt 2 cancelled mid-suite") {
			t.Errorf("%s = %q, want attempt 2 content", p, rec.Body.String())
		}
	}
}

func TestGrantScoping(t *testing.T) {
	t.Parallel()

	store := ci.NewRunStore(10)
	store.Put(ci.Run{
		ID:         "run-1",
		Repo:       "bwsalmon/grain",
		Conclusion: ci.ConclusionSuccess,
		Jobs: []ci.JobRun{
			{JobID: "go", Name: "go", Conclusion: ci.ConclusionSuccess, Steps: []ci.StepRun{{Name: "Test", Output: "ok\n"}}},
		},
	})
	store.Put(ci.Run{
		ID:         "run-other",
		Repo:       "acme/secret",
		Conclusion: ci.ConclusionSuccess,
		Jobs: []ci.JobRun{
			{JobID: "go", Name: "go", Conclusion: ci.ConclusionSuccess, Steps: []ci.StepRun{{Name: "Test", Output: "secret\n"}}},
		},
	})

	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := newTestServer(t, store, srcDir, nil)

	// 1. No ci-read -> 403 on ci://
	noCIScope := Scope{TaskRepo: "bwsalmon/grain", MayReadCI: false, MayReadSrc: false}
	if rec := serveRequest(srv, noCIScope, http.MethodGet, "/fs/ci/bwsalmon/grain/run-1/go/Test.log", nil); rec.Code != http.StatusForbidden {
		t.Errorf("no ci-read got status %d, want 403", rec.Code)
	}
	if rec := serveRequest(srv, noCIScope, http.MethodGet, "/fs/ci/?list", nil); rec.Code != http.StatusForbidden {
		t.Errorf("no ci-read root list got status %d, want 403", rec.Code)
	}

	// 2. Repo-scoped ci-read -> 200 on own repo, 403 on another repo
	repoCIScope := Scope{TaskRepo: "bwsalmon/grain", MayReadCI: true}
	if rec := serveRequest(srv, repoCIScope, http.MethodGet, "/fs/ci/bwsalmon/grain/run-1/go/Test.log", nil); rec.Code != http.StatusOK {
		t.Errorf("own repo ci-read got status %d, want 200", rec.Code)
	}
	if rec := serveRequest(srv, repoCIScope, http.MethodGet, "/fs/ci/acme/secret/run-other/go/Test.log", nil); rec.Code != http.StatusForbidden {
		t.Errorf("other repo ci-read got status %d, want 403", rec.Code)
	}

	// 3. No self-debug -> 403 on src://, with self-debug -> 200
	if rec := serveRequest(srv, repoCIScope, http.MethodGet, "/fs/src/main.go", nil); rec.Code != http.StatusForbidden {
		t.Errorf("no self-debug on src:// got status %d, want 403", rec.Code)
	}
	srcScope := Scope{MayReadSrc: true}
	if rec := serveRequest(srv, srcScope, http.MethodGet, "/fs/src/main.go", nil); rec.Code != http.StatusOK {
		t.Errorf("with self-debug on src:// got status %d, want 200", rec.Code)
	}
}

func TestRedactionInStreamAndGrep(t *testing.T) {
	t.Parallel()

	customSecret := "my-super-secret-credential-998877"
	ghToken := "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ123456"
	redactor := redact.New([]redact.KnownValue{{Label: "secret custom-cred", Value: customSecret}})

	store := ci.NewRunStore(10)
	store.Put(ci.Run{
		ID:         "run-redact",
		Repo:       "bwsalmon/grain",
		Conclusion: ci.ConclusionFailure,
		Jobs: []ci.JobRun{
			{
				JobID:      "go",
				Name:       "go",
				Conclusion: ci.ConclusionFailure,
				Steps: []ci.StepRun{
					{
						Name:       "Test",
						Conclusion: ci.ConclusionFailure,
						Output:     fmt.Sprintf("connecting with token=%s and github=%s\nFAIL: auth test\n", customSecret, ghToken),
					},
				},
			},
		},
	})

	srv := newTestServer(t, store, "", redactor)
	scope := Scope{TaskRepo: "bwsalmon/grain", MayReadCI: true}

	// 1. Direct file read redacts both custom and pattern-matched secrets
	rec := serveRequest(srv, scope, http.MethodGet, "/fs/ci/bwsalmon/grain/run-redact/go/Test.log", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, customSecret) || strings.Contains(body, ghToken) {
		t.Fatalf("unredacted secret leaked in stream: %q", body)
	}
	if !strings.Contains(body, "[redacted:") {
		t.Fatalf("expected [redacted:...] in stream, got %q", body)
	}

	// 2. Grep redacts before matching and before returning
	recGrep := serveRequest(srv, scope, http.MethodGet, "/fs/ci/bwsalmon/grain/run-redact/go/Test.log?grep=connecting", nil)
	if recGrep.Code != http.StatusOK {
		t.Fatalf("grep status = %d", recGrep.Code)
	}
	if strings.Contains(recGrep.Body.String(), customSecret) || strings.Contains(recGrep.Body.String(), ghToken) {
		t.Fatalf("unredacted secret leaked in grep: %q", recGrep.Body.String())
	}
}

// A secret set after the server was built is redacted by the next request
// when the server builds its redactor per request (NewRedactor), as the
// daemon's /fs/ server does.
func TestRedactorRebuiltPerRequest(t *testing.T) {
	t.Parallel()

	lateSecret := "set-after-daemon-start-445566"
	store := ci.NewRunStore(10)
	store.Put(ci.Run{
		ID:         "run-late",
		Repo:       "bwsalmon/grain",
		Conclusion: ci.ConclusionFailure,
		Jobs: []ci.JobRun{{
			JobID: "go", Name: "go", Conclusion: ci.ConclusionFailure,
			Steps: []ci.StepRun{{Name: "Test", Output: "using " + lateSecret + "\n"}},
		}},
	})

	var mu sync.Mutex
	var known []redact.KnownValue
	srv := newTestServer(t, store, "", nil)
	srv.NewRedactor = func() *redact.Redactor {
		mu.Lock()
		defer mu.Unlock()
		return redact.New(append([]redact.KnownValue(nil), known...))
	}
	scope := Scope{TaskRepo: "bwsalmon/grain", MayReadCI: true}
	const p = "/fs/ci/bwsalmon/grain/run-late/go/Test.log"

	if rec := serveRequest(srv, scope, http.MethodGet, p, nil); !strings.Contains(rec.Body.String(), lateSecret) {
		t.Fatalf("before the secret is known the log should read as written, got %q", rec.Body.String())
	}
	mu.Lock()
	known = append(known, redact.KnownValue{Label: "secret late", Value: lateSecret})
	mu.Unlock()
	for _, u := range []string{p, p + "?grep=using"} {
		rec := serveRequest(srv, scope, http.MethodGet, u, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d: %s", u, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), lateSecret) {
			t.Errorf("%s leaked a secret set after the server was built: %q", u, rec.Body.String())
		}
	}
}

// A window that starts or ends inside a secret -- a Range, a ?tail=, the
// byte ceiling grain_copy reads under, or a ?follow=1 poll that lands while
// the line is half written -- is not a way to read the secret a few bytes at
// a time: each line it touches is redacted whole before it is cut.
func TestWindowedReadsDoNotSplitASecret(t *testing.T) {
	t.Parallel()

	secret := "Zq9xWv7Lm3Kp2Rt8Ny4Hb6Jc5Df1"
	content := "first line\nusing " + secret + " here\nlast line\n"
	at := int64(strings.Index(content, secret))
	redactor := redact.New([]redact.KnownValue{{Label: "secret split", Value: secret}})

	store := ci.NewRunStore(10)
	store.Put(ci.Run{
		ID: "run-split", Repo: "bwsalmon/grain", Conclusion: ci.ConclusionFailure,
		Jobs: []ci.JobRun{{
			JobID: "go", Name: "go", Conclusion: ci.ConclusionFailure,
			Steps: []ci.StepRun{{Name: "Test", Conclusion: ci.ConclusionFailure, Output: content}},
		}},
	})
	srv := newTestServer(t, store, "", redactor)
	scope := Scope{TaskRepo: "bwsalmon/grain", MayReadCI: true}
	const p = "/fs/ci/bwsalmon/grain/run-split/go/Test.log"

	leaks := func(body string) bool {
		for i := 0; i+6 <= len(secret); i++ {
			if strings.Contains(body, secret[i:i+6]) {
				return true
			}
		}
		return false
	}
	for _, rng := range []string{
		fmt.Sprintf("bytes=%d-%d", at+4, at+9),
		fmt.Sprintf("bytes=0-%d", at+10),
		fmt.Sprintf("bytes=%d-", at+10),
	} {
		rec := serveRequest(srv, scope, http.MethodGet, p, map[string]string{"Range": rng})
		if rec.Code != http.StatusPartialContent {
			t.Fatalf("Range %s status = %d", rng, rec.Code)
		}
		if body := rec.Body.String(); leaks(body) || !strings.Contains(body, "[redacted: secret split]") {
			t.Errorf("Range %s served %q, want the secret's line whole and redacted", rng, body)
		}
	}
	tail := int64(len(content)) - (at + 10)
	if rec := serveRequest(srv, scope, http.MethodGet, fmt.Sprintf("%s?tail=%d", p, tail), nil); leaks(rec.Body.String()) {
		t.Errorf("?tail=%d leaked part of the secret: %q", tail, rec.Body.String())
	}
	// A line with nothing to redact is still cut to the byte.
	if rec := serveRequest(srv, scope, http.MethodGet, p, map[string]string{"Range": "bytes=2-6"}); rec.Body.String() != "rst l" {
		t.Errorf("Range bytes=2-6 = %q, want %q", rec.Body.String(), "rst l")
	}

	node, err := srv.Resolve(context.Background(), scope, "ci", "bwsalmon/grain/run-split/go/Test.log")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := srv.ReadRedacted(node, at+10); err != nil || leaks(got) {
		t.Errorf("ReadRedacted cut at the secret = %q, %v", got, err)
	}

	// ?follow=1 on a log whose secret's line is written in two halves.
	now := time.Now().UTC()
	half := "first line\nusing " + secret[:12]
	growing := func(out string, done bool) ci.Run {
		run := ci.Run{ID: "run-follow-split", Repo: "bwsalmon/grain", StartedAt: &now}
		step := ci.StepRun{Name: "Test", StartedAt: &now, Output: out}
		job := ci.JobRun{JobID: "go", Name: "go", StartedAt: &now, Steps: []ci.StepRun{step}}
		if done {
			run.Conclusion, run.FinishedAt = ci.ConclusionSuccess, &now
			job.Conclusion, job.FinishedAt = ci.ConclusionSuccess, &now
			job.Steps[0].Conclusion, job.Steps[0].FinishedAt = ci.ConclusionSuccess, &now
		}
		run.Jobs = []ci.JobRun{job}
		return run
	}
	store.Put(growing(half, false))
	go func() {
		time.Sleep(25 * time.Millisecond)
		store.Put(growing(content, true))
	}()
	rec := serveRequest(srv, scope, http.MethodGet, "/fs/ci/bwsalmon/grain/run-follow-split/go/Test.log?follow=1", nil)
	if body := rec.Body.String(); leaks(body) || !strings.Contains(body, "last line") {
		t.Errorf("?follow=1 served %q, want every line with the secret redacted", body)
	}
}

type chunkedLargeReader struct {
	line       string
	totalLines int
	readLines  int
	maxReadBuf int
}

func (r *chunkedLargeReader) Read(p []byte) (int, error) {
	if len(p) > r.maxReadBuf {
		r.maxReadBuf = len(p)
	}
	if r.readLines >= r.totalLines {
		return 0, io.EOF
	}
	n := copy(p, r.line)
	r.readLines++
	return n, nil
}

func (r *chunkedLargeReader) Close() error { return nil }

func TestStreamingLargeLogWithoutBufferingAndCeiling(t *testing.T) {
	t.Parallel()

	line := strings.Repeat("a", 1023) + "\n" // 1 KiB per line
	totalLines := 4096                       // 4 MiB total
	cr := &chunkedLargeReader{line: line, totalLines: totalLines}

	var streamedBytes int64
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		defer pw.Close()
		// Stream with a 1 MiB ceiling to verify both chunked streaming and ceiling enforcement.
		done <- streamRedacted(pw, io.LimitReader(cr, 1<<20), redact.New(nil))
	}()

	buf := make([]byte, 16*1024)
	for {
		n, err := pr.Read(buf)
		streamedBytes += int64(n)
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("pipe read error: %v", err)
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("streamRedacted error: %v", err)
	}
	if streamedBytes != 1<<20 {
		t.Errorf("streamedBytes = %d, want %d (1 MiB ceiling)", streamedBytes, 1<<20)
	}
	if cr.readLines > 1050 {
		t.Errorf("reader consumed %d lines for 1 MiB ceiling; expected streaming without buffering entire 4096-line input", cr.readLines)
	}
}

func TestRangeTailHeadAndFollowOnGrowingLog(t *testing.T) {
	t.Parallel()

	store := ci.NewRunStore(10)
	now := time.Now().UTC()
	run := ci.Run{
		ID:        "run-growing",
		Repo:      "bwsalmon/grain",
		StartedAt: &now,
		Jobs: []ci.JobRun{
			{
				JobID:     "go",
				Name:      "go",
				StartedAt: &now,
				Steps: []ci.StepRun{
					{
						Name:      "Test",
						StartedAt: &now,
						Output:    "0123456789abcdefghij\n", // 21 bytes
					},
				},
			},
		},
	}
	store.Put(run)

	srv := newTestServer(t, store, "", nil)
	scope := Scope{TaskRepo: "bwsalmon/grain", MayReadCI: true}

	// 1. HEAD reports size and X-Grain-Growing: true
	headRec := serveRequest(srv, scope, http.MethodHead, "/fs/ci/bwsalmon/grain/run-growing/go/Test.log", nil)
	if headRec.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d", headRec.Code)
	}
	if got := headRec.Header().Get("X-Grain-Growing"); got != "true" {
		t.Errorf("HEAD X-Grain-Growing = %q, want true", got)
	}
	if got := headRec.Header().Get("Content-Length"); got != "21" {
		t.Errorf("HEAD Content-Length = %q, want 21", got)
	}

	// 2. Range request on growing log
	rangeRec := serveRequest(srv, scope, http.MethodGet, "/fs/ci/bwsalmon/grain/run-growing/go/Test.log", map[string]string{
		"Range": "bytes=10-19",
	})
	if rangeRec.Code != http.StatusPartialContent {
		t.Fatalf("Range status = %d, want 206", rangeRec.Code)
	}
	if got := rangeRec.Body.String(); got != "abcdefghij" {
		t.Errorf("Range body = %q, want %q", got, "abcdefghij")
	}
	if got := rangeRec.Header().Get("X-Grain-Growing"); got != "true" {
		t.Errorf("Range X-Grain-Growing = %q, want true", got)
	}

	// 3. ?tail=11 on growing log
	tailRec := serveRequest(srv, scope, http.MethodGet, "/fs/ci/bwsalmon/grain/run-growing/go/Test.log?tail=11", nil)
	if tailRec.Code != http.StatusOK {
		t.Fatalf("tail status = %d", tailRec.Code)
	}
	if got := tailRec.Body.String(); got != "abcdefghij\n" {
		t.Errorf("tail body = %q, want %q", got, "abcdefghij\n")
	}
	if got := tailRec.Header().Get("X-Grain-Offset"); got != "10" {
		t.Errorf("tail X-Grain-Offset = %q, want 10", got)
	}

	// 4. ?follow=1 streams new lines as the step grows and exits when the step finishes
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(25 * time.Millisecond)
		r1 := run
		r1.Jobs = []ci.JobRun{
			{
				JobID:     "go",
				Name:      "go",
				StartedAt: &now,
				Steps: []ci.StepRun{
					{
						Name:      "Test",
						StartedAt: &now,
						Output:    "0123456789abcdefghij\nsecond line\n",
					},
				},
			},
		}
		store.Put(r1)

		time.Sleep(25 * time.Millisecond)
		r2 := r1
		r2.Conclusion = ci.ConclusionSuccess
		r2.FinishedAt = &now
		r2.Jobs = []ci.JobRun{
			{
				JobID:      "go",
				Name:       "go",
				Conclusion: ci.ConclusionSuccess,
				StartedAt:  &now,
				FinishedAt: &now,
				Steps: []ci.StepRun{
					{
						Name:       "Test",
						Conclusion: ci.ConclusionSuccess,
						StartedAt:  &now,
						FinishedAt: &now,
						Output:     "0123456789abcdefghij\nsecond line\nfinal line\n",
					},
				},
			},
		}
		store.Put(r2)
	}()

	followRec := serveRequest(srv, scope, http.MethodGet, "/fs/ci/bwsalmon/grain/run-growing/go/Test.log?follow=1&tail=11", nil)
	wg.Wait()
	if followRec.Code != http.StatusOK {
		t.Fatalf("follow status = %d", followRec.Code)
	}
	if got := followRec.Body.String(); got != "abcdefghij\nsecond line\nfinal line\n" {
		t.Errorf("follow body = %q, want %q", got, "abcdefghij\nsecond line\nfinal line\n")
	}
}

func TestGrepMatchLimitListJSONAndGlob(t *testing.T) {
	t.Parallel()

	srcDir := t.TempDir()
	var manyLines strings.Builder
	for i := 1; i <= 150; i++ {
		fmt.Fprintf(&manyLines, "match line %d\n", i)
	}
	if err := os.MkdirAll(filepath.Join(srcDir, "pkg", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "pkg", "sub", "many.txt"), []byte(manyLines.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "pkg", "other.go"), []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := newTestServer(t, ci.NewRunStore(5), srcDir, nil)
	scope := Scope{MayReadSrc: true}

	// 1. Default grep match limit (100) cuts at 100 matches and instructs caller how to raise ?max=
	rec := serveRequest(srv, scope, http.MethodGet, "/fs/src/pkg/sub/many.txt?grep=match+line&n=1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("grep status = %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Grain-Truncated"); got != "true" {
		t.Errorf("X-Grain-Truncated = %q, want true", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "100:match line 100") || strings.Contains(body, "101:match line 101") {
		t.Errorf("default grep did not stop at 100 matches:\n%s", body)
	}
	if !strings.Contains(body, "[truncated at match limit (100 matches); pass ?max=200 to raise the limit]") {
		t.Errorf("missing truncation notice in:\n%s", body)
	}

	// 2. Caller raises ?max=200 and gets all 150 matches without truncation
	rec2 := serveRequest(srv, scope, http.MethodGet, "/fs/src/pkg/sub/many.txt?grep=match+line&n=1&max=200", nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("grep max=200 status = %d", rec2.Code)
	}
	if got := rec2.Header().Get("X-Grain-Truncated"); got != "" {
		t.Errorf("X-Grain-Truncated = %q, want empty", got)
	}
	if !strings.Contains(rec2.Body.String(), "150:match line 150") {
		t.Errorf("expected line 150 in max=200 grep")
	}

	// 3. ?list with Accept: application/json
	listRec := serveRequest(srv, scope, http.MethodGet, "/fs/src/pkg?list", map[string]string{
		"Accept": "application/json",
	})
	if listRec.Code != http.StatusOK {
		t.Fatalf("JSON list status = %d", listRec.Code)
	}
	var payload struct {
		Scheme  string  `json:"scheme"`
		Path    string  `json:"path"`
		Entries []Entry `json:"entries"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal JSON list: %v", err)
	}
	if len(payload.Entries) != 2 || payload.Entries[0].Name != "other.go" || payload.Entries[1].Name != "sub" {
		t.Errorf("unexpected JSON entries: %+v", payload.Entries)
	}

	// 4. ?glob=**/*.txt
	globRec := serveRequest(srv, scope, http.MethodGet, "/fs/src/?glob=**/*.txt", nil)
	if globRec.Code != http.StatusOK {
		t.Fatalf("glob status = %d", globRec.Code)
	}
	if got := strings.TrimSpace(globRec.Body.String()); got != "pkg/sub/many.txt" {
		t.Errorf("glob = %q, want pkg/sub/many.txt", got)
	}
}

func TestNewRedactorAppliesToReadRedactedGrepAndGrainLog(t *testing.T) {
	t.Parallel()

	store := ci.NewRunStore(5)
	const secretVal = "super-secret-custom-token-998877"
	store.Put(ci.Run{
		ID:         "run-redact",
		Repo:       "bwsalmon/grain",
		Ref:        "main",
		Conclusion: ci.ConclusionFailure,
		Jobs: []ci.JobRun{
			{
				Name:       "go",
				Conclusion: ci.ConclusionFailure,
				Steps: []ci.StepRun{
					{
						Name:       "Test",
						Conclusion: ci.ConclusionFailure,
						Output:     "prefix line\nusing secret=" + secretVal + " in step\nsuffix line\n",
					},
				},
			},
		},
	})

	var mu sync.Mutex
	var secrets []redact.KnownValue
	srv := &Server{
		CIStore: store,
		NewRedactor: func() *redact.Redactor {
			mu.Lock()
			defer mu.Unlock()
			return redact.New(append([]redact.KnownValue(nil), secrets...))
		},
	}

	// Secret added after server construction (simulating runtime secret rotation/addition).
	mu.Lock()
	secrets = append(secrets, redact.KnownValue{Label: "secret custom", Value: secretVal})
	mu.Unlock()

	scope := Scope{TaskRepo: "bwsalmon/grain", MayReadCI: true, MayReadGrainLog: true}
	node, err := srv.Resolve(context.Background(), scope, "ci", "bwsalmon/grain/run-redact/go/Test.log")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	copied, err := srv.ReadRedacted(node, -1)
	if err != nil {
		t.Fatalf("ReadRedacted: %v", err)
	}
	if strings.Contains(copied, secretVal) || !strings.Contains(copied, "[redacted: secret custom]") {
		t.Errorf("ReadRedacted did not apply NewRedactor: %q", copied)
	}

	grepRes, err := srv.Grep(context.Background(), scope, "ci", "bwsalmon/grain/run-redact/go/Test.log", GrepOptions{
		Pattern: "using secret",
	})
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if strings.Contains(grepRes.Output, secretVal) || !strings.Contains(grepRes.Output, "[redacted: secret custom]") {
		t.Errorf("Grep did not apply NewRedactor: %q", grepRes.Output)
	}

	logOut := srv.RedactGrainLogText("daemon started with token=" + secretVal + "\n")
	if strings.Contains(logOut, secretVal) || !strings.Contains(logOut, "[redacted: secret custom]") {
		t.Errorf("RedactGrainLogText did not apply NewRedactor: %q", logOut)
	}
}

func TestThreeSegmentHostQualifiedRepoCI(t *testing.T) {
	t.Parallel()

	const repo = "git.forge.example/experimental/grain"
	store := ci.NewRunStore(10)
	now := time.Now().UTC()
	artDir := t.TempDir()
	artPath := filepath.Join(artDir, "run-forge-1", "go", "cover.out")
	if err := os.MkdirAll(filepath.Dir(artPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artPath, []byte("mode: set\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store.Put(ci.Run{
		ID:         "run-forge-1",
		Repo:       repo,
		Ref:        "main",
		Commit:     "abc123456789",
		Conclusion: ci.ConclusionFailure,
		StartedAt:  &now,
		FinishedAt: &now,
		Jobs: []ci.JobRun{
			{
				JobID:      "go",
				Name:       "go",
				Conclusion: ci.ConclusionFailure,
				Steps: []ci.StepRun{
					{
						Name:       "Test",
						Conclusion: ci.ConclusionFailure,
						Output:     "--- FAIL: TestForgeNamespace (0.02s)\n",
					},
				},
				Artifacts: []ci.Artifact{
					{Name: "cover.out", Job: "go", Stored: true, Bytes: 10},
				},
				Attempts: []ci.JobAttempt{
					{
						Attempt:    1,
						Conclusion: ci.ConclusionFailure,
						Steps: []ci.StepRun{
							{
								Name:       "Test",
								Conclusion: ci.ConclusionFailure,
								Output:     "--- FAIL: TestForgeNamespace (0.02s)\n",
							},
						},
					},
				},
			},
		},
	})

	srv := newTestServer(t, store, "", nil)
	srv.ArtifactDir = artDir
	scope := Scope{TaskRepo: repo, MayReadCI: true}

	for _, tc := range []struct {
		url  string
		want string
	}{
		{"/fs/ci/?list", "git.forge.example/experimental/grain/\n"},
		{"/fs/ci/git.forge.example?list", "experimental/grain/\n"},
		{"/fs/ci/git.forge.example/experimental?list", "grain/\n"},
		{"/fs/ci/git.forge.example/experimental/grain?list", "run-forge-1/\n"},
		{"/fs/ci/git.forge.example/experimental/grain/run-forge-1?list", "go/\nartifacts/\n"},
		{"/fs/ci/git.forge.example/experimental/grain/run-forge-1/go?list", "attempts/\nTest.log\ngotest.log\ntests/\n"},
		{"/fs/ci/git.forge.example/experimental/grain/run-forge-1/go/attempts?list", "1/\n"},
		{"/fs/ci/git.forge.example/experimental/grain/run-forge-1/go/Test.log", "--- FAIL: TestForgeNamespace (0.02s)\n"},
		{"/fs/ci/git.forge.example/experimental/grain/run-forge-1/go/attempts/1/Test.log", "--- FAIL: TestForgeNamespace (0.02s)\n"},
		{"/fs/ci/git.forge.example/experimental/grain/run-forge-1/artifacts/go/cover.out", "mode: set\n"},
		{"/fs/ci/git.forge.example/experimental/grain/run-forge-1/go/Test.log?grep=FAIL", "--- FAIL: TestForgeNamespace (0.02s)\n"},
	} {
		rec := serveRequest(srv, scope, http.MethodGet, tc.url, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d: %s", tc.url, rec.Code, rec.Body.String())
		}
		if got := rec.Body.String(); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.url, got, tc.want)
		}
	}

	// Out-of-scope 3-segment repo reports the full 3-segment name in the refusal.
	forbiddenRec := serveRequest(srv, scope, http.MethodGet, "/fs/ci/git.forge.example/experimental/other/run-forge-1", nil)
	if forbiddenRec.Code != http.StatusForbidden {
		t.Fatalf("out-of-scope 3-segment repo status = %d, want 403", forbiddenRec.Code)
	}
	if got := forbiddenRec.Body.String(); !strings.Contains(got, "ci://git.forge.example/experimental/other is outside this sandbox's CI read scope") {
		t.Errorf("out-of-scope refusal = %q, want full 3-segment repo name", got)
	}
}

// task://<id> reads another task's record where that task works in a
// repository the scope reaches -- its own, one it reads, or one an
// api:tasks:read@repo grant names -- and refuses every other task, and
// one that does not exist, alike.
func TestTaskReadsTasksInTheScopesRepositories(t *testing.T) {
	srv := &Server{Tasks: stubTaskProvider{tasks: map[string]*TaskView{
		"100": {ID: "100", Repo: "acme/widgets", Record: "Task 100\n"},
		"101": {ID: "101", Repo: "acme/widgets", Record: "Task 101: sibling\n"},
		"102": {ID: "102", Repo: "acme/library", Record: "Task 102: library\n"},
		"103": {ID: "103", Repo: "acme/gadgets", Record: "Task 103: gadgets\n"},
		"104": {ID: "104", Record: "Task 104: no repository\n"},
	}}}
	scope := Scope{
		TaskID: 100, TaskIDStr: "100", TaskRepo: "acme/widgets",
		TaskReadRepos: map[string]bool{"acme/widgets": true, "acme/library": true},
	}
	for _, id := range []string{"101", "102"} {
		if rec := serveRequest(srv, scope, http.MethodGet, "/fs/task/"+id+"/record.md", nil); rec.Code != http.StatusOK ||
			!strings.Contains(rec.Body.String(), "Task "+id) {
			t.Errorf("task://%s in a repository the scope reaches: %d %q", id, rec.Code, rec.Body.String())
		}
	}
	for _, id := range []string{"103", "104", "999"} {
		if rec := serveRequest(srv, scope, http.MethodGet, "/fs/task/"+id+"/record.md", nil); rec.Code != http.StatusForbidden ||
			!strings.Contains(rec.Body.String(), `"self-debug"`) {
			t.Errorf("task://%s outside the scope's repositories: %d %q", id, rec.Code, rec.Body.String())
		}
	}
	// A scope with only its own repository still reads its siblings.
	bare := Scope{TaskID: 100, TaskIDStr: "100", TaskRepo: "acme/widgets"}
	if rec := serveRequest(srv, bare, http.MethodGet, "/fs/task/101/record.md", nil); rec.Code != http.StatusOK {
		t.Errorf("task://101 in the task's own repository: %d %q", rec.Code, rec.Body.String())
	}
	if rec := serveRequest(srv, bare, http.MethodGet, "/fs/task/102/record.md", nil); rec.Code != http.StatusForbidden {
		t.Errorf("task://102 in a repository the bare scope does not reach: %d", rec.Code)
	}
}

// TestPlaybooksServedFromTheEmbeddedCopy checks src://playbooks lists
// and reads the playbooks embedded in the binary, whatever SrcDir is --
// no deployment's source tree has a playbooks/ of its own -- and still
// only for a scope the bootstrap-playbooks grant (or self-debug) opens.
func TestPlaybooksServedFromTheEmbeddedCopy(t *testing.T) {
	t.Parallel()

	want, err := fs.ReadFile(playbooks.FS, "gke-sandbox-cluster.md")
	if err != nil {
		t.Fatal(err)
	}
	unrelated := t.TempDir()
	if err := os.WriteFile(filepath.Join(unrelated, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, srcDir := range map[string]string{"no SrcDir": "", "unrelated SrcDir": unrelated} {
		t.Run(name, func(t *testing.T) {
			srv := &Server{SrcDir: srcDir, Redactor: redact.New(nil)}
			scope := Scope{MayReadPlaybooks: true}

			rec := serveRequest(srv, scope, http.MethodGet, "/fs/src/playbooks?list", nil)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "gke-sandbox-cluster.md\n") {
				t.Fatalf("list: %d %q", rec.Code, rec.Body.String())
			}
			for _, p := range []string{"/fs/src/playbooks/gke-sandbox-cluster.md", "/fs/src/playbooks/gke-sandbox-cluster"} {
				rec := serveRequest(srv, scope, http.MethodGet, p, nil)
				if rec.Code != http.StatusOK || rec.Body.String() != string(want) {
					t.Errorf("read %s: %d, %d bytes, want %d", p, rec.Code, rec.Body.Len(), len(want))
				}
			}
			if rec := serveRequest(srv, scope, http.MethodGet, "/fs/src/playbooks/nope", nil); rec.Code != http.StatusNotFound {
				t.Errorf("missing playbook: %d, want 404", rec.Code)
			}
			if rec := serveRequest(srv, scope, http.MethodGet, "/fs/src/main.go", nil); rec.Code != http.StatusForbidden {
				t.Errorf("bootstrap-playbooks reading outside playbooks/: %d, want 403", rec.Code)
			}
			rec = serveRequest(srv, Scope{}, http.MethodGet, "/fs/src/playbooks?list", nil)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "bootstrap-playbooks") {
				t.Errorf("no grant: %d %q, want 403 naming bootstrap-playbooks", rec.Code, rec.Body.String())
			}
		})
	}
}
