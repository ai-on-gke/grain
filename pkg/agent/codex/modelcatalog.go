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

package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// modelCatalogFileName is the model catalog grain writes into a run's
// CODEX_HOME and names in config.toml's model_catalog_json: the catalog
// the installed codex would have used, with apply_patch taken out of it.
//
// It exists because codex-cli 0.166.0 registers apply_patch whenever the
// model's catalog entry carries an apply_patch_tool_type -- no feature
// flag, no environment check (core/src/tools/spec_plan.rs) -- so a model
// whose entry names one is offered a native patch tool even in a home
// whose environments.toml withholds every environment. The catalog entry
// is the only thing codex consults, and model_catalog_json is the only
// configuration that replaces it. See docs/codex-isolation.md.
const modelCatalogFileName = "model-catalog.json"

// modelCatalogTimeout bounds the `codex debug models` a run starts
// before its turn: a local read of the bundled catalog, plus one /models
// refresh when the login is one codex refreshes for.
const modelCatalogTimeout = 30 * time.Second

// writeModelCatalog dumps the catalog the installed codex resolves in
// home -- the bundled one, refreshed against the provider the way a run
// would refresh it -- strips apply_patch from every model in it, writes
// it beside config.toml, and returns config with model_catalog_json
// pointing at it, rewritten in place in home.
//
// home must already hold this run's config.toml (without the key) and
// its link to a login, so that the dump sees the same provider and
// account the run will. A Framework with no dumpModels (one a unit test
// built around a fake runner, which has no codex to ask) is returned
// config unchanged; every Framework New builds has one.
func (f *Framework) writeModelCatalog(ctx context.Context, home string, config []byte) ([]byte, error) {
	if f.dumpModels == nil {
		return config, nil
	}
	env := []string{"CODEX_HOME=" + home, "CODEX_EXEC_SERVER_URL=none"}
	if f.apiKey != nil {
		apiKey, err := f.apiKey(ctx)
		if err != nil {
			return nil, fmt.Errorf("codex: reading the API key: %w", err)
		}
		if apiKey != "" {
			env = append(env, "OPENAI_API_KEY="+apiKey)
		}
	}
	dumpCtx, cancel := context.WithTimeout(ctx, modelCatalogTimeout)
	defer cancel()
	out, err := f.dumpModels(dumpCtx, home, env)
	if err != nil {
		return nil, fmt.Errorf("codex: reading the model catalog: %w", err)
	}
	catalog, err := stripApplyPatch([]byte(out))
	if err != nil {
		return nil, err
	}
	path := filepath.Join(home, modelCatalogFileName)
	if err := f.host.WriteFile(ctx, path, catalog, 0o600); err != nil {
		return nil, fmt.Errorf("codex: writing the model catalog: %w", err)
	}
	quoted, err := tomlString(path)
	if err != nil {
		return nil, err
	}
	// A top-level key, so ahead of the first table; the header comment
	// configTOML opens with stays first.
	header, rest, _ := bytes.Cut(config, []byte("\n"))
	updated := append(append(append([]byte{}, header...), "\nmodel_catalog_json = "+quoted+"\n"...), rest...)
	if err := f.host.WriteFile(ctx, filepath.Join(home, configFileName), updated, 0o600); err != nil {
		return nil, fmt.Errorf("codex: writing codex config: %w", err)
	}
	return updated, nil
}

// stripApplyPatch sets apply_patch_tool_type to null on every model in a
// `codex debug models` dump and leaves every other byte of each entry as
// codex wrote it: grain does not otherwise have opinions about a model's
// metadata, and re-encoding through a struct of its own would drop any
// field this version of grain does not know about.
func stripApplyPatch(dump []byte) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(dump, &top); err != nil {
		return nil, fmt.Errorf("codex: parsing the model catalog: %w", err)
	}
	var models []map[string]json.RawMessage
	if err := json.Unmarshal(top["models"], &models); err != nil {
		return nil, fmt.Errorf("codex: parsing the model catalog's models: %w", err)
	}
	if len(models) == 0 {
		// codex refuses an empty model_catalog_json anyway; say so here,
		// where the reason is known.
		return nil, errors.New("codex: the model catalog lists no models")
	}
	for _, model := range models {
		model["apply_patch_tool_type"] = json.RawMessage("null")
	}
	encoded, err := json.Marshal(models)
	if err != nil {
		return nil, err
	}
	top["models"] = encoded
	return json.Marshal(top)
}

// hostModelDump is dumpModels for the codex binary at codexPath on host:
// `codex debug models`, which prints the catalog as codex resolved it in
// the ModelsResponse shape model_catalog_json reads back.
func hostModelDump(host agent.Host, codexPath string) func(context.Context, string, []string) (string, error) {
	return func(ctx context.Context, home string, env []string) (string, error) {
		return host.Run(ctx, agent.Command{Path: codexPath, Args: []string{"debug", "models"}, Env: env, Dir: home}, nil)
	}
}
