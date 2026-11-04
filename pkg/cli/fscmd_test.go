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
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/nsclient"
	"github.com/ai-on-gke/grain/pkg/sandboxcli"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// fsAsked is one namespace request, with the prefix it came under (/fs
// from the sandbox CLI, /api/fs from grain fs) taken off.
type fsAsked struct {
	prefix, path, query, rng string
	authorized               bool
}

// fakeNamespaceServer answers the namespace under both /fs/ and
// /api/fs/ alike, as the sandbox proxy and the daemon do (both are
// namespace.Server), and records what each was asked.
func fakeNamespaceServer(t *testing.T) (*httptest.Server, func() []fsAsked) {
	t.Helper()
	var mu sync.Mutex
	var asked []fsAsked
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := nsclient.SandboxPrefix
		if strings.HasPrefix(r.URL.Path, nsclient.APIPrefix+"/") {
			prefix = nsclient.APIPrefix
		}
		rest := strings.TrimPrefix(r.URL.Path, prefix)
		mu.Lock()
		asked = append(asked, fsAsked{prefix: prefix, path: rest, query: r.URL.RawQuery, rng: r.Header.Get("Range"),
			authorized: r.Header.Get("Authorization") != ""})
		mu.Unlock()
		q := r.URL.Query()
		switch {
		case strings.HasPrefix(rest, "/ci/acme/secret"):
			http.Error(w, "forbidden: ci://acme/secret is not yours to read", http.StatusForbidden)
		case q.Get("grep") == "nowhere":
			w.Header().Set("X-Grain-Walk-Truncated", "true")
			w.Header().Set("X-Grain-Matches", "0")
			w.Write([]byte("[truncated: walked 2000 entries]\n"))
		case q.Get("grep") != "":
			w.Header().Set("X-Grain-Matches", "1")
			w.Write([]byte("run-1/go/Test.log:3:FAIL: TestThing\n"))
		case q.Has("list"):
			w.Write([]byte("run-1/\nrun-2/\n"))
		default:
			w.Header().Set("X-Grain-Total-Size", "24")
			w.Write([]byte("line one\nline two\nFAIL\n"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []fsAsked {
		mu.Lock()
		defer mu.Unlock()
		out := asked
		asked = nil
		return out
	}
}

// TestGrainFsLsCatReadGrepSharesClientWithSandboxCLI holds grain fs to a
// sandbox's grain ls, read and grep (#XC-25): each pair is asked the
// same namespace path with the same query -- an inline ?query in the URI
// included -- and prints the same answer, with read an exact alias for
// cat. Only where it goes differs: /fs/ on the sandbox proxy, /api/fs/
// on the daemon.
func TestGrainFsLsCatReadGrepSharesClientWithSandboxCLI(t *testing.T) {
	srv, takeAsked := fakeNamespaceServer(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GRAIN_SERVER", "")
	t.Setenv("GRAIN_SANDBOX_CLI", "")
	t.Setenv("GRAIN_SANDBOX_PROXY", srv.URL)
	t.Setenv("GRAIN_SANDBOX_TOKEN", "sandbox-token")

	sandbox := func(args ...string) (string, string, int) {
		var out, errb bytes.Buffer
		code, handled := sandboxcli.Main(args, &out, &errb)
		if !handled {
			t.Fatalf("the sandbox CLI did not take %q", args)
		}
		return out.String(), errb.String(), code
	}
	client := ui.NewHTTPClient(srv.URL)
	client.Token = "operator-token"
	external := func(args ...string) (string, error) {
		var out, errb bytes.Buffer
		err := runFS(context.Background(), client, args, false, &out, &errb)
		return out.String(), err
	}

	for _, tc := range []struct {
		name     string
		sandbox  []string
		external [][]string
		want     string
	}{
		{
			name:     "ls",
			sandbox:  []string{"ls", "ci://acme/widgets", "--glob", "run-*"},
			external: [][]string{{"ls", "ci://acme/widgets", "--glob", "run-*"}},
			want:     "run-1/\nrun-2/\n",
		},
		{
			name:     "read and cat, with an inline query",
			sandbox:  []string{"read", "ci://acme/widgets/run-1/go/Test.log?tail=24", "--lines", "1"},
			external: [][]string{{"cat", "ci://acme/widgets/run-1/go/Test.log?tail=24", "--lines", "1"}, {"read", "ci://acme/widgets/run-1/go/Test.log?tail=24", "--lines", "1"}},
			want:     "FAIL\n",
		},
		{
			name:     "read by bytes",
			sandbox:  []string{"read", "task://7/record.md", "--offset", "5", "--limit", "8"},
			external: [][]string{{"cat", "task://7/record.md", "--offset", "5", "--limit", "8"}},
			want:     "line one\nline two\nFAIL\n",
		},
		{
			name:     "grep",
			sandbox:  []string{"grep", "FAIL", "ci://acme/widgets?glob=*.log", "-i", "-C", "2", "--max", "5"},
			external: [][]string{{"grep", "FAIL", "ci://acme/widgets?glob=*.log", "-i", "-C", "2", "--max", "5"}, {"grep", "-n", "FAIL", "ci://acme/widgets?glob=*.log", "-i", "-C", "2", "--max", "5"}},
			want:     "run-1/go/Test.log:3:FAIL: TestThing\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			takeAsked()
			out, errOut, code := sandbox(tc.sandbox...)
			if code != 0 || out != tc.want {
				t.Fatalf("sandbox %q: exit %d, printed %q (stderr %q), want %q", tc.sandbox, code, out, errOut, tc.want)
			}
			sandboxAsked := takeAsked()
			if len(sandboxAsked) != 1 || sandboxAsked[0].prefix != nsclient.SandboxPrefix || !sandboxAsked[0].authorized {
				t.Fatalf("sandbox %q asked %+v, want one authorized /fs/ request", tc.sandbox, sandboxAsked)
			}
			for _, args := range tc.external {
				out, err := external(args...)
				if err != nil || out != tc.want {
					t.Fatalf("grain fs %q: printed %q, err %v, want %q", args, out, err, tc.want)
				}
				got := takeAsked()
				if len(got) != 1 || got[0].prefix != nsclient.APIPrefix || !got[0].authorized {
					t.Fatalf("grain fs %q asked %+v, want one authorized /api/fs/ request", args, got)
				}
				if got[0].path != sandboxAsked[0].path || got[0].query != sandboxAsked[0].query || got[0].rng != sandboxAsked[0].rng {
					t.Errorf("grain fs %q asked %s?%s, the sandbox CLI %s?%s", args,
						got[0].path, got[0].query, sandboxAsked[0].path, sandboxAsked[0].query)
				}
			}
		})
	}

	// The inline query reached the namespace as a query, not as a path.
	takeAsked()
	external("cat", "ci://acme/widgets/run-1/log?tail=24")
	if got := takeAsked(); len(got) != 1 || got[0].path != "/ci/acme/widgets/run-1/log" || got[0].query != "tail=24" {
		t.Errorf("an inline ?tail= was asked as %+v", got)
	}

	// A refusal is the namespace's own words on both.
	_, errOut, code := sandbox("read", "ci://acme/secret/run-1/log")
	if code != 1 || !strings.Contains(errOut, "not yours to read") {
		t.Errorf("sandbox refusal: exit %d, stderr %q", code, errOut)
	}
	_, err := external("cat", "ci://acme/secret/run-1/log")
	var forbidden *ui.ForbiddenError
	if !errors.As(err, &forbidden) || !strings.Contains(err.Error(), "not yours to read") {
		t.Errorf("grain fs refusal: %v (%T), want the namespace's 403 as a ForbiddenError", err, err)
	}

	// A grep the walk cap cut short before it found anything fails on both.
	if _, errOut, code := sandbox("grep", "nowhere", "src://pkg"); code != 1 || !strings.Contains(errOut, "not searched") {
		t.Errorf("sandbox truncated grep: exit %d, stderr %q", code, errOut)
	}
	_, err = external("grep", "nowhere", "src://pkg")
	if exit, _, _ := classifyError(err); exit != exitFailed || !strings.Contains(err.Error(), "not searched") {
		t.Errorf("grain fs truncated grep: %v (exit %d), want exit %d", err, exit, exitFailed)
	}

	// A command fs does not have is a usage error.
	if _, err := external("rm", "ci://acme/widgets"); err == nil {
		t.Error("grain fs rm was taken")
	} else if exit, _, _ := classifyError(err); exit != exitUsage {
		t.Errorf("grain fs rm: exit %d, want %d", exit, exitUsage)
	}
}
