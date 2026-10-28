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

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSandboxAccessDialDefaultsToTheService(t *testing.T) {
	if got := sandboxAccessDial(Config{}, "grain"); got != "grain-access.grain.svc:7443" {
		t.Fatalf("dial = %q", got)
	}
	if got := sandboxAccessDial(Config{SandboxAccessDial: "10.0.0.1:7443"}, "grain"); got != "10.0.0.1:7443" {
		t.Fatalf("dial = %q", got)
	}
}

func TestSandboxAccessControllerIPsAreTheServiceAndThisPod(t *testing.T) {
	env := map[string]string{"GRAIN_ACCESS_SERVICE_HOST": "10.8.0.4", "GRAIN_POD_IP": "10.4.1.9"}
	got := sandboxAccessControllerIPs(func(k string) string { return env[k] })
	if want := []string{"10.8.0.4", "10.4.1.9"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("controller IPs = %v, want %v", got, want)
	}
	if got := sandboxAccessControllerIPs(func(string) string { return "" }); len(got) != 0 {
		t.Fatalf("controller IPs with nothing set = %v", got)
	}
}

func TestStartSandboxAccessKeepsOneCAInTheDataDir(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	var live func(string) bool
	cfg := Config{DataDir: dir, SandboxAccessAddr: "127.0.0.1:0"}
	access, err := startSandboxAccess(ctx, cfg, "dep", "grain", &live)
	if err != nil {
		t.Fatal(err)
	}
	if access.Dial != "grain-access.grain.svc:7443" {
		t.Fatalf("Dial = %q", access.Dial)
	}
	path := filepath.Join(dir, "sandbox-access-ca.pem")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("CA file mode = %v, want 0600", st.Mode().Perm())
	}
	if _, err := access.Acceptor.Mint("1-1", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := startSandboxAccess(ctx, cfg, "dep", "grain", &live); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatal("a second start replaced the CA")
	}
}
