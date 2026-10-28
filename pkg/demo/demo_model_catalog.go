package demo

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/claude"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// demoModelCatalog is -model-catalog's ui.Config.ClaudeModels: the
// Anthropic model listing (GET /v1/models, claude.CatalogFrom) read from
// a stub on loopback rather than from Anthropic. It is what lets a demo
// show discovery (ui.Client.AdvertiseDiscoveredModels) at all: without a
// lister a demo has nothing to ask, so a Claude connection saved with no
// models keeps its framework's one default however long it waits.
//
// The token is a fixed placeholder, since a demo holds no real
// credential and the stub is the only thing it is ever sent to.
type demoModelCatalog struct {
	baseURL string
}

const demoModelCatalogToken = "demo-model-catalog-token"

func (c demoModelCatalog) ListModels(ctx context.Context, _ string) (ui.AgentModelCatalog, error) {
	models, err := claude.CatalogFrom(ctx, &http.Client{Timeout: 5 * time.Second}, c.baseURL, demoModelCatalogToken)
	if err != nil {
		return ui.AgentModelCatalog{}, fmt.Errorf("listing models from the -model-catalog stub: %w", err)
	}
	catalog := ui.AgentModelCatalog{}
	for _, m := range models {
		catalog.Models = append(catalog.Models, ui.AgentModel{ID: m.ID, Label: m.Label})
	}
	return catalog, nil
}
