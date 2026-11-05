package main

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/registry"
)

func TestGrainRegistryDefaultProvidersRegistered(t *testing.T) {
	signers := registry.RegisteredSignerProviders()
	for _, want := range []string{"kms", "local"} {
		if !slices.Contains(signers, want) {
			t.Fatalf("expected signer provider %q registered in cmd/grain-registry, got %v", want, signers)
		}
	}
	identities := registry.RegisteredFrontIdentities()
	if !slices.Contains(identities, "iap") {
		t.Fatalf("expected front identity provider \"iap\" registered, got %v", identities)
	}
	groups := registry.RegisteredGroupResolvers()
	for _, want := range []string{"cloud-identity", "static"} {
		if !slices.Contains(groups, want) {
			t.Fatalf("expected group resolver %q registered, got %v", want, groups)
		}
	}
	transports := registry.RegisteredConnectTransports()
	if !slices.Contains(transports, "ws-mux") {
		t.Fatalf("expected connect transport \"ws-mux\" registered, got %v", transports)
	}

	var stdout, stderr bytes.Buffer
	if err := registry.Run(context.Background(), []string{"version"}, &stdout, &stderr); err != nil {
		t.Fatalf("registry.Run(version): %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatalf("expected non-empty version output")
	}
}
