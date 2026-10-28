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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	grainsqlite "github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

func TestExportImportRoundtripUnencrypted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// 1. Set up source data directory
	srcDir := t.TempDir()
	storePath := filepath.Join(srcDir, "store")
	if err := os.MkdirAll(storePath, 0o700); err != nil {
		t.Fatal(err)
	}

	db, err := grainsqlite.Open(grainsqlite.DefaultConfig(storePath))
	if err != nil {
		t.Fatal(err)
	}
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}

	// Create test task
	task := model.Task{
		ID:    "task-migration-test-1",
		Title: "Migrate host task",
		Body:  "Check data migration across hosts",
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	// Create test state repository
	stateDir := filepath.Join(srcDir, "state-repo")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	execGitInDir(t, stateDir, "init", "-b", "main")
	execGitInDir(t, stateDir, "config", "user.email", "grain@test.local")
	execGitInDir(t, stateDir, "config", "user.name", "Grain Test")
	testFile := filepath.Join(stateDir, "config.yaml")
	if err := os.WriteFile(testFile, []byte("setting: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	execGitInDir(t, stateDir, "add", "config.yaml")
	execGitInDir(t, stateDir, "commit", "-m", "initial state config")

	// Create secrets directory and files
	secDir := filepath.Join(srcDir, "secrets")
	if err := os.MkdirAll(filepath.Join(secDir, "github"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(secDir, "gcp"), 0o700); err != nil {
		t.Fatal(err)
	}

	// GitHub ladder files
	credsJSON := `{"bwsalmon/*":"bot-cred"}`
	if err := os.WriteFile(filepath.Join(secDir, "github", "credentials.json"), []byte(credsJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secDir, "github", "bot-cred.token"), []byte("ghp_testtoken123"), 0o600); err != nil {
		t.Fatal(err)
	}

	// GCP credential
	if err := os.WriteFile(filepath.Join(secDir, "gcp", "sa.json"), []byte(`{"type":"service_account"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Secrets key & encrypted store
	secKey, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.WriteKeyFile(filepath.Join(secDir, secrets.DefaultKeyFileName), secKey); err != nil {
		t.Fatal(err)
	}
	secStore := secrets.Open(secrets.Config{
		File:    filepath.Join(secDir, secrets.DefaultFileName),
		KeyFile: filepath.Join(secDir, secrets.DefaultKeyFileName),
	})
	if err := secStore.Set("stripe", "key", []byte("sk_test_12345")); err != nil {
		t.Fatal(err)
	}

	// Close db before export snapshot
	_ = db.Close()

	// 2. Export
	bundleFile := filepath.Join(t.TempDir(), "migration.tar.gz")
	var exportOut bytes.Buffer
	if err := runExport(ctx, []string{"-data-dir", srcDir, "-out", bundleFile}, &exportOut); err != nil {
		t.Fatalf("runExport failed: %v", err)
	}

	if _, err := os.Stat(bundleFile); err != nil {
		t.Fatalf("bundle file not created: %v", err)
	}

	// 3. Import into a new target directory
	targetDir := t.TempDir()
	var importOut bytes.Buffer
	if err := runImport(ctx, []string{"-data-dir", targetDir, "-in", bundleFile}, &importOut); err != nil {
		t.Fatalf("runImport failed: %v", err)
	}

	// 4. Verify target restoration
	targetStorePath := filepath.Join(targetDir, "store")
	targetDB, err := grainsqlite.Open(grainsqlite.DefaultConfig(targetStorePath))
	if err != nil {
		t.Fatalf("opening target database: %v", err)
	}
	defer targetDB.Close()
	targetStore := model.New(targetDB)

	// Verify task in database
	loadedTask, err := targetStore.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("getting task from target store: %v", err)
	}
	if loadedTask.Title != task.Title || loadedTask.Body != task.Body {
		t.Fatalf("task mismatch: got title=%q body=%q, want title=%q body=%q",
			loadedTask.Title, loadedTask.Body, task.Title, task.Body)
	}

	// Verify file modes
	dbFileInfo, err := os.Stat(filepath.Join(targetStorePath, "grain.db"))
	if err != nil {
		t.Fatal(err)
	}
	if dbFileInfo.Mode().Perm() != 0o600 {
		t.Errorf("grain.db permission = %v, want 0600", dbFileInfo.Mode().Perm())
	}

	// Verify secrets and permissions
	targetBotToken := filepath.Join(targetDir, "secrets", "github", "bot-cred.token")
	tokenBytes, err := os.ReadFile(targetBotToken)
	if err != nil {
		t.Fatalf("reading restored token: %v", err)
	}
	if string(tokenBytes) != "ghp_testtoken123" {
		t.Fatalf("token content = %q, want 'ghp_testtoken123'", string(tokenBytes))
	}
	tokenInfo, err := os.Stat(targetBotToken)
	if err != nil {
		t.Fatal(err)
	}
	if tokenInfo.Mode().Perm() != 0o600 {
		t.Errorf("token permission = %v, want 0600", tokenInfo.Mode().Perm())
	}

	// Verify GitHub credential ladder loads cleanly
	ladder, err := gitproxy.LoadCredentialSet(filepath.Join(targetDir, "secrets", "github"))
	if err != nil {
		t.Fatalf("loading target credential ladder: %v", err)
	}
	if names := ladder.Names(); len(names) != 1 || names[0] != "bot-cred" {
		t.Fatalf("target credentials = %v", names)
	}

	// Verify encrypted secrets store
	targetSecStore := secrets.Open(secrets.Config{
		File:    filepath.Join(targetDir, "secrets", secrets.DefaultFileName),
		KeyFile: filepath.Join(targetDir, "secrets", secrets.DefaultKeyFileName),
	})
	val, err := targetSecStore.Resolve(ctx, "stripe/key")
	if err != nil {
		t.Fatalf("getting secret from target store: %v", err)
	}
	if val != "sk_test_12345" {
		t.Fatalf("secret value = %q, want 'sk_test_12345'", val)
	}

	// Verify state repository
	targetStateRepo := filepath.Join(targetDir, "state-repo")
	stateFileBytes, err := os.ReadFile(filepath.Join(targetStateRepo, "config.yaml"))
	if err != nil {
		t.Fatalf("reading state repo file: %v", err)
	}
	if string(stateFileBytes) != "setting: true\n" {
		t.Fatalf("state repo file content = %q", string(stateFileBytes))
	}
	_, err = staterepo.Open(ctx, staterepo.Config{Dir: targetStateRepo})
	if err != nil {
		t.Fatalf("opening target state repo: %v", err)
	}
}

func TestExportImportRoundtripEncrypted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srcDir := t.TempDir()
	storePath := filepath.Join(srcDir, "store")
	_ = os.MkdirAll(storePath, 0o700)
	db, err := grainsqlite.Open(grainsqlite.DefaultConfig(storePath))
	if err != nil {
		t.Fatal(err)
	}
	store := model.New(db)
	_ = store.Init(ctx)
	_ = store.PutTask(ctx, model.Task{
		ID:    "task-enc-test",
		Title: "Encrypted test task",
	})
	_ = db.Close()

	secretDir := t.TempDir()
	passFile := filepath.Join(secretDir, "passphrase")
	if err := os.WriteFile(passFile, []byte("correct-horse-battery-staple-12345\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wrongPassFile := filepath.Join(secretDir, "wrong-passphrase")
	if err := os.WriteFile(wrongPassFile, []byte("wrong-passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundleFile := filepath.Join(t.TempDir(), "migration.enc")

	var exportOut bytes.Buffer
	if err := runExport(ctx, []string{"-data-dir", srcDir, "-out", bundleFile, "-passphrase-file", passFile}, &exportOut); err != nil {
		t.Fatalf("encrypted export failed: %v", err)
	}

	// Verify magic prefix in encrypted bundle
	content, err := os.ReadFile(bundleFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(content), EncryptedBundleMagic) {
		t.Fatalf("encrypted bundle missing magic prefix %s", EncryptedBundleMagic)
	}

	// 1. Attempt import with wrong passphrase -> must fail
	targetDir1 := t.TempDir()
	var errOut bytes.Buffer
	err = runImport(ctx, []string{"-data-dir", targetDir1, "-in", bundleFile, "-passphrase-file", wrongPassFile}, &errOut)
	if err == nil {
		t.Fatal("expected import to fail with wrong passphrase, but it succeeded")
	}

	// 2. Attempt import without passphrase -> must fail
	targetDir2 := t.TempDir()
	err = runImport(ctx, []string{"-data-dir", targetDir2, "-in", bundleFile}, &errOut)
	if err == nil {
		t.Fatal("expected import to fail without passphrase, but it succeeded")
	}

	// 3. Attempt import with correct passphrase -> must succeed
	targetDir3 := t.TempDir()
	var successOut bytes.Buffer
	if err := runImport(ctx, []string{"-data-dir", targetDir3, "-in", bundleFile, "-passphrase-file", passFile}, &successOut); err != nil {
		t.Fatalf("encrypted import failed with correct passphrase: %v", err)
	}

	targetDB, err := grainsqlite.Open(grainsqlite.DefaultConfig(filepath.Join(targetDir3, "store")))
	if err != nil {
		t.Fatal(err)
	}
	defer targetDB.Close()
	targetStore := model.New(targetDB)
	tasks, err := targetStore.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Title != "Encrypted test task" {
		t.Fatalf("tasks = %v, want 1 task with title 'Encrypted test task'", tasks)
	}
}

func TestExportImportRoundtripKeyFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srcDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(srcDir, "store"), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := grainsqlite.Open(grainsqlite.DefaultConfig(filepath.Join(srcDir, "store")))
	if err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	keyFile := filepath.Join(t.TempDir(), "bundle.key")
	if err := os.WriteFile(keyFile, []byte(strings.Repeat("ab", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundleFile := filepath.Join(t.TempDir(), "migration.enc")
	if err := runExport(ctx, []string{"-data-dir", srcDir, "-out", bundleFile, "-key-file", keyFile}, io.Discard); err != nil {
		t.Fatalf("export with -key-file: %v", err)
	}
	content, err := os.ReadFile(bundleFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(content), EncryptedBundleMagic) {
		t.Fatalf("bundle exported with -key-file is not encrypted")
	}
	if err := runImport(ctx, []string{"-data-dir", t.TempDir(), "-in", bundleFile}, io.Discard); err == nil {
		t.Fatal("import without the key succeeded")
	}
	if err := runImport(ctx, []string{"-data-dir", t.TempDir(), "-in", bundleFile, "-key-file", keyFile}, io.Discard); err != nil {
		t.Fatalf("import with -key-file: %v", err)
	}
}

func TestBundleSecretFlags(t *testing.T) {
	t.Parallel()
	hexKey := strings.Repeat("0f", 32)
	parse := func(t *testing.T, stdin string, args ...string) (*bundleSecretFlags, error) {
		t.Helper()
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		f := addBundleSecretFlags(fs)
		f.stdin = strings.NewReader(stdin)
		return f, fs.Parse(args)
	}

	t.Run("passphrase from stdin", func(t *testing.T) {
		f, err := parse(t, "from-stdin\r\n", "-passphrase-file", "-")
		if err != nil {
			t.Fatal(err)
		}
		var warn bytes.Buffer
		pass, key, err := f.resolve(&warn)
		if err != nil || pass != "from-stdin" || key != nil {
			t.Fatalf("resolve = %q, %x, %v; want the stdin passphrase", pass, key, err)
		}
		if warn.Len() != 0 {
			t.Fatalf("unexpected warning %q", warn.String())
		}
	})

	t.Run("key from stdin", func(t *testing.T) {
		f, err := parse(t, hexKey+"\n", "-key-file", "-")
		if err != nil {
			t.Fatal(err)
		}
		pass, key, err := f.resolve(io.Discard)
		if err != nil || pass != "" || len(key) != KeyLength || key[0] != 0x0f {
			t.Fatalf("resolve = %q, %x, %v; want the decoded stdin key", pass, key, err)
		}
	})

	t.Run("malformed key is rejected, not used as a passphrase", func(t *testing.T) {
		for _, bad := range []string{"not-hex-at-all", hexKey[:62], hexKey + "00", "zz" + hexKey[2:]} {
			f, err := parse(t, bad, "-key-file", "-")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := f.resolve(io.Discard); err == nil {
				t.Errorf("key %q accepted", bad)
			}
		}
	})

	t.Run("argv forms still work but warn", func(t *testing.T) {
		f, err := parse(t, "", "-passphrase", "on-argv")
		if err != nil {
			t.Fatal(err)
		}
		var warn bytes.Buffer
		pass, _, err := f.resolve(&warn)
		if err != nil || pass != "on-argv" {
			t.Fatalf("resolve = %q, %v", pass, err)
		}
		if !strings.Contains(warn.String(), "deprecated") {
			t.Fatalf("no deprecation warning for -passphrase on argv: %q", warn.String())
		}

		f, err = parse(t, "", "-key", hexKey)
		if err != nil {
			t.Fatal(err)
		}
		warn.Reset()
		if _, key, err := f.resolve(&warn); err != nil || key == nil {
			t.Fatalf("resolve -key = %x, %v", key, err)
		}
		if !strings.Contains(warn.String(), "deprecated") {
			t.Fatalf("no deprecation warning for -key on argv: %q", warn.String())
		}
	})

	t.Run("passphrase and key together are refused", func(t *testing.T) {
		passFile := filepath.Join(t.TempDir(), "pass")
		if err := os.WriteFile(passFile, []byte("p"), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := parse(t, hexKey, "-passphrase-file", passFile, "-key-file", "-")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.resolve(io.Discard); err == nil {
			t.Fatal("passphrase and key together accepted")
		}
	})

	t.Run("both reading stdin is refused", func(t *testing.T) {
		f, err := parse(t, "x", "-passphrase-file", "-", "-key-file", "-")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.resolve(io.Discard); err == nil {
			t.Fatal("two stdin readers accepted")
		}
	})
}

func TestImportIntegrityTamperRejection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srcDir := t.TempDir()
	storePath := filepath.Join(srcDir, "store")
	_ = os.MkdirAll(storePath, 0o700)
	db, err := grainsqlite.Open(grainsqlite.DefaultConfig(storePath))
	if err != nil {
		t.Fatal(err)
	}
	store := model.New(db)
	_ = store.Init(ctx)
	_ = db.Close()

	bundleFile := filepath.Join(t.TempDir(), "migration.tar.gz")
	var exportOut bytes.Buffer
	if err := runExport(ctx, []string{"-data-dir", srcDir, "-out", bundleFile}, &exportOut); err != nil {
		t.Fatal(err)
	}

	// Unpack, tamper with manifest or db, and repack
	tamperStage := t.TempDir()
	bundleBytes, err := os.ReadFile(bundleFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := extractTarGz(bundleBytes, tamperStage); err != nil {
		t.Fatal(err)
	}

	// Tamper with db file content
	tamperedDB := filepath.Join(tamperStage, "database", "grain.db")
	_ = os.WriteFile(tamperedDB, []byte("corrupted data content"), 0o600)

	tamperedBundleBytes, err := createTarGz(tamperStage)
	if err != nil {
		t.Fatal(err)
	}
	tamperedBundleFile := filepath.Join(t.TempDir(), "tampered.tar.gz")
	if err := os.WriteFile(tamperedBundleFile, tamperedBundleBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	targetDir := t.TempDir()
	var importOut bytes.Buffer
	err = runImport(ctx, []string{"-data-dir", targetDir, "-in", tamperedBundleFile}, &importOut)
	if err == nil {
		t.Fatal("expected import to fail due to integrity checksum mismatch, but it succeeded")
	}
	if !strings.Contains(err.Error(), "integrity error") {
		t.Fatalf("expected error to mention 'integrity error', got: %v", err)
	}
}

func TestImportRequiresForceForExistingData(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srcDir := t.TempDir()
	storePath := filepath.Join(srcDir, "store")
	_ = os.MkdirAll(storePath, 0o700)
	db, err := grainsqlite.Open(grainsqlite.DefaultConfig(storePath))
	if err != nil {
		t.Fatal(err)
	}
	store := model.New(db)
	_ = store.Init(ctx)
	_ = db.Close()

	bundleFile := filepath.Join(t.TempDir(), "migration.tar.gz")
	var exportOut bytes.Buffer
	if err := runExport(ctx, []string{"-data-dir", srcDir, "-out", bundleFile}, &exportOut); err != nil {
		t.Fatal(err)
	}

	targetDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(targetDir, "store"), 0o700)
	_ = os.WriteFile(filepath.Join(targetDir, "store", "grain.db"), []byte("existing database"), 0o600)

	var importOut bytes.Buffer
	err = runImport(ctx, []string{"-data-dir", targetDir, "-in", bundleFile}, &importOut)
	if err == nil {
		t.Fatal("expected import without -force to fail against existing database, but it succeeded")
	}
	if !strings.Contains(err.Error(), "-force") {
		t.Fatalf("expected error to mention '-force', got: %v", err)
	}

	// Now with -force -> should succeed
	if err := runImport(ctx, []string{"-data-dir", targetDir, "-in", bundleFile, "-force"}, &importOut); err != nil {
		t.Fatalf("import with -force failed: %v", err)
	}
}

func TestImportSkipsRelaySettingsFromBundle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srcDir := t.TempDir()
	storePath := filepath.Join(srcDir, "store")
	if err := os.MkdirAll(storePath, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := grainsqlite.Open(grainsqlite.DefaultConfig(storePath))
	if err != nil {
		t.Fatal(err)
	}
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	secStore := daemon.OpenSecrets(srcDir)
	if err := secStore.Set("relay", "token", []byte("legacy-relay-secret")); err != nil {
		t.Fatal(err)
	}
	if err := secStore.Set("other", "key", []byte("keep-me")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(srcDir, "secrets", "relay"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "secrets", "relay", "token"), []byte("legacy-file"), 0o600); err != nil {
		t.Fatal(err)
	}

	bundleFile := filepath.Join(t.TempDir(), "migration.tar.gz")
	if err := runExport(ctx, []string{"-data-dir", srcDir, "-out", bundleFile}, io.Discard); err != nil {
		t.Fatal(err)
	}

	targetDir := t.TempDir()
	var importOut bytes.Buffer
	if err := runImport(ctx, []string{"-data-dir", targetDir, "-in", bundleFile}, &importOut); err != nil {
		t.Fatalf("import failed: %v", err)
	}
	if !strings.Contains(importOut.String(), "skipping relay settings") {
		t.Errorf("import output = %q; want note about skipping relay settings", importOut.String())
	}
	if _, err := os.Stat(filepath.Join(targetDir, "secrets", "relay", "token")); !os.IsNotExist(err) {
		t.Errorf("secrets/relay/token should have been skipped, got err = %v", err)
	}
	dstSec := daemon.OpenSecrets(targetDir)
	if has, err := dstSec.Has("relay", "token"); err != nil || has {
		t.Errorf("relay/token secret should have been stripped on import, got has=%v err=%v", has, err)
	}
	if got, err := dstSec.Resolve(ctx, "other/key"); err != nil || got != "keep-me" {
		t.Errorf("other/key secret = %q, %v; want keep-me", got, err)
	}
}

func execGitInDir(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v: %s", strings.Join(args, " "), err, string(out))
	}
}

// A bundle is an input import did not write, so the modes in its tar
// headers are not trusted: a credential packed as world-readable, or
// with its setuid bit set, still lands in the data directory as 0600.
func TestImportClampsBundleFileModesToOwnerOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srcDir := t.TempDir()
	storePath := filepath.Join(srcDir, "store")
	if err := os.MkdirAll(storePath, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := grainsqlite.Open(grainsqlite.DefaultConfig(storePath))
	if err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if err := os.MkdirAll(filepath.Join(srcDir, "secrets", "gcp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "secrets", "gcp", "sa.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	bundleFile := filepath.Join(t.TempDir(), "migration.tar.gz")
	if err := runExport(ctx, []string{"-data-dir", srcDir, "-out", bundleFile}, io.Discard); err != nil {
		t.Fatal(err)
	}

	// Rewrite every file header's mode, leaving content (and so the
	// manifest's checksums) untouched.
	raw, err := os.ReadFile(bundleFile)
	if err != nil {
		t.Fatal(err)
	}
	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gr)
	var repacked bytes.Buffer
	gw := gzip.NewWriter(&repacked)
	tw := tar.NewWriter(gw)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			hdr.Mode = 0o4777
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(tw, tr); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	loose := filepath.Join(t.TempDir(), "loose.tar.gz")
	if err := os.WriteFile(loose, repacked.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	targetDir := t.TempDir()
	if err := runImport(ctx, []string{"-data-dir", targetDir, "-in", loose}, io.Discard); err != nil {
		t.Fatalf("runImport: %v", err)
	}
	info, err := os.Stat(filepath.Join(targetDir, "secrets", "gcp", "sa.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode() & (os.ModePerm | os.ModeSetuid); got != 0o700 {
		t.Fatalf("restored credential mode = %v, want -rwx------ (owner bits only, no setuid)", got)
	}
}

// An archive entry that climbs out of the staging directory is refused
// rather than written; one whose name merely starts with two dots is not
// climbing anywhere and extracts like any other.
func TestExtractTarGzRefusesEntriesOutsideTheStage(t *testing.T) {
	t.Parallel()
	pack := func(name string) []byte {
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gw)
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o600, Size: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		_ = tw.Close()
		_ = gw.Close()
		return buf.Bytes()
	}
	for _, name := range []string{"../escape", "a/../../escape", "/abs"} {
		if err := extractTarGz(pack(name), t.TempDir()); err == nil {
			t.Errorf("extractTarGz(%q) succeeded, want an invalid-path error", name)
		}
	}
	dir := t.TempDir()
	if err := extractTarGz(pack("..dotted"), dir); err != nil {
		t.Fatalf("extractTarGz(%q): %v", "..dotted", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "..dotted")); err != nil {
		t.Fatal(err)
	}
}

// newExportSource is a data directory holding nothing but an
// initialised store, for tests that care about what else goes beside it.
func newExportSource(t *testing.T) string {
	t.Helper()
	srcDir := t.TempDir()
	storePath := filepath.Join(srcDir, "store")
	if err := os.MkdirAll(storePath, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := grainsqlite.Open(grainsqlite.DefaultConfig(storePath))
	if err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	return srcDir
}

// commitFile writes name in the working tree dir, initialising it as a
// repository on branch main first if it is not one, and commits it.
func commitFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		execGitInDir(t, dir, "init", "-b", "main")
		execGitInDir(t, dir, "config", "user.email", "grain@test.local")
		execGitInDir(t, dir, "config", "user.name", "Grain Test")
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	execGitInDir(t, dir, "add", name)
	execGitInDir(t, dir, "commit", "-m", "write "+name)
}

// repackBundle extracts bundleFile, lets edit change the stage, and
// writes the result back out as a new unencrypted bundle.
func repackBundle(t *testing.T, bundleFile string, edit func(stage string)) string {
	t.Helper()
	stage := t.TempDir()
	data, err := os.ReadFile(bundleFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := extractTarGz(data, stage); err != nil {
		t.Fatal(err)
	}
	edit(stage)
	repacked, err := createTarGz(stage)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "repacked.tar.gz")
	if err := os.WriteFile(out, repacked, 0o600); err != nil {
		t.Fatal(err)
	}
	return out
}

// A multi-team build kept a state repository per team under
// team-state-repos/<team>. grain/config is the only configuration
// repository now: export carries none of a leftover directory, and an
// older bundle that does list them still imports, says it is ignoring
// them, and restores nothing for them.
func TestExportImportIgnoreTeamStateRepos(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srcDir := newExportSource(t)
	commitFile(t, daemon.StateRepoDir(srcDir), "config.yaml", "setting: true\n")
	stray := filepath.Join(srcDir, "team-state-repos", "alpha")
	commitFile(t, stray, "templates.yaml", "team: alpha\n")

	bundleFile := filepath.Join(t.TempDir(), "migration.tar.gz")
	var exportOut bytes.Buffer
	if err := runExport(ctx, []string{"-data-dir", srcDir, "-out", bundleFile}, &exportOut); err != nil {
		t.Fatalf("runExport failed: %v", err)
	}

	// What an older build wrote: a team bundle, listed in the manifest.
	old := repackBundle(t, bundleFile, func(stage string) {
		var manifest ExportManifest
		data, err := os.ReadFile(filepath.Join(stage, ManifestFileName))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatal(err)
		}
		if len(manifest.TeamStateRepos) != 0 {
			t.Errorf("export listed team state repos %+v, want none", manifest.TeamStateRepos)
		}
		for _, f := range manifest.Files {
			if strings.HasPrefix(f.Path, "state/teams/") {
				t.Errorf("export carried %s", f.Path)
			}
		}
		teamBundle := filepath.Join(stage, "state", "teams", "alpha.bundle")
		if err := os.MkdirAll(filepath.Dir(teamBundle), 0o700); err != nil {
			t.Fatal(err)
		}
		execGitInDir(t, stray, "bundle", "create", teamBundle, "--all")
		regenerated, err := generateManifest(stage, manifest.DatabaseType, manifest.StateRepoHead, manifest.StateRepoBranch, false)
		if err != nil {
			t.Fatal(err)
		}
		regenerated.TeamStateRepos = []RetiredTeamStateRepo{{Team: "alpha", Bundle: "state/teams/alpha.bundle"}}
		out, err := json.MarshalIndent(regenerated, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stage, ManifestFileName), out, 0o600); err != nil {
			t.Fatal(err)
		}
	})

	targetDir := t.TempDir()
	var importOut bytes.Buffer
	if err := runImport(ctx, []string{"-data-dir", targetDir, "-in", old}, &importOut); err != nil {
		t.Fatalf("importing a bundle that lists a team state repo: %v", err)
	}
	if !strings.Contains(importOut.String(), `ignoring 1 team configuration repository this bundle carries ("grain/config-alpha")`) ||
		!strings.Contains(importOut.String(), "grain/config is the only configuration repository") {
		t.Errorf("import output = %q, want the note that it ignored grain/config-alpha", importOut.String())
	}
	if _, err := os.Stat(filepath.Join(targetDir, "team-state-repos")); !os.IsNotExist(err) {
		t.Errorf("import restored team-state-repos: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(daemon.StateRepoDir(targetDir), "config.yaml")); err != nil || string(got) != "setting: true\n" {
		t.Errorf("grain/config's config.yaml = %q, %v", got, err)
	}
}

// Without -force, import refuses a target that holds secrets or a state
// repository even when it has no database, and writes nothing into it.
func TestImportRequiresForceForExistingSecretsAndRepos(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srcDir := newExportSource(t)
	if err := os.MkdirAll(filepath.Join(srcDir, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "secrets", "token"), []byte("from-bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundleFile := filepath.Join(t.TempDir(), "migration.tar.gz")
	var out bytes.Buffer
	if err := runExport(ctx, []string{"-data-dir", srcDir, "-out", bundleFile}, &out); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  string
	}{
		{"secrets", func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "secrets", "token"), []byte("already here"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "secrets"},
		{"state repo", func(t *testing.T, dir string) {
			commitFile(t, daemon.StateRepoDir(dir), "x.yaml", "x\n")
		}, "a state repository"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			targetDir := t.TempDir()
			tc.setup(t, targetDir)
			err := runImport(ctx, []string{"-data-dir", targetDir, "-in", bundleFile}, &out)
			if err == nil || !strings.Contains(err.Error(), "-force") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("import without -force: err = %v, want one naming %q and -force", err, tc.want)
			}
			if _, err := os.Stat(filepath.Join(targetDir, "store", "grain.db")); !os.IsNotExist(err) {
				t.Errorf("refused import still wrote a database: %v", err)
			}
			if tc.name == "secrets" {
				if data, _ := os.ReadFile(filepath.Join(targetDir, "secrets", "token")); string(data) != "already here" {
					t.Errorf("refused import overwrote secrets/token with %q", data)
				}
			}
		})
	}
}

// Import restores by walking the extracted bundle, so a file the manifest
// does not list -- and so nothing has checksummed -- is refused outright
// rather than restored unchecked.
func TestImportRejectsFilesTheManifestDoesNotList(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srcDir := newExportSource(t)
	bundleFile := filepath.Join(t.TempDir(), "migration.tar.gz")
	var out bytes.Buffer
	if err := runExport(ctx, []string{"-data-dir", srcDir, "-out", bundleFile}, &out); err != nil {
		t.Fatal(err)
	}

	for _, extra := range []string{"secrets/github/injected.token", "state/teams/ghost.bundle"} {
		t.Run(extra, func(t *testing.T) {
			tampered := repackBundle(t, bundleFile, func(stage string) {
				path := filepath.Join(stage, filepath.FromSlash(extra))
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("unlisted"), 0o600); err != nil {
					t.Fatal(err)
				}
			})
			targetDir := t.TempDir()
			err := runImport(ctx, []string{"-data-dir", targetDir, "-in", tampered}, &out)
			if err == nil || !strings.Contains(err.Error(), "not listed in the manifest") {
				t.Fatalf("import of a bundle with unlisted %s: err = %v, want 'not listed in the manifest'", extra, err)
			}
			if _, err := os.Stat(filepath.Join(targetDir, filepath.FromSlash(extra))); !os.IsNotExist(err) {
				t.Errorf("unlisted %s was restored: %v", extra, err)
			}
		})
	}
}

// A bundle written before team state repositories were carried (format
// version 1) still imports.
func TestImportAcceptsFormatVersion1Bundles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srcDir := newExportSource(t)
	commitFile(t, daemon.StateRepoDir(srcDir), "config.yaml", "setting: true\n")
	bundleFile := filepath.Join(t.TempDir(), "migration.tar.gz")
	var out bytes.Buffer
	if err := runExport(ctx, []string{"-data-dir", srcDir, "-out", bundleFile}, &out); err != nil {
		t.Fatal(err)
	}
	v1 := repackBundle(t, bundleFile, func(stage string) {
		path := filepath.Join(stage, ManifestFileName)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var manifest map[string]any
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatal(err)
		}
		manifest["formatVersion"] = 1
		data, err = json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	})

	targetDir := t.TempDir()
	if err := runImport(ctx, []string{"-data-dir", targetDir, "-in", v1}, &out); err != nil {
		t.Fatalf("importing a version 1 bundle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(daemon.StateRepoDir(targetDir), "config.yaml")); err != nil {
		t.Errorf("state repo not restored from version 1 bundle: %v", err)
	}
}
