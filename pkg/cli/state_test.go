package cli

// `grain state status` is what an operator runs to find out where this
// installation's state lives and what survives a rebuilt host -- which
// is exactly why the secrets key is reported there. Everything else in
// that output can be cloned back from the remote; the key cannot, and a
// deployment only finds that out at the moment it no longer has it.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/daemon"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/secrets"
	"github.com/bwsalmon/grain/pkg/staterepo"
)

func TestStateStatusReportsTheSecretsKeyAndSaysToBackItUp(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.WriteKeyFile(daemon.SecretsConfig(dataDir).KeyFile, key); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := stateStatus(context.Background(), dataDir, &out); err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{
		daemon.SecretsConfig(dataDir).KeyFile,
		key.Public(),
		"back this file up",
		// The way to put it back, named where the problem is: a redeploy
		// that has to be told about this file at the moment it is gone is
		// too late.
		"GRAIN_SECRETS_KEY",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("`grain state status` never mentions %q:\n%s", want, out.String())
		}
	}
}

// A command that reports on a key must not be the thing that creates
// one: secrets.Open mints on a data directory that has none, and a
// status run that quietly minted would hand an operator a key their
// repository's secrets file is not encrypted to.
func TestStateRestoreSnapshotAndStatusReporting(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	// Initialize sqlite store
	db, err := daemon.OpenRawDB(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := model.New(db).Init(ctx); err != nil {
		t.Fatal(err)
	}

	// Create a dummy snapshot directory
	snapDir := filepath.Join(dataDir, staterepo.SettingsSnapshotsDir, "20261008T120000Z-abcd12345678")
	if err := os.MkdirAll(filepath.Join(snapDir, staterepo.TablesDir), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := staterepo.SnapshotManifest{
		CreatedAt: time.Now().UTC(),
		Base:      "base1111",
		Head:      "abcd12345678",
	}
	mb, _ := json.Marshal(manifest)
	_ = os.WriteFile(filepath.Join(snapDir, staterepo.SnapshotManifestFile), mb, 0o600)
	// Write empty/valid grain_config.json table dump
	_ = os.WriteFile(filepath.Join(snapDir, staterepo.TablesDir, "grain_config.json"), []byte("[]"), 0o600)

	// Test state status reports snapshot
	var out bytes.Buffer
	if err := stateStatus(ctx, dataDir, &out); err != nil {
		t.Fatalf("stateStatus failed: %v", err)
	}
	if !strings.Contains(out.String(), "snapshot:") || !strings.Contains(out.String(), snapDir) {
		t.Errorf("stateStatus output does not mention snapshot %s:\n%s", snapDir, out.String())
	}

	// Test state restore-snapshot
	var restoreOut bytes.Buffer
	if err := stateRestoreSnapshot(ctx, dataDir, &restoreOut, []string{snapDir}); err != nil {
		t.Fatalf("stateRestoreSnapshot failed: %v", err)
	}
	if !strings.Contains(restoreOut.String(), "restored settings and memory tables from") {
		t.Errorf("unexpected restore-snapshot output:\n%s", restoreOut.String())
	}
}

func TestStateRepoCommandNamesReportsAndClearsExternalConfigRepo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := t.TempDir()

	// Initially internal mode.
	var out bytes.Buffer
	if err := stateRepoCommand(ctx, dataDir, &out, nil); err != nil {
		t.Fatalf("stateRepoCommand(no args): %v", err)
	}
	if !strings.Contains(out.String(), "mode:       internal") {
		t.Fatalf("expected mode: internal in output, got:\n%s", out.String())
	}

	// Configure an external config repository URL with branch and token-file.
	out.Reset()
	const externalURL = "https://github.com/bwsalmon/grain-config-external.git"
	if err := stateRepoCommand(ctx, dataDir, &out, []string{"-branch", "trunk", "-token-file", "/tmp/tok", externalURL}); err != nil {
		t.Fatalf("stateRepoCommand(set url): %v", err)
	}
	settings, err := staterepo.LoadSettings(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Remote != externalURL || settings.Branch != "trunk" || settings.TokenFile != "/tmp/tok" {
		t.Fatalf("saved settings = %+v", settings)
	}
	if !strings.Contains(out.String(), "mode:       external (read-only IaC)") ||
		!strings.Contains(out.String(), externalURL) ||
		!strings.Contains(out.String(), "branch trunk") {
		t.Fatalf("expected external mode URL and branch in output, got:\n%s", out.String())
	}

	// Switch back to internal mode with -none.
	out.Reset()
	if err := stateRepoCommand(ctx, dataDir, &out, []string{"-none"}); err != nil {
		t.Fatalf("stateRepoCommand(-none): %v", err)
	}
	settings, err = staterepo.LoadSettings(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Remote != "" || settings.TokenFile != "" {
		t.Fatalf("-none left settings = %+v", settings)
	}
}
