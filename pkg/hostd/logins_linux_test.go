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

package hostd

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/tunnel/mux"
)

func TestLoginsUnderReadsEachCLIsOwnLogin(t *testing.T) {
	t.Parallel()
	write := func(t *testing.T, path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	noEnv := func(string) string { return "" }

	home := t.TempDir()
	if got := loginsUnder(home, noEnv); len(got) != 0 {
		t.Errorf("empty home: logins = %q, want none", got)
	}
	write(t, filepath.Join(home, ".codex", "auth.json"))
	write(t, filepath.Join(home, ".claude", ".credentials.json"))
	if err := os.MkdirAll(filepath.Join(home, ".gemini", "antigravity-cli"), 0o700); err != nil {
		t.Fatal(err)
	}
	want := []string{LoginAntigravity, LoginClaude, LoginCodex}
	if got := loginsUnder(home, noEnv); !reflect.DeepEqual(got, want) {
		t.Errorf("logins = %q, want %q", got, want)
	}

	// $CODEX_HOME and $CLAUDE_CONFIG_DIR move the login, as they move
	// the CLI's own reading of it.
	other := t.TempDir()
	env := func(k string) string {
		switch k {
		case "CODEX_HOME":
			return filepath.Join(other, "codex")
		case "CLAUDE_CONFIG_DIR":
			return filepath.Join(other, "claude")
		}
		return ""
	}
	if got := loginsUnder(home, env); !reflect.DeepEqual(got, []string{LoginAntigravity}) {
		t.Errorf("with CODEX_HOME and CLAUDE_CONFIG_DIR elsewhere: logins = %q, want antigravity alone", got)
	}
	write(t, filepath.Join(other, "codex", "auth.json"))
	if got := loginsUnder(home, env); !reflect.DeepEqual(got, []string{LoginAntigravity, LoginCodex}) {
		t.Errorf("with $CODEX_HOME/auth.json: logins = %q", got)
	}
	// A directory where the file should be is not a login.
	gem := t.TempDir()
	if err := os.MkdirAll(filepath.Join(gem, ".codex", "auth.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := loginsUnder(gem, noEnv); len(got) != 0 {
		t.Errorf("auth.json a directory: logins = %q, want none", got)
	}
}

// A hostd that dials out reports its machine's agent CLI logins in its
// hello, and the controller keeps them in that hostd's status for the
// host pickers in Settings.
func TestAHostdsLoginsReachTheControllersStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex-home"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "no-claude"))
	if err := os.MkdirAll(filepath.Join(home, "codex-home"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "codex-home", "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctl := startController(t, "s3cret")
	startRemoteServer(t, ctl, "s3cret", "")

	deadline := time.Now().Add(10 * time.Second)
	for {
		st := ctl.remote.Status()
		if st.LoginsKnown {
			if !reflect.DeepEqual(st.Logins, []string{LoginCodex}) {
				t.Fatalf("status logins = %q, want codex alone", st.Logins)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the controller never learned the hostd's logins: %+v", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAllowlistLoginsMapAndFeatureLogins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "ignored-codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "ignored-claude"))

	// Built-in codex file exists, so we prove an explicit logins: map
	// overrides the built-in table rather than merging with it.
	if err := os.MkdirAll(filepath.Join(home, "ignored-codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "ignored-codex", "auth.json"), []byte(`{"secret":"built-in"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	customCred := filepath.Join(home, ".acme", "token.json")
	if err := os.MkdirAll(filepath.Dir(customCred), 0o700); err != nil {
		t.Fatal(err)
	}
	const secretValue = "super-secret-credential-payload"
	if err := os.WriteFile(customCred, []byte(`{"token":"`+secretValue+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	ctl := startController(t, "s3cret")
	startRemoteServer(t, ctl, "s3cret", "logins:\n  acme: ~/.acme/token.json\n  missing: ~/.acme/none.json\n")

	deadline := time.Now().Add(10 * time.Second)
	for {
		st := ctl.remote.Status()
		if st.LoginsKnown {
			if !reflect.DeepEqual(st.Logins, []string{"acme"}) {
				t.Fatalf("status logins = %q, want [\"acme\"]", st.Logins)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("controller never learned mapped logins: %+v", st)
		}
		time.Sleep(10 * time.Millisecond)
	}

	h, err := ctl.client().Hello(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !h.Has(FeatureLoginsMap) || !h.Has(FeatureLogins) {
		t.Fatalf("hello features = %v, want logins and logins_map", h.Features)
	}
	if !reflect.DeepEqual(h.Logins, []string{"acme"}) {
		t.Errorf("Hello.Logins = %q, want [\"acme\"]", h.Logins)
	}
	if want := map[string]string{"acme": customCred}; !reflect.DeepEqual(h.LoginTargets, want) {
		t.Errorf("Hello.LoginTargets = %v, want %v", h.LoginTargets, want)
	}
	if h.CodexAuth != "" {
		t.Errorf("Hello.CodexAuth = %q, want empty when codex is omitted from logins:", h.CodexAuth)
	}
	for _, s := range append(append([]string{}, h.Logins...), h.LoginTargets["acme"]) {
		if strings.Contains(s, secretValue) {
			t.Fatalf("Hello leaked login file contents: %q", s)
		}
	}

	// An explicit empty logins: {} lends no logins, and Has(FeatureLogins)
	// marks that empty list authoritative (LoginsKnown == true).
	ctlNone := startController(t, "s3cret")
	startRemoteServer(t, ctlNone, "s3cret", "logins: {}\n")
	deadline = time.Now().Add(10 * time.Second)
	for {
		st := ctlNone.remote.Status()
		if st.LoginsKnown {
			if len(st.Logins) != 0 {
				t.Fatalf("empty logins: status = %q, want none", st.Logins)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("controller did not record authoritative empty logins: %+v", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRemoteLearnLoginsKeepsFeaturesWithoutFeatureLogins(t *testing.T) {
	t.Parallel()

	connectPeer := func(t *testing.T, peer RemotePeer, h Hello) RemoteStatus {
		t.Helper()
		r := &Remote{Token: "tok", Logf: testLogf(t)}
		serverConn, clientConn := net.Pipe()
		ctx, cancel := context.WithCancel(t.Context())
		serveDone := make(chan struct{})
		go func() {
			defer close(serveDone)
			r.Serve(ctx, serverConn, peer)
		}()

		cm := mux.NewMux(clientConn, mux.Config{IsServer: false})
		helloSent := make(chan struct{})
		peerDone := make(chan struct{})
		go func() {
			defer close(peerDone)
			defer cm.Close()
			st, err := cm.AcceptStream()
			if err != nil {
				return
			}
			defer st.Close()
			if _, _, err := readFrame(st); err != nil {
				return
			}
			_ = writeJSON(st, frameHello, h)
			close(helloSent)
			<-ctx.Done()
		}()

		t.Cleanup(func() {
			cancel()
			_ = serverConn.Close()
			_ = clientConn.Close()
			<-peerDone
			<-serveDone
		})

		select {
		case <-helloSent:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for controller to request OpHello")
		}

		deadline := time.Now().Add(5 * time.Second)
		for {
			st := r.Status()
			if len(st.Features) > 0 {
				return st
			}
			if time.Now().After(deadline) {
				t.Fatalf("controller never populated Status().Features: %+v", st)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	t.Run("legacy protocol 4 without FeatureLogins and empty Logins", func(t *testing.T) {
		t.Parallel()
		st := connectPeer(t,
			RemotePeer{Host: "ws-old", Build: "v3.1.0", Protocol: "4"},
			Hello{Protocol: 4, MinCompatible: 1, Build: "v3.1.0"},
		)
		if st.LoginsKnown || len(st.Logins) != 0 {
			t.Errorf("Logins, LoginsKnown = (%v, %v), want (nil, false)", st.Logins, st.LoginsKnown)
		}
		wantFeatures := legacyFeatures(4)
		if !reflect.DeepEqual(st.Features, wantFeatures) {
			t.Errorf("Features = %v, want %v", st.Features, wantFeatures)
		}
		if !st.Hello().Has(FeatureStage) || st.Hello().Has(FeatureEnvOpen) || st.Hello().Has(FeatureLogins) {
			t.Errorf("st.Hello() = %+v, unexpected feature resolution", st.Hello())
		}
	})

	t.Run("explicit Features without FeatureLogins and empty Logins", func(t *testing.T) {
		t.Parallel()
		explicit := []Feature{FeatureStart, FeatureFeatures, FeatureEnvOpen}
		st := connectPeer(t,
			RemotePeer{Host: "ws-explicit", Build: "v3.2.0", Protocol: "4"},
			Hello{Protocol: 4, MinCompatible: 1, Build: "v3.2.0", Features: explicit},
		)
		if st.LoginsKnown || len(st.Logins) != 0 {
			t.Errorf("Logins, LoginsKnown = (%v, %v), want (nil, false)", st.Logins, st.LoginsKnown)
		}
		if !reflect.DeepEqual(st.Features, explicit) {
			t.Errorf("Features = %v, want %v", st.Features, explicit)
		}
		if !st.Hello().Has(FeatureEnvOpen) || st.Hello().Has(FeatureStage) || st.Hello().Has(FeatureLogins) {
			t.Errorf("st.Hello() = %+v, want FeatureEnvOpen preserved without falling back to legacyFeatures(4)", st.Hello())
		}
	})

	t.Run("legacy protocol 4 without FeatureLogins and non-empty Logins", func(t *testing.T) {
		t.Parallel()
		st := connectPeer(t,
			RemotePeer{Host: "ws-codex", Build: "v3.1.5", Protocol: "4"},
			Hello{Protocol: 4, MinCompatible: 1, Build: "v3.1.5", Logins: []string{LoginCodex}},
		)
		if !st.LoginsKnown || !reflect.DeepEqual(st.Logins, []string{LoginCodex}) {
			t.Errorf("Logins, LoginsKnown = (%v, %v), want ([%q], true)", st.Logins, st.LoginsKnown, LoginCodex)
		}
		wantFeatures := legacyFeatures(4)
		if !reflect.DeepEqual(st.Features, wantFeatures) {
			t.Errorf("Features = %v, want %v", st.Features, wantFeatures)
		}
	})
}
