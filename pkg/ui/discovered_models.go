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
	"context"
	"errors"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// ModelDiscoveryInterval is how often RunModelDiscovery asks every
// discovering connection's provider again. The listers it asks cache a
// catalog that answered for a few minutes of their own (agyModelCacheTTL
// and its kin), so this is also how soon a model a provider adds or drops
// reaches the pickers and dispatch.
const ModelDiscoveryInterval = 10 * time.Minute

// discoveredConfirmEvery is how often a discovered list that has not
// changed is written back anyway, to move its RefreshedAt on. A pass
// whose answer matches what is stored writes nothing otherwise: the list
// lives in the settings row, and rewriting it every ten minutes for a
// timestamp would be churn in every export of that row.
const discoveredConfirmEvery = time.Hour

// DefaultModelDiscoveryListTimeout bounds each lister
// AdvertiseDiscoveredModels asks (Client.ModelDiscoveryListTimeout). A
// lister may shell out to a provider's CLI, and one that never answers
// would otherwise hold modelDiscoveryMu -- and with it every
// later pass, each save's kick included -- for good (grain/task-3903).
// Nothing is held in the store while it waits: the config is read before
// listing and read again before the one write after.
const DefaultModelDiscoveryListTimeout = 30 * time.Second

// modelListerFor is the lister that can ask a connection of framework
// what its provider serves -- the same one GET /api/agent-models asks for
// that framework -- or nil for one that cannot be asked (a custom
// command) or a UI that was never wired to a lister.
func (c *Client) modelListerFor(framework string) AgentModelLister {
	switch model.NormalizeAgentFrameworkName(framework) {
	case model.AgentFrameworkAntigravity:
		return c.Config.AgentModels
	case model.AgentFrameworkCodex:
		return c.Config.CodexModels
	case model.AgentFrameworkClaude:
		return c.Config.ClaudeModels
	}
	return nil
}

// discoveredModelIDs is catalog's model IDs, trimmed, deduplicated and
// in the provider's order.
func discoveredModelIDs(catalog AgentModelCatalog) []string {
	var ids []string
	for _, m := range catalog.Models {
		if id := strings.TrimSpace(m.ID); id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// sameModelSet reports whether a and b name the same models, in any
// order.
func sameModelSet(a, b []string) bool {
	as, bs := slices.Clone(a), slices.Clone(b)
	for i := range as {
		as[i] = strings.TrimSpace(as[i])
	}
	slices.Sort(as)
	as = slices.Compact(as)
	slices.Sort(bs)
	bs = slices.Compact(bs)
	return slices.Equal(as, bs)
}

// discoveryResult is what one pass learned about one connection: the
// provider's list, or why it could not be had.
type discoveryResult struct {
	ids []string
	err string
}

// applyDiscovery records r on b and reports whether anything worth
// writing changed. It is also the one migration a stored connection
// gets: one whose Models was saved before discovery became live, and
// matches exactly what its provider reports the first time it is asked
// under this scheme, is the earlier AdvertiseDiscoveredModels' own fill
// (or an operator's "advertise all"), and becomes auto-advertising. A
// list that differs at all is somebody's curation and stays a pin --
// widening it silently is what this must never do; the connection's page
// offers "Advertise all models" for it instead.
func applyDiscovery(b *model.AgentBackend, r discoveryResult, now time.Time) bool {
	if len(r.ids) == 0 {
		msg := r.err
		if msg == "" {
			msg = "the provider listed no models"
		}
		if b.Discovered == nil {
			b.Discovered = &model.DiscoveredModels{Error: msg}
			return true
		}
		if b.Discovered.Error == msg {
			return false
		}
		d := *b.Discovered
		d.Error = msg
		b.Discovered = &d
		return true
	}
	changed := false
	if b.Discovered == nil && len(b.PinnedModels()) > 0 && sameModelSet(b.PinnedModels(), r.ids) {
		b.Models = nil
		changed = true
	}
	if prev := b.Discovered; prev == nil || !slices.Equal(prev.Models, r.ids) || prev.Error != "" ||
		now.Sub(prev.RefreshedAt) >= discoveredConfirmEvery {
		changed = true
	}
	if changed {
		b.Discovered = &model.DiscoveredModels{Models: slices.Clone(r.ids), RefreshedAt: now}
	}
	return changed
}

// AdvertiseDiscoveredModels asks the provider of every connection that
// can be asked (model.AgentBackend.DiscoversModels) which models it
// serves, and keeps the answer as the connection's Discovered list. A
// connection with no pinned Models advertises that list minus its
// ExcludedModels (model.AgentBackend.SupportedModels), so adding a
// provider is enough to make every model its key can reach a choice in
// each Model picker and one dispatch will route to -- and a model the
// provider stops listing stops being either at the next pass. A provider
// that cannot be asked keeps the last list it gave, with the error
// beside it; one never asked successfully advertises no models -- grain
// does not guess a name for it (model.AgentBackend.HasModels). A custom command cannot be asked and is left alone. It
// reports how many connections' records it changed.
func (c *Client) AdvertiseDiscoveredModels(ctx context.Context) (int, error) {
	return c.advertiseDiscovered(ctx, "")
}

// advertiseDiscovered is AdvertiseDiscoveredModels for every discovering
// connection, or -- with only set -- for that one connection alone.
func (c *Client) advertiseDiscovered(ctx context.Context, only string) (int, error) {
	c.modelDiscoveryMu.Lock()
	defer c.modelDiscoveryMu.Unlock()
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil || cfg == nil {
		return 0, err
	}
	timeout := c.ModelDiscoveryListTimeout
	if timeout <= 0 {
		timeout = DefaultModelDiscoveryListTimeout
	}
	results := map[string]discoveryResult{}
	for _, b := range cfg.AgentBackends {
		if !b.DiscoversModels() || (only != "" && b.ID != only) {
			continue
		}
		lister := c.modelListerFor(b.Framework)
		if lister == nil {
			continue
		}
		listCtx, cancel := context.WithTimeout(ctx, timeout)
		catalog, err := lister.ListModels(listCtx, b.ID)
		cancel()
		if err != nil {
			results[b.ID] = discoveryResult{err: err.Error()}
			continue
		}
		results[b.ID] = discoveryResult{ids: discoveredModelIDs(catalog)}
	}
	if len(results) == 0 {
		return 0, nil
	}
	// Read again before writing: listing can take a while, and an edit
	// saved meanwhile -- a list typed in by hand above all -- wins. The
	// read and the write are one transaction (Store.UpdateConfig): this
	// writes the whole settings row, so a read-then-PutConfig here put
	// back whatever the row said before any save that landed between
	// the two -- a repo POST /api/repos had just added to the target
	// list among them, which this pass, run at every daemon start, lost.
	now := time.Now().UTC()
	changed := 0
	err = c.Store.UpdateConfig(ctx, func(cfg *model.Config) error {
		changed = 0
		for i := range cfg.AgentBackends {
			b := &cfg.AgentBackends[i]
			r, ok := results[b.ID]
			if !ok || !b.DiscoversModels() {
				continue
			}
			if applyDiscovery(b, r, now) {
				changed++
			}
		}
		if changed == 0 {
			return errDiscoveryUnchanged
		}
		return nil
	})
	if errors.Is(err, errDiscoveryUnchanged) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return changed, nil
}

// errDiscoveryUnchanged is how AdvertiseDiscoveredModels' mutate leaves
// the settings row unwritten when no connection's record changed.
var errDiscoveryUnchanged = errors.New("no discovered model list changed")

// RefreshDiscoveredModelsResponse is
// POST /api/deployment/agent-backends/{backendId}/discover's body: the
// connection as stored after its provider was asked, whether there was a
// lister to ask at all, and why the list could not be refreshed when it
// could not.
type RefreshDiscoveredModelsResponse struct {
	Backend model.AgentBackend `json:"backend"`
	// Asked is false when this deployment has no lister for the
	// connection's framework (a UI not colocated with the daemon, `grain
	// demo`): nothing was asked and nothing was written.
	Asked bool `json:"asked"`
	// Error is the provider's failure (Discovered.Error) after asking, or
	// why nothing could be asked.
	Error string `json:"error,omitempty"`
}

// RefreshDiscoveredModels asks backendID's provider which models it
// serves now, the same pass RunModelDiscovery makes every
// ModelDiscoveryInterval, for that one connection and without waiting
// for the tick -- so a person who just enabled a model on their provider
// sees it in the pickers at once. Like that pass it writes only the
// connection's Discovered cache, never anything a person configured.
func (c *Client) RefreshDiscoveredModels(ctx context.Context, backendID string) (RefreshDiscoveredModelsResponse, error) {
	backendID = strings.TrimSpace(backendID)
	find := func() (model.AgentBackend, error) {
		cfg, err := c.Store.GetConfig(ctx)
		if err != nil {
			return model.AgentBackend{}, err
		}
		if cfg != nil {
			for _, b := range cfg.AgentBackends {
				if b.ID == backendID {
					return b, nil
				}
			}
		}
		return model.AgentBackend{}, &NotFoundError{ID: backendID, message: "no connection " + backendID}
	}
	b, err := find()
	if err != nil {
		return RefreshDiscoveredModelsResponse{}, err
	}
	if !b.DiscoversModels() {
		return RefreshDiscoveredModelsResponse{}, validationErrorf(
			"connection %q runs framework %q, whose provider cannot be asked which models it serves; list them on the connection instead",
			b.ID, b.Framework)
	}
	if c.modelListerFor(b.Framework) == nil {
		return RefreshDiscoveredModelsResponse{Backend: b,
			Error: "this deployment cannot ask " + model.NormalizeAgentFrameworkName(b.Framework) + " providers which models they serve"}, nil
	}
	if _, err := c.advertiseDiscovered(ctx, b.ID); err != nil {
		return RefreshDiscoveredModelsResponse{}, err
	}
	if b, err = find(); err != nil {
		return RefreshDiscoveredModelsResponse{}, err
	}
	resp := RefreshDiscoveredModelsResponse{Backend: b, Asked: true}
	if b.Discovered != nil {
		resp.Error = b.Discovered.Error
	}
	return resp, nil
}

// handleRefreshDiscoveredModels is
// POST /api/deployment/agent-backends/{backendId}/discover.
func (s *Server) handleRefreshDiscoveredModels(w http.ResponseWriter, r *http.Request) {
	resp, err := s.tasks.RefreshDiscoveredModels(r.Context(), r.PathValue("backendId"))
	writeDeploymentResult(w, resp, err)
}

// carryDiscovered copies each connection's stored Discovered list onto
// next, the backends a settings save is about to write: the list is
// grain's cache of what the provider said, never something a save sets,
// so whatever a request carried for it is ignored. A connection moved to
// another framework starts over, since what it discovered belonged to a
// different provider.
func carryDiscovered(stored, next []model.AgentBackend) {
	byID := make(map[string]model.AgentBackend, len(stored))
	for _, b := range stored {
		byID[b.ID] = b
	}
	for i := range next {
		next[i].Discovered = nil
		prev, ok := byID[next[i].ID]
		if !ok || prev.Discovered == nil ||
			model.NormalizeAgentFrameworkName(prev.Framework) != model.NormalizeAgentFrameworkName(next[i].Framework) {
			continue
		}
		d := *prev.Discovered
		d.Models = slices.Clone(d.Models)
		next[i].Discovered = &d
	}
}

// kickModelDiscovery runs AdvertiseDiscoveredModels in the background
// after a connection or its credential is saved, so a provider just added
// or re-keyed lists its models in seconds rather than at
// RunModelDiscovery's next tick.
// It is best effort: the save it follows has already succeeded.
func (c *Client) kickModelDiscovery() {
	if c.Store == nil {
		return
	}
	go func() {
		if _, err := c.AdvertiseDiscoveredModels(context.Background()); err != nil {
			log.Printf("grain ui: advertising discovered models: %v", err)
		}
	}()
}

// RunModelDiscovery calls AdvertiseDiscoveredModels now and then every
// interval until ctx is done.
func (c *Client) RunModelDiscovery(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if n, err := c.AdvertiseDiscoveredModels(ctx); err != nil {
			log.Printf("grain ui: advertising discovered models: %v", err)
		} else if n > 0 {
			log.Printf("grain ui: refreshed the models %d connection(s) discovered", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
