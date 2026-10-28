package cli

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/daemon"
	"github.com/bwsalmon/grain/pkg/model"
)

// A bundle `grain export` wrote on the build before this one carries a
// store at the previous schema. `grain import` used to restore it and
// then refuse it ("found 19, this build knows 20") with no way across;
// it now arms the same migration `grain state prepare-upgrade` does, and
// the next start keeps the deployment's rows (grain/task-4853).
func TestImportCarriesABundleFromThePreviousSchema(t *testing.T) {
	if model.V2BaselineSchemaVersion-1 < model.BaselineSchemaVersion {
		t.Skip("this build is at the single-tenant baseline, so the schema before it is one no build migrates from")
	}
	// Both daemons below set the process-wide default git host to their
	// fake GitHub (model.SetDefaultGitHost); put it back for the tests
	// that run after this one.
	prev := model.DefaultRepoHost()
	t.Cleanup(func() { model.SetDefaultGitHost(prev) })
	ctx := context.Background()
	srcDir, _, filed := deploymentMidUpgrade(t, false)

	bundle := filepath.Join(t.TempDir(), "migration.tar.gz")
	if err := runExport(ctx, []string{"-data-dir", srcDir, "-out", bundle}, &bytes.Buffer{}); err != nil {
		t.Fatalf("exporting the deployment at the previous schema: %v", err)
	}

	dataDir := filepath.Join(t.TempDir(), "imported")
	var out bytes.Buffer
	if err := runImport(ctx, []string{"-data-dir", dataDir, "-in", bundle}, &out); err != nil {
		t.Fatalf("importing a bundle at the previous schema: %v", err)
	}
	want := "schema version: " + strconv.Itoa(model.V2BaselineSchemaVersion-1) + ", migrates to " +
		strconv.Itoa(model.SchemaVersion) + " when grain next starts"
	if !strings.Contains(out.String(), want) {
		t.Errorf("import printed:\n%s\nwant a line containing %q", out.String(), want)
	}
	if _, err := os.Stat(filepath.Join(daemon.SchemaUpgradeDir(dataDir), daemon.SchemaUpgradeManifest)); err != nil {
		t.Fatalf("import armed no upgrade note: %v", err)
	}

	uiAddr := freeTCPAddr(t)
	runCtx, cancel := context.WithCancel(ctx)
	runErr := make(chan error, 1)
	seedTestAccessCode(t, dataDir)
	go func() {
		err := daemon.Run(runCtx, daemon.Config{
			DataDir: dataDir, SandboxDir: t.TempDir(), MaxWorkers: 1, PollInterval: time.Hour,
			GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true, UiAddr: uiAddr, Actor: "tester",
		})
		if err != nil {
			log.Printf("test: the daemon stopped: %v", err)
		}
		runErr <- err
	}()
	defer func() {
		cancel()
		<-runErr
	}()
	client := accessCodeClient("http://" + uiAddr)
	waitForUI(t, runCtx, client)

	task, err := client.Task(runCtx, filed)
	if err != nil {
		t.Fatalf("the task in the bundle did not survive the import across the schema change: %v", err)
	}
	if task.Title != "Filed by the build before the schema change" {
		t.Errorf("the task came back as %q", task.Title)
	}
	if _, err := os.Stat(daemon.SchemaUpgradeDir(dataDir)); !os.IsNotExist(err) {
		t.Errorf("the upgrade note is still armed after the start that spent it (%v)", err)
	}
}
