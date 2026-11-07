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

package staterepo_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

const implicitHostServer = "https://example.com/caps/host-scripts"

// hostdLocal is a server's hostd: local as an exported dump spells it,
// in JSON (escaped or not) or YAML.
var hostdLocal = regexp.MustCompile(`hostd\W{1,6}local\b`)

// implicitHostDump is a grain/config dump holding host work nothing
// places: a run: host capability server, and a connection whose CLI runs
// on the host because Settings -> Models says "Host". extraHosts are
// registered beside the co-located one.
func implicitHostDump(t *testing.T, extraHosts ...string) string {
	t.Helper()
	ctx := context.Background()
	src, srcDB := openDB(t)
	if err := src.EnsureLocalHostd(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range extraHosts {
		if _, _, err := src.CreateHostd(ctx, model.Hostd{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := model.DefaultConfig()
	cfg.AgentBackends = []model.AgentBackend{
		{ID: "agy-login", Framework: model.AgentFrameworkAntigravity, AuthMode: model.AntigravityAuthAmbient},
		{ID: "claude-key", Framework: model.AgentFrameworkClaude, AuthMode: model.CodexAuthAPIKey},
	}
	cfg.CLIRun = map[string]model.CLIRunSetting{model.AgentFrameworkAntigravity: {Location: model.CLIRunHost}}
	cfg.MCPServers = map[string]protocol.ServerConfig{
		implicitHostServer: {Command: "grain-host-scripts", Run: protocol.RunHost},
	}
	if err := src.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, srcDB, dir); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.WriteSchemaVersion(dir, model.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Not parallel: it sets the package's import hook, which every import in
// the process reads. Parallel tests run once the sequential ones are done.
func TestImportPlacesImplicitHostWorkOnTheOnlyHost(t *testing.T) {
	ctx := context.Background()
	restore := staterepo.SetImplicitHostPlacement(&model.ImplicitHostPlacement{Default: model.LocalHostd, LocalUsable: true})
	t.Cleanup(restore)

	dir := implicitHostDump(t)
	dst, dstDB := openDB(t)
	if err := staterepo.Import(ctx, dstDB, dir); err != nil {
		t.Fatal(err)
	}
	cfg, err := dst.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, b := range cfg.AgentBackends {
		got[b.ID] = b.Hostd
	}
	// The antigravity connection runs on the host (Settings says so) and
	// is placed; the claude one runs in the container and is left alone.
	if got["agy-login"] != model.LocalHostd || got["claude-key"] != "" {
		t.Errorf("connection hosts = %v, want agy-login on %s and claude-key unplaced", got, model.LocalHostd)
	}
	s := cfg.MCPServers[implicitHostServer]
	if s.Run != protocol.RunHostd || string(s.Hostd) != model.LocalHostd {
		t.Errorf("run: host server = %+v, want run: hostd, hostd: %s", s, model.LocalHostd)
	}

	// The next export writes the placement down in grain/config.
	out := t.TempDir()
	if err := staterepo.Export(ctx, dstDB, out); err != nil {
		t.Fatal(err)
	}
	var placed bool
	err = filepath.WalkDir(out, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(data)
		if strings.Contains(text, "grain-host-scripts") && hostdLocal.MatchString(text) {
			placed = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !placed {
		t.Errorf("the exported dump does not place %s on %s", implicitHostServer, model.LocalHostd)
	}
}

func TestImportLeavesImplicitHostWorkWithSeveralHosts(t *testing.T) {
	ctx := context.Background()
	restore := staterepo.SetImplicitHostPlacement(&model.ImplicitHostPlacement{Default: model.LocalHostd, LocalUsable: true})
	t.Cleanup(restore)

	dir := implicitHostDump(t, "ws-a")
	dst, dstDB := openDB(t)
	if err := staterepo.Import(ctx, dstDB, dir); err != nil {
		t.Fatal(err)
	}
	cfg, err := dst.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range cfg.AgentBackends {
		if b.Hostd != "" {
			t.Errorf("connection %s placed on %q with two hosts registered", b.ID, b.Hostd)
		}
	}
	if s := cfg.MCPServers[implicitHostServer]; s.Hostd != "" {
		t.Errorf("server placed on %q with two hosts registered", s.Hostd)
	}
}

func TestImportWithoutAPlacementHookLeavesTheDumpAsItIs(t *testing.T) {
	ctx := context.Background()
	restore := staterepo.SetImplicitHostPlacement(nil)
	t.Cleanup(restore)

	dir := implicitHostDump(t)
	dst, dstDB := openDB(t)
	if err := staterepo.Import(ctx, dstDB, dir); err != nil {
		t.Fatal(err)
	}
	cfg, err := dst.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s := cfg.MCPServers[implicitHostServer]; s.Run != protocol.RunHost || s.Hostd != "" {
		t.Errorf("server = %+v, want it as the dump has it", s)
	}
}
