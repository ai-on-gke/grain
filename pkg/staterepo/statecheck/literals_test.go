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

package statecheck

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// exportServers writes, into dir, the state repository a deployment whose
// settings row carries servers exports.
func exportServers(t *testing.T, dir string, servers map[string]protocol.ServerConfig) {
	t.Helper()
	ctx := context.Background()
	db, cleanup, err := ScratchStore()
	if err != nil {
		t.Fatalf("opening a store: %v", err)
	}
	defer cleanup()
	if err := model.New(db).PutConfig(ctx, model.Config{PollInterval: 30 * time.Second, MaxWorkers: 1, MCPServers: servers}); err != nil {
		t.Fatalf("putting a settings row: %v", err)
	}
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(dir, model.SchemaVersion); err != nil {
		t.Fatalf("stamping: %v", err)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com",
		"-c", "commit.gpgsign=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

const (
	cleanServer   = "https://example.com/caps/clean"
	literalServer = "https://example.com/caps/typed-in"
	heldServer    = "https://example.com/caps/held"
	typedInValue  = "plain-value-nobody-would-guess"
	heldValue     = "held-value-from-before-the-rule"
)

// CE-2 (docs/security/audits/2026-11-10-credential-exposure.md): a
// grain/config pull request that types a credential into a capability
// server's headers fails `grain state check`, while one the base already
// held is only warned about -- it is not this change's doing, and the
// daemon moves it at its next start. Neither message repeats a value.
func TestStateCheckRefusesALiteralHeaderAPullRequestAdds(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	base := map[string]protocol.ServerConfig{
		cleanServer: {ServerURL: "https://cap.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer $secret:cap-token"}},
		heldServer:  {ServerURL: "https://held.example.com/mcp", Headers: map[string]string{"X-Api-Key": heldValue}},
	}
	exportServers(t, dir, base)
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "base")

	// The base alone: the held literal warns, and the check passes.
	var out bytes.Buffer
	b, err := GitBase(dir, "main")
	if err != nil {
		t.Fatalf("GitBase: %v", err)
	}
	if err := Run(context.Background(), &out, dir, Options{Base: b}); err != nil {
		t.Fatalf("checking the base against itself: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "caps/held carries a credential typed in at header X-Api-Key") {
		t.Errorf("output does not warn about the held literal:\n%s", out.String())
	}

	// The pull request: a new server with a literal header, and nothing
	// else changed.
	pr := map[string]protocol.ServerConfig{literalServer: {
		ServerURL: "https://typed.example.com/mcp", Headers: map[string]string{"X-Api-Key": typedInValue},
	}}
	for id, sc := range base {
		pr[id] = sc
	}
	exportServers(t, dir, pr)
	out.Reset()
	err = Run(context.Background(), &out, dir, Options{Base: b})
	if err == nil {
		t.Fatalf("a pull request adding a literal header passed:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "caps/typed-in: header X-Api-Key") || strings.Contains(err.Error(), "caps/held") {
		t.Errorf("error = %v, want it naming only the added literal", err)
	}
	if !strings.Contains(out.String(), "caps/held carries a credential typed in") {
		t.Errorf("output does not still warn about the held literal:\n%s", out.String())
	}
	for _, v := range []string{typedInValue, heldValue} {
		if strings.Contains(err.Error()+out.String(), v) {
			t.Errorf("the check repeated a value it refused or warned about: %q", v)
		}
	}

	// Changing a held literal's value is adding one, too.
	pr[heldServer] = protocol.ServerConfig{ServerURL: "https://held.example.com/mcp", Headers: map[string]string{"X-Api-Key": "a-new-value"}}
	delete(pr, literalServer)
	exportServers(t, dir, pr)
	out.Reset()
	if err := Run(context.Background(), &out, dir, Options{Base: b}); err == nil || !strings.Contains(err.Error(), "caps/held: header X-Api-Key") {
		t.Errorf("changing a held literal: err = %v, want it refused", err)
	}

	// With no base to compare with, nothing can be told new from held:
	// every one is a warning, as it is when -migrate rehearses an upgrade.
	for _, opts := range []Options{{}, {Base: b, Migrate: true}} {
		out.Reset()
		if err := Run(context.Background(), &out, dir, opts); err != nil {
			t.Errorf("Run(%+v) = %v, want warnings only\n%s", opts, err, out.String())
		}
		if !strings.Contains(out.String(), "caps/held carries a credential typed in") {
			t.Errorf("Run(%+v) output does not warn:\n%s", opts, out.String())
		}
	}

	// And the fix the error points at passes: a $secret: reference.
	pr[heldServer] = protocol.ServerConfig{ServerURL: "https://held.example.com/mcp", Headers: map[string]string{"X-Api-Key": "$secret:held-key"}}
	exportServers(t, dir, pr)
	out.Reset()
	if err := Run(context.Background(), &out, dir, Options{Base: b}); err != nil {
		t.Errorf("a pull request moving the literal to a reference: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "typed in") {
		t.Errorf("output still warns after the literal moved:\n%s", out.String())
	}
}

// A base the check cannot read fails closed: every literal counts as new.
func TestStateCheckCountsEveryLiteralNewWhenTheBaseIsUnreadable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	exportServers(t, dir, map[string]protocol.ServerConfig{
		heldServer: {ServerURL: "https://held.example.com/mcp", Headers: map[string]string{"X-Api-Key": heldValue}},
	})
	unreadable := func(path string) ([]byte, error) { return []byte("{not json"), nil }
	var out bytes.Buffer
	if err := Run(context.Background(), &out, dir, Options{Base: unreadable}); err == nil {
		t.Fatalf("an unreadable base let a literal through:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "could not be read") {
		t.Errorf("output does not say the base could not be read:\n%s", out.String())
	}
}

func TestGitBaseRefusesARevisionThatIsNotThere(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	if _, err := GitBase(dir, "no-such-branch"); err == nil {
		t.Error("GitBase accepted a revision the checkout does not have")
	}
}
