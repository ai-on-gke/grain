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
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

func TestGrainUpgradeCLI(t *testing.T) {
	dir := t.TempDir()
	store, db, err := daemon.OpenStore("sqlite", "", dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConfig(ctx, model.DefaultConfig()); err != nil {
		t.Fatal(err)
	}

	prevDigest := "sha256:" + strings.Repeat("a", 64)
	newDigest := "sha256:" + strings.Repeat("b", 64)
	prevRef := "docker.io/grainai/grain:v3.1.0@" + prevDigest
	refFile := filepath.Join(dir, "image.env")
	if err := os.WriteFile(refFile, []byte("GRAIN_IMAGE="+prevRef+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	up := upgrade.New(upgrade.Config{
		GraceWindow: 20 * time.Second,
		Image: &upgrade.ImageConfig{
			Repository: "docker.io/grainai/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			DigestCmd:  []string{"sh", "-c", `case "$1" in *v3.1.0*) echo "docker.io/grainai/grain@` + prevDigest + `";; *) echo "docker.io/grainai/grain@` + newDigest + `";; esac`, "sh"},
			RunCmd:     []string{"true"},
			ListCmd:    []string{"true"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})
	t.Cleanup(func() { _, _ = up.Cancel("cleanup", "") })

	srv := ui.NewServer(ui.Config{
		Actor:         ui.DefaultActor("alice@example.com"),
		Capabilities:  ui.OfferedCapabilities(),
		Upgrader:      up,
		LocalAPIToken: testAccessCode,
	}, store)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	// Start manual upgrade and inspect status via CLI.
	if err := runCLI([]string{"-server", ts.URL, "-token", testAccessCode, "upgrade", "start", "v3.2.0"}); err != nil {
		t.Fatalf("grain upgrade start: %v", err)
	}
	if err := runCLI([]string{"-server", ts.URL, "-token", testAccessCode, "upgrade", "status"}); err != nil {
		t.Fatalf("grain upgrade status: %v", err)
	}

	// Cancel running upgrade with -reason and check audit log.
	if err := runCLI([]string{"-server", ts.URL, "-token", testAccessCode, "upgrade", "cancel", "-reason", "clicked wrong button"}); err != nil {
		t.Fatalf("grain upgrade cancel: %v", err)
	}
	st, err := up.Status()
	if err != nil || st.Phase != upgrade.PhaseCancelled {
		t.Fatalf("status after CLI cancel = %+v, %v; want PhaseCancelled", st, err)
	}
	audits, err := store.ConfigAudit(ctx, 5)
	if err != nil || len(audits) == 0 || !strings.Contains(audits[0].Summary, "clicked wrong button") {
		t.Fatalf("config_audit = %+v, %v; want reason recorded", audits, err)
	}

	// Let a scheduled/immediate upgrade complete and then run `grain upgrade rollback`.
	if err := up.StartScheduled("v3.2.0"); err != nil {
		t.Fatalf("StartScheduled: %v", err)
	}
	<-up.Done()
	if cur, _ := up.Running(); !strings.Contains(cur.Ref, "v3.2.0") {
		t.Fatalf("Running = %+v, want v3.2.0", cur)
	}

	if err := runCLI([]string{"-server", ts.URL, "-token", testAccessCode, "upgrade", "rollback"}); err != nil {
		t.Fatalf("grain upgrade rollback: %v", err)
	}
	// Rollback started in manual mode (hits GraceWindow); verify it targets v3.1.0, then cancel cleanup.
	rbStatus, err := up.Status()
	if err != nil || rbStatus.Branch != "v3.1.0" {
		t.Fatalf("rollback status = %+v, %v; want branch=v3.1.0", rbStatus, err)
	}
	_, _ = up.Cancel("cleanup", "")
}
