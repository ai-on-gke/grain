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
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/hub"
)

func TestGrainHubDefaultProvidersRegistered(t *testing.T) {
	signers := hub.RegisteredSignerProviders()
	for _, want := range []string{"kms", "local"} {
		if !slices.Contains(signers, want) {
			t.Fatalf("expected signer provider %q registered in cmd/grain-hub, got %v", want, signers)
		}
	}
	identities := hub.RegisteredFrontIdentities()
	if !slices.Contains(identities, "iap") {
		t.Fatalf("expected front identity provider \"iap\" registered, got %v", identities)
	}
	groups := hub.RegisteredGroupResolvers()
	for _, want := range []string{"cloud-identity", "static"} {
		if !slices.Contains(groups, want) {
			t.Fatalf("expected group resolver %q registered, got %v", want, groups)
		}
	}
	transports := hub.RegisteredConnectTransports()
	if !slices.Contains(transports, "ws-mux") {
		t.Fatalf("expected connect transport \"ws-mux\" registered, got %v", transports)
	}

	var stdout, stderr bytes.Buffer
	if err := hub.Run(context.Background(), []string{"version"}, &stdout, &stderr); err != nil {
		t.Fatalf("registry.Run(version): %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatalf("expected non-empty version output")
	}
}
