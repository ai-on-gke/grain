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

package orchestrator

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/granule"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/sandboxaccess"
	granuleagent "github.com/bwsalmon/granule/pkg/agent"
	"github.com/bwsalmon/granule/pkg/guestexec"
	granulesandbox "github.com/bwsalmon/granule/pkg/sandbox"
)

type fakeMinter struct {
	mu         sync.Mutex
	minted     []string
	mintedTTLs map[string]time.Duration
	registered map[string]string
	revoked    []string
}

func (f *fakeMinter) Mint(sandbox string, ttl time.Duration) (sandboxaccess.Credentials, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.minted = append(f.minted, sandbox)
	if f.mintedTTLs == nil {
		f.mintedTTLs = map[string]time.Duration{}
	}
	f.mintedTTLs[sandbox] = ttl
	return sandboxaccess.Credentials{
		Sandbox: sandbox, Token: "tok-" + sandbox,
		CertPEM: []byte("CERT"), KeyPEM: []byte("KEY"), CAPEM: []byte("CA"),
		NotAfter: time.Now().Add(ttl),
	}, nil
}

func (f *fakeMinter) Register(sandbox, tokenHash string, notAfter time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.registered == nil {
		f.registered = map[string]string{}
	}
	f.registered[sandbox] = tokenHash
	return nil
}

func (f *fakeMinter) Revoke(sandbox string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, sandbox)
}

// writeRecordingKubectl is a fake kubectl that answers a granule pod's
// lifecycle, answers the uid read with "uid-1", and saves every stdin it
// is handed (the pod and Secret manifests) under dir as <n>.json, with
// the argv beside it as <n>.args.
func writeRecordingKubectl(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "kubectl")
	script := `#!/bin/sh
n=$(ls "` + dir + `" | wc -l)
printf '%s\n' "$*" > "` + dir + `/$n.args"
case "$*" in
	*" -f -"*) cat > "` + dir + `/$n.json"; exit 0 ;;
	*"jsonpath={.metadata.uid}"*) printf 'uid-1'; exit 0 ;;
	*"get pods"*) printf 'grain-granule-901-1\ngrain-granule-902-1\n'; exit 0 ;;
	*" delete "*) exit 0 ;;
esac
printf 'Running|True|docker-pullable://example/granule@sha256:abc|\n'
`
	src := bin + ".src"
	if err := os.WriteFile(src, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("install", "-m", "0755", src, bin).CombinedOutput(); err != nil {
		t.Fatalf("installing fake kubectl: %v: %s", err, out)
	}
	return bin
}

func TestK8sGranuleAcquireMintsAccessCredentialsIntoOwnedSecret(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	minter := &fakeMinter{}
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{
		Namespace:         "grain",
		Kubectl:           writeRecordingKubectl(t, dir),
		Image:             "example/granule:latest",
		ReadyPollInterval: 5 * time.Millisecond,
		ForwardRunner: func(ctx context.Context, cfg granule.KubectlForwardConfig) error {
			if cfg.OnReady != nil {
				cfg.OnReady()
			}
			<-ctx.Done()
			return ctx.Err()
		},
		Access: &K8sGranuleAccess{
			Acceptor:      minter,
			Dial:          "grain-access.grain.svc:4244",
			ControllerIPs: []string{"10.0.0.7", "10.4.0.12"},
		},
	})
	sb, err := k.Acquire(context.Background(), "901-1", Shape{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if !k.Live("901-1") || k.Live("902-1") {
		t.Fatal("Live does not reflect the acquired sandbox")
	}

	var pod, secret map[string]any
	var secretArgs string
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var obj map[string]any
		if err := json.Unmarshal(data, &obj); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		switch obj["kind"] {
		case "Pod":
			pod = obj
		case "Secret":
			secret = obj
			args, _ := os.ReadFile(strings.TrimSuffix(f, ".json") + ".args")
			secretArgs = string(args)
		}
	}
	if pod == nil || secret == nil {
		t.Fatalf("manifests seen: %v; want a Pod and a Secret", files)
	}
	if !strings.Contains(secretArgs, " create -f -") {
		t.Fatalf("Secret was not created with `create` (the Role grants no apply): %q", secretArgs)
	}

	// The Secret: named for the pod, owned by it, holding the credentials.
	meta := secret["metadata"].(map[string]any)
	if meta["name"] != "grain-access-grain-granule-901-1" {
		t.Fatalf("Secret name = %v", meta["name"])
	}
	owner := meta["ownerReferences"].([]any)[0].(map[string]any)
	if owner["kind"] != "Pod" || owner["name"] != "grain-granule-901-1" || owner["uid"] != "uid-1" {
		t.Fatalf("ownerReference = %v", owner)
	}
	data := secret["data"].(map[string]any)
	if tok, _ := base64.StdEncoding.DecodeString(data["token"].(string)); string(tok) != "tok-901-1" {
		t.Fatalf("Secret token = %q", tok)
	}

	// The pod: the wrapper's dial-out env, and the Secret mounted
	// read-only into its one container -- never an env var holding the
	// token itself.
	spec := pod["spec"].(map[string]any)
	containers := spec["containers"].([]any)
	if len(containers) != 1 || spec["initContainers"] != nil {
		t.Fatalf("pod has %d containers and init containers %v; want the wrapper alone", len(containers), spec["initContainers"])
	}
	c := containers[0].(map[string]any)
	env := map[string]string{}
	for _, e := range c["env"].([]any) {
		e := e.(map[string]any)
		env[e["name"].(string)] = e["value"].(string)
	}
	want := map[string]string{
		"GRANULE_ACCESS_DIAL":           "grain-access.grain.svc:4244",
		"GRANULE_ACCESS_SANDBOX_ID":     "901-1",
		"GRANULE_ACCESS_TOKEN_FILE":     "/var/run/granule/access/token",
		"GRANULE_ACCESS_CERT_FILE":      "/var/run/granule/access/tls.crt",
		"GRANULE_ACCESS_KEY_FILE":       "/var/run/granule/access/tls.key",
		"GRANULE_ACCESS_CA_FILE":        "/var/run/granule/access/ca.crt",
		"GRANULE_ACCESS_CONTROLLER_IPS": "10.0.0.7,10.4.0.12",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env %s = %q, want %q", k, env[k], v)
		}
	}
	if _, ok := env["GRANULE_ACCESS_TOKEN"]; ok {
		t.Error("the token is in the pod's env")
	}
	var mounted bool
	for _, m := range c["volumeMounts"].([]any) {
		m := m.(map[string]any)
		if m["mountPath"] == sandboxaccess.MountDir {
			mounted = m["readOnly"] == true
		}
	}
	var volume bool
	for _, v := range spec["volumes"].([]any) {
		if s, ok := v.(map[string]any)["secret"].(map[string]any); ok && s["secretName"] == "grain-access-grain-granule-901-1" {
			volume = true
		}
	}
	if !mounted || !volume {
		t.Fatalf("Secret volume %v, read-only mount at %s %v", volume, sandboxaccess.MountDir, mounted)
	}

	if err := sb.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	minter.mu.Lock()
	defer minter.mu.Unlock()
	if len(minter.revoked) != 1 || minter.revoked[0] != "901-1" {
		t.Fatalf("revoked = %v, want [901-1] on Release", minter.revoked)
	}
	if k.Live("901-1") {
		t.Fatal("a released sandbox is still live")
	}
}

func openAccessTestStore(t *testing.T) *model.Store {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}

func extractSecretMountDir(t *testing.T, kubectlDir string) (mountDir, token string) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(kubectlDir, "*.json"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var obj map[string]any
		if err := json.Unmarshal(data, &obj); err != nil {
			continue
		}
		if obj["kind"] != "Secret" {
			continue
		}
		secData, _ := obj["data"].(map[string]any)
		mountDir = t.TempDir()
		for name, raw := range secData {
			decoded, err := base64.StdEncoding.DecodeString(raw.(string))
			if err != nil {
				t.Fatalf("decoding Secret[%s]: %v", name, err)
			}
			if err := os.WriteFile(filepath.Join(mountDir, name), decoded, 0o600); err != nil {
				t.Fatal(err)
			}
			if name == sandboxaccess.KeyToken {
				token = string(decoded)
			}
		}
		return mountDir, token
	}
	t.Fatalf("no Secret manifest found in %s", kubectlDir)
	return "", ""
}

func startInProcessGranuleWrapper(t *testing.T, ctx context.Context, addr, sandboxID, token, mountDir string) <-chan error {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	srv := granulesandbox.NewServer(granulesandbox.ServerConfig{
		SandboxID: sandboxID,
		Token:     token,
		Guest:     guestexec.Config{User: u.Username},
		DialGuest: func(dialCtx context.Context) (net.Conn, error) {
			c1, c2 := net.Pipe()
			go func() {
				defer c2.Close()
				_ = granuleagent.Serve(ctx, c2)
			}()
			return c1, nil
		},
	})
	opts := granulesandbox.DialOutOptions{
		CAFile:     filepath.Join(mountDir, sandboxaccess.KeyCA),
		CertFile:   filepath.Join(mountDir, sandboxaccess.KeyCert),
		KeyFile:    filepath.Join(mountDir, sandboxaccess.KeyKey),
		MaxBackoff: 50 * time.Millisecond,
	}
	done := make(chan error, 1)
	go func() { done <- srv.DialOut(ctx, addr, opts) }()
	return done
}

func TestK8sGranuleRestartReadmitsFromStoredHashAndRefusesReleased(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := openAccessTestStore(t)
	ca, err := sandboxaccess.NewCA("dep-restart", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	kubectlDir1 := t.TempDir()
	var live1 func(string) bool
	acc1, err := sandboxaccess.Listen(sandboxaccess.Config{
		Deployment:  "dep-restart",
		CA:          ca,
		ServerNames: []string{"127.0.0.1", "grain-access.grain.svc"},
		Live:        func(id string) bool { return live1 != nil && live1(id) },
	}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx1, cancel1 := context.WithCancel(ctx)
	go func() { _ = acc1.Serve(ctx1) }()
	t.Cleanup(func() {
		cancel1()
		_ = acc1.Close()
	})

	noopForward := func(ctx context.Context, cfg granule.KubectlForwardConfig) error {
		if cfg.OnReady != nil {
			cfg.OnReady()
		}
		<-ctx.Done()
		return ctx.Err()
	}

	k1 := NewK8sGranuleSandboxes(K8sGranuleConfig{
		Namespace:         "grain",
		Kubectl:           writeRecordingKubectl(t, kubectlDir1),
		Image:             "example/granule:latest",
		ReadyPollInterval: 5 * time.Millisecond,
		ForwardRunner:     noopForward,
		Access: &K8sGranuleAccess{
			Acceptor: acc1,
			Store:    store,
			Dial:     acc1.Addr().String(),
			Life:     7 * 24 * time.Hour,
		},
	})
	live1 = k1.Live

	const sandboxID = "901-1"
	wantLife := 45 * time.Minute
	beforeAcquire := time.Now()
	if _, err := k1.Acquire(ctx, sandboxID, Shape{Life: wantLife}); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	mountDir, rawToken := extractSecretMountDir(t, kubectlDir1)
	if rawToken == "" {
		t.Fatal("Secret held an empty token")
	}

	// Verify only the SHA-256 hash and expiry are persisted on the row, never the raw token.
	stored, ok, err := store.GetSandboxAccess(ctx, sandboxID)
	if err != nil || !ok {
		t.Fatalf("GetSandboxAccess(%q) = %v, %v", sandboxID, ok, err)
	}
	if wantHash := sandboxaccess.TokenHash(rawToken); stored.TokenHash != wantHash {
		t.Fatalf("stored TokenHash = %q, want %q", stored.TokenHash, wantHash)
	}
	if stored.TokenHash == rawToken || strings.Contains(stored.TokenHash, rawToken) {
		t.Fatal("raw token was stored in sandbox_access")
	}
	ttlRemaining := stored.NotAfter.Sub(beforeAcquire)
	if ttlRemaining < wantLife-5*time.Second || ttlRemaining > wantLife+30*time.Second {
		t.Fatalf("stored NotAfter is %s after acquire, want ~%s (not the 7d fallback)", ttlRemaining, wantLife)
	}

	// Connect the pod's fake wrapper to the first acceptor before restart.
	w1Ctx, w1Cancel := context.WithCancel(ctx)
	startInProcessGranuleWrapper(t, w1Ctx, acc1.Addr().String(), sandboxID, rawToken, mountDir)
	waitCtx1, waitCancel1 := context.WithTimeout(ctx, 10*time.Second)
	cli1, err := acc1.WaitClient(waitCtx1, sandboxID)
	waitCancel1()
	if err != nil {
		t.Fatalf("WaitClient before restart: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if code, err := cli1.Exec(ctx, granulesandbox.ExecRequest{Argv: []string{"echo", "before-restart"}}, nil, &stdout, &stderr); err != nil || code != 0 {
		t.Fatalf("Exec before restart = %d, %v (stderr %q)", code, err, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "before-restart" {
		t.Fatalf("stdout before restart = %q, want before-restart", got)
	}

	// Simulate a grain restart: stop the old acceptor and wrapper session, then
	// bring up a fresh acceptor and K8sGranuleSandboxes over the same store and CA.
	w1Cancel()
	cancel1()
	_ = acc1.Close()

	kubectlDir2 := t.TempDir()
	var live2 func(string) bool
	acc2, err := sandboxaccess.Listen(sandboxaccess.Config{
		Deployment:  "dep-restart",
		CA:          ca,
		ServerNames: []string{"127.0.0.1", "grain-access.grain.svc"},
		Live:        func(id string) bool { return live2 != nil && live2(id) },
	}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = acc2.Close() })

	k2 := NewK8sGranuleSandboxes(K8sGranuleConfig{
		Namespace:         "grain",
		Kubectl:           writeRecordingKubectl(t, kubectlDir2),
		Image:             "example/granule:latest",
		ReadyPollInterval: 5 * time.Millisecond,
		ForwardRunner:     noopForward,
		Access: &K8sGranuleAccess{
			Acceptor: acc2,
			Store:    store,
			Dial:     acc2.Addr().String(),
		},
	})
	live2 = k2.Live
	go func() { _ = acc2.Serve(ctx) }()

	// Even before Readopt runs, NewK8sGranuleSandboxes has registered stored
	// hashes and marked the sandbox readoptable. A wrapper reconnecting during
	// startup is admitted immediately.
	if !k2.Live(sandboxID) {
		t.Fatal("sandbox should be reported Live from stored credentials before Readopt")
	}
	w2PreCtx, w2PreCancel := context.WithCancel(ctx)
	startInProcessGranuleWrapper(t, w2PreCtx, acc2.Addr().String(), sandboxID, rawToken, mountDir)
	waitCtxPre, waitCancelPre := context.WithTimeout(ctx, 10*time.Second)
	cliPre, err := acc2.WaitClient(waitCtxPre, sandboxID)
	waitCancelPre()
	if err != nil {
		t.Fatalf("WaitClient on restart before Readopt: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if code, err := cliPre.Exec(ctx, granulesandbox.ExecRequest{Argv: []string{"echo", "startup-admitted"}}, nil, &stdout, &stderr); err != nil || code != 0 {
		t.Fatalf("Exec on restart before Readopt = %d, %v", code, err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "startup-admitted" {
		t.Fatalf("stdout = %q, want startup-admitted", got)
	}
	w2PreCancel()

	// Now run Readopt (including over a freshly initialized acceptor state to
	// prove Readopt itself also calls Acceptor.Register) and verify the pod's
	// wrapper is re-admitted and executes commands.
	acc2.Revoke(sandboxID)
	sb2, err := k2.Readopt(ctx, sandboxID)
	if err != nil {
		t.Fatalf("Readopt: %v", err)
	}
	w2Ctx, w2Cancel := context.WithCancel(ctx)
	defer w2Cancel()
	startInProcessGranuleWrapper(t, w2Ctx, acc2.Addr().String(), sandboxID, rawToken, mountDir)
	waitCtx2, waitCancel2 := context.WithTimeout(ctx, 10*time.Second)
	cli2, err := acc2.WaitClient(waitCtx2, sandboxID)
	waitCancel2()
	if err != nil {
		t.Fatalf("WaitClient after Readopt: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if code, err := cli2.Exec(ctx, granulesandbox.ExecRequest{Argv: []string{"echo", "after-readopt"}}, nil, &stdout, &stderr); err != nil || code != 0 {
		t.Fatalf("Exec after Readopt = %d, %v (stderr %q)", code, err, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "after-readopt" {
		t.Fatalf("stdout after Readopt = %q, want after-readopt", got)
	}

	// Release the readopted sandbox: its stored hash must be deleted and its
	// wrapper must be refused with ErrAuthFailed.
	if err := sb2.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, ok, err := store.GetSandboxAccess(ctx, sandboxID); err != nil || ok {
		t.Fatalf("GetSandboxAccess after Release: ok=%v, err=%v; want cleared", ok, err)
	}
	if k2.Live(sandboxID) {
		t.Fatal("released sandbox is still reported Live")
	}
	if _, ok := acc2.Client(sandboxID); ok {
		t.Fatal("released sandbox kept its channel")
	}
	w2Cancel()

	refusedDone := startInProcessGranuleWrapper(t, ctx, acc2.Addr().String(), sandboxID, rawToken, mountDir)
	select {
	case err := <-refusedDone:
		if !errors.Is(err, granulesandbox.ErrAuthFailed) {
			t.Fatalf("DialOut after Release = %v, want ErrAuthFailed", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("released sandbox's wrapper was not refused")
	}
}

func TestK8sGranuleSweepOrphansClearsStoredHashAndRefusesWrapper(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := openAccessTestStore(t)
	ca, err := sandboxaccess.NewCA("dep-reap", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	kubectlDir := t.TempDir()
	var live func(string) bool
	acc, err := sandboxaccess.Listen(sandboxaccess.Config{
		Deployment:  "dep-reap",
		CA:          ca,
		ServerNames: []string{"127.0.0.1", "grain-access.grain.svc"},
		Live:        func(id string) bool { return live != nil && live(id) },
	}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = acc.Serve(ctx) }()
	t.Cleanup(func() { _ = acc.Close() })

	noopForward := func(ctx context.Context, cfg granule.KubectlForwardConfig) error {
		if cfg.OnReady != nil {
			cfg.OnReady()
		}
		<-ctx.Done()
		return ctx.Err()
	}

	k1 := NewK8sGranuleSandboxes(K8sGranuleConfig{
		Namespace:         "grain",
		Kubectl:           writeRecordingKubectl(t, kubectlDir),
		Image:             "example/granule:latest",
		ReadyPollInterval: 5 * time.Millisecond,
		ForwardRunner:     noopForward,
		Access: &K8sGranuleAccess{
			Acceptor: acc,
			Store:    store,
			Dial:     acc.Addr().String(),
		},
	})
	live = k1.Live

	const sandboxID = "902-1"
	if _, err := k1.Acquire(ctx, sandboxID, Shape{}); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	mountDir, rawToken := extractSecretMountDir(t, kubectlDir)

	// Simulate restart onto k2 where 902-1 is NOT readopted (it is an orphan),
	// followed by ReapOrphans.
	k2 := NewK8sGranuleSandboxes(K8sGranuleConfig{
		Namespace:         "grain",
		Kubectl:           writeRecordingKubectl(t, t.TempDir()),
		Image:             "example/granule:latest",
		ReadyPollInterval: 5 * time.Millisecond,
		ForwardRunner:     noopForward,
		Access: &K8sGranuleAccess{
			Acceptor: acc,
			Store:    store,
			Dial:     acc.Addr().String(),
		},
	})
	live = k2.Live

	if _, err := k2.ReapOrphans(ctx); err != nil {
		t.Fatalf("ReapOrphans: %v", err)
	}
	if _, ok, err := store.GetSandboxAccess(ctx, sandboxID); err != nil || ok {
		t.Fatalf("GetSandboxAccess after ReapOrphans: ok=%v, err=%v; want cleared", ok, err)
	}
	if k2.Live(sandboxID) {
		t.Fatal("reaped orphan sandbox is still reported Live")
	}

	refusedDone := startInProcessGranuleWrapper(t, ctx, acc.Addr().String(), sandboxID, rawToken, mountDir)
	select {
	case err := <-refusedDone:
		if !errors.Is(err, granulesandbox.ErrAuthFailed) {
			t.Fatalf("DialOut after ReapOrphans = %v, want ErrAuthFailed", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reaped sandbox's wrapper was not refused")
	}
}

func TestK8sGranuleCredentialLifeUsesSandboxLifeCappedAtMax(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	minter := &fakeMinter{}
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{
		Namespace:         "grain",
		Kubectl:           writeRecordingKubectl(t, t.TempDir()),
		Image:             "example/granule:latest",
		ReadyPollInterval: 5 * time.Millisecond,
		ForwardRunner: func(ctx context.Context, cfg granule.KubectlForwardConfig) error {
			if cfg.OnReady != nil {
				cfg.OnReady()
			}
			<-ctx.Done()
			return ctx.Err()
		},
		Access: &K8sGranuleAccess{
			Acceptor: minter,
			Dial:     "grain-access.grain.svc:4244",
			Life:     2 * time.Hour,
		},
	})

	// Explicit Shape.Life (e.g. a CI job's timeout or persistent sandbox's TTL).
	sbShort, err := k.Acquire(ctx, "910-1", Shape{Life: 20 * time.Minute})
	if err != nil {
		t.Fatalf("Acquire 910-1: %v", err)
	}
	defer sbShort.Release(ctx)

	// Shape.Life exceeding MaxCredentialLife is capped at MaxCredentialLife.
	sbLong, err := k.Acquire(ctx, "911-1", Shape{Life: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatalf("Acquire 911-1: %v", err)
	}
	defer sbLong.Release(ctx)

	// Zero Shape.Life falls back to Access.Life (capped at MaxCredentialLife).
	sbDefault, err := k.Acquire(ctx, "912-1", Shape{})
	if err != nil {
		t.Fatalf("Acquire 912-1: %v", err)
	}
	defer sbDefault.Release(ctx)

	minter.mu.Lock()
	defer minter.mu.Unlock()
	if got := minter.mintedTTLs["910-1"]; got != 20*time.Minute {
		t.Fatalf("910-1 ttl = %s, want 20m", got)
	}
	if got := minter.mintedTTLs["911-1"]; got != sandboxaccess.MaxCredentialLife {
		t.Fatalf("911-1 ttl = %s, want %s (MaxCredentialLife)", got, sandboxaccess.MaxCredentialLife)
	}
	if got := minter.mintedTTLs["912-1"]; got != 2*time.Hour {
		t.Fatalf("912-1 ttl = %s, want 2h (Access.Life fallback)", got)
	}
}

// A sandbox_access row whose run had already finished is left out of
// SandboxAccessCredentials, so before DeleteFinishedSandboxAccess no sweep
// ever saw it to clear it: a daemon that died between finishing a run and
// releasing its sandbox left the token hash behind for good (seen across a
// real daemon restart in docs/test-runs/2026-10-29-gke-sandbox-access.md).
func TestK8sGranuleSweepClearsFinishedRunsStoredHash(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openAccessTestStore(t)
	notAfter := time.Now().Add(time.Hour)
	hash := sandboxaccess.TokenHash("tok")
	for _, sb := range []string{"905-1", "906-1", "907-1", "908-1"} {
		if err := store.SetSandboxAccess(ctx, sb, hash, notAfter); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now().Add(-time.Hour).UTC()
	finished := time.Now().Add(-time.Minute).UTC()
	for _, r := range []struct {
		id, task, sandbox string
		finished          any
	}{
		{"r905", "905", "905-1", finished}, // finished: cleared
		{"r906", "906", "906-1", nil},      // still running: kept
		{"r908a", "908a", "908-1", finished},
		{"r908b", "908b", "908-1", nil}, // a live run still claims it: kept
	} {
		if _, err := store.DB().ExecContext(ctx,
			"INSERT INTO task_run (id, task_id, sandbox, attempt, started_at, finished_at) VALUES (?, ?, ?, 1, ?, ?)",
			r.id, r.task, r.sandbox, started, r.finished); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.DeleteFinishedSandboxAccess(ctx); err != nil {
		t.Fatalf("DeleteFinishedSandboxAccess: %v", err)
	}
	for sb, want := range map[string]bool{"905-1": false, "906-1": true, "907-1": true, "908-1": true} {
		if _, ok, err := store.GetSandboxAccess(ctx, sb); err != nil || ok != want {
			t.Errorf("GetSandboxAccess(%q) after DeleteFinishedSandboxAccess = %v, %v; want %v", sb, ok, err, want)
		}
	}

	// The backend's orphan sweep is what runs it: at startup (ReapOrphans)
	// and on every reconcile tick.
	if err := store.SetSandboxAccess(ctx, "905-1", hash, notAfter); err != nil {
		t.Fatal(err)
	}
	minter := &fakeMinter{}
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{
		Namespace: "grain",
		Kubectl:   writeRecordingKubectl(t, t.TempDir()),
		Image:     "example/granule:latest",
		Access:    &K8sGranuleAccess{Acceptor: minter, Store: store, Dial: "127.0.0.1:1"},
	})
	if _, err := k.ReapOrphans(ctx); err != nil {
		t.Fatalf("ReapOrphans: %v", err)
	}
	if _, ok, err := store.GetSandboxAccess(ctx, "905-1"); err != nil || ok {
		t.Fatalf("finished run's stored hash after ReapOrphans: ok=%v, err=%v; want cleared", ok, err)
	}
}
