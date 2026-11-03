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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/attest"
	"github.com/ai-on-gke/grain/pkg/grit/actions"
	"github.com/ai-on-gke/grain/pkg/gritapi"
)

func TestMappingsString(t *testing.T) {
	m := mappings{}
	if got := m.String(); got != "label=image" {
		t.Errorf("mappings.String() = %q, want %q", got, "label=image")
	}
}

func TestMappingsSet(t *testing.T) {
	m := mappings{}
	if err := m.Set("ubuntu-latest=grain-sandbox:custom"); err != nil {
		t.Fatalf("mappings.Set valid: unexpected error %v", err)
	}
	if m["ubuntu-latest"] != "grain-sandbox:custom" {
		t.Errorf("m[ubuntu-latest] = %q, want %q", m["ubuntu-latest"], "grain-sandbox:custom")
	}

	for _, invalid := range []string{"", "ubuntu-latest", "=image", "label=", "noequals"} {
		if err := m.Set(invalid); err == nil {
			t.Errorf("mappings.Set(%q) = nil error, want error", invalid)
		}
	}
}

// The flag-only form scripts were written against still runs act, and so
// does `grit act` with the same flags.
func TestTheFlagOnlyFormAndGritActRunTheSameJob(t *testing.T) {
	var got []actions.RunRequest
	actRun = func(_ context.Context, req actions.RunRequest, _, _ io.Writer) error {
		got = append(got, req)
		return nil
	}
	defer func() { actRun = actions.Run }()

	flags := []string{"-workflow", ".github/workflows/ci.yml", "-job", "build", "-runner-image", "ubuntu-latest=img:1"}
	for _, args := range [][]string{flags, append([]string{"act"}, flags...)} {
		if code := run(context.Background(), args, io.Discard, io.Discard); code != 0 {
			t.Fatalf("run(%v) = %d", args, code)
		}
	}
	if len(got) != 2 || got[0].Workflow != ".github/workflows/ci.yml" || got[0].Job != "build" || got[0].Images["ubuntu-latest"] != "img:1" || got[0].Event != "push" {
		t.Fatalf("requests = %+v", got)
	}
	if got[0].Workflow != got[1].Workflow || got[0].Job != got[1].Job || got[0].Images["ubuntu-latest"] != got[1].Images["ubuntu-latest"] {
		t.Errorf("flag-only %+v and act %+v differ", got[0], got[1])
	}
}

func TestAnUnknownCommandIsAUsageError(t *testing.T) {
	var stderr bytes.Buffer
	if code := run(context.Background(), []string{"frobnicate"}, io.Discard, &stderr); code != 2 || !strings.Contains(stderr.String(), "frobnicate") {
		t.Errorf("run(frobnicate) = %d, %q", code, stderr.String())
	}
}

func TestGritVerify(t *testing.T) {
	dir := t.TempDir()
	signer, err := attest.LoadOrCreateFileSigner(filepath.Join(dir, "attest.key"))
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := attest.MarshalPublicKey(signer.Public())
	keyFile := filepath.Join(dir, "attest.pub")
	os.WriteFile(keyFile, pub, 0o644)

	tree := attest.Tree{SHA256: strings.Repeat("a", 64), GitTree: strings.Repeat("b", 40)}
	env, err := attest.Sign(context.Background(), signer, attest.NewStatement(tree, "", attest.RunPredicate{
		Grit:     attest.GritInfo{Version: "grit/v0.1.0"},
		Client:   attest.ClientInfo{ID: "grain-test"},
		Request:  attest.RequestInfo{RequestID: "grain:1", Run: "r-1", Repo: "bwsalmon/grain"},
		Jobs:     []attest.Job{{ID: "tests.yml/go", Result: attest.Result{Conclusion: "success"}}},
		IssuedAt: time.Now(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(env)
	envFile := filepath.Join(dir, "run.dsse.json")
	os.WriteFile(envFile, b, 0o644)

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"verify", "-key", keyFile, "-tree", tree.SHA256, "-git-tree", tree.GitTree, envFile}, &stdout, &stderr); code != 0 {
		t.Fatalf("verify = %d: %s", code, stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, "verified") || !strings.Contains(out, "tests.yml/go success") {
		t.Errorf("verify printed %q", out)
	}

	stderr.Reset()
	if code := run(context.Background(), []string{"verify", "-key", keyFile, "-tree", strings.Repeat("c", 64), "-git-tree", tree.GitTree, envFile}, io.Discard, &stderr); code != 1 {
		t.Errorf("verify against another tree = %d, want 1 (%s)", code, stderr.String())
	}

	other, _ := attest.LoadOrCreateFileSigner(filepath.Join(dir, "other.key"))
	otherPub, _ := attest.MarshalPublicKey(other.Public())
	os.WriteFile(keyFile, otherPub, 0o644)
	if code := run(context.Background(), []string{"verify", "-key", keyFile, envFile}, io.Discard, io.Discard); code != 1 {
		t.Errorf("verify with another key = %d, want 1", code)
	}
}

func TestGritClientAndServe(t *testing.T) {
	dataDir := t.TempDir()

	// 1. grit client add
	var addOut, stderr bytes.Buffer
	if code := run(context.Background(), []string{
		"client", "add",
		"-data-dir", dataDir,
		"-id", "grain-main",
		"-scope", "submit:bwsalmon/*,read:*",
	}, &addOut, &stderr); code != 0 {
		t.Fatalf("grit client add = %d: %s", code, stderr.String())
	}
	tok := strings.TrimSpace(addOut.String())
	if !strings.HasPrefix(tok, "grain-main.") {
		t.Fatalf("unexpected token %q", tok)
	}

	// 2. grit client scope
	stderr.Reset()
	if code := run(context.Background(), []string{
		"client", "scope",
		"-data-dir", dataDir,
		"-id", "grain-main",
		"-scope", "submit:*,read:*,cancel:*",
	}, io.Discard, &stderr); code != 0 {
		t.Fatalf("grit client scope = %d: %s", code, stderr.String())
	}

	// 3. grit client list
	var listOut bytes.Buffer
	if code := run(context.Background(), []string{"client", "list", "-data-dir", dataDir}, &listOut, &stderr); code != 0 {
		t.Fatalf("grit client list = %d: %s", code, stderr.String())
	}
	if !strings.Contains(listOut.String(), "grain-main\tactive\t") {
		t.Fatalf("unexpected client list output: %q", listOut.String())
	}

	// 4. grit client revoke
	if code := run(context.Background(), []string{"client", "revoke", "-data-dir", dataDir, "grain-main"}, io.Discard, &stderr); code != 0 {
		t.Fatalf("grit client revoke = %d: %s", code, stderr.String())
	}

	// 5. grit serve -executor=local starts and shuts down cleanly on context cancel.
	ctx, cancel := context.WithCancel(context.Background())
	var serveOut bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"serve", "-listen", "127.0.0.1:0", "-data-dir", dataDir, "-executor", "local"}, &serveOut, &stderr)
	}()
	time.Sleep(150 * time.Millisecond)
	cancel()
	if code := <-done; code != 0 {
		t.Fatalf("grit serve = %d: %s", code, stderr.String())
	}
	if !strings.Contains(serveOut.String(), "grit serve listening on") {
		t.Fatalf("grit serve output = %q", serveOut.String())
	}

	// 6. grit version
	var verOut bytes.Buffer
	if code := run(context.Background(), []string{"version"}, &verOut, io.Discard); code != 0 || strings.TrimSpace(verOut.String()) == "" {
		t.Fatalf("grit version = %d, %q", code, verOut.String())
	}

	// 7. grit serve -grain-url reverse-tunnel mode connects to SessionHub.
	var tok2Out bytes.Buffer
	if code := run(context.Background(), []string{
		"client", "add",
		"-data-dir", dataDir,
		"-id", "grain-tunnel",
		"-scope", "submit:*,read:*,cancel:*",
	}, &tok2Out, &stderr); code != 0 {
		t.Fatalf("grit client add grain-tunnel = %d: %s", code, stderr.String())
	}
	tunnelTok := strings.TrimSpace(tok2Out.String())

	hub := gritapi.NewSessionHub(tunnelTok)
	defer func() { _ = hub.Close() }()
	muxHandler := http.NewServeMux()
	muxHandler.Handle("/v1/session", hub)
	grainSrv := httptest.NewServer(muxHandler)
	defer grainSrv.Close()

	tCtx, tCancel := context.WithCancel(context.Background())
	defer tCancel()
	tDone := make(chan int, 1)
	go func() {
		tDone <- run(tCtx, []string{
			"serve",
			"-listen", "127.0.0.1:0",
			"-data-dir", dataDir,
			"-executor", "local",
			"-grain-url", grainSrv.URL,
			"-client-token", tunnelTok,
		}, io.Discard, &stderr)
	}()

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()
	if err := hub.WaitConnected(waitCtx); err != nil {
		t.Fatalf("hub.WaitConnected: %v", err)
	}
	hc := hub.NewClient(gritapi.ClientConfig{ClientToken: tunnelTok})
	info, err := hc.Info(waitCtx)
	if err != nil || info.Epoch == "" {
		t.Fatalf("Info over reverse tunnel = (%+v, %v)", info, err)
	}
	tCancel()
	if code := <-tDone; code != 0 {
		t.Fatalf("grit serve -grain-url = %d: %s", code, stderr.String())
	}
}

// TestDialGrainLoopLogsAndBacksOff is the regression for the 2026-10-29
// walkthrough's F5: a refused reverse session was redialed every 100ms
// with nothing logged.
func TestDialGrainLoopLogsAndBacksOff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var stamps []time.Time
	dial := func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		stamps = append(stamps, time.Now())
		if len(stamps) == 5 {
			cancel()
		}
		return errors.New("grit api (404 http_error)")
	}
	var log bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		dialGrainLoop(ctx, "http://grain.example", dial, &log, 5*time.Millisecond, 40*time.Millisecond)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dialGrainLoop did not return after its context was cancelled")
	}
	if got := strings.Count(log.String(), "reverse session to http://grain.example: grit api (404 http_error)"); got != 1 {
		t.Fatalf("logged the same failure %d times, want once:\n%s", got, log.String())
	}
	// 5ms, 10ms, 20ms, 40ms between the five dials: the gaps grow.
	if gap := stamps[4].Sub(stamps[3]); gap < 30*time.Millisecond {
		t.Fatalf("last redial gap %s, want the backoff to have grown towards 40ms", gap)
	}
}
