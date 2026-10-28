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

package daemon

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/ai-on-gke/grain/pkg/agent/codex"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// Resolve configuration and credentials on each request, just as a new
// dispatch does. A failed probe never disables the manual model field.
type CodexModelLister struct {
	Live  *LiveConfig
	Creds *secrets.Store
}

func (l *CodexModelLister) ListModels(ctx context.Context, backendID string) (ui.AgentModelCatalog, error) {
	cfg := l.Live.modelListingConfig(ctx)
	cfg = credentialConfigForBackend(cfg, backendID)
	binary, err := codexProbeTarget(cfg)
	if err != nil {
		return ui.AgentModelCatalog{}, err
	}
	key := ""
	if cfg.codexAuthMode != model.CodexAuthSubscription {
		var err error
		key, err = backendCredential(ctx, cfg, l.Creds, secrets.OpenAIAPIKeySecret, cfg.OpenAIAPIKeyFile)
		if err != nil {
			return ui.AgentModelCatalog{}, fmt.Errorf("reading the OpenAI API key: %w", err)
		}
	}
	if key == "" && cfg.codexAuthMode == model.CodexAuthAPIKey {
		return ui.AgentModelCatalog{}, errors.New("no OpenAI API key is configured for this backend")
	}
	authFile := ""
	if key == "" {
		authFile = configuredCodexAuthFile(cfg)
	}
	models, err := codex.Catalog(ctx, binary, key, authFile)
	if err != nil {
		return ui.AgentModelCatalog{}, err
	}
	catalog := ui.AgentModelCatalog{Efforts: codex.Efforts()}
	for _, m := range models {
		converted := ui.AgentModel{ID: m.ID, Label: m.Label, DefaultEffort: m.DefaultEffort}
		for _, e := range m.Efforts {
			converted.SupportedEfforts = append(converted.SupportedEfforts, e.Effort)
		}
		catalog.Models = append(catalog.Models, converted)
	}
	return catalog, nil
}

// codexProbeTarget is the codex binary the model picker's own app-server
// listing runs: the connection's own (CLIPath), the one this deployment
// was pointed at, or whatever $PATH resolves in this container.
func codexProbeTarget(cfg Config) (codexPath string, err error) {
	if err := checkConnectionCLI(cfg); err != nil {
		return "", err
	}
	if cfg.CodexPath != "" {
		return cfg.CodexPath, nil
	}
	resolved, lookErr := exec.LookPath("codex")
	if lookErr != nil {
		return "", fmt.Errorf("the Codex CLI is not installed; type a model name instead: %w", lookErr)
	}
	return resolved, nil
}
