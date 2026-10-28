package mcp

import (
	"context"
	"strings"
	"testing"
)

func TestRunCommandSanitizesEnvironment(t *testing.T) {
	// Set various sensitive daemon / host environment variables.
	t.Setenv("GEMINI_API_KEY", "secret-gemini-key-12345")
	t.Setenv("GITHUB_TOKEN", "ghp_secrettoken98765")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/etc/secrets/sa-key.json")
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("GRAIN_DAEMON_SECRET", "daemon-internal-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret-key")
	t.Setenv("GCP_SERVICE_ACCOUNT", "sa-secret")

	// Set allowlisted environment variables.
	t.Setenv("USER", "testuser")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("GOMODCACHE", "/tmp/gomodcache")

	root := t.TempDir()
	client := newTestClient(t, root)
	ctx := context.Background()

	res, err := client.CallTool(ctx, "run_command", map[string]any{
		"command": "env | sed 's/=.*//' | sort\necho \"__VARS_END__\"\necho \"home=$HOME\"\necho \"user=$USER\"\necho \"term=$TERM\"\necho \"lang=$LANG\"\necho \"modcache=$GOMODCACHE\"",
	})
	if err != nil {
		t.Fatalf("run_command failed: %v", err)
	}
	if res.IsError {
		t.Fatalf("run_command reported error: %s", res.Text())
	}

	output := res.Text()

	// Extract stdout from formatRunCommandResult.
	_, stdoutWithRest, ok := strings.Cut(output, "stdout:\n")
	if !ok {
		t.Fatalf("expected stdout section in output:\n%s", output)
	}
	stdoutText, _, _ := strings.Cut(stdoutWithRest, "\nstderr:\n")

	allowed := map[string]bool{
		// What sandboxEnv composes.
		"HOME": true, "PATH": true, "USER": true, "LOGNAME": true,
		"SHELL": true, "TERM": true, "LANG": true, "LC_ALL": true,
		"LC_CTYPE": true, "TMPDIR": true, "GOMODCACHE": true,
		"GOCACHE": true, "npm_config_cache": true,
		// What bash sets for itself.
		"PWD": true, "OLDPWD": true, "SHLVL": true, "_": true,
	}

	parts := strings.Split(stdoutText, "\n__VARS_END__\n")
	if len(parts) != 2 {
		t.Fatalf("unexpected stdout format:\n%s", stdoutText)
	}

	for _, line := range strings.Split(parts[0], "\n") {
		name := strings.TrimSpace(line)
		if name == "" || allowed[name] {
			continue
		}
		t.Errorf("subprocess environment contains unexpected variable %q", name)
	}

	// Verify allowlisted variable values.
	vals := parts[1]
	if !strings.Contains(vals, "home="+root) {
		t.Errorf("expected home=%s, got:\n%s", root, vals)
	}
	if !strings.Contains(vals, "user=testuser") {
		t.Errorf("expected user=testuser, got:\n%s", vals)
	}
	if !strings.Contains(vals, "term=xterm-256color") {
		t.Errorf("expected term=xterm-256color, got:\n%s", vals)
	}
	if !strings.Contains(vals, "lang=en_US.UTF-8") {
		t.Errorf("expected lang=en_US.UTF-8, got:\n%s", vals)
	}
	if !strings.Contains(vals, "modcache=/tmp/gomodcache") {
		t.Errorf("expected modcache=/tmp/gomodcache, got:\n%s", vals)
	}

	// Verify sensitive daemon variables are scrubbed.
	sensitive := []string{
		"GEMINI_API_KEY",
		"secret-gemini-key-12345",
		"GITHUB_TOKEN",
		"ghp_secrettoken98765",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"DATABASE_URL",
		"GRAIN_DAEMON_SECRET",
		"AWS_SECRET_ACCESS_KEY",
		"GCP_SERVICE_ACCOUNT",
	}
	for _, s := range sensitive {
		if strings.Contains(output, s) {
			t.Errorf("sensitive variable or secret %q leaked into subprocess output:\n%s", s, output)
		}
	}
}

func TestSandboxEnvFallbackPath(t *testing.T) {
	t.Setenv("PATH", "")
	root := t.TempDir()
	env := sandboxEnv(root)
	found := false
	for _, e := range env {
		if e == "PATH="+sandboxFallbackPath {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected PATH=%s in sandboxEnv when PATH is empty, got: %v", sandboxFallbackPath, env)
	}
}
