package registrypush

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

type credentials string

func (c credentials) Resolve(context.Context, string) (string, error) { return string(c), nil }

type errResolver struct{}

func (errResolver) Resolve(context.Context, string) (string, error) {
	return "", errors.New("secret not found")
}

func TestRegistryPushIsPublishOnlyAndPlacesTheCredential(t *testing.T) {
	p := New()
	if !p.Spec().SupportsCI() || p.Spec().SupportsTask() ||
		p.SupportsCIEvent("push") || p.SupportsCIEvent("pull_request") ||
		!p.SupportsCIEvent("release") || !p.SupportsCIEvent("workflow_dispatch") {
		t.Fatalf("unexpected CI scope: spec=%+v", p.Spec())
	}
	m, err := p.Materialize(context.Background(), model.CapabilityContext{Credentials: credentials(
		`{"registry":"ghcr.io","username":"release-bot","token":"secret"}`)})
	if err != nil {
		t.Fatalf("materializing: %v", err)
	}
	if len(m.Placements) != 3 {
		t.Fatalf("placements = %+v", m.Placements)
	}
	if m.Placements[0].Path != RegistryPath || m.Placements[0].Content != "ghcr.io" {
		t.Errorf("placement[0] = %+v", m.Placements[0])
	}
	if m.Placements[1].Path != UsernamePath || m.Placements[1].Content != "release-bot" {
		t.Errorf("placement[1] = %+v", m.Placements[1])
	}
	if m.Placements[2].Path != TokenPath || m.Placements[2].Content != "secret" {
		t.Errorf("placement[2] = %+v", m.Placements[2])
	}
}

func TestCheckCredential(t *testing.T) {
	p := New()
	ctx := context.Background()

	// 1. Valid
	check, err := p.CheckCredential(ctx, credentials(`{"registry":"ghcr.io","username":"bot","token":"tok"}`))
	if err != nil {
		t.Fatalf("CheckCredential(valid) error: %v", err)
	}
	if !strings.Contains(check.Detail, "ghcr.io") || !strings.Contains(check.Detail, "bot") {
		t.Errorf("check.Detail = %q, want mention of ghcr.io and bot", check.Detail)
	}

	// 2. Nil credentials
	_, err = p.CheckCredential(ctx, nil)
	if err == nil {
		t.Error("expected error for nil credentials")
	}

	// 3. Resolver error
	_, err = p.CheckCredential(ctx, errResolver{})
	if err == nil {
		t.Error("expected error for failed resolver")
	}

	// 4. Invalid JSON
	_, err = p.CheckCredential(ctx, credentials(`not json`))
	if err == nil {
		t.Error("expected error for invalid json")
	}

	// 5. Missing required fields
	_, err = p.CheckCredential(ctx, credentials(`{"registry":"ghcr.io","username":""}`))
	if err == nil {
		t.Error("expected error for missing username/token")
	}

	// 6. Invalid registry format (scheme or path)
	_, err = p.CheckCredential(ctx, credentials(`{"registry":"https://ghcr.io","username":"bot","token":"tok"}`))
	if err == nil {
		t.Error("expected error for registry with scheme/path")
	}
}

func TestRegistryPushRejectsIncompleteCredentials(t *testing.T) {
	p := New()
	ctx := context.Background()

	// Incomplete json
	resolution, err := p.Resolve(ctx, model.CapabilityContext{Credentials: credentials(`{"registry":"ghcr.io"}`)})
	if err != nil || !resolution.Refused {
		t.Fatalf("Resolve = %+v, %v", resolution, err)
	}

	// Nil credentials
	resolution, err = p.Resolve(ctx, model.CapabilityContext{Credentials: nil})
	if err != nil || !resolution.Refused {
		t.Fatalf("Resolve(nil) = %+v, %v", resolution, err)
	}

	// Resolver error
	resolution, err = p.Resolve(ctx, model.CapabilityContext{Credentials: errResolver{}})
	if err != nil || !resolution.Refused {
		t.Fatalf("Resolve(err) = %+v, %v", resolution, err)
	}

	// Invalid characters in registry
	resolution, err = p.Resolve(ctx, model.CapabilityContext{Credentials: credentials(`{"registry":"ghcr.io:5000/sub","username":"u","token":"t"}`)})
	if err != nil || !resolution.Refused {
		t.Fatalf("Resolve(invalid registry) = %+v, %v", resolution, err)
	}

	// Valid credentials
	resolution, err = p.Resolve(ctx, model.CapabilityContext{Credentials: credentials(`{"registry":"ghcr.io","username":"u","token":"t"}`)})
	if err != nil || resolution.Refused {
		t.Fatalf("Resolve(valid) = %+v, %v", resolution, err)
	}

	// Materialize with invalid credentials
	_, err = p.Materialize(ctx, model.CapabilityContext{Credentials: credentials(`{"registry":"ghcr.io"}`)})
	if err == nil {
		t.Error("Materialize(invalid) should have failed")
	}
}
