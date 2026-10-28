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

package custom

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
)

func TestFrameworkRunsStateRepoCommand(t *testing.T) {
	state := t.TempDir()
	command := filepath.Join(state, "backend")
	script := `#!/bin/sh
request=$(cat)
case "$request" in
  *'"protocol_version":2'*'"prompt":"do the thing"'*'"model_proxy_url":"http://proxy.local"'*'"model_proxy_token":"scoped-token"'*) ;;
  *) echo "bad request: $request" >&2; exit 2 ;;
esac
printf '%s' '{"protocol_version":2,"result":{"final_text":"done","tool_calls":[],"transcript":"trace"}}'
`
	if err := os.WriteFile(command, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	released := 0
	framework, err := New(Config{
		Command: "backend", StateRepoDir: state, Model: "m", Effort: "high",
		ModelProxy: func(context.Context, agent.RunConfig) (string, string, func(), error) {
			return "http://proxy.local", "scoped-token", func() { released++ }, nil
		},
		MCPServer: func(context.Context, agent.RunConfig) (Command, error) {
			return Command{Command: "grain", Args: []string{"mcpserver"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := framework.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: state})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "done" || result.Transcript != "trace" {
		t.Fatalf("result = %#v", result)
	}
	if released != 1 {
		t.Fatalf("model proxy token released %d times, want once when the run ends", released)
	}
}

func TestFrameworkReportsInvalidOutput(t *testing.T) {
	state := t.TempDir()
	command := filepath.Join(state, "backend")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nprintf nope\n"), 0755); err != nil {
		t.Fatal(err)
	}
	framework, err := New(Config{Command: command, MCPServer: func(context.Context, agent.RunConfig) (Command, error) {
		return Command{Command: "grain"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = framework.Run(context.Background(), agent.RunConfig{SandboxRoot: state})
	if err == nil || !strings.Contains(err.Error(), "decoding response") {
		t.Fatalf("err = %v", err)
	}
}

func TestFrameworkCancelsProcessGroup(t *testing.T) {
	state := t.TempDir()
	command := filepath.Join(state, "backend")
	script := `#!/bin/sh
trap '' TERM INT
sleep 30 &
wait $!
`
	if err := os.WriteFile(command, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	framework, err := New(Config{
		Command: command,
		MCPServer: func(context.Context, agent.RunConfig) (Command, error) {
			return Command{Command: "grain"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = framework.Run(ctx, agent.RunConfig{SandboxRoot: state})
	if err == nil {
		t.Fatal("expected error on cancelled context, got nil")
	}
}

// The token a run is handed is released once the command has exited,
// whether the run succeeded or not -- and not before: the command must
// still be able to use it while it runs.
func TestFrameworkReleasesTheModelProxyTokenAfterTheCommandExits(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		wantErr      bool
	}{
		{"success", `printf '%s' '{"protocol_version":2,"result":{"final_text":"done"}}'`, false},
		{"failure", `echo broken >&2; exit 3`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			command := filepath.Join(state, "backend")
			marker := filepath.Join(state, "ran")
			script := "#!/bin/sh\ncat >/dev/null\ntouch " + marker + "\n" + tc.script + "\n"
			if err := os.WriteFile(command, []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			released := 0
			framework, err := New(Config{
				Command: command,
				ModelProxy: func(context.Context, agent.RunConfig) (string, string, func(), error) {
					return "http://proxy.local", "scoped-token", func() {
						if _, err := os.Stat(marker); err != nil {
							t.Errorf("token released before the command ran: %v", err)
						}
						released++
					}, nil
				},
				MCPServer: func(context.Context, agent.RunConfig) (Command, error) {
					return Command{Command: "grain"}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = framework.Run(context.Background(), agent.RunConfig{SandboxRoot: state})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if released != 1 {
				t.Fatalf("release called %d times, want 1", released)
			}
		})
	}
}

// A token minted before a later step of Run fails is still released.
func TestFrameworkReleasesTheModelProxyTokenWhenRunFailsEarly(t *testing.T) {
	state := t.TempDir()
	command := filepath.Join(state, "backend")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	released := false
	framework, err := New(Config{
		Command: command,
		ModelProxy: func(context.Context, agent.RunConfig) (string, string, func(), error) {
			return "http://proxy.local", "scoped-token", func() { released = true }, nil
		},
		MCPServer: func(context.Context, agent.RunConfig) (Command, error) {
			return Command{}, errors.New("no transport")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := framework.Run(context.Background(), agent.RunConfig{SandboxRoot: state}); err == nil {
		t.Fatal("expected the MCP server error")
	}
	if !released {
		t.Fatal("token was not released")
	}
}

// The backend is an executable an operator supplied; it must not see
// the provider credentials sitting in the daemon's own environment.
func TestFrameworkWithholdsTheDaemonsEnvironment(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "gemini-secret")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/secret/sa.json")
	t.Setenv("GRAIN_RELAY_TOKEN", "relay-secret")
	t.Setenv("LC_ALL", "C.UTF-8")
	state := t.TempDir()
	command := filepath.Join(state, "backend")
	dump := filepath.Join(state, "env")
	script := "#!/bin/sh\ncat >/dev/null\nenv > " + dump + "\n" +
		`printf '%s' '{"protocol_version":2,"result":{"final_text":"done"}}'` + "\n"
	if err := os.WriteFile(command, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	framework, err := New(Config{Command: command, MCPServer: func(context.Context, agent.RunConfig) (Command, error) {
		return Command{Command: "grain"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := framework.Run(context.Background(), agent.RunConfig{SandboxRoot: state}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	env := string(data)
	for _, secret := range []string{"GEMINI_API_KEY", "ANTHROPIC_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS", "GRAIN_RELAY_TOKEN"} {
		if strings.Contains(env, secret+"=") {
			t.Errorf("backend environment contains %s:\n%s", secret, env)
		}
	}
	for _, want := range []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C.UTF-8"} {
		if !strings.Contains(env, want) {
			t.Errorf("backend environment lacks %q:\n%s", want, env)
		}
	}
}

func TestFrameworkGivesTheBackendAHomeOfItsOwn(t *testing.T) {
	t.Setenv("GRAIN_CUSTOM_SENTINEL_SECRET", "do-not-leak")
	t.Setenv("DOCKER_HOST", "unix:///run/sentinel-docker.sock")
	daemonHome := t.TempDir()
	t.Setenv("HOME", daemonHome)
	state := t.TempDir()
	command := filepath.Join(state, "backend")
	envFile := filepath.Join(state, "env.out")
	homeFile := filepath.Join(state, "home.out")
	requestFile := filepath.Join(state, "request.out")
	script := `#!/bin/sh
cat > ` + requestFile + `
env > ` + envFile + `
printf '%s\n' "$HOME" > ` + homeFile + `
ls -A "$HOME" >> ` + homeFile + `
printf '%s' '{"protocol_version":2,"result":{"final_text":"done"}}'
`
	if err := os.WriteFile(command, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	framework, err := New(Config{Command: command, MCPServer: func(context.Context, agent.RunConfig) (Command, error) {
		return Command{Command: "grain", Env: []string{"KEEP=1"}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := framework.Run(context.Background(), agent.RunConfig{SandboxRoot: state}); err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"GRAIN_CUSTOM_SENTINEL_SECRET", "do-not-leak", "DOCKER_HOST=", "HOME=" + daemonHome + "\n"} {
		if strings.Contains(string(env), leaked) {
			t.Fatalf("backend environment contains %q:\n%s", leaked, env)
		}
	}
	home, err := os.ReadFile(homeFile)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(string(home)), "\n"); len(lines) != 1 || lines[0] == "" || lines[0] == daemonHome || lines[0] == state {
		t.Fatalf("backend HOME and its contents = %q, want an empty directory of the run's own", home)
	} else if _, err := os.Stat(lines[0]); !os.IsNotExist(err) {
		t.Fatalf("backend HOME %s survived the run: %v", lines[0], err)
	}
	request, err := os.ReadFile(requestFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"KEEP=1"`, `"DOCKER_HOST=unix:///run/sentinel-docker.sock"`, `"HOME=` + daemonHome + `"`} {
		if !strings.Contains(string(request), want) {
			t.Fatalf("mcp_server.env lacks %s: %s", want, request)
		}
	}
	if strings.Contains(string(request), "do-not-leak") {
		t.Fatalf("request carries the sentinel variable: %s", request)
	}
}

// The daemon exports the path of its local API token as GRAIN_LOCAL_API_TOKEN_FILE, and the grain mcpserver a
// custom backend starts needs it to call that daemon back: the backend's
// own environment is filtered (commandEnv), so it has to travel in
// mcp_server.env (grain/task-2380, docs/test-runs/2026-10-07-loopback-
// trust.md finding 1).
func TestFrameworkHandsTheMCPServerTheLocalAPITokenPath(t *testing.T) {
	t.Setenv("GRAIN_LOCAL_API_TOKEN_FILE", "/var/lib/grain/data/local-api-token")
	state := t.TempDir()
	command := filepath.Join(state, "backend")
	requestFile := filepath.Join(state, "request.out")
	script := `#!/bin/sh
cat > ` + requestFile + `
printf '%s' '{"protocol_version":2,"result":{"final_text":"done"}}'
`
	if err := os.WriteFile(command, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	framework, err := New(Config{Command: command, MCPServer: func(context.Context, agent.RunConfig) (Command, error) {
		return Command{Command: "grain"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := framework.Run(context.Background(), agent.RunConfig{SandboxRoot: state}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(requestFile)
	if err != nil {
		t.Fatal(err)
	}
	var request Request
	if err := json.Unmarshal(data, &request); err != nil {
		t.Fatalf("decoding request: %v\n%s", err, data)
	}
	if !slices.Contains(request.MCPServer.Env, "GRAIN_LOCAL_API_TOKEN_FILE=/var/lib/grain/data/local-api-token") {
		t.Fatalf("mcp_server.env = %q, want it to carry GRAIN_LOCAL_API_TOKEN_FILE", request.MCPServer.Env)
	}
}

func TestUsageLimitReadsResumeAtAndRetryAfter(t *testing.T) {
	resetAt := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		err        ResponseError
		resetAt    time.Time
		retryAfter time.Duration
		message    string
	}{
		{name: "resume_at", err: ResponseError{ResumeAt: "2026-09-09T00:00:00Z"}, resetAt: resetAt, message: "quota gone"},
		{name: "retry_after", err: ResponseError{RetryAfter: "30m"}, retryAfter: 30 * time.Minute, message: "quota gone"},
		{name: "both", err: ResponseError{ResumeAt: "2026-09-09T00:00:00Z", RetryAfter: "30m"}, resetAt: resetAt, retryAfter: 30 * time.Minute, message: "quota gone"},
		{name: "unreadable resume_at", err: ResponseError{ResumeAt: "tomorrow"}, message: `quota gone (backend sent an unreadable resume_at "tomorrow")`},
		{name: "unreadable retry_after", err: ResponseError{RetryAfter: "30 minutes"}, message: `quota gone (backend sent an unreadable retry_after "30 minutes")`},
		{name: "negative retry_after", err: ResponseError{RetryAfter: "-5m"}, message: `quota gone (backend sent an unreadable retry_after "-5m")`},
		{name: "both unreadable", err: ResponseError{ResumeAt: "tomorrow", RetryAfter: "soon"}, message: `quota gone (backend sent an unreadable resume_at "tomorrow"; backend sent an unreadable retry_after "soon")`},
		{name: "unreadable resume_at beside a readable retry_after", err: ResponseError{ResumeAt: "tomorrow", RetryAfter: "30m"}, retryAfter: 30 * time.Minute, message: `quota gone (backend sent an unreadable resume_at "tomorrow")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limit := usageLimit("quota gone", &tc.err)
			if !limit.ResetAt.Equal(tc.resetAt) || limit.RetryAfter != tc.retryAfter || limit.Message != tc.message {
				t.Fatalf("limit = %#v", limit)
			}
		})
	}
	if limit := usageLimit("", &ResponseError{ResumeAt: "tomorrow"}); limit.Message != `(backend sent an unreadable resume_at "tomorrow")` {
		t.Fatalf("message with no backend text = %q", limit.Message)
	}
}

func TestFrameworkKeepsAMalformedUsageLimitAUsageLimit(t *testing.T) {
	state := t.TempDir()
	command := filepath.Join(state, "backend")
	script := `#!/bin/sh
cat >/dev/null
printf '%s' '{"protocol_version":2,"error":{"message":"quota gone","kind":"usage_limit","resume_at":"tomorrow"}}'
`
	if err := os.WriteFile(command, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	framework, err := New(Config{Command: command, MCPServer: func(context.Context, agent.RunConfig) (Command, error) {
		return Command{Command: "grain"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = framework.Run(context.Background(), agent.RunConfig{SandboxRoot: state})
	var limit *agent.UsageLimitError
	if !errors.As(err, &limit) {
		t.Fatalf("err = %v, want a usage limit", err)
	}
	if !limit.ResetAt.IsZero() || !strings.Contains(limit.Message, `unreadable resume_at "tomorrow"`) {
		t.Fatalf("limit = %#v", limit)
	}
}
