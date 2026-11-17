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

package agentd

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agentd/release"
	"github.com/ai-on-gke/grain/pkg/hostd"
)

// fakeBuild is a stand-in grain-agentd: -build prints v, -check passes.
func fakeBuild(v string) []byte {
	return []byte("#!/bin/sh\ncase \"$1\" in -build) echo " + v + " ;; -check) exit 0 ;; *) exit 3 ;; esac\n")
}

type updateFixture struct {
	home    string
	p       paths
	l       updateLayout
	priv    ed25519.PrivateKey
	server  *httptest.Server
	release struct {
		manifest []byte
		binary   []byte
	}
}

// newUpdateFixture is a home with grain-agentd v1.0.0 installed the way
// install leaves it (a plain binary in ~/.local/bin, a unit, a pinned
// release key), and a controller serving release v, signed by that key.
func newUpdateFixture(t *testing.T, v string, allowlist string) *updateFixture {
	t.Helper()
	h := newFakeHome(t)
	t.Setenv("PATH", "/usr/bin:/bin")
	f := &updateFixture{home: h.home, p: pathsFor(h.home), l: layoutFor(h.home)}
	pub, priv, err := release.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	f.priv = priv
	pem, _ := release.FormatPublicKeyPEM(pub)
	writeFile(t, f.p.ReleasePub, string(pem), 0o644)
	writeFile(t, f.p.Allowlist, allowlist, 0o644)
	writeFile(t, f.p.Bin, string(fakeBuild("v1.0.0")), 0o755)
	unit, err := renderUnit(unitConfig{Bin: f.p.Bin, Server: "https://grain.example.com", Token: f.p.Token,
		Allowlist: f.p.Allowlist, StateDir: f.p.StateDir, Guard: f.l.Guard})
	if err != nil {
		t.Fatal(err)
	}
	// A unit an earlier install wrote, before the guard.
	unit = strings.Replace(unit, "ExecStartPre=-/bin/sh "+f.l.Guard+"\n", "", 1)
	writeFile(t, filepath.Join(f.p.UnitDir, UnitName), unit, 0o644)
	f.sign(t, v, fakeBuild(v), priv)

	mux := http.NewServeMux()
	base := "/api/hostd/agentd/" + updatePlatform
	mux.HandleFunc("GET "+base+"/manifest.json", func(w http.ResponseWriter, r *http.Request) { w.Write(f.release.manifest) })
	mux.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) { w.Write(f.release.binary) })
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *updateFixture) sign(t *testing.T, v string, bin []byte, priv ed25519.PrivateKey) {
	t.Helper()
	m, err := release.NewManifestForBinary(v, updatePlatform, bin, nil)
	if err != nil {
		t.Fatal(err)
	}
	if priv != nil {
		if m, err = release.SignManifest(m, priv, nil); err != nil {
			t.Fatal(err)
		}
	}
	f.release.manifest, _ = json.Marshal(m)
	f.release.binary = bin
}

func (f *updateFixture) update(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := update(append([]string{"-server", f.server.URL, "-allowlist", f.p.Allowlist,
		"-socket", filepath.Join(f.home, "no.sock")}, args...), &out, &out)
	return code, out.String()
}

const agentsAllowlist = "clis: {}\n"

func TestUpdateStagesVerifiesAndSwitchesCurrent(t *testing.T) {
	f := newUpdateFixture(t, "v2.0.0", agentsAllowlist)
	code, out := f.update(t)
	if code != 0 {
		t.Fatalf("update = %d:\n%s", code, out)
	}
	// ~/.local/bin/grain-agentd resolves through current to the new build.
	if target, err := os.Readlink(f.p.Bin); err != nil || target != filepath.Join(f.l.Current, Program) {
		t.Fatalf("%s -> %q (%v), want a link through current", f.p.Bin, target, err)
	}
	if v := f.l.currentVersion(); v != "v2.0.0" {
		t.Fatalf("current names %q, want v2.0.0", v)
	}
	got, err := exec.Command(f.p.Bin, "-build").Output()
	if err != nil || strings.TrimSpace(string(got)) != "v2.0.0" {
		t.Fatalf("%s -build = %q, %v", f.p.Bin, got, err)
	}
	// The installed build is kept for rollback, and named in pending.
	if _, err := os.Stat(f.l.binary("v1.0.0")); err != nil {
		t.Fatalf("previous build not kept: %v", err)
	}
	if b, _ := os.ReadFile(f.l.Pending); string(b) != "v1.0.0\nv2.0.0\n" {
		t.Fatalf("pending = %q", b)
	}
	unit, _ := os.ReadFile(filepath.Join(f.p.UnitDir, UnitName))
	if !strings.Contains(string(unit), "ExecStartPre=-/bin/sh "+f.l.Guard+"\nExecStart=") {
		t.Fatalf("unit has no guard before ExecStart:\n%s", unit)
	}
	if st, err := os.Stat(f.l.Guard); err != nil || st.Mode().Perm() != 0o755 {
		t.Fatalf("guard script: %v", err)
	}
	if !strings.Contains(out, "no host agent answers") {
		t.Fatalf("update with nothing running did not say so:\n%s", out)
	}

}

func TestUpdateRefusesWhatThePinnedKeyDidNotSign(t *testing.T) {
	for name, tamper := range map[string]func(t *testing.T, f *updateFixture){
		"another key": func(t *testing.T, f *updateFixture) {
			_, other, _ := release.GenerateKeyPair()
			f.sign(t, "v2.0.0", fakeBuild("v2.0.0"), other)
		},
		"unsigned": func(t *testing.T, f *updateFixture) {
			f.sign(t, "v2.0.0", fakeBuild("v2.0.0"), nil)
		},
		"swapped binary": func(t *testing.T, f *updateFixture) {
			b := fakeBuild("v2.0.1")
			f.release.binary = b
		},
		"other platform": func(t *testing.T, f *updateFixture) {
			other := "linux/arm64"
			if updatePlatform == other {
				other = "linux/amd64"
			}
			m, _ := release.NewManifestForBinary("v2.0.0", other, fakeBuild("v2.0.0"), nil)
			m, _ = release.SignManifest(m, f.priv, nil)
			f.release.manifest, _ = json.Marshal(m)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newUpdateFixture(t, "v2.0.0", agentsAllowlist)
			tamper(t, f)
			code, out := f.update(t)
			if code == 0 {
				t.Fatalf("update took it:\n%s", out)
			}
			if _, err := os.Lstat(f.l.Current); err == nil {
				t.Fatalf("current moved after a refused release:\n%s", out)
			}
			if st, _ := os.Lstat(f.p.Bin); st.Mode()&os.ModeSymlink != 0 {
				t.Fatalf("%s replaced after a refused release", f.p.Bin)
			}
		})
	}
}

func TestUpdateRefusesABuildThatFailsItsSmokeTest(t *testing.T) {
	f := newUpdateFixture(t, "v2.0.0", agentsAllowlist)
	f.sign(t, "v2.0.0", fakeBuild("v2.0.1"), f.priv) // -build disagrees
	if code, out := f.update(t); code == 0 || !strings.Contains(out, "-build says") {
		t.Fatalf("update = %d:\n%s", code, out)
	}
	if _, err := os.Lstat(f.l.Lib); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed -build smoke test left %s behind (err=%v)", f.l.Lib, err)
	}
	bad := []byte("#!/bin/sh\ncase \"$1\" in -build) echo v2.0.0 ;; *) exit 2 ;; esac\n")
	f.sign(t, "v2.0.0", bad, f.priv)
	if code, out := f.update(t); code == 0 || !strings.Contains(out, "-check") {
		t.Fatalf("update = %d:\n%s", code, out)
	}
	if _, err := os.Lstat(f.l.Current); err == nil {
		t.Fatal("current moved to a build that failed its smoke test")
	}
	if _, err := os.Lstat(f.l.Lib); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed -check smoke test left %s behind (err=%v)", f.l.Lib, err)
	}
	// When versions/ already holds a prior version, a failed smoke test
	// leaves neither a versions/<v> directory nor a temp file beside it.
	writeFile(t, f.l.binary("v1.0.0"), string(fakeBuild("v1.0.0")), 0o755)
	if code, out := f.update(t); code == 0 || !strings.Contains(out, "-check") {
		t.Fatalf("update with existing versions/ = %d:\n%s", code, out)
	}
	entries, err := os.ReadDir(f.l.Versions)
	if err != nil {
		t.Fatalf("reading versions/: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "v1.0.0" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("versions/ after failed smoke test = %v, want [v1.0.0]", names)
	}
}

func TestUpdateHonoursTheAllowlistsPolicy(t *testing.T) {
	f := newUpdateFixture(t, "v2.0.0", "clis: {}\nupdate:\n  mode: off\n")
	if code, out := f.update(t); code == 0 || !strings.Contains(out, "mode: off") {
		t.Fatalf("mode off: update = %d:\n%s", code, out)
	}
	f = newUpdateFixture(t, "v2.0.0", "clis: {}\nupdate:\n  pin: v1.*\n")
	if code, out := f.update(t); code == 0 || !strings.Contains(out, "outside") {
		t.Fatalf("pinned: update = %d:\n%s", code, out)
	}
	f = newUpdateFixture(t, "v2.0.0", agentsAllowlist)
	if code, out := f.update(t, "-version", "v2.1.0"); code == 0 || !strings.Contains(out, "not the v2.1.0") {
		t.Fatalf("-version mismatch: update = %d:\n%s", code, out)
	}
}

func TestCheckUpdateVersion(t *testing.T) {
	for _, c := range []struct {
		running, target, want, pin string
		ok                         bool
	}{
		{"v3.1.0", "v3.2.0", "", "", true},
		{"v3.1.0", "v3.2.0", "", "v3.*", true},
		{"v3.1.0", "v4.0.0", "", "v3.*", false},
		{"v3.2.0", "v3.1.0", "", "", false},       // downgrade
		{"v3.2.0", "v3.1.0", "v3.1.0", "", false}, // -version alone does not downgrade
		{"v3.2.0", "v3.1.0", "", "v3.1.0", true},  // the pin asks for it
		{"v3.2.0", "v3.1.0", "", "v3.1.*", false}, // a glob does not
		{"v3.2.0", "v3.2.0", "", "", false},       // up to date
		{"v3.2.0", "head", "", "", false},         // unordered: only by name
		{"v3.2.0", "head", "head", "", true},
		{"dev", "v3.2.0", "", "", true},
	} {
		err := checkUpdateVersion(c.running, c.target, c.want, hostd.UpdatePolicy{Pin: c.pin})
		if (err == nil) != c.ok {
			t.Errorf("checkUpdateVersion(%q -> %q, -version %q, pin %q) = %v, want ok %v", c.running, c.target, c.want, c.pin, err, c.ok)
		}
	}
}

// layoutAfterUpdate is a layout with v1 previous and v2 current, pending.
func layoutAfterUpdate(t *testing.T) updateLayout {
	t.Helper()
	home := t.TempDir()
	l := layoutFor(home)
	for _, v := range []string{"v1.0.0", "v2.0.0"} {
		writeFile(t, l.binary(v), string(fakeBuild(v)), 0o755)
	}
	if err := switchCurrent(l, "v2.0.0"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, l.Pending, "v1.0.0\nv2.0.0\n", 0o644)
	if err := writeGuard(l); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestGuardRollsBackAnUnconfirmedUpdate(t *testing.T) {
	for _, how := range []string{"go", "script"} {
		t.Run(how, func(t *testing.T) {
			l := layoutAfterUpdate(t)
			guard := func() {
				t.Helper()
				if how == "go" {
					if _, err := guardRollback(l, time.Now()); err != nil {
						t.Fatal(err)
					}
					return
				}
				c := exec.Command("/bin/sh", l.Guard)
				c.Env = []string{"PATH=/usr/bin:/bin"}
				if out, err := c.CombinedOutput(); err != nil {
					t.Fatalf("guard: %v\n%s", err, out)
				}
			}
			// The first start begins the window; a start inside it keeps
			// the new build.
			guard()
			if _, err := os.Stat(l.Started); err != nil {
				t.Fatalf("first start did not begin the window: %v", err)
			}
			guard()
			if v := l.currentVersion(); v != "v2.0.0" {
				t.Fatalf("rolled back inside the window: current %s", v)
			}
			// Past it, still unconfirmed: back to v1.
			old := time.Now().Add(-UpdateConfirmWindow - time.Minute)
			if err := os.Chtimes(l.Started, old, old); err != nil {
				t.Fatal(err)
			}
			guard()
			if v := l.currentVersion(); v != "v1.0.0" {
				t.Fatalf("current %s after the window, want v1.0.0", v)
			}
			if _, err := os.Stat(l.Pending); err == nil {
				t.Fatal("pending kept after a rollback")
			}
			if b, _ := os.ReadFile(l.Failed); string(b) != "v1.0.0\nv2.0.0\n" {
				t.Fatalf("update-failed = %q", b)
			}
			if fi, err := os.Lstat(l.Current); err != nil || fi.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("current is no longer a link: %v", err)
			}
			// Nothing pending: nothing to do.
			guard()
			if v := l.currentVersion(); v != "v1.0.0" {
				t.Fatalf("current %s", v)
			}
		})
	}
}

func TestWatchdogConfirmsOnTheControllersHello(t *testing.T) {
	l := layoutAfterUpdate(t)
	home := filepath.Dir(filepath.Dir(filepath.Dir(l.Lib)))
	onHello, stop := updateWatchdog(home, "v2.0.0", t.Logf, func() { t.Error("cut over after a confirmed update") })
	defer stop()
	if onHello == nil {
		t.Fatal("no watchdog for the pending build")
	}
	onHello()
	waitForCond(t, func() bool { _, err := os.Stat(l.Pending); return os.IsNotExist(err) })
	if v := l.currentVersion(); v != "v2.0.0" {
		t.Fatalf("current %s", v)
	}
	// Another build (the previous one, say) does not watch.
	writeFile(t, l.Pending, "v1.0.0\nv2.0.0\n", 0o644)
	if h, stop := updateWatchdog(home, "v1.0.0", t.Logf, nil); h != nil {
		stop()
		t.Fatal("watchdog armed for a build the update does not name")
	}
}

func TestWatchdogRollsBackWithoutAHello(t *testing.T) {
	old := UpdateConfirmWindow
	UpdateConfirmWindow = 50 * time.Millisecond
	t.Cleanup(func() { UpdateConfirmWindow = old })
	l := layoutAfterUpdate(t)
	home := filepath.Dir(filepath.Dir(filepath.Dir(l.Lib)))
	cut := make(chan struct{})
	_, stop := updateWatchdog(home, "v2.0.0", t.Logf, func() { close(cut) })
	defer stop()
	select {
	case <-cut:
	case <-time.After(10 * time.Second):
		t.Fatal("no cutover after the window")
	}
	if v := l.currentVersion(); v != "v1.0.0" {
		t.Fatalf("current %s, want v1.0.0", v)
	}
}

func waitForCond(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out")
}

func TestWithGuardAddsTheGuardOnce(t *testing.T) {
	unit := "[Service]\nExecStart=/x\n"
	got, changed := withGuard(unit, "/g.sh")
	if !changed || got != "[Service]\nExecStartPre=-/bin/sh /g.sh\nExecStart=/x\n" {
		t.Fatalf("withGuard = %q, %v", got, changed)
	}
	if _, changed := withGuard(got, "/g.sh"); changed {
		t.Fatal("added twice")
	}
	if s, i := unitDefaults(writeTemp(t, "ExecStart=/b -server https://d.example -token-file /t -iap-audience aud\n")); s != "https://d.example" || i != "aud" {
		t.Fatalf("unitDefaults = %q, %q", s, i)
	}
}

func writeTemp(t *testing.T, content string) string {
	p := filepath.Join(t.TempDir(), "u")
	writeFile(t, p, content, 0o644)
	return p
}
