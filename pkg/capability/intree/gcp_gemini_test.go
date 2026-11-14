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

package intree

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/geminikey"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

// fakeAPIKeys is one project's API Keys, recording which credential each
// call authenticated as.
type fakeAPIKeys struct {
	mu    sync.Mutex
	n     int
	keys  map[string]geminikey.MintedKey
	calls []string // "<credential>@<project>"
}

func (f *fakeAPIKeys) factory(_ context.Context, credentialJSON, project string) (geminikey.KeyMinter, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, credentialJSON+"@"+project)
	return f, nil
}

func (f *fakeAPIKeys) CreateKey(_ context.Context, displayName, apiTargetService string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if apiTargetService != geminikey.DefaultAPITargetService {
		return "", "", fmt.Errorf("key restricted to %q", apiTargetService)
	}
	f.n++
	name := fmt.Sprintf("projects/p/locations/global/keys/k%d", f.n)
	if f.keys == nil {
		f.keys = map[string]geminikey.MintedKey{}
	}
	f.keys[name] = geminikey.MintedKey{Name: name, DisplayName: displayName, CreateTime: time.Now()}
	return name, fmt.Sprintf("AIza-key-%d", f.n), nil
}

func (f *fakeAPIKeys) DeleteKey(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.keys, name)
	return nil
}

func (f *fakeAPIKeys) ListKeys(context.Context) ([]geminikey.MintedKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []geminikey.MintedKey
	for _, k := range f.keys {
		out = append(out, k)
	}
	return out, nil
}

func (f *fakeAPIKeys) live() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.keys)
}

type secretMap map[string]string

func (m secretMap) Resolve(_ context.Context, name string) (string, error) {
	if v, ok := m[name]; ok {
		return v, nil
	}
	return "", fmt.Errorf("no secret %q", name)
}

// A run holding the in-process gemini-key and a run holding gcp#gemini-key
// on a gcp server configured with the same project and minter credential
// (gemini_key, the minter as its admin) get a key minted in the same
// project with the same credential and restriction, placed at the same
// path with the same mode, and the same prompt; and each deletes its key
// when the run is over.
func TestGeminiKeyMethodFidelity(t *testing.T) {
	const project = "acme-ai"
	cloud := &fakeAPIKeys{}

	legacy := geminikey.New(project, model.CredentialRef{Name: "gcp-key-minter"})
	legacy.SetKeyMinter(cloud.factory)
	cc := model.CapabilityContext{Run: model.Run{ID: "42-1"}, Now: time.Now(),
		Credentials: secretMap{"gcp-key-minter": testMinterKey}}
	if res, err := legacy.Resolve(context.Background(), cc); err != nil || res.Refused {
		t.Fatalf("legacy resolve = %+v, %v", res, err)
	}
	mat, err := legacy.Materialize(context.Background(), cc)
	if err != nil {
		t.Fatal(err)
	}
	legacyPrompt, err := legacy.PromptSection(context.Background(), cc, mat.Placements)
	if err != nil {
		t.Fatal(err)
	}

	s, err := newConfigurableGCP(GCPConfig{GeminiMinter: cloud.factory})
	if err != nil {
		t.Fatal(err)
	}
	ps, err := attachConfigured(t, s, map[string]any{
		"admin_credential_source": "configured",
		"admin_credential":        "$secret:gcp-key-minter",
		"gemini_key":              map[string]any{"project": project},
	}, map[string]string{"gcp-key-minter": testMinterKey})
	if err != nil {
		t.Fatal(err)
	}
	if ps.Methods["gcp-key"] != nil {
		t.Error("a gcp server with no permission set offers gcp-key")
	}
	if ps.Methods[GeminiKeyMethod] == nil {
		t.Fatalf("the gcp server offers %v, without gemini-key", ps.Methods)
	}
	resp := mint(t, ps, GeminiKeyMethod, "grain-42-1-0", map[string]any{})
	if resp.Status != protocol.StatusOK {
		t.Fatalf("mint = %+v", resp)
	}

	if len(resp.Placements) != len(mat.Placements) || len(resp.Placements) != 1 {
		t.Fatalf("gcp#gemini-key placed %+v, the in-process gemini-key %+v", resp.Placements, mat.Placements)
	}
	got, was := resp.Placements[0], mat.Placements[0]
	if got.Path != was.Path || got.Path != geminikey.KeyPath || got.FileMode() != 0o600 || was.EffectiveMode() != "600" {
		t.Errorf("placed %s %o, in-process %s %s", got.Path, got.FileMode(), was.Path, was.EffectiveMode())
	}
	if !strings.HasPrefix(got.Content, "AIza-key-") || !strings.HasPrefix(was.Content, "AIza-key-") {
		t.Errorf("placed %q and %q, want the minted keys", got.Content, was.Content)
	}
	if resp.PromptSection != legacyPrompt {
		t.Errorf("prompt differs:\ngcp:        %q\nin-process: %q", resp.PromptSection, legacyPrompt)
	}
	for _, call := range cloud.calls {
		if call != testMinterKey+"@"+project {
			t.Errorf("a key was minted as %q, want the minter credential in %s", call, project)
		}
	}
	if n := cloud.live(); n != 2 {
		t.Fatalf("%d keys live, want the two minted", n)
	}

	if err := legacy.Revoke(context.Background(), cc, *mat.Lease); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.Release(context.Background(), GeminiKeyMethod, "grain-42-1-0"); err != nil {
		t.Fatal(err)
	}
	if n := cloud.live(); n != 0 {
		t.Errorf("%d keys live after both were released", n)
	}
}

// gemini_key needs an admin to mint with, and one in a project id.
func TestGeminiKeyConfigurationIsValidated(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config map[string]any
		want   string
	}{
		{"unknown admin", map[string]any{"gemini_key": map[string]any{"project": "acme-ai", "admin": "nobody"}}, `no admin is called "nobody"`},
		{"bad project", map[string]any{"gemini_key": map[string]any{"project": "acme/../ai"}}, "is not a project id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := newConfigurableGCP(GCPConfig{GeminiMinter: (&fakeAPIKeys{}).factory})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := attachConfigured(t, s, tc.config, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("attach = %v, want an error saying %q", err, tc.want)
			}
		})
	}
}

// A run granted the retired bare gemini-key and one granted the
// gcp#gemini-key={} schema migration 103 rewrote it to reach the same
// method with the same arguments, and get the same placement and prompt.
func TestGeminiKeyLegacyGrantFidelity(t *testing.T) {
	s, err := newConfigurableGCP(GCPConfig{GeminiMinter: (&fakeAPIKeys{}).factory})
	if err != nil {
		t.Fatal(err)
	}
	ps, err := attachConfigured(t, s, map[string]any{
		"admin_credential_source": "configured",
		"admin_credential":        "$secret:gcp-key-minter",
		"gemini_key":              map[string]any{"project": "acme-ai"},
	}, map[string]string{"gcp-key-minter": testMinterKey})
	if err != nil {
		t.Fatal(err)
	}
	var placed []protocol.Placement
	var prompts []string
	for i, grant := range []string{"gemini-key", model.GCPGeminiKeyGrant} {
		canonical := model.RetiredGrantsBecame(grant)[0]
		if canonical != model.GCPGeminiKeyGrant {
			t.Fatalf("%q is granted as %q, want %q", grant, canonical, model.GCPGeminiKeyGrant)
		}
		spec, err := protocol.ParseCLIGrant(canonical)
		if err != nil {
			t.Fatal(err)
		}
		ref, method, _ := strings.Cut(spec.Capability, "#")
		if ref != model.GCPCapabilityServerID || method != GeminiKeyMethod {
			t.Fatalf("%q names %s#%s, want the gcp server's %s", grant, ref, method, GeminiKeyMethod)
		}
		resp := mint(t, ps, method, fmt.Sprintf("grain-42-%d-0", i+1), spec.With)
		if resp.Status != protocol.StatusOK || len(resp.Placements) != 1 {
			t.Fatalf("%q minted %+v", grant, resp)
		}
		placed = append(placed, resp.Placements[0])
		prompts = append(prompts, resp.PromptSection)
	}
	if placed[0].Path != placed[1].Path || placed[0].Path != geminikey.KeyPath || placed[0].FileMode() != placed[1].FileMode() || placed[0].FileMode() != 0o600 {
		t.Errorf("placed %s %o and %s %o, want both at %s 0600",
			placed[0].Path, placed[0].FileMode(), placed[1].Path, placed[1].FileMode(), geminikey.KeyPath)
	}
	if prompts[0] != prompts[1] || prompts[0] != geminikey.Prompt() {
		t.Errorf("prompts differ:\nlegacy:    %q\nrewritten: %q", prompts[0], prompts[1])
	}
}
