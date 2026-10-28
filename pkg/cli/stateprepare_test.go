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
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// scripts/setup.sh re-run across a schema change (grain/task-4357): the
// build being installed finds a store the build before it wrote, and
// `grain state prepare-upgrade` has to leave exactly what the Upgrade
// button leaves -- so the next start keeps the deployment's rows rather
// than coming up on an empty store.
func TestPrepareUpgradeCarriesASetupShReRunAcrossASchemaChange(t *testing.T) {
	if model.V2BaselineSchemaVersion-1 < model.BaselineSchemaVersion {
		t.Skip("this build is at the single-tenant baseline, so the schema before it is one no build migrates from")
	}
	dataDir, _, filed := deploymentMidUpgrade(t, false)

	var out bytes.Buffer
	if err := statePrepareUpgrade(context.Background(), dataDir, &out, nil); err != nil {
		t.Fatalf("prepare-upgrade: %v", err)
	}
	want := fmt.Sprintf("prepared %d %d %s\n", model.V2BaselineSchemaVersion-1, model.SchemaVersion,
		daemon.SchemaUpgradeDir(dataDir))
	if out.String() != want {
		t.Fatalf("prepare-upgrade printed %q, want %q", out.String(), want)
	}

	uiAddr := freeTCPAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		err := daemon.Run(ctx, daemon.Config{
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
	client := ui.NewHTTPClient("http://" + uiAddr)
	waitForUI(t, ctx, client)

	task, err := client.Task(ctx, filed)
	if err != nil {
		t.Fatalf("the task filed before the schema change did not survive it: %v", err)
	}
	if task.Title != "Filed by the build before the schema change" {
		t.Errorf("the task came back as %q", task.Title)
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	archived := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "store.schema"+strconv.Itoa(model.V2BaselineSchemaVersion-1)+"-") {
			archived = true
		}
	}
	if !archived {
		t.Errorf("the store at the old schema was not kept; %s holds %v", dataDir, names(entries))
	}
}

// A store already at this build's schema -- setup.sh's own marker was
// stale, or an interrupted migration has already rebuilt the store --
// is reported as current and nothing is written over it.
func TestPrepareUpgradeLeavesACurrentStoreAlone(t *testing.T) {
	dataDir := t.TempDir()
	db := openDataDirStore(t, dataDir)
	if err := model.New(db).Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	db.Close()

	var out bytes.Buffer
	if err := statePrepareUpgrade(context.Background(), dataDir, &out, nil); err != nil {
		t.Fatalf("prepare-upgrade: %v", err)
	}
	if got, want := out.String(), fmt.Sprintf("current %d\n", model.SchemaVersion); got != want {
		t.Fatalf("prepare-upgrade printed %q, want %q", got, want)
	}
	if _, err := os.Stat(daemon.SchemaUpgradeDir(dataDir)); !os.IsNotExist(err) {
		t.Errorf("an upgrade note was written for a store that needs none (%v)", err)
	}
}

// No store at all is nothing to carry, and says so rather than creating
// one.
func TestPrepareUpgradeWithNoStore(t *testing.T) {
	dataDir := t.TempDir()
	var out bytes.Buffer
	if err := statePrepareUpgrade(context.Background(), dataDir, &out, nil); err != nil {
		t.Fatalf("prepare-upgrade: %v", err)
	}
	if out.String() != "none\n" {
		t.Fatalf("prepare-upgrade printed %q, want none", out.String())
	}
	if _, err := os.Stat(filepath.Join(dataDir, "store")); !os.IsNotExist(err) {
		t.Errorf("prepare-upgrade created a store (%v)", err)
	}
}
