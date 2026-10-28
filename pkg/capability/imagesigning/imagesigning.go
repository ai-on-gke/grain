// Package imagesigning provides the "image-signing" CI capability: the
// cosign private key a release signs this repository's images with, and
// the password that decrypts it (docs/image-signing.md).
//
// It is package-publish's neighbour on the release job and deliberately
// not its twin. A signature is only worth checking if whoever can push
// to the package cannot also produce one, so this is held more narrowly
// than the push credential on every axis there is:
//
//   - the event: `release` alone. package-publish and registry-push also
//     answer `workflow_dispatch`, and .grain/workflows/image.yml's
//     `guest-cache` runs under that event from branches, so a key given
//     to a dispatch is a key given to a branch's own steps;
//   - the material: two secrets an operator stored on purpose, not a
//     credential the deployment already holds for something else. There
//     is no fallback to resolve when they are missing -- the job is
//     refused, saying which secret is not set;
//   - the grant: CI-only and admin-team only, like package-publish, and
//     enabled per repository by an operator.
//
// scripts/release-images.sh reads the two files this places
// ($IMAGE_SIGNING_KEY, $IMAGE_SIGNING_PASSWORD_FILE) and signs each
// image by digest with `cosign sign --key ... --tlog-upload=false`.
package imagesigning

import (
	"context"
	"fmt"
	"strings"

	"github.com/bwsalmon/grain/pkg/model"
)

const (
	CapabilityName = "image-signing"

	// The secret store's names for the two halves: one secret,
	// "image-signing", with two keys (`grain secrets set image-signing
	// key` and `... password`).
	KeyCredential      = "image-signing/key"
	PasswordCredential = "image-signing/password"

	// Where scripts/release-images.sh looks for them by default.
	KeyPath      = "/home/debian/.image-signing-key"
	PasswordPath = "/home/debian/.image-signing-password"
)

// Config is what the provider needs beyond the grant itself.
type Config struct{}

type Provider struct {
	model.BaseCapability
}

func New(_ Config) *Provider { return &Provider{} }

func (p *Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name: CapabilityName, Label: "image signing",
		Description: "Sign this repository's release images with the deployment's cosign key, on the post-merge release event only",
		Source:      model.GrantByLabel, Provision: model.ProvisionPlace,
		// A task is not an event, and the event is the whole of this
		// gate (model.CapabilitySpec.CIOnly).
		CIOnly:   true,
		Requires: []string{KeyCredential, PasswordCredential},
	}
}

// SupportsCIEvent is `release` and nothing else -- narrower than
// package-publish on purpose. `workflow_dispatch` is refused because a
// dispatch can name a branch, and a branch's steps are whatever its
// author wrote: a key handed to one would let anybody who can push a
// branch sign whatever they can push to the package, which is exactly
// the "signature proves nothing beyond push access" this capability
// exists to avoid.
func (*Provider) SupportsCIEvent(event string) bool {
	return event == "release"
}

func (*Provider) CheckCredential(ctx context.Context, credentials model.CredentialResolver) (model.CredentialCheck, error) {
	check := model.CredentialCheck{Credentials: []string{KeyCredential, PasswordCredential}}
	if _, err := resolve(ctx, credentials); err != nil {
		return check, err
	}
	check.Detail = "The key and its password are both set; the next release's cosign sign is the check that the password decrypts the key."
	return check, nil
}

func (*Provider) Resolve(ctx context.Context, cc model.CapabilityContext) (model.Resolution, error) {
	if _, err := resolve(ctx, cc.Credentials); err != nil {
		return model.RefusedBecause(err.Error()), nil
	}
	return model.Honoured(), nil
}

func (*Provider) Materialize(ctx context.Context, cc model.CapabilityContext) (model.Materialization, error) {
	m, err := resolve(ctx, cc.Credentials)
	if err != nil {
		return model.Materialization{}, err
	}
	return model.Materialization{Placements: []model.Placement{
		{Side: model.SideSandbox, Path: KeyPath, Content: m.key},
		{Side: model.SideSandbox, Path: PasswordPath, Content: m.password},
	}}, nil
}

type material struct{ key, password string }

func resolve(ctx context.Context, credentials model.CredentialResolver) (material, error) {
	key, err := lookup(ctx, credentials, KeyCredential)
	if err != nil {
		return material{}, err
	}
	// cosign generate-key-pair writes a PEM block it calls an encrypted
	// sigstore (formerly cosign) private key. Anything else -- the
	// public key pasted by mistake, most likely -- would only fail at
	// the release's signing step, after the build and both suites.
	if !strings.Contains(key, "-----BEGIN ") || !strings.Contains(key, "PRIVATE KEY-----") {
		return material{}, fmt.Errorf("the %q secret is not a PEM private key: store the cosign.key file `cosign generate-key-pair` wrote, not cosign.pub", KeyCredential)
	}
	password, err := lookup(ctx, credentials, PasswordCredential)
	if err != nil {
		return material{}, err
	}
	if !strings.HasSuffix(key, "\n") {
		key += "\n"
	}
	// The password file is read with $(cat ...), which drops trailing
	// newlines anyway; trimming them here keeps one pasted with a
	// newline from reading as a different password anywhere else.
	return material{key: key, password: strings.TrimRight(password, "\r\n")}, nil
}

func lookup(ctx context.Context, credentials model.CredentialResolver, name string) (string, error) {
	notSet := fmt.Sprintf("the %q secret is not configured, so there is no key to sign release images with: "+
		"run `cosign generate-key-pair`, store cosign.key as %q and its password as %q, and publish cosign.pub",
		name, KeyCredential, PasswordCredential)
	if credentials == nil {
		return "", fmt.Errorf("%s", notSet)
	}
	v, err := credentials.Resolve(ctx, name)
	if err != nil {
		return "", fmt.Errorf("%s: %w", notSet, err)
	}
	if strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("the %q secret is empty", name)
	}
	return v, nil
}
