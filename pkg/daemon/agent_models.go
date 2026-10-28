package daemon

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// agyModelTimeout bounds one `agy models`. It is a network fetch made
// from an HTTP handler an operator is waiting on, so this is short enough
// that a Settings pane opened against an agy that cannot reach Google
// falls back to its text field rather than hanging on it.
const agyModelTimeout = 30 * time.Second

// agyModelCacheTTL is how long a catalog that answered is reused. What it
// lists changes only when the binary is upgraded or an account's access
// does, and the pane behind it is opened repeatedly while a deployment is
// being configured -- so a few minutes spares an operator a subprocess
// and a fetch per visit while still following an upgrade within one.
// Only successes are cached: a failure is usually something being fixed
// right now (a key just pasted, an agy just installed), and the retry
// that proves it has to be allowed to happen on the next reload.
const agyModelCacheTTL = 5 * time.Minute

// agyModelLister is startUIServer's ui.Config.AgentModels: what Settings'
// Gemini model picker is filled from (grain/task-365). It asks the agy
// binary this deployment actually dispatches through -- resolved exactly
// as buildAntigravityFramework resolves it, from the live configuration
// rather than from whatever was true at startup -- with the same Gemini
// key a run would authenticate as, so what the picker offers is what a
// dispatch could really be asked for.
//
// It fails with the same sentence buildAntigravityFramework fails with
// when there is no binary or no key, because they are the same two
// conditions and an operator reading one on the model field is reading
// what the next dispatch would have told them anyway.
type AgyModelLister struct {
	Live  *LiveConfig
	creds *secrets.Store

	mu              sync.Mutex
	cached          ui.AgentModelCatalog
	cachedAt        time.Time
	cachedBackendID string
	// cachedPath is the binary that answered: a connection pointed at
	// another agy since (its CLIPath) is asked again, not answered with
	// the old binary's list.
	cachedPath string
}

func (l *AgyModelLister) ListModels(ctx context.Context, backendID string) (ui.AgentModelCatalog, error) {
	cfg := l.Live.modelListingConfig(ctx)
	cfg = credentialConfigForBackend(cfg, backendID)
	host, agyPath, cachePath, err := agyProbeTarget(ctx, cfg)
	if err != nil {
		return ui.AgentModelCatalog{}, err
	}
	if cached, ok := l.fresh(backendID, cachePath); ok {
		return cached, nil
	}
	apiKey := ""
	if cfg.AgentAuthMode != model.AntigravityAuthAmbient {
		var err error
		apiKey, err = backendCredential(ctx, cfg, l.creds, secrets.GeminiAPIKeySecret, cfg.GeminiAPIKeyFile)
		if err != nil {
			return ui.AgentModelCatalog{}, fmt.Errorf("reading the Gemini API key: %w", err)
		}
	}
	if apiKey == "" && cfg.AgentAuthMode == model.CodexAuthAPIKey {
		return ui.AgentModelCatalog{}, errors.New("no Gemini API key is configured for this backend")
	}
	// Not an error: agy may have an ambient session of its own to list
	// through (antigravity.Catalog's own contract, matching Run's). If it
	// has neither, the refusal it prints is what comes back below, which
	// says more than a guess made here would.
	ctx, cancel := context.WithTimeout(ctx, agyModelTimeout)
	defer cancel()
	var catalog antigravity.ModelCatalog
	if host != nil {
		catalog, err = antigravity.CatalogOn(ctx, host, agyPath, apiKey)
	} else {
		// Catalog rather than CatalogOn over an agent.LocalHost: the two
		// stage the same files and run the same two subcommands, but this
		// one reads stdout and stderr together, which is what keeps `agy
		// --help`'s effort vocabulary readable whichever stream a release
		// prints it on.
		catalog, err = antigravity.Catalog(ctx, agyPath, apiKey)
	}
	if err != nil {
		return ui.AgentModelCatalog{}, err
	}
	converted := ui.AgentModelCatalog{Efforts: catalog.Efforts}
	for _, m := range catalog.Models {
		converted.Models = append(converted.Models, ui.AgentModel{
			ID: m.ID, Label: m.Label, Effort: m.Effort, Family: m.Family,
		})
	}
	l.mu.Lock()
	l.cached, l.cachedAt, l.cachedBackendID, l.cachedPath = converted, time.Now(), backendID, cachePath
	l.mu.Unlock()
	return converted, nil
}

func (l *AgyModelLister) fresh(backendID, agyPath string) (ui.AgentModelCatalog, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cachedBackendID != backendID || l.cachedPath != agyPath {
		return ui.AgentModelCatalog{}, false
	}
	if l.cachedAt.IsZero() || time.Since(l.cachedAt) > agyModelCacheTTL {
		return ui.AgentModelCatalog{}, false
	}
	return l.cached, true
}

// agyProbeTarget is the agy binary the model picker's own `agy models`
// runs: through grain hostd when agy is set to run on the host, or in
// this container (the connection's own CLIPath, the one this deployment
// was pointed at, or whatever $PATH resolves).
func agyProbeTarget(ctx context.Context, cfg Config) (host agent.Host, agyPath, cachePath string, err error) {
	if r := cfg.ResolveCLIRun(ctx, model.AgentFrameworkAntigravity); r.Via == ViaHostd {
		h, err := cfg.cliHostdHost(r)
		if err != nil {
			return nil, "", "", err
		}
		path := r.HostPath
		if path == "" {
			path = "agy"
		}
		return h, path, "hostd:" + path, nil
	}
	if err := checkConnectionCLI(cfg); err != nil {
		return nil, "", "", err
	}
	if cfg.AgyPath != "" {
		return nil, cfg.AgyPath, cfg.AgyPath, nil
	}
	resolved, lookErr := exec.LookPath("agy")
	if lookErr != nil {
		return nil, "", "", fmt.Errorf("the Antigravity CLI (agy) is not installed: %w -- "+
			"the deployment image carries one (Dockerfile), so there is no model catalog to read here; "+
			"type a model name instead", lookErr)
	}
	return nil, resolved, resolved, nil
}
