package custom

// The one error kind this protocol gives a backend a word for, and the
// only part of a Response that changes what the *deployment* does rather
// than what this run reports: `"kind": "usage_limit"` means the account
// behind the backend has no budget left, and
// orchestrator.RunDispatch answers an agent.UsageLimitError by pausing
// dispatch of that framework until the window resets (see
// docs/custom-agent-backends.md, and pkg/orchestrator/pause.go for what
// the pause is).
//
// Getting it wrong in either direction is expensive and silent. A limit
// read as an ordinary failure sends the whole queue at the same wall,
// one task at a time, each spending an attempt and a sandbox on it; an
// ordinary failure read as a limit stops a deployment that has nothing
// wrong with it. So every field the protocol document promises is
// asserted here against a backend that really is exec'd.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// backendSaying builds a backend command that prints response verbatim
// and exits with code, which is every shape this file needs: the
// protocol is a JSON document on stdout and nothing else.
func backendSaying(t *testing.T, response string, code int) *Framework {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "backend")
	script := "#!/bin/sh\ncat >/dev/null\nprintf '%s' " + shellQuote(response) + "\nexit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := New(Config{Command: path, MCPServer: func(context.Context, agent.RunConfig) (Command, error) {
		return Command{Command: "grain", Args: []string{"mcpserver"}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func shellQuote(s string) string {
	quoted := "'"
	for _, r := range s {
		if r == '\'' {
			quoted += `'\''`
			continue
		}
		quoted += string(r)
	}
	return quoted + "'"
}

// The documented shape, with the absolute instant: a limit, the
// backend's own message, the reset it named -- and the work the run had
// already done coming back beside it rather than instead of it, which is
// agent.Framework's contract and what lets a branch pushed before the
// refusal be salvaged.
func TestAUsageLimitKindIsReportedAsALimit(t *testing.T) {
	f := backendSaying(t, `{"protocol_version":2,`+
		`"result":{"final_text":"","tool_calls":[{"name":"run_command","text":"pushed"}],"transcript":"pushed the branch"},`+
		`"error":{"message":"provider refused the request","kind":"usage_limit","resume_at":"2026-09-09T00:00:00Z"}}`, 0)

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	limit, ok := agent.UsageLimit(err)
	if !ok {
		t.Fatalf("Run err = %v, want an agent.UsageLimitError", err)
	}
	if limit.Framework != "custom" {
		t.Errorf("Framework = %q, want custom -- a pause is scoped to the framework that met the limit", limit.Framework)
	}
	if limit.Message != "provider refused the request" {
		t.Errorf("Message = %q, want the backend's own words", limit.Message)
	}
	want := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	if !limit.ResetAt.Equal(want) {
		t.Errorf("ResetAt = %s, want %s", limit.ResetAt, want)
	}
	if resume, ok := limit.ResumeAt(want.Add(-time.Hour)); !ok || !resume.Equal(want) {
		t.Errorf("ResumeAt = (%s, %v), want the instant the backend named", resume, ok)
	}
	if result == nil || len(result.ToolCalls) != 1 {
		t.Fatalf("Result = %+v, want the work the run did before the refusal", result)
	}
}

// The other spelling the document allows, for a backend whose provider
// answers in relative terms.
func TestAUsageLimitCanNameADelayInstead(t *testing.T) {
	f := backendSaying(t, `{"protocol_version":2,"error":{"message":"out of quota",`+
		`"kind":"usage_limit","retry_after":"30m"}}`, 0)

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	limit, ok := agent.UsageLimit(err)
	if !ok {
		t.Fatalf("Run err = %v, want an agent.UsageLimitError", err)
	}
	if limit.RetryAfter != 30*time.Minute {
		t.Errorf("RetryAfter = %s, want 30m", limit.RetryAfter)
	}
	if !limit.ResetAt.IsZero() {
		t.Errorf("ResetAt = %s, want none: the backend named a delay, not an instant", limit.ResetAt)
	}
}

// A backend that wrote its limit and then exited non-zero -- the
// ordinary shape for a wrapper whose CLI failed -- is still a limit. The
// exit status says the run ended badly, which nobody disputes; the
// response says *why*, and that is the half the deployment can act on.
func TestAUsageLimitSurvivesANonZeroExit(t *testing.T) {
	f := backendSaying(t, `{"protocol_version":2,"error":{"message":"out of quota","kind":"usage_limit"}}`, 1)

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err == nil {
		t.Fatal("Run err = nil, want the limit the backend reported")
	} else if _, ok := agent.UsageLimit(err); !ok {
		t.Fatalf("Run err = %v, want the exit status not to bury the limit", err)
	}
}

// A limit that named no reset at all is still a limit. What it is not is
// a run that failed: the wait becomes the caller's own default
// (orchestrator's defaultUsageLimitPause), which is exactly what a bare
// 429 gets everywhere else -- and the same is true of a resume_at this
// backend spelled wrong, since a time nobody can parse is a time nobody
// named.
func TestAUsageLimitWithNoUsableResetIsStillALimit(t *testing.T) {
	for _, tc := range []struct{ name, payload string }{
		{"nothing named", `{"protocol_version":2,"error":{"message":"out of quota","kind":"usage_limit"}}`},
		{"an unparseable instant", `{"protocol_version":2,"error":{"message":"out of quota",` +
			`"kind":"usage_limit","resume_at":"tomorrow"}}`},
		{"an unparseable delay", `{"protocol_version":2,"error":{"message":"out of quota",` +
			`"kind":"usage_limit","retry_after":"half an hour"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := backendSaying(t, tc.payload, 0)
			_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
			limit, ok := agent.UsageLimit(err)
			if !ok {
				t.Fatalf("Run err = %v, want an agent.UsageLimitError", err)
			}
			if _, named := limit.ResumeAt(time.Now()); named {
				t.Error("the limit named a reset the backend did not give it")
			}
		})
	}
}

// And the direction that would stop a healthy deployment: every other
// error is an ordinary failure, whatever it says. Only the kind decides
// this -- a backend whose message happens to mention a quota is
// describing its run, not reporting the account's own ceiling.
func TestOnlyTheUsageLimitKindPausesTheDeployment(t *testing.T) {
	for _, tc := range []struct{ name, payload string }{
		{"no kind at all", `{"protocol_version":2,"error":{"message":"the tests failed"}}`},
		{"another kind", `{"protocol_version":2,"error":{"message":"bad configuration","kind":"invalid_request"}}`},
		{"an ordinary error that mentions a quota",
			`{"protocol_version":2,"error":{"message":"the log said: 429 you exceeded your current quota"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := backendSaying(t, tc.payload, 0)
			_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
			if err == nil {
				t.Fatal("Run err = nil, want the backend's failure")
			}
			var limit *agent.UsageLimitError
			if errors.As(err, &limit) {
				t.Errorf("Run err = %v, want an ordinary failure: nothing here is the protocol's usage_limit", err)
			}
		})
	}
}
