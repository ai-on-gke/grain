package gcpkey

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// KeyProvider is the capability provider for one of the deployment's
// named GCP keys (e.g. "gcp-key:deploy"). It reads the key's
// configuration from the store by name at every Resolve, Materialize,
// PromptSection, Revoke, Reap and CheckCredential, so an edit takes effect
// without a restart. Nothing it does reads the task's team: the key is the
// deployment's (single-tenant reset stage 4e).
type KeyProvider struct {
	model.BaseCapability
	name  string
	store *model.Store

	// NewMinter can be set to override the Minter constructor (useful in tests).
	NewMinter func(ctx context.Context, credentialJSON string) (Minter, error)

	nextAccount uint32
}

func (*KeyProvider) ScarceResourceProvider() {}

// NewKeyProvider creates a KeyProvider for the given key name and store.
func NewKeyProvider(name string, store *model.Store) *KeyProvider {
	return &KeyProvider{
		name:      name,
		store:     store,
		NewMinter: NewIAMMinter,
	}
}

func (p *KeyProvider) providerFor(k model.GCPKey) *Provider {
	prov := NewProvider(Config{
		Name:                k.Name,
		ProjectID:           k.ProjectID,
		ServiceAccountEmail: k.ServiceAccountEmail,
		ServiceAccountCount: k.ServiceAccountCount,
		MaxKeyAge:           k.MaxKeyAge,
		MinterCredential:    k.MinterCredential,
	})
	if p.NewMinter != nil {
		prov.NewMinter = p.NewMinter
	}
	prov.nextAccount = atomic.LoadUint32(&p.nextAccount)
	return prov
}

// key is the deployment's key named p.name, or nil when it has none.
func (p *KeyProvider) key(ctx context.Context) (*model.GCPKey, error) {
	if p.store == nil {
		return nil, fmt.Errorf("GCP keys require a database store")
	}
	k, err := p.store.GetGCPKey(ctx, p.name)
	if err != nil {
		return nil, fmt.Errorf("gcpkey: looking up GCP key %s: %w", p.name, err)
	}
	return k, nil
}

func (p *KeyProvider) Spec() model.CapabilitySpec {
	capName := model.GCPKeyCapability(p.name)
	label := "grain-gcp-key"
	if p.name != "" && p.name != "default" {
		label = "grain-gcp-key-" + p.name
	}
	desc := "Mint a short-lived GCP service-account key for this task"
	if p.name != "" && p.name != "default" {
		desc = fmt.Sprintf("Mint a short-lived GCP service-account key (%s) for this task", p.name)
	}

	maxLease := DefaultMaxKeyAge
	requires := []string{model.GCPKey{Name: p.name}.EffectiveMinterCredential()}
	if p.store != nil {
		if k, err := p.store.GetGCPKey(context.Background(), p.name); err == nil && k != nil {
			requires = p.providerFor(*k).Spec().Requires
			if k.MaxKeyAge > maxLease {
				maxLease = k.MaxKeyAge
			}
		}
	}

	return model.CapabilitySpec{
		Name:        capName,
		Label:       label,
		Description: desc,
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionMint,
		MaxLease:    maxLease,
		Requires:    requires,
	}
}

func (p *KeyProvider) Resolve(ctx context.Context, cc model.CapabilityContext) (model.Resolution, error) {
	if p.store == nil {
		return model.RefusedBecause("GCP keys require a database store"), nil
	}
	k, err := p.key(ctx)
	if err != nil {
		return model.Resolution{}, err
	}
	if k == nil {
		return model.RefusedBecause(model.MissingGCPKeyRefusal(model.GCPKeyCapability(p.name))), nil
	}
	return p.providerFor(*k).Resolve(ctx, cc)
}

func (p *KeyProvider) Materialize(ctx context.Context, cc model.CapabilityContext) (model.Materialization, error) {
	k, err := p.key(ctx)
	if err != nil {
		return model.Materialization{}, err
	}
	if k == nil {
		return model.Materialization{}, fmt.Errorf("this deployment has no GCP key %q configured", p.name)
	}
	prov := p.providerFor(*k)
	mat, err := prov.Materialize(ctx, cc)
	atomic.StoreUint32(&p.nextAccount, prov.nextAccount)
	return mat, err
}

func (p *KeyProvider) PromptSection(ctx context.Context, cc model.CapabilityContext, placements []model.Placement) (string, error) {
	if k, err := p.key(ctx); err == nil && k != nil {
		return p.providerFor(*k).PromptSection(ctx, cc, placements)
	}
	return NewProvider(Config{Name: p.name}).PromptSection(ctx, cc, placements)
}

// Revoke deletes the lease's key through the deployment's key of this
// name. A key deleted since the lease was minted has no minter left to
// revoke through; its GCP key is left for whoever deleted the row.
func (p *KeyProvider) Revoke(ctx context.Context, cc model.CapabilityContext, lease model.Lease) error {
	k, err := p.key(ctx)
	if err != nil || k == nil {
		return nil
	}
	return p.providerFor(*k).Revoke(ctx, cc, lease)
}

func (p *KeyProvider) Reap(ctx context.Context, creds model.CredentialResolver, before time.Time) ([]string, error) {
	if p.store == nil {
		return nil, nil
	}
	k, err := p.key(ctx)
	if err != nil || k == nil {
		return nil, err
	}
	return p.providerFor(*k).Reap(ctx, creds, before)
}

func (p *KeyProvider) CheckCredential(ctx context.Context, creds model.CredentialResolver) (model.CredentialCheck, error) {
	k, err := p.key(ctx)
	if err != nil {
		return model.CredentialCheck{}, err
	}
	if k == nil {
		return model.CredentialCheck{}, fmt.Errorf("this deployment has no GCP key named %q", p.name)
	}
	return p.providerFor(*k).CheckCredential(ctx, creds)
}

// DynamicProvider returns a dynamic capability provider resolver for named GCP keys backed by store.
func DynamicProvider(store *model.Store) model.DynamicProvider {
	return DynamicProviderWithMinter(store, nil)
}

// DynamicProviderWithMinter returns a dynamic capability provider resolver for named GCP keys backed by store
// with a custom Minter constructor (useful in tests).
func DynamicProviderWithMinter(store *model.Store, newMinter func(ctx context.Context, credentialJSON string) (Minter, error)) model.DynamicProvider {
	var mu sync.Mutex
	providers := make(map[string]*KeyProvider)

	return func(capName string) (model.CapabilityProvider, bool) {
		keyName, ok := model.GCPKeyName(capName)
		if !ok || keyName == "" {
			return nil, false
		}
		if store == nil {
			return nil, false
		}
		k, err := store.GetGCPKey(context.Background(), keyName)
		if err != nil {
			return nil, false
		}
		mu.Lock()
		defer mu.Unlock()
		if k == nil {
			delete(providers, keyName)
			return nil, false
		}
		prov, ok := providers[keyName]
		if !ok {
			prov = NewKeyProvider(keyName, store)
			if newMinter != nil {
				prov.NewMinter = newMinter
			}
			providers[keyName] = prov
		}
		return prov, true
	}
}
