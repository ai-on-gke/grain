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

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// statusServer answers every request with status and a JSON error body,
// the way the daemon's writeError does.
func statusServer(t *testing.T, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"error":"refused with %d"}`, status)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// closedServer is a URL nothing listens on.
func closedServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return "http://" + addr
}

// runForExit runs the task CLI the way Main does and returns its exit
// code, what it printed and what it said on stderr.
func runForExit(t *testing.T, args []string) (code int, stdout, stderr string) {
	t.Helper()
	var errOut strings.Builder
	stdout = captureStdout(t, func() { code = runTaskCLI(args, &errOut) })
	return code, stdout, errOut.String()
}

// #XC-13: under -json a failure is one {"error": {...}} object on stderr
// and nothing at all on stdout, wherever -json was written.
func TestJSONErrorsWriteStructuredStderrAndLeaveStdoutEmpty(t *testing.T) {
	server := listTestServer(t)
	for _, tc := range []struct {
		name     string
		args     []string
		wantExit int
		wantCode string
	}{
		{"unknown flag after the verb", []string{"-server", server, "list", "--json", "--no-such-flag"}, exitUsage, "invalid_flag"},
		{"stray argument", []string{"-server", server, "--json", "list", "junk"}, exitUsage, "usage"},
		{"unknown command", []string{"-server", server, "--json", "frobnicate"}, exitUsage, "unknown_command"},
		{"missing task", []string{"-server", server, "get", "999", "-json"}, exitUsage, "not_found"},
		{"unauthenticated", []string{"-server", statusServer(t, http.StatusUnauthorized), "list", "--json"}, exitAuth, "unauthenticated"},
		{"server error", []string{"-server", statusServer(t, http.StatusInternalServerError), "list", "--json"}, exitFailed, "server_error"},
		{"conflict", []string{"-server", statusServer(t, http.StatusConflict), "list", "--json"}, exitUsage, "conflict"},
		{"rate limited", []string{"-server", statusServer(t, http.StatusTooManyRequests), "list", "--json"}, exitFailed, "rate_limited"},
		{"unreachable", []string{"-server", closedServer(t), "--connect-timeout", "200ms", "list", "--json"}, exitUnreachable, "daemon_unreachable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"-token", testAccessCode}, tc.args...)
			code, stdout, stderr := runForExit(t, args)
			if code != tc.wantExit {
				t.Errorf("exit = %d, want %d (stderr %q)", code, tc.wantExit, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want nothing on a failure", stdout)
			}
			dec := json.NewDecoder(strings.NewReader(stderr))
			var got jsonError
			if err := dec.Decode(&got); err != nil {
				t.Fatalf("stderr = %q, want one JSON error object: %v", stderr, err)
			}
			if dec.More() {
				t.Fatalf("stderr = %q, want the error object and nothing else", stderr)
			}
			if got.Error.Code != tc.wantCode {
				t.Errorf("error.code = %q, want %q", got.Error.Code, tc.wantCode)
			}
			if got.Error.Message == "" {
				t.Errorf("error.message is empty in %q", stderr)
			}
		})
	}

	// Without -json the same failure is a sentence, and the exit code
	// does not change.
	code, _, stderr := runForExit(t, []string{"-token", testAccessCode, "-server", server, "list", "junk"})
	if code != exitUsage || !strings.HasPrefix(stderr, "grain: ") {
		t.Errorf("grain list junk = exit %d, stderr %q; want exit 2 and a \"grain: \" sentence", code, stderr)
	}

	// A red run under -json is the run document on stdout and exit 1:
	// the answer, not an error about asking.
	red := ciRunTestServer(t, "failure")
	code, stdout, stderr := runForExit(t, []string{"-server", red, "ci", "watch", "ci-42", "--json"})
	if code != exitFailed {
		t.Errorf("grain ci watch on a red run = exit %d, want 1 (stderr %q)", code, stderr)
	}
	var run map[string]any
	if err := json.Unmarshal([]byte(stdout), &run); err != nil || run["id"] != "ci-42" {
		t.Errorf("grain ci watch on a red run printed %q, want the run document", stdout)
	}
}

func TestExitCodeMappingCovers0Through5(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"success", nil, 0},
		{"help", fmt.Errorf("wrapped: %w", flag.ErrHelp), 0},
		{"red CI run", targetFailed(errors.New("run ci-42: failure")), 1},
		{"5xx", &ui.StatusError{StatusCode: 500, Status: "500 Internal Server Error", Message: "boom"}, 1},
		{"503 from grain", &ui.UnavailableError{}, 1},
		{"unclassified", errors.New("decoding response from GET /api/tasks: unexpected EOF"), 1},
		{"unknown command", unknownCommandError{"frobnicate"}, 2},
		{"unknown flag", errors.New("flag provided but not defined: -frob"), 2},
		{"stray argument", noArgsFor("grain list", []string{"junk"}), 2},
		{"missing id", mustErr(taskID("")), 2},
		{"400", clientError(t, http.StatusBadRequest), 2},
		{"404", &ui.NotFoundError{ID: "9"}, 2},
		{"409", &ui.StatusError{StatusCode: 409, Status: "409 Conflict", Message: "already merged"}, 2},
		{"422", &ui.StatusError{StatusCode: 422, Status: "422 Unprocessable Entity", Message: "bad"}, 2},
		{"401", &ui.StatusError{StatusCode: 401, Status: "401 Unauthorized", Message: "who"}, 3},
		{"403", clientError(t, http.StatusForbidden), 3},
		{"401 over the wire", clientError(t, http.StatusUnauthorized), 3},
		{"500 over the wire", clientError(t, http.StatusInternalServerError), 1},
		{"sign-in page", errors.New("decoding response from GET /api/tasks: invalid character '<' looking for beginning of value"), 3},
		{"refused", fmt.Errorf("dial: %w", syscall.ECONNREFUSED), 4},
		{"reset", fmt.Errorf("read: %w", syscall.ECONNRESET), 4},
		{"dns", &net.DNSError{Err: "no such host", Name: "grain.invalid"}, 4},
		{"daemon down", &ui.DaemonUnavailableError{BaseURL: "http://x", Err: errors.New("eof")}, 4},
		{"https client to http server", errors.New(`Get "https://127.0.0.1:9000/api/tasks": http: server gave HTTP response to HTTPS client`), 4},
		{"call timeout", fmt.Errorf("get: %w", context.DeadlineExceeded), 4},
		{"explained refusal", explainDaemonError(fmt.Errorf("dial: %w", syscall.ECONNREFUSED), cliTarget{server: "http://x", source: "-server"}), 4},
		{"wait timeout", waitResult(ctx, fmt.Errorf("stopped waiting: %w", ctx.Err())), 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, code, _ := classifyError(tc.err); got != tc.want {
				t.Errorf("classifyError(%v) = %d (%s), want %d", tc.err, got, code, tc.want)
			}
		})
	}

	// A wait timeout survives the daemon-error explanation runCLI wraps
	// every command's error in, rather than reading as an unreachable
	// daemon.
	wrapped := explainDaemonError(waitResult(ctx, fmt.Errorf("x: %w", ctx.Err())), cliTarget{server: "http://x"})
	if got, _, _ := classifyError(wrapped); got != exitWaitTimedOut {
		t.Errorf("explained wait timeout = exit %d, want 5", got)
	}

	// Plain HTTP sent to an HTTPS port returns Go's 400 "Client sent an HTTP
	// request to an HTTPS server."; classify it as exit 4 (unreachable/TLS mismatch).
	tlsMismatchSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Client sent an HTTP request to an HTTPS server.", http.StatusBadRequest)
	}))
	defer tlsMismatchSrv.Close()
	_, tlsErr := ui.NewHTTPClient(tlsMismatchSrv.URL).ListTasks(ctx)
	if got, code, _ := classifyError(tlsErr); got != exitUnreachable || code != "daemon_unreachable" {
		t.Errorf("HTTP-to-HTTPS 400 = exit %d (%s), err=%v, want 4 (daemon_unreachable)", got, code, tlsErr)
	}
}

// clientError is what ui.HTTPClient returns for a daemon answering
// status, as the CLI receives it.
func clientError(t *testing.T, status int) error {
	t.Helper()
	c := ui.NewHTTPClient(statusServer(t, status))
	_, err := c.Config(context.Background())
	if err == nil {
		t.Fatalf("a %d answer returned no error", status)
	}
	return err
}

func mustErr(_ string, err error) error { return err }

// A 429 from the external-agent rate limit carries Retry-After; the
// CLI's error says it, so an agent reading -json knows when the same
// command will go through (docs/test-runs/2026-11-09-external-agent-rails.md, F4).
func TestRateLimitedErrorSaysWhenToRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, `{"error":"external agent \"x\" exceeded rate limit (60 requests/minute)"}`)
	}))
	t.Cleanup(srv.Close)
	code, _, stderr := runForExit(t, []string{"-token", testAccessCode, "-server", srv.URL, "list", "--json"})
	var got jsonError
	if err := json.Unmarshal([]byte(stderr), &got); err != nil {
		t.Fatalf("stderr = %q: %v", stderr, err)
	}
	if code != exitFailed || got.Error.Code != "rate_limited" || !strings.Contains(got.Error.Message, "retry in 17s") {
		t.Errorf("rate-limited grain list = exit %d %+v, want exit %d, rate_limited, naming \"retry in 17s\"", code, got.Error, exitFailed)
	}
}

// grain approve -reason and grain run-now -reason send the reason as
// X-Grain-Acting-Reason, the one place the agent API gate reads it: an
// external agent credential could not approve from the CLI at all
// without it (docs/test-runs/2026-11-09-external-agent-rails.md, F2).
func TestApproveAndRunNowSendTheirReason(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			mu.Lock()
			seen[r.URL.Path] = r.Header.Get(ui.ActingReasonHeader)
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"42","title":"t","state":"queued"}`)
	}))
	t.Cleanup(srv.Close)
	for _, verb := range []string{"approve", "run-now"} {
		runForExit(t, []string{"-token", "gma_test", "-server", srv.URL, verb, "-reason", "ship the fix\nnow", "42"})
	}
	runForExit(t, []string{"-token", "gma_test", "-server", srv.URL, "approve", "43"})
	mu.Lock()
	defer mu.Unlock()
	for path, want := range map[string]string{
		"/api/tasks/42/approve": "ship the fix now",
		"/api/tasks/42/run-now": "ship the fix now",
		"/api/tasks/43/approve": "",
	} {
		if got, ok := seen[path]; !ok || got != want {
			t.Errorf("POST %s sent %s %q (sent: %v), want %q", path, ui.ActingReasonHeader, got, ok, want)
		}
	}
}
