package mcp

import (
	"context"
	"strings"
	"testing"
)

// What an operator opts a host sandbox into with -host-sandbox-env reaches
// run_command, and nothing else does: a variable outside the allowlist the
// daemon happens to have stays out unless named, and HOME stays the
// sandbox root whatever extra says.
func TestSandboxToolsExtraEnvAddsOnlyWhatWasNamed(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/tmp/daemon-agent.sock")
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")
	root := t.TempDir()

	run := func(tools []Tool) string {
		t.Helper()
		res := tools[0].Handler(context.Background(), map[string]any{
			"command": `echo "sock=$SSH_AUTH_SOCK"; echo "proxy=$HTTPS_PROXY"; echo "gcloud=$CLOUDSDK_CONFIG"; echo "home=$HOME"`,
		})
		if res.IsError {
			t.Fatalf("run_command: %s", res.Text)
		}
		return res.Text
	}

	plain := run(NewSandboxTools(root))
	for _, want := range []string{"sock=\n", "proxy=\n", "home=" + root} {
		if !strings.Contains(plain, want) {
			t.Errorf("NewSandboxTools output missing %q:\n%s", want, plain)
		}
	}

	opted := run(NewSandboxTools(root, []string{
		"SSH_AUTH_SOCK=/tmp/daemon-agent.sock",
		"CLOUDSDK_CONFIG=/home/me/.config/gcloud",
		"HOME=/home/me",
	}...))
	// The values reach the command, and come back out of it redacted
	// under their own names (GSA1-12): the agent sees which variable is
	// set, never what it holds.
	for _, want := range []string{
		"sock=[redacted: sandbox env SSH_AUTH_SOCK]\n",
		"gcloud=[redacted: sandbox env CLOUDSDK_CONFIG]\n",
		"proxy=\n",
		"home=" + root,
	} {
		if !strings.Contains(opted, want) {
			t.Errorf("NewSandboxTools with extra env output missing %q:\n%s", want, opted)
		}
	}
}

func TestValidateSandboxEnv(t *testing.T) {
	for _, ok := range [][]string{
		nil,
		{"SSH_AUTH_SOCK=/run/agent.sock"},
		{"no_proxy=", "HTTPS_PROXY=http://user:pw@proxy:3128"},
		{"CLOUDSDK_CONFIG=/home/me/.config/gcloud"},
	} {
		if err := ValidateSandboxEnv(ok); err != nil {
			t.Errorf("ValidateSandboxEnv(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range [][]string{
		{"SSH_AUTH_SOCK"},
		{"=value"},
		{"1ABC=x"},
		{"A-B=x"},
		{"HOME=/home/me"},
	} {
		if err := ValidateSandboxEnv(bad); err == nil {
			t.Errorf("ValidateSandboxEnv(%q) = nil, want an error", bad)
		}
	}
}

// run_command in a sandbox carrying its operator's environment cuts every
// value passed through, wherever the command prints it, and the token
// shapes a login it carries hands out (gcloud's ya29. access token, a
// GitHub token), before the output reaches the agent -- and so before it
// reaches a stored transcript (GSA1-12).
func TestSandboxToolsExtraEnvRedactsOutput(t *testing.T) {
	root := t.TempDir()
	tools := NewSandboxTools(root,
		"HTTPS_PROXY=http://alice:hunter2-proxy-pw@proxy.example:3128",
		"GRAIN_TEST_TOKEN=shapeless-secret-value",
	)
	res := tools[0].Handler(context.Background(), map[string]any{
		"command": `env; echo "$GRAIN_TEST_TOKEN" >&2; ` +
			`echo ya29.a0AfB_byC1234567890abcdefghijklmnop; echo gho_abcdefghijklmnopqrstuvwxyz0123`,
	})
	if res.IsError {
		t.Fatalf("run_command: %s", res.Text)
	}
	for _, leaked := range []string{"hunter2-proxy-pw", "shapeless-secret-value", "ya29.a0AfB", "gho_abcdefghij"} {
		if strings.Contains(res.Text, leaked) {
			t.Errorf("run_command output carries %q:\n%s", leaked, res.Text)
		}
	}
	for _, want := range []string{
		"HTTPS_PROXY=[redacted: sandbox env HTTPS_PROXY]",
		"[redacted: sandbox env GRAIN_TEST_TOKEN]",
		"[redacted: google oauth token]",
		"[redacted: github token]",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("run_command output missing %q:\n%s", want, res.Text)
		}
	}

	// A sandbox carrying nothing of its operator's is left as it prints:
	// redaction is for the runs whose environment holds somebody's
	// identity, not a rewrite of every command's output.
	plain := NewSandboxTools(root)[0].Handler(context.Background(), map[string]any{
		"command": `echo shapeless-secret-value`,
	})
	if !strings.Contains(plain.Text, "shapeless-secret-value") {
		t.Errorf("run_command with no extra env changed its output:\n%s", plain.Text)
	}
}
