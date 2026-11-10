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
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func runAccessCLI(t *testing.T, serverURL, token string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("GRAIN_SERVER", serverURL)
	t.Setenv("GRAIN_TOKEN", token)
	t.Setenv("GRAIN_LOCAL_API_TOKEN_FILE", "/nonexistent")

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	runErr := runCLI(args)
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	_ = r.Close()
	return buf.String(), runErr
}

func TestGrainAccessCLI(t *testing.T) {
	const code = "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567"
	store, _ := sqlitetest.Open(t)
	srv := ui.NewServer(ui.Config{
		Actor:         ui.DefaultActor("operator@example.com"),
		LocalAPIToken: code,
	}, store)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// 1. Empty list initially.
	out, err := runAccessCLI(t, ts.URL, code, "access", "list")
	if err != nil {
		t.Fatalf("grain access list: %v", err)
	}
	if !strings.Contains(out, "No allowed users or groups.") {
		t.Fatalf("empty list output = %q", out)
	}

	// 2. Allow user:<email> -> created at grain/operator, which needs a
	// reason; re-running is an idempotent no-op.
	if _, err := runAccessCLI(t, ts.URL, code, "access", "allow", "user:Alice@Example.com"); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("grain access allow at grain/operator without --reason: %v, want refused naming the reason", err)
	}
	out, err = runAccessCLI(t, ts.URL, code, "access", "allow", "--reason", "on-call", "user:Alice@Example.com")
	if err != nil {
		t.Fatalf("grain access allow: %v", err)
	}
	if !strings.Contains(out, "allowed user:alice@example.com as grain/operator") {
		t.Fatalf("allow output = %q", out)
	}

	out, err = runAccessCLI(t, ts.URL, code, "access", "allow", "user:alice@example.com")
	if err != nil {
		t.Fatalf("grain access allow (idempotent repeat): %v", err)
	}
	if !strings.Contains(out, "already allowed: user:alice@example.com") {
		t.Fatalf("repeat allow output = %q", out)
	}

	// 3. Allow group:<email> and list (-json and plain).
	if _, err := runAccessCLI(t, ts.URL, code, "access", "allow", "--role", "grain/viewer", "group:team@example.com"); err != nil {
		t.Fatalf("grain access allow group: %v", err)
	}
	out, err = runAccessCLI(t, ts.URL, code, "access", "role", "group:team@example.com", "grain/contributor")
	if err != nil || !strings.Contains(out, "group:team@example.com: role grain/viewer -> grain/contributor") {
		t.Fatalf("grain access role: %q, %v", out, err)
	}

	jsonOut, err := runAccessCLI(t, ts.URL, code, "-json", "access", "list")
	if err != nil {
		t.Fatalf("grain -json access list: %v", err)
	}
	var parsed ui.AccessPrincipalsResponse
	if err := json.Unmarshal([]byte(jsonOut), &parsed); err != nil {
		t.Fatalf("decoding json list %q: %v", jsonOut, err)
	}
	if len(parsed.Entries) != 2 || parsed.Entries[1].Role != "grain/contributor" {
		t.Fatalf("expected 2 entries, the group at grain/contributor, got %+v", parsed.Entries)
	}

	// 4. Deny removes the entry.
	out, err = runAccessCLI(t, ts.URL, code, "access", "deny", "--reason", "left the team", "--confirm-self", "user:alice@example.com")
	if err != nil {
		t.Fatalf("grain access deny: %v", err)
	}
	if !strings.Contains(out, "removed user:alice@example.com") {
		t.Fatalf("deny output = %q", out)
	}
	audits, err := store.ConfigAudit(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	var sawReason bool
	for _, a := range audits {
		sawReason = sawReason || (a.Surface == "access" && a.Summary == "removed user:alice@example.com (was grain/operator) (left the team)")
	}
	if !sawReason {
		t.Fatalf("config_audit = %+v, want the removal with its old role and --reason", audits)
	}

	// 5. Invalid token / unauthenticated caller is refused.
	if _, err := runAccessCLI(t, ts.URL, "WRONG-TOKEN", "access", "allow", "user:mallory@example.com"); err == nil {
		t.Fatal("grain access allow with wrong token succeeded, want error")
	}
}

func TestAccessAllowDelegateCLI(t *testing.T) {
	const code = "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567"
	store, _ := sqlitetest.Open(t)
	srv := ui.NewServer(ui.Config{
		Actor:         ui.DefaultActor("operator@example.com"),
		LocalAPIToken: code,
	}, store)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// delegate:<email> without --reason is refused.
	if _, err := runAccessCLI(t, ts.URL, code, "access", "allow", "delegate:slack-bot@proj.iam.gserviceaccount.com"); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("grain access allow delegate without --reason: %v, want refused naming reason", err)
	}

	// delegate:<email> with --reason and --label defaults to grain/delegated.
	out, err := runAccessCLI(t, ts.URL, code, "access", "allow",
		"--reason", "workspace slack bot",
		"--label", "slack-bot",
		"delegate:Slack-Bot@proj.iam.gserviceaccount.com")
	if err != nil {
		t.Fatalf("grain access allow delegate: %v", err)
	}
	if !strings.Contains(out, "allowed delegate:slack-bot@proj.iam.gserviceaccount.com as grain/delegated") {
		t.Fatalf("allow delegate output = %q", out)
	}

	jsonOut, err := runAccessCLI(t, ts.URL, code, "-json", "access", "list")
	if err != nil {
		t.Fatalf("grain -json access list: %v", err)
	}
	var parsed ui.AccessPrincipalsResponse
	if err := json.Unmarshal([]byte(jsonOut), &parsed); err != nil {
		t.Fatalf("decoding json list %q: %v", jsonOut, err)
	}
	if len(parsed.Entries) != 1 ||
		parsed.Entries[0].Entry != "delegate:slack-bot@proj.iam.gserviceaccount.com" ||
		parsed.Entries[0].Role != "grain/delegated" ||
		parsed.Entries[0].Label != "slack-bot" {
		t.Fatalf("expected 1 delegate entry at grain/delegated with label slack-bot, got %+v", parsed.Entries)
	}

	// The plain-text list shows the label too, not only -json.
	textOut, err := runAccessCLI(t, ts.URL, code, "access", "list")
	if err != nil {
		t.Fatalf("grain access list: %v", err)
	}
	if !strings.Contains(textOut, "delegate:slack-bot@proj.iam.gserviceaccount.com\tgrain/delegated") ||
		!strings.Contains(textOut, "\tlabel=slack-bot") {
		t.Fatalf("access list text output = %q, want the delegate entry with label=slack-bot", textOut)
	}
}

func TestAccessAllowHelpDocumentsLabelRule(t *testing.T) {
	helpOut, err := runAccessCLI(t, "http://127.0.0.1:1", "", "access", "--help")
	if err != nil {
		t.Fatalf("grain access --help: %v", err)
	}
	if !strings.Contains(helpOut, "[a-z][a-z0-9-]{0,31}") {
		t.Errorf("grain access --help missing label format rule:\n%s", helpOut)
	}

	origStderr := os.Stderr
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	os.Stderr = w
	defer func() { os.Stderr = origStderr }()

	_, _ = runAccessCLI(t, "http://127.0.0.1:1", "", "access", "allow", "--help")
	_ = w.Close()
	var errBuf bytes.Buffer
	_, _ = io.Copy(&errBuf, r)
	_ = r.Close()
	if !strings.Contains(errBuf.String(), "[a-z][a-z0-9-]{0,31}") {
		t.Errorf("grain access allow --help missing label format rule:\n%s", errBuf.String())
	}
}
