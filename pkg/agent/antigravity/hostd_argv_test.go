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

package antigravity

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/hostd"
)

// The argv and environment agy is started with, exactly as Run builds
// them, pass grain hostd's checks for an entry shaped like the one the
// installer writes (design/design/cli-run-location.design.md, "What a host-run agy
// is sent"): every path in them sits under the data dir, and the four
// variables are the ones the entry names. hostd's checks refuse anything
// with a path outside paths_under, a ".." or a "~", so this is what keeps
// them from refusing agy itself.
func TestTheArgvAndEnvRunBuildsPassHostdsChecks(t *testing.T) {
	data, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Staging, the private HOME among it, goes under the data dir, as it
	// does behind hostd.
	t.Setenv("TMPDIR", data)
	sandbox := filepath.Join(data, "sandboxes", "task-1")
	if err := os.MkdirAll(sandbox, 0o755); err != nil {
		t.Fatal(err)
	}
	allow, err := hostd.ParseAllowlist([]byte(strings.ReplaceAll(`
clis:
  agy:
    path: /usr/local/bin/agy
    paths_under: [DATA]
    cwd_under: [DATA]
    env: [HOME, AGY_CLI_DISABLE_AUTO_UPDATE, GEMINI_API_KEY, GOOGLE_API_KEY]
`, "DATA", data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		opts []Option
	}{
		{"ambient identity", []Option{WithModel("gemini-3.1-pro")}},
		{"api key", []Option{WithModel("gemini-3.1-pro"), WithAPIKey("AIza-test-key")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &recordingRunner{stdout: okStream()}
			// hostd refuses a path that does not exist, so the check runs
			// as agy starts, while what Run staged is there.
			var checkErr error
			r.onRun = func(args, env []string, dir string) {
				req := &hostd.Request{Kind: hostd.KindCLI, Name: "agy", Args: args, Env: env, Dir: dir}
				if _, err := allow.Check(req, []string{"PATH=/usr/bin", "HOME=/home/me"}); err != nil && checkErr == nil {
					checkErr = err
				}
			}
			f := newFramework(r, "/usr/local/bin/grain", tc.opts...)
			if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "fix /etc/hosts handling, see ../README", SandboxRoot: sandbox}); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(r.args) == 0 {
				t.Fatal("agy was never started")
			}
			if !slices.Contains(r.args, sandbox) || !slices.ContainsFunc(r.env, func(kv string) bool { return strings.HasPrefix(kv, "HOME="+data+"/") }) {
				t.Fatalf("args %q, env %q: want --add-dir %s and a HOME under %s", r.args, r.env, sandbox, data)
			}
			if checkErr != nil {
				t.Errorf("hostd refused agy as Run starts it: %v\nargs %q\nenv %q\ndir %q", checkErr, r.args, r.env, r.dir)
			}
		})
	}
}

func TestTheArgvAndEnvCatalogOnBuildsPassHostdsChecks(t *testing.T) {
	data, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", data)
	allow, err := hostd.ParseAllowlist([]byte(strings.ReplaceAll(`
clis:
  agy:
    path: /opt/agy/bin/agy
    paths_under: [DATA]
    cwd_under: [DATA]
    env: [HOME, AGY_CLI_DISABLE_AUTO_UPDATE, GEMINI_API_KEY, GOOGLE_API_KEY]
`, "DATA", data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		apiKey string
	}{
		{"ambient identity", ""},
		{"api key", "AIza-test-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := &fakeCatalogHost{
				out: map[string]string{
					"--help": agyHelpOutput,
					"models": agyModelsOutput,
				},
				err: map[string]error{},
			}
			checkErrs := map[string]error{}
			host.onRun = func(c agent.Command) {
				req := &hostd.Request{Kind: hostd.KindCLI, Name: "agy", Args: c.Args, Env: c.Env}
				_, checkErrs[strings.Join(c.Args, " ")] = allow.Check(req, []string{"PATH=/usr/bin", "HOME=/home/me"})
			}
			if _, err := CatalogOn(context.Background(), host, "/opt/agy/bin/agy", tc.apiKey); err != nil {
				t.Fatalf("CatalogOn: %v", err)
			}
			if len(host.args) != 2 {
				t.Fatalf("host.Run calls = %d, want 2 (--help and models)", len(host.args))
			}
			for _, args := range host.args {
				env := host.envs[strings.Join(args, " ")]
				if err := checkErrs[strings.Join(args, " ")]; err != nil {
					t.Errorf("hostd refused agy as CatalogOn starts it (%v): %v\nenv %q", args, err, env)
				}
			}
		})
	}
}
