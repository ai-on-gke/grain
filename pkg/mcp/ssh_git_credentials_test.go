package mcp

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/kubectlgate"
)

func TestConfigureGitCredentialsOverSSHWritesACredentialLineMatchingGitCredentialStore(t *testing.T) {
	home := t.TempDir()
	runner := localExecRunner{dir: home}
	if err := ConfigureGitCredentialsOverSSH(runner, "http://10.100.0.1:8080/owner/repo.git", "secret-token", GitIdentity{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".git-credentials"))
	if err != nil {
		t.Fatal(err)
	}
	want := "http://sandbox:secret-token@10.100.0.1:8080\n"
	if string(data) != want {
		t.Errorf("got %q, want %q", data, want)
	}
}

func TestConfigureGitCredentialsOverSSHWritesAGitconfigWithTheCredentialHelperAndAnIdentity(t *testing.T) {
	home := t.TempDir()
	runner := localExecRunner{dir: home}
	if err := ConfigureGitCredentialsOverSSH(runner, "http://proxy:8080/owner/repo.git", "tok", GitIdentity{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".gitconfig"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"helper = store", "name = grain agent", "email = grain-agent@localhost"} {
		if !strings.Contains(got, want) {
			t.Errorf(".gitconfig missing %q, got:\n%s", want, got)
		}
	}
	// The helper reset reaches a VM too, not only a host directory: both
	// transports render the same file through GitCredentialFiles, and a VM
	// image with a credential helper of its own in /etc/gitconfig would
	// otherwise be handed the sandbox's proxy token to store (see
	// TestConfigureGitCredentialsResetsTheHelperListBeforeNamingGrainsOwn
	// for what git does with an appended helper).
	if want := []string{"helper =", "helper = store"}; !reflect.DeepEqual(helperLines(got), want) {
		t.Errorf(".gitconfig helper lines = %q, want %q, got:\n%s", helperLines(got), want, got)
	}
}

func TestConfigureGitCredentialsOverSSHFilesAreReadableOnlyByTheOwner(t *testing.T) {
	home := t.TempDir()
	runner := localExecRunner{dir: home}
	if err := ConfigureGitCredentialsOverSSH(runner, "http://proxy:8080/owner/repo.git", "tok", GitIdentity{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".git-credentials", ".gitconfig"} {
		info, err := os.Stat(filepath.Join(home, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", name, info.Mode().Perm())
		}
	}
}

func TestConfigureGitCredentialsOverSSHRejectsAMalformedRemoteURL(t *testing.T) {
	runner := localExecRunner{dir: t.TempDir()}
	if err := ConfigureGitCredentialsOverSSH(runner, "not-a-url", "tok", GitIdentity{}); err == nil {
		t.Fatal("expected an error for a remote URL with no scheme or host")
	}
}

func TestConfigureGitCredentialsOverSSHSurfacesARemoteWriteFailure(t *testing.T) {
	// A session directory the remote user has no permission to write
	// into -- exercises the exitCode != 0 path a real SSH session would
	// hit if e.g. the VM's disk were full or its home directory somehow
	// unwritable.
	if os.Geteuid() == 0 {
		// A 0500 directory stops every user but root, which is who a
		// container-based CI job or a `sudo make test` runs as -- there
		// is no permission left to deny, so the write this test wants to
		// see fail would succeed. Skipped rather than left to fail
		// confusingly, the same reason the missing-credential test stopped
		// depending on $PATH.
		t.Skip("root ignores directory permissions; nothing here can make the write fail")
	}
	home := t.TempDir()
	if err := os.Chmod(home, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(home, 0o700) })
	runner := localExecRunner{dir: home}
	if err := ConfigureGitCredentialsOverSSH(runner, "http://proxy:8080/owner/repo.git", "tok", GitIdentity{}); err == nil {
		t.Fatal("expected an error writing into an unwritable session directory")
	}
}

// countingRunner counts the round trips made through it.
type countingRunner struct {
	inner remoteRunner
	calls int
}

func (r *countingRunner) Run(ctx context.Context, argv []string, stdin string) (stdout, stderr string, exitCode int) {
	r.calls++
	return r.inner.Run(ctx, argv, stdin)
}

func TestConfigureGitCredentialsOverSSHTakesOneRoundTrip(t *testing.T) {
	// Every round trip over `kubectl exec` is a process and a handshake
	// with the API server, and each one is a chance for a busy control
	// plane to fail the sandbox's setup.
	home := t.TempDir()
	runner := &countingRunner{inner: localExecRunner{dir: home}}
	if err := ConfigureGitCredentialsOverSSH(runner, "http://proxy:8080/owner/repo.git", "tok", GitIdentity{}); err != nil {
		t.Fatal(err)
	}
	if runner.calls != 1 {
		t.Errorf("round trips = %d, want 1", runner.calls)
	}
	// And the split between the two files landed on the boundary.
	creds, _ := os.ReadFile(filepath.Join(home, ".git-credentials"))
	if string(creds) != "http://sandbox:tok@proxy:8080\n" {
		t.Errorf(".git-credentials = %q", creds)
	}
	gitconfig, _ := os.ReadFile(filepath.Join(home, ".gitconfig"))
	if !strings.HasPrefix(string(gitconfig), "[") || strings.Contains(string(gitconfig), "sandbox:tok") {
		t.Errorf(".gitconfig = %q, want the config alone", gitconfig)
	}
}

func TestConfigureGitCredentialsOverSSHTightensALeftoverFile(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{".git-credentials", ".gitconfig"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte("an old and much longer file than the new one\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := ConfigureGitCredentialsOverSSH(localExecRunner{dir: home}, "http://proxy:8080/owner/repo.git", "tok", GitIdentity{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".git-credentials", ".gitconfig"} {
		info, err := os.Stat(filepath.Join(home, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", name, info.Mode().Perm())
		}
		data, _ := os.ReadFile(filepath.Join(home, name))
		if strings.Contains(string(data), "an old") {
			t.Errorf("%s = %q, want the old content replaced", name, data)
		}
	}
}

// flakyRunner fails its first failures calls the way a cut-off kubectl
// exec does, and passes every later one through to inner.
type flakyRunner struct {
	inner    remoteRunner
	failures int
	calls    int
}

func (r *flakyRunner) Run(ctx context.Context, argv []string, stdin string) (stdout, stderr string, exitCode int) {
	r.calls++
	if r.calls <= r.failures {
		return "", SessionCutOffNotice, -1
	}
	return r.inner.Run(ctx, argv, stdin)
}

func TestConfigureGitCredentialsOverSSHRetriesASessionCutOffPartWay(t *testing.T) {
	defer kubectlgate.SetBackoffForTest(time.Millisecond)()
	home := t.TempDir()
	runner := &flakyRunner{inner: localExecRunner{dir: home}, failures: 1}
	if err := ConfigureGitCredentialsOverSSH(runner, "http://proxy:8080/owner/repo.git", "tok", GitIdentity{}); err != nil {
		t.Fatalf("one cut-off session failed the write: %v", err)
	}
	if runner.calls != 2 {
		t.Errorf("round trips = %d, want 2 (the cut-off one and its retry)", runner.calls)
	}
	if creds, _ := os.ReadFile(filepath.Join(home, ".git-credentials")); string(creds) != "http://sandbox:tok@proxy:8080\n" {
		t.Errorf(".git-credentials = %q", creds)
	}
}

func TestConfigureGitCredentialsOverSSHGivesUpAfterMaxAttempts(t *testing.T) {
	defer kubectlgate.SetBackoffForTest(time.Millisecond)()
	runner := &flakyRunner{inner: localExecRunner{dir: t.TempDir()}, failures: kubectlgate.MaxAttempts}
	err := ConfigureGitCredentialsOverSSH(runner, "http://proxy:8080/owner/repo.git", "tok", GitIdentity{})
	if err == nil || !strings.Contains(err.Error(), "the exec session ended") {
		t.Fatalf("error = %v, want the last cut-off reported", err)
	}
	if runner.calls != kubectlgate.MaxAttempts {
		t.Errorf("round trips = %d, want %d", runner.calls, kubectlgate.MaxAttempts)
	}
}
