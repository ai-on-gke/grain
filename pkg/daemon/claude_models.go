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
	"os"

	"github.com/ai-on-gke/grain/pkg/agent/claude"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// claudeModelLister is startUIServer's ui.Config.ClaudeModels: what the
// Claude model field's picker is filled from, the way agyModelLister and
// codexModelLister fill the two beside it.
//
// It asks Anthropic rather than a binary, which is the one way this
// differs from those two: the claude CLI has no model listing to run
// (claude.Catalog says what was looked for and where). The credential is
// resolved per request, exactly as a dispatch for this backend resolves
// it, so the picker offers what a run could really be asked for and a
// token pasted a moment ago is the one used.
//
// A failure here never costs an operator the ability to set a model: the
// field is free text with a listing over it, and the sentence this
// returns is what the pane shows beside the box it left typable.
type ClaudeModelLister struct {
	Live  *LiveConfig
	Creds *secrets.Store
	// list is what asks Anthropic, nil meaning claude.Catalog. The one
	// seam the tests need: unlike the CLI probes beside it, there is no
	// binary here for a test to write.
	List func(ctx context.Context, token string) ([]claude.Model, error)
}

func (l *ClaudeModelLister) ListModels(ctx context.Context, backendID string) (ui.AgentModelCatalog, error) {
	cfg := credentialConfigForBackend(l.Live.modelListingConfig(ctx), backendID)
	if cfg.ClaudeAuthMode == model.ClaudeAuthVertex || cfg.AgentAuthMode == model.ClaudeAuthVertex || os.Getenv("CLAUDE_CODE_USE_VERTEX") == "1" {
		catalog := ui.AgentModelCatalog{}
		for _, m := range claude.VertexModels() {
			catalog.Models = append(catalog.Models, ui.AgentModel{ID: m.ID, Label: m.Label})
		}
		return catalog, nil
	}
	token, err := backendCredential(ctx, cfg, l.Creds, secrets.ClaudeOAuthTokenSecret, cfg.ClaudeOAuthTokenFile)
	if err != nil {
		return ui.AgentModelCatalog{}, fmt.Errorf("reading the Claude Code OAuth token: %w", err)
	}
	if token == "" {
		// The same condition buildClaudeFramework refuses a dispatch on,
		// said where it can still be fixed: this is the pane holding the
		// field that sets it.
		return ui.AgentModelCatalog{}, errors.New("no Claude Code OAuth token is configured for this model: " +
			"set one above and the list fills itself in")
	}
	lister := l.List
	if lister == nil {
		lister = claude.Catalog
	}
	models, err := lister(ctx, token)
	if err != nil {
		return ui.AgentModelCatalog{}, err
	}
	// No Efforts: claude takes no reasoning-effort flag of its own, so
	// the field the other two frameworks show beside the model is not
	// one this framework has (pkg/agent/claude's own argv).
	catalog := ui.AgentModelCatalog{}
	for _, m := range models {
		catalog.Models = append(catalog.Models, ui.AgentModel{ID: m.ID, Label: m.Label})
	}
	return catalog, nil
}
