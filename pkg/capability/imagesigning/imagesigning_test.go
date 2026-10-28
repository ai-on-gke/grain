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

package imagesigning

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

const testKey = "-----BEGIN ENCRYPTED SIGSTORE PRIVATE KEY-----\nabc\n-----END ENCRYPTED SIGSTORE PRIVATE KEY-----"

// secrets is a store holding exactly the names in it.
type secrets map[string]string

func (s secrets) Resolve(_ context.Context, name string) (string, error) {
	v, ok := s[name]
	if !ok {
		return "", errors.New("secret not found")
	}
	return v, nil
}

func TestImageSigningIsReleaseOnly(t *testing.T) {
	p := New(Config{})
	spec := p.Spec()
	if !spec.SupportsCI() || spec.SupportsTask() || !spec.CIOnly {
		t.Fatalf("image-signing must be CI-only: %+v", spec)
	}
	if !p.SupportsCIEvent("release") {
		t.Error("image-signing must be honoured for release")
	}
	// workflow_dispatch above all: image.yml's guest-cache runs under it
	// from branches, and package-publish answers it.
	for _, event := range []string{"workflow_dispatch", "push", "pull_request", "pull_request_target", "schedule", ""} {
		if p.SupportsCIEvent(event) {
			t.Errorf("image-signing is honoured for %q", event)
		}
	}
}

func TestImageSigningPlacesKeyAndPassword(t *testing.T) {
	p := New(Config{})
	cc := model.CapabilityContext{Credentials: secrets{KeyCredential: testKey, PasswordCredential: "hunter2\n"}}
	res, err := p.Resolve(context.Background(), cc)
	if err != nil || res.Refused {
		t.Fatalf("Resolve = %+v, %v", res, err)
	}
	m, err := p.Materialize(context.Background(), cc)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Placements) != 2 {
		t.Fatalf("placements = %+v", m.Placements)
	}
	if pl := m.Placements[0]; pl.Side != model.SideSandbox || pl.Path != KeyPath || pl.Content != testKey+"\n" {
		t.Errorf("key placement = %+v", pl)
	}
	if pl := m.Placements[1]; pl.Side != model.SideSandbox || pl.Path != PasswordPath || pl.Content != "hunter2" {
		t.Errorf("password placement = %+v", pl)
	}
	if KeyPath != "/home/debian/.image-signing-key" || PasswordPath != "/home/debian/.image-signing-password" {
		t.Error("the placed paths must be the defaults scripts/release-images.sh reads")
	}
}

func TestImageSigningRefusesWithoutItsSecrets(t *testing.T) {
	cases := map[string]struct {
		creds model.CredentialResolver
		want  string
	}{
		"no store":       {nil, KeyCredential},
		"no key":         {secrets{PasswordCredential: "pw"}, KeyCredential},
		"no password":    {secrets{KeyCredential: testKey}, PasswordCredential},
		"empty password": {secrets{KeyCredential: testKey, PasswordCredential: " \n"}, PasswordCredential},
		"public key":     {secrets{KeyCredential: "-----BEGIN PUBLIC KEY-----\nabc\n-----END PUBLIC KEY-----", PasswordCredential: "pw"}, "not a PEM private key"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := New(Config{})
			cc := model.CapabilityContext{Credentials: tc.creds}
			res, err := p.Resolve(context.Background(), cc)
			if err != nil {
				t.Fatal(err)
			}
			if !res.Refused || !strings.Contains(res.Reason, tc.want) {
				t.Errorf("Resolve = %+v, want a refusal naming %q", res, tc.want)
			}
			if _, err := p.Materialize(context.Background(), cc); err == nil {
				t.Error("Materialize placed a key Resolve refused")
			}
			if _, err := p.CheckCredential(context.Background(), tc.creds); err == nil {
				t.Error("CheckCredential passed a key Resolve refused")
			}
		})
	}
}
