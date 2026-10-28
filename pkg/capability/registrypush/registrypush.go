// Package registrypush provides the "registry-push" CI capability. An
// operator stores one JSON credential and explicitly enables the capability
// for a repository's CI. Post-merge releases and manually dispatched workflows
// may receive it as three mode-0600 files a shell can pass to docker login;
// ordinary branch and pull-request runs may not.
package registrypush

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bwsalmon/grain/pkg/model"
)

const (
	CapabilityName = "registry-push"
	CredentialName = "registry-push/credential"
	RegistryPath   = "/home/debian/.registry-push-registry"
	UsernamePath   = "/home/debian/.registry-push-username"
	TokenPath      = "/home/debian/.registry-push-token"
)

type credential struct {
	Registry string `json:"registry"`
	Username string `json:"username"`
	Token    string `json:"token"`
}

// Config configures registry-push capability settings.
type Config struct{}

type Provider struct {
	model.BaseCapability
}

func New() *Provider                   { return &Provider{} }
func NewWithConfig(_ Config) *Provider { return &Provider{} }

func (p *Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name: CapabilityName, Label: "registry push",
		Description: "Publish artifacts with an explicitly granted OCI registry credential from release or manual CI",
		Source:      model.GrantByLabel, Provision: model.ProvisionPlace,
		// CI's alone: SupportsCIEvent below is the whole of when this
		// credential may be handed out, and a task produces none of
		// those events (model.CapabilitySpec.CIOnly).
		CIOnly:   true,
		Requires: []string{CredentialName},
	}
}

func (*Provider) SupportsCIEvent(event string) bool {
	return event == "release" || event == "workflow_dispatch"
}

func (*Provider) CheckCredential(ctx context.Context, credentials model.CredentialResolver) (model.CredentialCheck, error) {
	c, err := resolve(ctx, credentials)
	if err != nil {
		return model.CredentialCheck{Credentials: []string{CredentialName}}, err
	}
	return model.CredentialCheck{
		Credentials: []string{CredentialName},
		Detail:      fmt.Sprintf("The credential is complete for %s as %s; the next release push is the authoritative permission check.", c.Registry, c.Username),
	}, nil
}

func (*Provider) Resolve(ctx context.Context, cc model.CapabilityContext) (model.Resolution, error) {
	_, err := resolve(ctx, cc.Credentials)
	if err != nil {
		return model.RefusedBecause(err.Error()), nil
	}
	return model.Honoured(), nil
}

func (*Provider) Materialize(ctx context.Context, cc model.CapabilityContext) (model.Materialization, error) {
	c, err := resolve(ctx, cc.Credentials)
	if err != nil {
		return model.Materialization{}, err
	}
	return model.Materialization{Placements: []model.Placement{
		{Side: model.SideSandbox, Path: RegistryPath, Content: c.Registry},
		{Side: model.SideSandbox, Path: UsernamePath, Content: c.Username},
		{Side: model.SideSandbox, Path: TokenPath, Content: c.Token},
	}}, nil
}

func resolve(ctx context.Context, credentials model.CredentialResolver) (credential, error) {
	if credentials == nil {
		return credential{}, fmt.Errorf("the %q secret is not configured", CredentialName)
	}
	raw, err := credentials.Resolve(ctx, CredentialName)
	if err != nil {
		return credential{}, fmt.Errorf("the %q secret is not configured: %w", CredentialName, err)
	}
	var c credential
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return credential{}, fmt.Errorf("the %q secret is not valid JSON: %w", CredentialName, err)
	}
	c.Registry, c.Username, c.Token = strings.TrimSpace(c.Registry), strings.TrimSpace(c.Username), strings.TrimSpace(c.Token)
	if c.Registry == "" || c.Username == "" || c.Token == "" {
		return credential{}, fmt.Errorf("the %q secret must contain non-empty registry, username, and token fields", CredentialName)
	}
	if strings.ContainsAny(c.Registry, "/:@") {
		return credential{}, fmt.Errorf("the %q secret's registry must be a hostname without a scheme or path", CredentialName)
	}
	return c, nil
}
