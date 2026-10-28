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
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/granule"
	"github.com/ai-on-gke/grain/pkg/sandboxaccess"
)

type fakeMinter struct {
	mu      sync.Mutex
	minted  []string
	revoked []string
}

func (f *fakeMinter) Mint(sandbox string, ttl time.Duration) (sandboxaccess.Credentials, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.minted = append(f.minted, sandbox)
	return sandboxaccess.Credentials{
		Sandbox: sandbox, Token: "tok-" + sandbox,
		CertPEM: []byte("CERT"), KeyPEM: []byte("KEY"), CAPEM: []byte("CA"),
		NotAfter: time.Now().Add(ttl),
	}, nil
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
			Dial:          "grain-access.grain.svc:7443",
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
		"GRANULE_ACCESS_DIAL":           "grain-access.grain.svc:7443",
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
