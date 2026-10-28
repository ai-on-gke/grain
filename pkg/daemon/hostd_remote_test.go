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

package daemon

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/whererun"
)

// startDialingHostd serves a real grain hostd with no socket at all,
// dialing out to the controller at url with token, as one on an
// operator's workstation does to a daemon in a GKE cluster. Its
// allowlist is formatted as startTestHostd's is.
func startDialingHostd(t *testing.T, url, token, allowlist string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("hostd refuses to run as root")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no shell: %v", err)
	}
	sh, _ = filepath.EvalSymlinks(sh)
	dir := t.TempDir()
	allow := filepath.Join(dir, "hostd.yaml")
	if err := os.WriteFile(allow, []byte(fmt.Sprintf(allowlist, sh, dir)), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &hostd.Server{
		Allowlist:        allow,
		AllowlistOwners:  []uint32{uint32(os.Getuid())},
		StateDir:         filepath.Join(dir, "state"),
		Logf:             func(string, ...any) {},
		Remote:           hostd.Dialer{Server: url, Token: token, Host: "cloudtop", Build: "hostd-build"}.Dial,
		RemoteMinBackoff: 20 * time.Millisecond,
		RemoteMaxBackoff: 100 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

// A daemon on Kubernetes has no host of its own, but one taking a hostd
// that dials in has that hostd as its host executor: Settings lists it
// as connected with what it offers, a CLI set to host resolves through
// it, and a run: host capability server starts on it -- all over the
// session the hostd made.
func TestAKubernetesDaemonRunsHostCLIsThroughAHostdThatDialsIn(t *testing.T) {
	remote := &hostd.Remote{Token: "hostd-token"}
	ctl := httptest.NewServer(remote)
	t.Cleanup(ctl.Close)

	cfg := Config{K8sSandboxes: true, HostdRemote: remote}
	if err := CheckHostExecutorFlags(cfg); err != nil {
		t.Fatalf("CheckHostExecutorFlags = %v", err)
	}
	cfg = withExecutor(cfg, whererun.Detection{Signal: "KUBERNETES_SERVICE_HOST"}, noHostdLookup(t))
	if e := cfg.hostExecutor(); e.Kind != HostExecutorHostd || !e.RemoteHostd || !e.HostAvailable() {
		t.Fatalf("host executor = %+v, want hostd through the session", e)
	}

	// Before any hostd has dialed in it is configured but not connected.
	st := cfg.HostExecutorStatus(context.Background())
	if st.Hostd == nil || st.Hostd.Reachable || st.Hostd.Remote == nil || st.Hostd.Remote.Connected {
		t.Fatalf("status before hostd dials in = %+v", st.Hostd)
	}

	startDialingHostd(t, ctl.URL, "hostd-token",
		offersAgy+"capabilities:\n  grain-gcp:\n    command: %[1]s\n    args: [-c, \"read l; echo minted:$l\"]\n")
	waitFor(t, func() bool { return remote.Status().Connected })

	st = cfg.HostExecutorStatus(context.Background())
	if st.Hostd == nil || !st.Hostd.Reachable || st.Hostd.Error != "" {
		t.Fatalf("status = %+v, want hostd reachable", st.Hostd)
	}
	if !slices.Contains(st.Hostd.Offers, "cli agy") || !slices.Contains(st.Hostd.Offers, "capability grain-gcp") {
		t.Errorf("offers = %v", st.Hostd.Offers)
	}
	if r := st.Hostd.Remote; r == nil || !r.Connected || r.Host != "cloudtop" || r.Build != "hostd-build" {
		t.Errorf("remote = %+v, want cloudtop connected", r)
	}
	if st.Hostd.Protocol != hostd.ProtocolVersion {
		t.Errorf("protocol = %d", st.Hostd.Protocol)
	}

	cfg.CLIRun = map[string]model.CLIRunSetting{"antigravity": {Location: model.CLIRunHost}}
	r := cfg.ResolveCLIRun(context.Background(), model.AgentFrameworkAntigravity)
	if r.Location != model.CLIRunHost || r.Via != ViaHostd || r.Err != "" {
		t.Fatalf("agy resolved %+v, want host via hostd", r)
	}
	if !strings.Contains(r.Reason, "dials in") {
		t.Errorf("reason = %q, want it to say the hostd dials in", r.Reason)
	}
	if _, err := cfg.cliHostdHost(r); err != nil {
		t.Fatal(err)
	}

	c := cfg.capabilityHostd()
	if c == nil {
		t.Fatal("no capability hostd")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out bytes.Buffer
	exit, err := c.Run(ctx, hostd.Request{Kind: hostd.KindCapability, Name: "grain-gcp"}, strings.NewReader("token\n"), &out, nil)
	if err != nil || exit.Code != 0 || out.String() != "minted:token\n" {
		t.Fatalf("capability server = %+v, %v, %q", exit, err, out.String())
	}
}

// A hostd presenting another token is refused before anything reaches it.
func TestAHostdWithTheWrongTokenIsRefused(t *testing.T) {
	remote := &hostd.Remote{Token: "hostd-token"}
	ctl := httptest.NewServer(remote)
	t.Cleanup(ctl.Close)
	_, err := hostd.Dialer{Server: ctl.URL, Token: "executor-token"}.Dial(context.Background())
	if err == nil || !strings.Contains(err.Error(), fmt.Sprint(http.StatusUnauthorized)) {
		t.Fatalf("dial = %v, want a 401", err)
	}
	if remote.Status().Connected {
		t.Fatal("a refused hostd is connected")
	}
}

// -hostd-socket and a hostd dialing in are two hostds; only one is the
// host executor.
func TestASocketAndADialingInHostdAreRefusedTogether(t *testing.T) {
	err := CheckHostExecutorFlags(Config{HostdSocket: "/run/grain-hostd/hostd.sock", HostdRemote: &hostd.Remote{Token: "x"}})
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("err = %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
