package githubtoken

import (
	"context"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

func TestSpecIsTheNamedTokensOwnCapabilityID(t *testing.T) {
	spec := New("release-bot", Config{}).Spec()
	if spec.Name != model.GitCredentialCapability("release-bot") {
		t.Errorf("Name = %q, want %q -- the id is what a Grant carries", spec.Name, model.GitCredentialCapability("release-bot"))
	}
	if spec.Provision != model.ProvisionSelect {
		t.Errorf("Provision = %q, want %q -- this names a standing credential, it mints nothing",
			spec.Provision, model.ProvisionSelect)
	}
	if len(spec.Requires) != 0 {
		t.Errorf("Requires = %v, want none -- the token is an operator's file, not a secrets-store entry", spec.Requires)
	}
	if spec.MaxLease != 0 {
		t.Errorf("MaxLease = %v, want 0 -- a SELECT capability has no lease to expire", spec.MaxLease)
	}
	if !strings.Contains(spec.Description, "release-bot") {
		t.Errorf("Description = %q, want it to name the token", spec.Description)
	}
}

// Registering two providers under one id would silently replace the
// first (model.CapabilityRegistry.Register), and an empty name would
// offer a capability whose id is the bare prefix -- neither is a token
// anyone asked for.
func TestProvidersSkipsEmptyAndDuplicateNames(t *testing.T) {
	providers := Providers([]string{"release-bot", "", "release-bot", "docs-bot"}, Config{})
	var ids []string
	for _, p := range providers {
		ids = append(ids, p.Spec().Name)
	}
	want := []string{
		model.GitCredentialCapability("release-bot"),
		model.GitCredentialCapability("docs-bot"),
	}
	if len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] {
		t.Errorf("Providers ids = %v, want %v", ids, want)
	}
}

func TestMaterializeRecordsTheCredentialAndPlacesNothing(t *testing.T) {
	m, err := New("release-bot", Config{}).Materialize(context.Background(), model.CapabilityContext{})
	if err != nil {
		t.Fatal(err)
	}
	if m.CredentialOverride == nil || m.CredentialOverride.Name != "release-bot" {
		t.Errorf("CredentialOverride = %+v, want the credential's own name", m.CredentialOverride)
	}
	// The token reaches the sandbox through the git proxy, never as
	// material -- a placement here would put a standing credential on a
	// sandbox's disk, which is exactly what pkg/gitproxy exists to avoid.
	if len(m.Placements) != 0 {
		t.Errorf("Placements = %+v, want none", m.Placements)
	}
	if m.Lease != nil {
		t.Errorf("Lease = %+v, want none -- nothing was minted to give back", m.Lease)
	}
}

func TestPromptSectionNamesTheTokenWithoutHandingItOver(t *testing.T) {
	section, err := New("release-bot", Config{}).PromptSection(context.Background(), model.CapabilityContext{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(section, "release-bot") {
		t.Errorf("prompt section %q should name the token the run's pushes carry", section)
	}
}

func TestResolveHonoursTheGrant(t *testing.T) {
	res, err := New("release-bot", Config{}).Resolve(context.Background(), model.CapabilityContext{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Refused {
		t.Errorf("resolution = %+v, want honoured -- the token exists or it would never have been offered", res)
	}
}
