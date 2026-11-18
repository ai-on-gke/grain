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

//go:build linux

package intree

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// Run the generated, unmodified collector body through hostd, the real
// capability protocol and the sandbox CLI. Only the allowlist entry name
// and server binary are replaced by the existing hostd test helper.
func TestOverlayFSEvidenceCanBeCalledThroughHostdWithApproval(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	output, err := exec.Command(python, "-I", "../../../scripts/overlayfs-evidence-host-script.py").CombinedOutput()
	if err != nil {
		t.Fatalf("generate host-script registration: %v: %s", err, output)
	}
	var bundle struct {
		ID    string                `json:"id"`
		Entry protocol.ServerConfig `json:"entry"`
	}
	if err := json.Unmarshal(output, &bundle); err != nil {
		t.Fatal(err)
	}
	var approved atomic.Bool
	var approvals atomic.Int32
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/wait") {
			status := "denied"
			if approved.Load() {
				status = "approved"
			}
			json.NewEncoder(w).Encode(map[string]string{"status": status})
			return
		}
		var request struct {
			ID string `json:"id"`
		}
		json.NewDecoder(r.Body).Decode(&request)
		approvals.Add(1)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"id": request.ID, "status": "pending"})
	}))
	defer callback.Close()
	data := t.TempDir()
	client := startHostScriptHostd(t, data)
	bundle.Entry.Command = "host-script"
	bundle.Entry.Env = map[string]string{hostScriptHelperEnv: "1", hostScriptHelperData: data}
	ps, err := protocol.Attach(context.Background(), bundle.ID, bundle.Entry,
		func(string) (string, bool) { return "", false },
		protocol.DialOptions{Hostd: &client, Stderr: os.Stderr})
	if err != nil {
		t.Fatal(err)
	}
	defer ps.Close()
	ctx := protocol.WithApprovalCallback(context.Background(), protocol.ApprovalCallback{
		URL: callback.URL, Token: "fixture-token",
	})
	response, err := ps.Mint(ctx, "host-script", "e1-42-1-0", time.Now().Add(2*time.Minute),
		map[string]any{"script": "overlayfs-evidence"})
	if err != nil || response.Status != protocol.StatusOK {
		t.Fatalf("mint: %+v, %v", response, err)
	}
	sandbox := t.TempDir()
	for _, placement := range response.Placements {
		if err := os.WriteFile(filepath.Join(sandbox, filepath.Base(placement.Path)), []byte(placement.Content), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	command := hostScriptCmd{dir: sandbox, name: "overlayfs-evidence"}
	if denied := call(t, command); denied.code != 125 || denied.stdout != "" {
		t.Fatalf("denied call ran the collector: %+v", denied)
	}
	if rejected := call(t, command, "$(id)"); rejected.code != 125 || rejected.stdout != "" {
		t.Fatalf("arguments were not refused: %+v", rejected)
	}
	approved.Store(true)
	result := call(t, command)
	if result.code != 0 {
		t.Fatalf("approved collector: exit %d, stderr %s", result.code, result.stderr)
	}
	lines := strings.Split(strings.TrimSpace(result.stdout), "\n")
	if len(lines) < 2 {
		t.Fatalf("collector did not return evidence records: %q", result.stdout)
	}
	var capture, summary map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &capture); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &summary); err != nil {
		t.Fatal(err)
	}
	if capture["section"] != "capture" || capture["uid"] != float64(os.Getuid()) ||
		summary["section"] != "summary" || approvals.Load() != 2 {
		t.Fatalf("unexpected capture/approval coverage: %+v, %+v, approvals=%d", capture, summary, approvals.Load())
	}
}
