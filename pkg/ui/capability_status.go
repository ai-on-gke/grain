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

package ui

import (
	"slices"
	"strings"

	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/capability/admincaps"
	"github.com/ai-on-gke/grain/pkg/capability/bootstrap"
	consolecap "github.com/ai-on-gke/grain/pkg/capability/console"
	"github.com/ai-on-gke/grain/pkg/capability/durable"
	"github.com/ai-on-gke/grain/pkg/capability/gcpkey"
	"github.com/ai-on-gke/grain/pkg/capability/geminikey"
	"github.com/ai-on-gke/grain/pkg/capability/githubsandbox"
	"github.com/ai-on-gke/grain/pkg/capability/gitpushanyref"
	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/capability/imagesigning"
	modelproxycap "github.com/ai-on-gke/grain/pkg/capability/modelproxy"
	"github.com/ai-on-gke/grain/pkg/capability/packagepublish"
	"github.com/ai-on-gke/grain/pkg/capability/prioritydispatch"
	"github.com/ai-on-gke/grain/pkg/capability/quarantine"
	"github.com/ai-on-gke/grain/pkg/capability/readissues"
	"github.com/ai-on-gke/grain/pkg/capability/registrypush"
	"github.com/ai-on-gke/grain/pkg/capability/releasenotes"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/modelproxy"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// CapabilityStatus is one capability's deployment-wide readiness, as
// shown on Settings' "Capabilities" tab -- distinct from the Capability
// type labels.go already exports, which is the per-task picker's
// attach/detach listing and says nothing about whether granting one
// would actually work. Ready mirrors what pkg/cli/daemon.go's own
// capabilityProviders would register -- out of the configuration this
// process is *running* with, not merely the one it has stored, which is
// what PendingConfig below is about -- plus, for a capability that
// registers unconditionally (github-sandbox, self-debug, self-repair,
// bootstrap-playbooks, per that function's own doc comment), whether
// every secret it
// resolves through CapabilityContext.Credentials is actually set --
// registering is not the same as working, and a task granted an
// unready capability today only discovers that later as a refused
// resolution or a failed materialize.
//
// Ready answers "would this work if a task were granted it", which is a
// different question from "can a task be granted it at all" -- see
// Grantable, which is the half a deployment configured perfectly can
// still fail on.
type CapabilityStatus struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Ready       bool   `json:"ready"`
	// Grantable reports whether any task can actually be granted this
	// capability: whether Config.Capabilities -- the picker's own
	// listing, and the one list (*Client).grantsFor and SetCapability
	// validate an id against before writing a model.Grant -- carries a
	// row for it. False means grain registers a provider for this
	// capability that nothing can ever reach it through: every attempt to
	// attach it, from the UI's picker or from `grain create
	// -capability`, is rejected as "unknown capability", so the provider
	// is never resolved, never materialized, and places nothing in any
	// sandbox.
	//
	// It is reported separately from Ready, and deliberately not folded
	// into it, because the two name opposite kinds of gap and are fixed
	// in opposite places: an unready capability is this deployment's
	// configuration (set a project, paste a secret), while an
	// ungrantable one is grain's own code -- ui.OfferedCapabilities and
	// pkg/cli/daemon.go's capabilityProviders having drifted apart --
	// and no amount of configuring will move it. Showing only Ready is
	// what let a deployment sit with a fully configured, "Ready" gcp-key
	// that no task had ever been able to ask for.
	Grantable bool `json:"grantable"`
	// CIOnly reports that no task can be granted this capability *by
	// design* -- model.CapabilitySpec.CIOnly -- rather than through the
	// code drift Grantable is otherwise the symptom of. The two travel
	// together: a CI-only capability is always Grantable false, and this
	// is what tells the pane to say "granted per repository, under CI
	// capabilities" instead of "grain registers a provider nothing can
	// reach; configuration cannot fix this one".
	//
	// It is not folded into Grantable for the same reason Checkable is
	// not folded into Ready: they answer different questions, and one
	// badge meaning either would leave a real drift indistinguishable
	// from a deliberate restriction.
	CIOnly bool `json:"ciOnly,omitempty"`
	// Checkable reports whether this deployment can actually *test* this
	// capability's standing credential -- whether grain ships a check for
	// it (model.CredentialChecker, capabilityCheckable) and this UI is
	// wired to something that can run one (Config.CapabilityChecks). It
	// is what decides whether the pane offers the action at all; the
	// answer a check gives is POST /api/capabilities/{id}/check's own
	// (CapabilityCheck), never stored here.
	//
	// A third axis again, beside Ready and Grantable, and for the reason
	// this whole field exists: Ready means *configured* -- a project, an
	// account and a secret are all set -- and no configuration pane can
	// see whether the key inside that secret is one the far end still
	// accepts. A checkable capability is one where that question has an
	// answer somebody can go and get; it says nothing about what the
	// answer is.
	//
	// False for a capability holding no standing credential at all
	// (self-debug, self-repair, bootstrap-playbooks: nothing to go stale
	// behind grain's back), and false on a UI not colocated with a
	// daemon that could make the call -- the same
	// nil-means-unavailable contract MissingSecrets already follows for
	// a nil Config.Secrets.
	Checkable bool `json:"checkable,omitempty"`
	// Default reports whether this capability is in
	// model.Config.DefaultCapabilities: whether every task filed on this
	// deployment starts out holding it, rather than only the ones
	// somebody ticks it on.
	//
	// Deployment-wide only, and deliberately not widened to mean "some
	// task somewhere starts with this" now that a repo can default one
	// too (DefaultRepos below). A pane that folded the two layers into
	// one flag would describe a deployment-wide default that only some
	// tasks actually get, which is exactly the reporting failure two
	// layers introduce.
	//
	// It is a separate axis from Grantable, not a stronger form of it.
	// Grantable still means what it always did -- the picker offers a row
	// -- and a defaulted capability needs that row for two reasons: it is
	// what UpdateSettings validates the default set against, and it is
	// what lets a human who does not want it on one task take it off
	// again. A capability that were defaulted without being grantable
	// would be one every task holds and none can drop, which is exactly
	// the deployment-wide, un-detachable grant this deliberately is not.
	Default bool `json:"default"`
	// DefaultRepos is every repo whose own row
	// (model.RepoConfig.DefaultCapabilities) names this capability --
	// the second layer, reported as the repos it actually applies to
	// rather than as a bare boolean, since "which repos" is the only
	// useful form of that answer. Sorted by repo, the order
	// Store.ListRepoConfigs already returns.
	//
	// Empty for a capability no repo names, including on a deployment
	// where every task gets it deployment-wide: the two axes are read
	// together, and Default above is the one that says "everywhere".
	// Reported even for an entry that is also in Default -- a repo may
	// restate one the deployment already gives, and the pane that lists
	// repos should list the repo that said so.
	DefaultRepos []string `json:"defaultRepos,omitempty"`
	// BuildDefault reports whether this build turns the capability on by
	// default (capability.Registration.DefaultOn): a deployment installed
	// fresh on it starts with it in DefaultCapabilities. Read beside
	// Default, it is how Settings tells an operator of a deployment that
	// predates the build -- or one who took it out -- that this build
	// would have it on, and offers to add it, without grain ever adding
	// it to a stored configuration by itself.
	BuildDefault bool `json:"buildDefault,omitempty"`
	// MissingConfig is every deployment setting (this Settings tab's own
	// General fields) this capability still needs -- e.g. "GCP project"
	// for gcp-key/gemini-key. Empty for a capability with no such gate
	// (github-sandbox, self-debug, self-repair and bootstrap-playbooks
	// all register unconditionally).
	MissingConfig []string `json:"missingConfig,omitempty"`
	// PendingConfig is every deployment setting this capability needs
	// that is *stored* -- so MissingConfig has nothing to say about it,
	// and the field on the General tab is filled in -- but is not part of
	// the configuration the process serving this page is actually
	// running with (Config.RunningConfig). It is the gap between what an
	// operator has saved and what this deployment has adopted, reported
	// per capability because that is where it is felt: a provider is
	// registered out of the running configuration (pkg/cli/daemon.go's
	// capabilityProviders), so a project saved but not yet adopted is a
	// capability that cannot be resolved, materialized or checked,
	// however green the row it was saved on looks.
	//
	// Normally empty, and briefly non-empty: a running daemon re-reads
	// the stored row once per reconcile tick and applies exactly these
	// settings itself (liveConfig.refresh), so this usually names a
	// setting saved seconds ago. It stays non-empty for as long as the
	// loop that applies them is not running at all -- a daemon whose
	// reconciler died leaves this UI up, serving a Settings pane whose
	// saved values nothing is reading any more (Config.ReconcilerDown is
	// the deployment-wide half of that same fact) -- which is the
	// deployment this field was written for: it showed **Ready** on a
	// capability whose own "Test credential" answered that this
	// deployment registers no provider for it, two lines apart on one
	// card, because the badge was read off the stored row and the check
	// off the running process.
	//
	// Empty, always, where there is nothing to compare against: a UI with
	// no Config.RunningConfig is not colocated with a daemon whose
	// configuration it could speak for, the same nil-means-unavailable
	// contract MissingSecrets follows for a nil Config.Secrets.
	PendingConfig []string `json:"pendingConfig,omitempty"`
	// MissingSecrets is every CapabilitySpec.Requires entry this
	// deployment's secrets store has no matching secret/key for -- the
	// same check TargetReposMissingCredentials already runs for the git
	// proxy's credential ladder, applied here to a capability's own
	// Requires instead. Always empty when Config.Secrets is nil (a UI
	// not colocated with the store this checks against has nothing
	// actionable to say, the same nil-means-unavailable contract that
	// field's own doc comment gives every other check built on it).
	MissingSecrets []string `json:"missingSecrets,omitempty"`
	// Secrets is every CapabilitySpec.Requires entry -- set or not --
	// resolved into the secret and key a colocated UI would write it to,
	// so the pane that reports a missing secret is also the pane that
	// can fill it in (grain/task-110: secrets belong with whatever uses
	// them, not in a pane of their own where nothing says which name
	// goes with which capability).
	//
	// A superset of MissingSecrets, which stays what it was: the names
	// this deployment is short of, and what Ready is computed from.
	// Empty for the same nil-Config.Secrets reason MissingSecrets is --
	// with no store to write to there is nothing to offer.
	Secrets []CapabilitySecret `json:"secrets,omitempty"`
}

// CapabilitySecret is one credential a capability resolves through
// CapabilityContext.Credentials, addressed the way the secrets endpoints
// take it: PUT/DELETE /api/secrets/{Secret}/{Key}.
//
// Presence only, never a value -- the same write-only contract
// secrets.Store.List gives every other reader in this package.
type CapabilitySecret struct {
	// Name is the credential name as the capability itself resolves it,
	// in either form secrets.Store.Resolve accepts: "github-app/app-id"
	// names a key directly, "gcp-key-minter" names a secret whose sole
	// key it is. It is what MissingSecrets reports and what a hint
	// naming this credential should say.
	Name string `json:"name"`
	// Secret and Key are Name split into the two path elements a write
	// needs. For the "<secret>/<key>" form they are exactly that. For the
	// bare "<secret>" form Key is the key already stored, when the secret
	// holds exactly one -- so setting a value replaces it rather than
	// adding a second key that would leave the bare name resolving to
	// nothing -- and defaultSecretKey otherwise.
	Secret string `json:"secret"`
	Key    string `json:"key"`
	// Set reports whether this deployment resolves the credential today,
	// by exactly the check missingSecretsFor makes: a secret sitting
	// there with the wrong number of keys is not set, because Resolve
	// would refuse it.
	Set bool `json:"set"`
}

// defaultSecretKey is the key a bare "<secret>" credential is written to
// when nothing is stored under it yet -- the same secrets.
// AgentCredentialKey the agent credentials already use, and for the same
// reason: the secret's own name says what the value is, so the key
// inside it need only be a name Resolve's sole-key form can find.
//
// Deliberately not the name a seeding script happened to pick for one of
// these (setup.sh writes the minter key as gcp-key-minter/key.json);
// where such a key already exists it is reused rather than added to.
const defaultSecretKey = secrets.AgentCredentialKey

// capabilityDisplayNames gives each capabilityCatalog entry a short,
// human-facing name for Settings to show -- CapabilitySpec.Label is not
// it: that field is what used to be a GitHub label before v1's label
// taxonomy was retired, deliberately kept out of every wire shape this
// package sends since (TestConfigEndpointReportsActorAndCapabilities'
// own "the GitHub label a capability used to carry is gone from the
// wire shape along with the labels themselves"). gemini-key, self-debug
// and self-repair repeat the same names OfferedCapabilities already
// gives the per-task picker's own listing of the very same capabilities
// -- two different views of one capability, the same duplication
// OfferedCapabilities and each provider's own Spec().Description
// already accept for their descriptions.
// The model proxies are not written down here: there is one per vendor
// grain proxies to, and their names ("Claude proxy", "OpenAI proxy",
// "Gemini proxy") are the vendor's own -- see modelProxyDisplayNames
// below, which folds them in.
var capabilityDisplayNames = withModelProxyDisplayNames(map[string]string{
	"gcp-key":             "GCP key",
	"gemini-key":          "Gemini key",
	"github-sandbox":      "GitHub sandbox",
	"self-debug":          "Self debug",
	"deployment-health":   "Deployment health",
	"grain-log":           "Grain log",
	"self-repair":         "Self repair",
	"self-upgrade":        "Self upgrade",
	"bootstrap-playbooks": "Bootstrap playbooks",
	"grain-ci":            "grain CI",
	"registry-push":       "Registry push",
	"package-publish":     "Package publish",
	"image-signing":       "Image signing",
	"memory":              "Memory",
	"release-notes":       "Release notes",
	"git-push-any-ref":    "Push any ref",
	"console":             "Interactive console",
	"test-quarantine":     "Test quarantine",
	"priority-dispatch":   "Priority dispatch",
	"durable":             "Durable",
	"read-issues":         "Read issues",
	"db-read":             "Grain DB read",
	"db-write":            "Grain DB read/write",
})

// withModelProxyDisplayNames adds one row per vendor's proxy capability
// to names, built from the vendor itself so that a name here cannot
// drift from the capability it labels.
func withModelProxyDisplayNames(names map[string]string) map[string]string {
	for _, vendor := range modelproxy.Vendors() {
		names[modelproxycap.CapabilityName(vendor)] = vendor.Display + " proxy"
	}
	return names
}

// capabilityCatalog is every capability grain ships a provider for,
// regardless of whether this deployment currently has enough
// configuration to register it -- unlike pkg/cli/daemon.go's own
// capabilityProviders, which only builds the ones a real dispatch can
// use, this exists purely to read each provider's own Spec() back out,
// so Settings has one place to learn a capability's id/name/description/
// requirements from rather than repeating them. Every constructor here
// is called with zero-value config on purpose: Spec() reads none of it,
// and gemini-key's own credential name is the same
// gcpkey.DefaultMinterCredential daemon.go always passes regardless of
// whether GCPProject is set, since that constant, not the project, is
// what Requires needs to name.
func capabilityCatalog() []model.CapabilitySpec {
	providers := capabilityProviderCatalog()
	out := make([]model.CapabilitySpec, 0, len(providers))
	for _, p := range providers {
		out = append(out, p.Spec())
	}
	return out
}

// capabilityProviderCatalog is the providers capabilityCatalog above
// reads its specs out of, kept as providers rather than specs alone for
// the one question a spec cannot answer: whether this capability's
// standing credential can be *checked* (model.CredentialChecker), which
// is a property of the provider's own code and not of any deployment's
// configuration -- so it is answered here, from grain's own build, the
// same way the rest of the catalog is.
//
// capabilityDisplayName is capabilityDisplayNames[id], or for a
// capability contributed from outside core the Name it was registered
// with (capability.RegisterProvider).
func capabilityDisplayName(id string) string {
	if name, ok := capabilityDisplayNames[id]; ok {
		return name
	}
	if r, ok := capability.Lookup(id); ok {
		return r.Name
	}
	return ""
}

// capabilityProviderCatalog is every provider grain's own build ships,
// built the way cmd/grain's capabilityProviders builds them for
// dispatch, so their specs can be read back out.
func capabilityProviderCatalog() []model.CapabilityProvider {
	providers := []model.CapabilityProvider{
		gcpkey.NewProvider(gcpkey.Config{}),
		geminikey.New("", model.CredentialRef{Name: gcpkey.DefaultMinterCredential}),
		githubsandbox.NewProvider(githubsandbox.Config{}),
	}
	providers = append(providers, admincaps.Providers()...)
	providers = append(providers,
		gitpushanyref.New(),
		bootstrap.New(),
		releasenotes.New(),
		registrypush.NewWithConfig(registrypush.Config{}),
		packagepublish.New(packagepublish.Config{}),
		imagesigning.New(imagesigning.Config{}),
		consolecap.New(),
		quarantine.New(),
		readissues.New(),
		prioritydispatch.New(),
		durable.New(),
	)
	// One per vendor grain proxies to, built with no endpoint at all --
	// this catalog exists to read Spec() back out, and a spec is the
	// same whether or not this process is serving that proxy.
	for _, p := range modelproxycap.Providers(nil) {
		providers = append(providers, p)
	}
	return providers
}

// capabilityShipped reports whether name is a capability this build
// ships a provider for at all -- the catalog above, regardless of
// whether this deployment configures it or the picker offers it.
func capabilityShipped(name string) bool {
	if _, ok := model.GCPKeyName(name); ok {
		return true
	}
	for _, spec := range capabilityCatalog() {
		if spec.Name == name {
			return true
		}
	}
	return false
}

// ciOnlyCapability reports whether name is a capability this build ships
// for CI jobs and for nothing else (model.CapabilitySpec.CIOnly): read
// off the catalog, so the answer is the provider's own and not a second
// list of ids maintained here. False for a name grain ships nothing for,
// which is the ordinary unknown id every picker already refuses.
func ciOnlyCapability(name string) bool {
	if name == model.ReleaseGateReadCapability {
		return true
	}
	if _, ok := model.RepoReadTarget(name); ok {
		return true
	}
	if _, ok := model.RepoWriteTarget(name); ok {
		return true
	}
	for _, spec := range capabilityCatalog() {
		if spec.Name == name {
			return spec.CIOnly
		}
	}
	return false
}

// capabilityRefusal is what every picker says about an id it will not
// accept -- the one sentence for a CI-only capability
// (model.CIOnlyRefusal, the same words a refused grant would have been
// failed with), the one for grain-ci (grainci.Refusal: a repo's CI Mode
// is what grants it), and the plain "unknown capability" for everything
// else.
//
// One function because there are five of these validations
// (UpdateSettings, SetRepoDefaultCapabilities, grantsFor, SetCapability,
// console sessions) and a CI-only id is rejected by all of them for the
// same reason: it has no OfferedCapabilities row. Without this they all
// answered "unknown capability package-publish" about a capability this
// build ships, registers a provider for, and publishes releases with --
// true of the picker and useless to the person reading it.
func capabilityRefusal(id string) string {
	if id == grainci.CapabilityName {
		return grainci.Refusal()
	}
	if ciOnlyCapability(id) {
		return model.CIOnlyRefusal(id)
	}
	if model.IsCapabilityRef(id) {
		if err := model.CheckCapabilityGrantSyntax(id); err != nil {
			return err.Error()
		}
	}
	if g, ok, err := model.ParseAPIGrant(id); ok && err == nil {
		if err := g.OutsideRepoLimit(); err != nil {
			return err.Error()
		}
	}
	return "unknown capability " + id
}

// capabilityCheckable reports whether grain ships a live credential
// check for this capability -- whether its provider implements
// model.CredentialChecker. Read off capabilityProviderCatalog above and
// not off the deployment: a capability either has a check written for it
// in this build or it does not, and whether *this* deployment can run
// one is the separate question Config.CapabilityChecks answers.
func capabilityCheckable(name string) bool {
	if _, ok := model.GCPKeyName(name); ok {
		return true
	}
	for _, p := range capabilityProviderCatalog() {
		if p.Spec().Name != name {
			continue
		}
		_, ok := p.(model.CredentialChecker)
		return ok
	}
	return false
}

// checkable is capabilityCheckable widened by the one listing that is
// not this build's catalog: a named GitHub token
// (githubTokenStatuses below). Every one of those has a check --
// githubtoken.Provider implements model.CredentialChecker for all of
// them, which TestEveryNamedGitHubTokenRowCanBeChecked holds it to --
// so what decides it here is whether this deployment offers that token
// at all, which is exactly the id's own picker row and the thing
// pkg/cli/daemon.go registered a provider beside.
//
// A method rather than a second package-level function because that
// second half can only be answered from a Client: which tokens exist is
// an operator's files, read at startup into Config.Capabilities, not a
// property of this build.
func (c *Client) checkable(id string) bool {
	if _, ok := model.GitCredentialName(id); ok {
		_, offered := c.capabilityByID(id)
		return offered
	}
	return capabilityCheckable(id)
}

// missingConfigFor is capabilityProviders' own gates
// (pkg/cli/daemon.go), read back as the human-facing labels Settings'
// Capabilities tab already uses for the same two fields -- gcp-key needs
// both a project and the service account it mints keys for; gemini-key
// needs only the project, sharing it with whatever standing credential
// mints under it.
func missingConfigFor(name string, cfg model.Config) []string {
	switch name {
	case "gcp-key":
		var missing []string
		if cfg.GCPProject == "" {
			missing = append(missing, "GCP project")
		}
		if cfg.GCPServiceAccountEmail == "" {
			missing = append(missing, "GCP service account email")
		}
		return missing
	case "gemini-key":
		if cfg.GCPProject == "" {
			return []string{"GCP project"}
		}
	}
	return nil
}

// pendingConfigFor is the settings missingConfigFor names against the
// configuration this deployment is *running* (liveConfig.current, via
// Config.RunningConfig) and does not name against the row it has stored
// -- every setting an operator has saved for this capability that the
// running process has not adopted, and so is not registering a provider
// for.
//
// The subtraction is what makes the two lists mean different things: a
// setting nobody has filled in yet is MissingConfig, a setting filled in
// and not yet in effect is this, and no setting is ever both. A
// deployment that has adopted everything stored -- the ordinary one,
// within a reconcile tick of any change -- gets nothing from here.
func pendingConfigFor(name string, stored, running model.Config) []string {
	missing := missingConfigFor(name, stored)
	var pending []string
	for _, setting := range missingConfigFor(name, running) {
		if !slices.Contains(missing, setting) {
			pending = append(pending, setting)
		}
	}
	return pending
}

// missingSecretsFor is requires filtered down to the names list has no
// covering secret/key for, following the exact two forms
// secrets.Store.Resolve itself accepts: "<secret>/<key>" names one key
// directly, and a bare "<secret>" resolves only when that secret holds
// exactly one key -- so a secret sitting there with the wrong number of
// keys is reported missing here the same way Resolve would refuse it.
func missingSecretsFor(requires []string, list []secrets.SecretInfo) []string {
	byName := make(map[string][]string, len(list))
	for _, s := range list {
		byName[s.Name] = s.Keys
	}
	var missing []string
	for _, name := range requires {
		secret, key, explicit := strings.Cut(name, "/")
		keys, ok := byName[secret]
		if !ok {
			missing = append(missing, name)
			continue
		}
		if explicit {
			if !slices.Contains(keys, key) {
				missing = append(missing, name)
			}
			continue
		}
		if len(keys) != 1 {
			missing = append(missing, name)
		}
	}
	return missing
}

// capabilitySecretsFor is requires paired with where each entry would be
// written and whether it is set -- CapabilityStatus.Secrets, built from
// the same listing missingSecretsFor checks against so the two cannot
// disagree about what "set" means.
func capabilitySecretsFor(requires []string, list []secrets.SecretInfo) []CapabilitySecret {
	byName := make(map[string][]string, len(list))
	for _, s := range list {
		byName[s.Name] = s.Keys
	}
	out := make([]CapabilitySecret, 0, len(requires))
	for _, name := range requires {
		secret, key, explicit := strings.Cut(name, "/")
		if !explicit {
			key = defaultSecretKey
			if keys := byName[secret]; len(keys) == 1 {
				key = keys[0]
			}
		}
		out = append(out, CapabilitySecret{
			Name:   name,
			Secret: secret,
			Key:    key,
			Set:    len(missingSecretsFor([]string{name}, list)) == 0,
		})
	}
	return out
}

// capabilitiesWithReadiness is Config.Capabilities -- the per-task
// picker's own listing -- with each row told whether this deployment can
// actually honour it, from the same capabilityStatuses the Settings
// pane's Capabilities tab is built from.
//
// The two listings were entirely independent before this, which is the
// whole problem: a deployment could show gemini-key as "Not ready --
// Needs: GCP project" on one pane while the picker on another offered it
// as an ordinary tickable row, and the only place the disagreement
// surfaced was a task failing to dispatch. Joining them here rather than
// re-deriving readiness keeps one answer to "would this work", so the
// two panes cannot drift into saying different things again.
//
// A copy is returned, never Config.Capabilities itself: that slice is
// shared by every request this Client serves, and readiness is a
// per-deployment answer written per call.
//
// A picker row with no capabilityStatuses entry keeps a nil Ready --
// unknown, not broken. That can only happen if OfferedCapabilities and
// capabilityCatalog drift, which
// TestOfferedCapabilitiesCoversEveryShippedCapability exists to stop,
// and inventing "not ready" for one would be a worse answer than saying
// nothing.
func (c *Client) capabilitiesWithReadiness(cfg *model.Config, repoConfigs []model.RepoConfig) []Capability {
	var deployment model.Config
	if cfg != nil {
		deployment = *cfg
	}
	byID := make(map[string]CapabilityStatus, len(c.Config.Capabilities))
	for _, s := range c.capabilityStatuses(deployment, repoConfigs) {
		byID[s.ID] = s
	}
	out := make([]Capability, 0, len(c.Config.Capabilities))
	for _, row := range c.Config.Capabilities {
		if status, ok := byID[row.ID]; ok {
			ready := status.Ready
			row.Ready = &ready
			row.Needs = append(append([]string{}, status.MissingConfig...), status.MissingSecrets...)
			// Named as what it is rather than as something still to
			// type in: the picker's own sentence is "needs X", and a
			// setting that is already saved needs applying, not
			// setting (CapabilityStatus.PendingConfig).
			for _, pending := range status.PendingConfig {
				row.Needs = append(row.Needs, pending+" (saved, not yet in effect on the running daemon)")
			}
			if len(row.Needs) == 0 {
				row.Needs = nil
			}
		}
		out = append(out, row)
	}
	return out
}

// ciCapabilitiesWithReadiness returns every capability supported for CI shells
// (including CI-only capabilities like package-publish and registry-push),
// annotated with readiness for this deployment.
func (c *Client) ciCapabilitiesWithReadiness(cfg *model.Config, repoConfigs []model.RepoConfig) []Capability {
	var deployment model.Config
	if cfg != nil {
		deployment = *cfg
	}
	statuses := c.capabilityStatuses(deployment, repoConfigs)
	byID := make(map[string]CapabilityStatus, len(statuses))
	for _, s := range statuses {
		byID[s.ID] = s
	}

	ciIDs := c.ciCapabilityIDs()
	out := make([]Capability, 0, len(ciIDs))
	for _, id := range ciIDs {
		if status, ok := byID[id]; ok {
			ready := status.Ready
			row := Capability{
				ID:          status.ID,
				Name:        status.Name,
				Description: status.Description,
				Ready:       &ready,
				Needs:       append(append([]string{}, status.MissingConfig...), status.MissingSecrets...),
			}
			for _, pending := range status.PendingConfig {
				row.Needs = append(row.Needs, pending+" (saved, not yet in effect on the running daemon)")
			}
			if len(row.Needs) == 0 {
				row.Needs = nil
			}
			out = append(out, row)
		}
	}
	return out
}

// capabilityStatuses builds every CapabilityStatus for cfg -- the
// deployment's current store-backed settings -- repoConfigs, every repo
// that adds defaults of its own (Store.ListRepoConfigs), c.Config.Secrets,
// this Client's own secrets store, if any, and c.Config.Capabilities, the
// picker listing that decides Grantable.
func (c *Client) capabilityStatuses(cfg model.Config, repoConfigs []model.RepoConfig) []CapabilityStatus {
	// Inverted once, here, rather than rescanned per capability: the
	// listing is one row per capability and the answer each row needs is
	// "which repos name me".
	reposByCapability := make(map[string][]string)
	for _, rc := range repoConfigs {
		repo := rc.Repo.String()
		for _, id := range rc.DefaultCapabilities {
			if slices.Contains(reposByCapability[id], repo) {
				continue
			}
			reposByCapability[id] = append(reposByCapability[id], repo)
		}
	}
	var secretList []secrets.SecretInfo
	if c.Config.Secrets != nil {
		// Best-effort: a listing error here would only ever be a store
		// this Client could not open, which GetSettings/UpdateSettings
		// have no other reason to fail on -- reported as "nothing known
		// to be missing" rather than surfaced as an error this endpoint
		// never returned before.
		if list, err := c.Config.Secrets.List(); err == nil {
			secretList = list
		}
	}

	// What this process is actually running with, as against the row cfg
	// was read from -- the two are the same within a reconcile tick of
	// any change, and stay apart for as long as nothing is applying
	// changes at all (CapabilityStatus.PendingConfig). A UI with no
	// RunningConfig has no second reading to compare, and reports the
	// stored one as the whole answer.
	running, haveRunning := model.Config{}, false
	if c.Config.RunningConfig != nil {
		running, haveRunning = c.Config.RunningConfig(), true
	}

	var catalog []model.CapabilitySpec
	for _, p := range capabilityProviderCatalog() {
		catalog = append(catalog, p.Spec())
	}
	buildDefaults := capability.DefaultOn()
	out := make([]CapabilityStatus, 0, len(catalog))
	for _, spec := range catalog {
		_, grantable := c.capabilityByID(spec.Name)
		status := CapabilityStatus{
			ID:            spec.Name,
			Name:          capabilityDisplayName(spec.Name),
			Description:   spec.Description,
			Grantable:     grantable,
			CIOnly:        spec.CIOnly,
			Checkable:     c.Config.CapabilityChecks != nil && c.checkable(spec.Name),
			Default:       slices.Contains(cfg.DefaultCapabilities, spec.Name),
			DefaultRepos:  reposByCapability[spec.Name],
			BuildDefault:  slices.Contains(buildDefaults, spec.Name),
			MissingConfig: missingConfigFor(spec.Name, cfg),
		}
		if haveRunning {
			status.PendingConfig = pendingConfigFor(spec.Name, cfg, running)
		}
		if c.Config.Secrets != nil {
			status.MissingSecrets = missingSecretsFor(spec.Requires, secretList)
			status.Secrets = capabilitySecretsFor(spec.Requires, secretList)
		}
		// Every gap that stops a grant working, not only the ones an
		// operator still has to close: a capability whose provider this
		// process is not registering cannot be resolved for a task now,
		// whatever the row it is configured on says.
		status.Ready = len(status.MissingConfig) == 0 &&
			len(status.PendingConfig) == 0 &&
			len(status.MissingSecrets) == 0
		out = append(out, status)
	}
	return append(out, c.githubTokenStatuses(cfg, reposByCapability)...)
}

// githubTokenStatuses is one row per named GitHub token this deployment
// offers as a capability of its own (grain/task-117) -- read off
// c.Config.Capabilities rather than capabilityCatalog above, because
// unlike every other row here these are not a property of this build at
// all: they are whatever tokens an operator has placed under
// secrets/github, which pkg/cli/daemon.go turned into picker rows and
// providers at startup.
//
// Ready and Grantable are both true by construction, and that stays the
// honest answer to what those two fields ask: a row exists here only
// because a credential file of that name exists (gitproxy.CredentialSet.
// ExtraNames), and it exists in the picker only because the same startup
// pass put it there beside the matching provider. There is no
// deployment setting and no secrets-store entry either could be waiting
// on -- MissingConfig and MissingSecrets ask about the two gates a
// GitHub token has neither of.
//
// What that answer never covered is the way these credentials actually
// fail: a token revoked, expired or rotated at GitHub's end, or one
// whose access to a repo was withdrawn, changes nothing about the file,
// so this row went on saying **Ready** while every push through it was
// refused (grain/task-189). Checkable is where that is answered now, and
// deliberately not folded into Ready -- the same rule grain/task-172
// landed on for every other standing credential: Ready means configured,
// a check is a point-in-time answer somebody asked for, and a badge that
// silently meant both would be a worse answer than two fields that each
// mean one thing.
func (c *Client) githubTokenStatuses(cfg model.Config, reposByCapability map[string][]string) []CapabilityStatus {
	var out []CapabilityStatus
	for _, capability := range c.Config.Capabilities {
		name, ok := model.GitCredentialName(capability.ID)
		if !ok {
			continue
		}
		out = append(out, CapabilityStatus{
			ID:           capability.ID,
			Name:         GitHubTokenDisplayName(name),
			Description:  capability.Description,
			Ready:        true,
			Grantable:    true,
			Checkable:    c.Config.CapabilityChecks != nil && c.checkable(capability.ID),
			Default:      slices.Contains(cfg.DefaultCapabilities, capability.ID),
			DefaultRepos: reposByCapability[capability.ID],
		})
	}
	return out
}
