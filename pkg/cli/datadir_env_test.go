package cli

// grain/task-303: `grain state` and `grain secrets` edit a colocated
// deployment's files, and there is one such deployment per host -- so
// the host can say where its data directory is once (GRAIN_DATA_DIR,
// exported by scripts/setup.sh into both the CLI wrapper's container and
// /etc/profile.d/grain.sh) rather than every operator carrying a
// -data-dir on every invocation. What made that a bug rather than a
// convenience: setup.sh's own closing report tells the operator to run
// `grain state status`, and typed exactly as printed it failed with
// "-data-dir is required".

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A value file for `grain secrets set`, whose write is the observable
// side effect these tests use to tell which data directory was chosen.
func valueFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "value")
	if err := os.WriteFile(path, []byte("a-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// hasSecrets reports whether a data directory has been written into at
// all -- secrets.Open mints the key and Set writes the encrypted file,
// both under <data-dir>/secrets.
func hasSecrets(t *testing.T, dataDir string) bool {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataDir, "secrets"))
	if err != nil {
		if os.IsNotExist(err) {
			return false
		}
		t.Fatal(err)
	}
	return len(entries) > 0
}

func TestSecretsDefaultsTheDataDirToTheEnvironment(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(dataDirEnvVar, dataDir)

	if err := runSecrets([]string{"set", "-value-file", valueFile(t), "a-secret", "a-key"}); err != nil {
		t.Fatalf("`grain secrets set` with no -data-dir: %v", err)
	}
	if !hasSecrets(t, dataDir) {
		t.Errorf("nothing was written under %s, so $%s was not what it used", dataDir, dataDirEnvVar)
	}
}

func TestStateDefaultsTheDataDirToTheEnvironment(t *testing.T) {
	t.Setenv(dataDirEnvVar, t.TempDir())

	// The command setup.sh's closing report prints, typed exactly as
	// printed.
	if err := runState([]string{"status"}); err != nil {
		t.Fatalf("`grain state status` with no -data-dir: %v", err)
	}
}

// The flag is still the answer when both are given: an operator pointing
// one invocation at some other root -- an archived copy, a second
// installation -- must not be quietly redirected back to the host's own
// by a variable a login shell set for them.
func TestAnExplicitDataDirBeatsTheEnvironment(t *testing.T) {
	fromEnv, fromFlag := t.TempDir(), t.TempDir()
	t.Setenv(dataDirEnvVar, fromEnv)

	if err := runSecrets([]string{"-data-dir", fromFlag, "set", "-value-file", valueFile(t), "a-secret", "a-key"}); err != nil {
		t.Fatalf("`grain secrets set -data-dir`: %v", err)
	}
	if !hasSecrets(t, fromFlag) {
		t.Errorf("nothing was written under the -data-dir given (%s)", fromFlag)
	}
	if hasSecrets(t, fromEnv) {
		t.Errorf("$%s (%s) was written to even though -data-dir named %s", dataDirEnvVar, fromEnv, fromFlag)
	}
}

// A host that sets neither is exactly where it was: the failure has to
// stay a clear "you must say which deployment", not become a guess at
// one.
func TestNeitherFlagNorEnvironmentIsStillAnError(t *testing.T) {
	t.Setenv(dataDirEnvVar, "")

	for name, run := range map[string]func() error{
		"grain state status": func() error { return runState([]string{"status"}) },
		"grain secrets list": func() error { return runSecrets([]string{"list"}) },
	} {
		err := run()
		if err == nil {
			t.Errorf("`%s` with no data directory anywhere succeeded", name)
			continue
		}
		if !strings.Contains(err.Error(), "-data-dir is required") {
			t.Errorf("`%s` failed with %v, want the -data-dir requirement", name, err)
		}
	}
}

// An exported-but-empty variable is a broken profile script, not a
// request to operate on a data directory named "" -- the same reading
// serverDefault gives GRAIN_SERVER.
func TestABlankEnvironmentValueIsTreatedAsUnset(t *testing.T) {
	t.Setenv(dataDirEnvVar, "   ")
	if got := dataDirDefault(); got != "" {
		t.Errorf("dataDirDefault() = %q, want it to ignore whitespace", got)
	}
}
