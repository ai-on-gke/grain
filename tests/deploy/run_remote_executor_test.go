package deploy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRemoteExecutorIsSyntacticallyValidBash(t *testing.T) {
	t.Parallel()
	scriptPath := filepath.Join(repoRoot(t), "scripts", "run-remote-executor.sh")
	out, err := exec.Command("bash", "-n", scriptPath).CombinedOutput()
	if err != nil {
		t.Fatalf("bash -n scripts/run-remote-executor.sh: %v\n%s", err, out)
	}

	helpOut, err := exec.Command("bash", scriptPath, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("scripts/run-remote-executor.sh --help: %v\n%s", err, helpOut)
	}
	if !strings.Contains(string(helpOut), "run-remote-executor.sh [options]") {
		t.Errorf("expected usage in --help output, got:\n%s", helpOut)
	}
}

func TestBootstrapExecutorSymlink(t *testing.T) {
	t.Parallel()
	symlinkPath := filepath.Join(repoRoot(t), "scripts", "bootstrap-executor.sh")
	target, err := os.Readlink(symlinkPath)
	if err != nil {
		t.Fatalf("reading symlink %s: %v", symlinkPath, err)
	}
	if target != "run-remote-executor.sh" {
		t.Errorf("expected symlink to point to run-remote-executor.sh, got %q", target)
	}
}

func TestGrainRemoteExecutorSymlink(t *testing.T) {
	t.Parallel()
	symlinkPath := filepath.Join(repoRoot(t), "scripts", "grain-remote-executor.sh")
	target, err := os.Readlink(symlinkPath)
	if err != nil {
		t.Fatalf("reading symlink %s: %v", symlinkPath, err)
	}
	if target != "run-remote-executor.sh" {
		t.Errorf("expected symlink to point to run-remote-executor.sh, got %q", target)
	}
}

type scriptResult struct {
	stdout string
	stderr string
	err    error
	args   []string
}

func runExecutorScript(t *testing.T, extraEnv []string, scriptArgs ...string) scriptResult {
	t.Helper()
	dir := t.TempDir()

	recordFile := filepath.Join(dir, "grain-args.txt")
	stubGrain := filepath.Join(dir, "grain")
	stubGrainScript := `#!/usr/bin/env bash
printf '%s\n' "$@" > "` + recordFile + `"
exit 0
`
	if err := os.WriteFile(stubGrain, []byte(stubGrainScript), 0o755); err != nil {
		t.Fatalf("writing stub grain: %v", err)
	}

	scriptPath := filepath.Join(repoRoot(t), "scripts", "run-remote-executor.sh")
	cmd := exec.Command("bash", append([]string{scriptPath}, scriptArgs...)...)

	baseEnv := []string{
		"PATH=" + dir + ":" + os.Getenv("PATH"),
		"GRAIN_BIN=" + stubGrain,
		"HOME=" + dir,
	}
	cmd.Env = append(baseEnv, extraEnv...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	var recordedArgs []string
	if data, readErr := os.ReadFile(recordFile); readErr == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line != "" {
				recordedArgs = append(recordedArgs, line)
			}
		}
	}

	return scriptResult{
		stdout: stdout.String(),
		stderr: stderr.String(),
		err:    err,
		args:   recordedArgs,
	}
}

func TestRunRemoteExecutorRequiresServerAndToken(t *testing.T) {
	t.Parallel()

	// Missing server and token
	res := runExecutorScript(t, nil)
	if res.err == nil {
		t.Errorf("expected error when server is missing, got success")
	}
	if !strings.Contains(res.stderr, "server address is required") {
		t.Errorf("expected server address error message, got: %s", res.stderr)
	}

	// Missing token
	res = runExecutorScript(t, nil, "--server", "127.0.0.1:8422")
	if res.err == nil {
		t.Errorf("expected error when token is missing, got success")
	}
	if !strings.Contains(res.stderr, "token is required") {
		t.Errorf("expected token error message, got: %s", res.stderr)
	}
}

func TestRunRemoteExecutorArgumentConstructionAndDiscovery(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	writeStub := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/usr/bin/env bash\necho stub\n"), 0o755); err != nil {
			t.Fatalf("writing stub %s: %v", name, err)
		}
		return p
	}

	agyPath := writeStub("agy")
	claudePath := writeStub("claude")
	codexPath := writeStub("codex")

	env := []string{
		"GRAIN_AGY_PATH=" + agyPath,
		"GRAIN_CLAUDE_PATH=" + claudePath,
		"GRAIN_CODEX_PATH=" + codexPath,
	}

	res := runExecutorScript(t, env,
		"--server", "remote.grain.test:8422",
		"--token", "super-secret-token",
		"--team", "infrastructure",
		"--name", "worker-01",
		"--concurrency", "4",
		"--instance-id", "custom-instance-999",
		"--grain-url", "http://remote.grain.test:8420",
		"--iap-audience", "1234567890-iap.apps.googleusercontent.com",
		"--tls",
	)

	if res.err != nil {
		t.Fatalf("script failed: %v\nstdout:\n%s\nstderr:\n%s", res.err, res.stdout, res.stderr)
	}

	if len(res.args) == 0 {
		t.Fatalf("no arguments recorded for grain execution")
	}

	if res.args[0] != "executor" {
		t.Errorf("expected first argument to be 'executor', got %q", res.args[0])
	}

	hasArg := func(flag, val string) bool {
		for i, a := range res.args {
			if a == flag && i+1 < len(res.args) && res.args[i+1] == val {
				return true
			}
		}
		return false
	}

	hasFlag := func(flag string) bool {
		for _, a := range res.args {
			if a == flag {
				return true
			}
		}
		return false
	}

	if !hasArg("-server", "remote.grain.test:8422") {
		t.Errorf("missing or incorrect -server arg in %v", res.args)
	}
	if !hasArg("-team", "infrastructure") {
		t.Errorf("missing or incorrect -team arg in %v", res.args)
	}
	if !hasArg("-name", "worker-01") {
		t.Errorf("missing or incorrect -name arg in %v", res.args)
	}
	if !hasArg("-concurrency", "4") {
		t.Errorf("missing or incorrect -concurrency arg in %v", res.args)
	}
	if !hasArg("-instance-id", "custom-instance-999") {
		t.Errorf("missing or incorrect -instance-id arg in %v", res.args)
	}
	if !hasArg("-grain-url", "http://remote.grain.test:8420") {
		t.Errorf("missing or incorrect -grain-url arg in %v", res.args)
	}
	if !hasArg("-iap-audience", "1234567890-iap.apps.googleusercontent.com") {
		t.Errorf("missing or incorrect -iap-audience arg in %v", res.args)
	}
	if !hasFlag("-tls") {
		t.Errorf("missing -tls flag in %v", res.args)
	}

	// Verify token file was created and contains the secret token
	var tokenFile string
	for i, a := range res.args {
		if a == "-token-file" && i+1 < len(res.args) {
			tokenFile = res.args[i+1]
			break
		}
	}
	if tokenFile == "" {
		t.Fatalf("missing -token-file in %v", res.args)
	}
	tokenBytes, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("reading token file %s: %v", tokenFile, err)
	}
	if string(tokenBytes) != "super-secret-token" {
		t.Errorf("token file content = %q, want %q", string(tokenBytes), "super-secret-token")
	}

	// Verify binary flags
	hasBinary := func(prefix string) bool {
		for i, a := range res.args {
			if a == "-binary" && i+1 < len(res.args) && strings.HasPrefix(res.args[i+1], prefix) {
				return true
			}
		}
		return false
	}

	if !hasBinary("grain=") {
		t.Errorf("missing -binary grain=... in %v", res.args)
	}
	if !hasBinary("agy=" + agyPath) {
		t.Errorf("missing -binary agy=%s in %v", agyPath, res.args)
	}
	if !hasBinary("claude=" + claudePath) {
		t.Errorf("missing -binary claude=%s in %v", claudePath, res.args)
	}
	if !hasBinary("codex=" + codexPath) {
		t.Errorf("missing -binary codex=%s in %v", codexPath, res.args)
	}
}

func TestRunRemoteExecutorEnvironmentVariables(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	tokenPath := filepath.Join(dir, "env-token.txt")
	if err := os.WriteFile(tokenPath, []byte("env-token-content"), 0o600); err != nil {
		t.Fatalf("writing token: %v", err)
	}

	env := []string{
		"GRAIN_SERVER=env.grain.internal:9000",
		"GRAIN_TOKEN_FILE=" + tokenPath,
		"GRAIN_TEAM=qa-team",
		"GRAIN_NAME=env-worker",
		"GRAIN_CONCURRENCY=3",
		"GRAIN_INSTANCE_ID=inst-env-777",
		"GRAIN_IAP_AUDIENCE=iap-env-aud-12345",
		"GRAIN_TLS=1",
	}

	res := runExecutorScript(t, env)
	if res.err != nil {
		t.Fatalf("script failed: %v\nstdout:\n%s\nstderr:\n%s", res.err, res.stdout, res.stderr)
	}

	hasArg := func(flag, val string) bool {
		for i, a := range res.args {
			if a == flag && i+1 < len(res.args) && res.args[i+1] == val {
				return true
			}
		}
		return false
	}

	if !hasArg("-server", "env.grain.internal:9000") {
		t.Errorf("expected -server from env, got %v", res.args)
	}
	if !hasArg("-token-file", tokenPath) {
		t.Errorf("expected -token-file from env, got %v", res.args)
	}
	if !hasArg("-team", "qa-team") {
		t.Errorf("expected -team from env, got %v", res.args)
	}
	if !hasArg("-name", "env-worker") {
		t.Errorf("expected -name from env, got %v", res.args)
	}
	if !hasArg("-concurrency", "3") {
		t.Errorf("expected -concurrency from env, got %v", res.args)
	}
	if !hasArg("-instance-id", "inst-env-777") {
		t.Errorf("expected -instance-id from env, got %v", res.args)
	}
	if !hasArg("-iap-audience", "iap-env-aud-12345") {
		t.Errorf("expected -iap-audience from env, got %v", res.args)
	}
}

func TestRunRemoteExecutorMetadataAutoDetection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	tokenPath := filepath.Join(dir, "env-token.txt")
	if err := os.WriteFile(tokenPath, []byte("env-token-content"), 0o600); err != nil {
		t.Fatalf("writing token: %v", err)
	}

	mockMetadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata-Flavor") != "Google" {
			http.Error(w, "missing flavor", http.StatusBadRequest)
			return
		}
		if strings.Contains(r.URL.Path, "instance/attributes/iap-audience") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("auto-detected-iap-aud-777"))
			return
		}
		http.NotFound(w, r)
	}))
	defer mockMetadata.Close()

	host, port, _ := net.SplitHostPort(strings.TrimPrefix(mockMetadata.URL, "http://"))
	metaHost := net.JoinHostPort(host, port)

	env := []string{
		"GRAIN_SERVER=env.grain.internal:9000",
		"GRAIN_TOKEN_FILE=" + tokenPath,
		"GCE_METADATA_HOST=" + metaHost,
	}

	res := runExecutorScript(t, env)
	if res.err != nil {
		t.Fatalf("script failed: %v\nstdout:\n%s\nstderr:\n%s", res.err, res.stdout, res.stderr)
	}

	hasArg := func(flag, val string) bool {
		for i, a := range res.args {
			if a == flag && i+1 < len(res.args) && res.args[i+1] == val {
				return true
			}
		}
		return false
	}

	if !hasArg("-iap-audience", "auto-detected-iap-aud-777") {
		t.Errorf("expected auto-detected -iap-audience in %v", res.args)
	}
}

// runDownloadScenario runs the script with no grain binary installed, so it
// has to download one from a mock grain server. sidecar decides what the
// server answers for <binary>.sha256: "match", "mismatch" or "missing".
func runDownloadScenario(t *testing.T, sidecar string, extraArgs ...string) (recordFile string, stdout, stderr string, err error) {
	t.Helper()
	dir := t.TempDir()

	recordFile = filepath.Join(dir, "grain-args.txt")
	stubGrainScript := `#!/usr/bin/env bash
printf '%s\n' "$@" > "` + recordFile + `"
exit 0
`
	sum := sha256.Sum256([]byte(stubGrainScript))
	goodSum := hex.EncodeToString(sum[:])

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/executors/bin/") && !strings.HasPrefix(r.URL.Path, "/install/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			switch sidecar {
			case "match":
				_, _ = w.Write([]byte(goodSum + "  grain\n"))
			case "mismatch":
				_, _ = w.Write([]byte(strings.Repeat("0", 64) + "  grain\n"))
			default:
				http.NotFound(w, r)
			}
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte(stubGrainScript))
	}))
	defer mockServer.Close()

	scriptPath := filepath.Join(repoRoot(t), "scripts", "run-remote-executor.sh")
	args := append([]string{scriptPath,
		"--server", "remote.grain.test:8422",
		"--token", "super-secret-token",
		"--grain-url", mockServer.URL,
	}, extraArgs...)
	cmd := exec.Command("bash", args...)
	cmd.Env = []string{
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME=" + dir,
		// This test's own directory, never the host's: run as root the
		// script would otherwise install to (and first look in)
		// /usr/local/bin, where these parallel cases overwrite each
		// other's stub and every later run finds one already there.
		"GRAIN_INSTALL_DIR=" + filepath.Join(dir, "bin"),
		// The script falls back to github.com release URLs once the
		// mock server's are exhausted; send those to a closed port so
		// the test neither reaches the network nor waits on it.
		"https_proxy=http://127.0.0.1:1",
		"HTTPS_PROXY=http://127.0.0.1:1",
	}
	// "PINNED" in extraArgs stands for the stub's real digest, which only
	// this helper knows.
	for i, a := range cmd.Args {
		if a == "PINNED" {
			cmd.Args[i] = goodSum
		}
	}

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return recordFile, outBuf.String(), errBuf.String(), err
}

func TestRunRemoteExecutorDownloadsGrainFromGrainURL(t *testing.T) {
	t.Parallel()

	recordFile, stdout, stderr, err := runDownloadScenario(t, "match")
	if err != nil {
		t.Fatalf("run-remote-executor failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stderr, "Verified SHA-256") {
		t.Errorf("expected the download to be checksum-verified, stderr:\n%s", stderr)
	}

	if data, readErr := os.ReadFile(recordFile); readErr != nil {
		t.Fatalf("stub grain was not executed: %v", readErr)
	} else if !strings.Contains(string(data), "executor") {
		t.Errorf("expected stub grain to receive 'executor' subcommand, got: %s", string(data))
	}
}

func TestRunRemoteExecutorRefusesUnverifiedDownloads(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		sidecar    string
		args       []string
		wantRun    bool
		wantStderr string
	}{
		{name: "checksum mismatch", sidecar: "mismatch", wantStderr: "Checksum mismatch"},
		{name: "pinned checksum overrides published one", sidecar: "mismatch", args: []string{"--sha256", "PINNED"}, wantRun: true, wantStderr: "Verified SHA-256"},
		{name: "pinned checksum mismatch", sidecar: "match", args: []string{"--sha256", strings.Repeat("a", 64)}, wantStderr: "Checksum mismatch"},
		{name: "no checksum published", sidecar: "missing", wantStderr: "refusing to install an unverified binary"},
		{name: "no checksum published, pinned", sidecar: "missing", args: []string{"--sha256", "PINNED"}, wantRun: true, wantStderr: "Verified SHA-256"},
		{name: "no checksum published, override", sidecar: "missing", args: []string{"--allow-unverified-binary"}, wantRun: true, wantStderr: "installing unverified"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			recordFile, stdout, stderr, err := runDownloadScenario(t, tc.sidecar, tc.args...)
			_, statErr := os.Stat(recordFile)
			ran := statErr == nil
			if tc.wantRun {
				if err != nil || !ran {
					t.Fatalf("expected the downloaded binary to run (err=%v)\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
				}
			} else {
				if err == nil || ran {
					t.Fatalf("expected the script to refuse the download (err=%v, ran=%v)\nstdout:\n%s\nstderr:\n%s", err, ran, stdout, stderr)
				}
			}
			if !strings.Contains(stderr, tc.wantStderr) {
				t.Errorf("stderr missing %q:\n%s", tc.wantStderr, stderr)
			}
		})
	}
}

func TestRunRemoteExecutorTokenFileIsPrivate(t *testing.T) {
	t.Parallel()

	res := runExecutorScript(t, []string{"GRAIN_TOKEN=env-secret"}, "--server", "127.0.0.1:8422", "--name", "box")
	if res.err != nil {
		t.Fatalf("run-remote-executor failed: %v\nstderr:\n%s", res.err, res.stderr)
	}
	if strings.Contains(res.stderr, "Warning: --token") {
		t.Errorf("GRAIN_TOKEN should not trigger the --token warning, stderr:\n%s", res.stderr)
	}
	var tokenFile string
	for i, a := range res.args {
		if a == "-token-file" && i+1 < len(res.args) {
			tokenFile = res.args[i+1]
		}
	}
	if tokenFile == "" {
		t.Fatalf("expected -token-file in %v", res.args)
	}
	fi, err := os.Stat(tokenFile)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Errorf("token file mode = %o, want 600", mode)
	}
	if data, _ := os.ReadFile(tokenFile); string(data) != "env-secret" {
		t.Errorf("token file content = %q, want %q", data, "env-secret")
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(tokenFile), ".executor-token.*"))
	if len(leftovers) != 0 {
		t.Errorf("temporary token files left behind: %v", leftovers)
	}

	res = runExecutorScript(t, nil, "--server", "127.0.0.1:8422", "--token", "argv-secret")
	if res.err != nil {
		t.Fatalf("run-remote-executor failed: %v\nstderr:\n%s", res.err, res.stderr)
	}
	if !strings.Contains(res.stderr, "Warning: --token") {
		t.Errorf("expected a warning about --token on the command line, stderr:\n%s", res.stderr)
	}
}
