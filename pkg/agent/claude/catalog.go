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

package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/credcheck"
)

// Model is one model this deployment's Claude credential can ask for, as
// the Settings model picker offers it: the id `claude --model` takes,
// and Anthropic's own display name for it.
type Model struct {
	// ID is what goes in the Claude model setting and what the CLI is
	// passed as --model ("claude-sonnet-5").
	ID string
	// Label is Anthropic's display name ("Claude Sonnet 5"), or empty
	// for a listing that gave none.
	Label string
}

// DefaultCatalogBaseURL is the API a listing is read from: the host the
// claude CLI itself authenticates against, and the same one
// credcheck tests this deployment's Claude credential against.
const DefaultCatalogBaseURL = credcheck.DefaultAnthropicBaseURL

// catalogTimeout bounds one listing. An operator is waiting on an HTTP
// handler for it and the model field falls back to free text when it
// does not answer, so an unreachable Anthropic reads as "could not ask"
// quickly rather than hanging the pane.
const catalogTimeout = 20 * time.Second

// catalogPageSize is what one request asks for. Anthropic's listing
// pages at 20 by default and accepts up to 1000, which is more than the
// number of models any account has ever been shown -- so in practice
// this is the whole catalog in one round trip, and the paging below is
// what keeps that a fact about today rather than an assumption.
const catalogPageSize = 1000

// catalogMaxPages bounds the follow-up requests a has_more chain can
// cost. A listing that never says it is done is a far end behaving
// unexpectedly, and a Settings pane is not the place to discover how
// long it would go on for.
const catalogMaxPages = 5

// maxCatalogBytes caps what is read back from one page, for the reason
// credcheck's own limit exists: a captive portal or a proxy answering
// instead of Anthropic should not be read into a handler in full.
const maxCatalogBytes = 1 << 20

// Catalog lists the models a Claude credential can be dispatched with.
//
// Deliberately an API call rather than a subprocess, which is where this
// differs from antigravity.Catalog and codex.Catalog beside it: the
// claude CLI has no listing of its own to run. Its --model flag
// documents aliases and full names and enumerates neither
// (docs/claude-surface.md holds the captured --help), so the vendor's
// own listing -- the same call credcheck makes to test this credential,
// which creates nothing and spends no tokens -- is the only place the
// names exist. It follows that a deployment whose image carries no
// claude binary still gets a filled picker, and that an expired token
// empties the picker and fails the credential check with one sentence
// about one secret.
//
// The listing is a convenience over a setting that has always been free
// text, and never the only way to set one: every caller here falls back
// to the typed name, so an alias, a dated model name, or anything
// released since this deployment last talked to Anthropic still saves.
func Catalog(ctx context.Context, token string) ([]Model, error) {
	return CatalogFrom(ctx, nil, DefaultCatalogBaseURL, token)
}

// CatalogFrom is Catalog against a named base URL, over a named client.
// Exported for tests, which point it at an httptest server; a deployment
// calls Catalog, since listing models against a host that did not issue
// the credential answers a different question than the one asked.
func CatalogFrom(ctx context.Context, client *http.Client, baseURL, token string) ([]Model, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("no Claude Code OAuth token is configured")
	}
	if client == nil {
		client = &http.Client{Timeout: catalogTimeout}
	}
	ctx, cancel := context.WithTimeout(ctx, catalogTimeout)
	defer cancel()
	var models []Model
	after := ""
	for page := 0; page < catalogMaxPages; page++ {
		listing, err := catalogPage(ctx, client, baseURL, token, after)
		if err != nil {
			return nil, err
		}
		for _, m := range listing.Data {
			if m.ID == "" {
				continue
			}
			models = append(models, Model{ID: m.ID, Label: m.DisplayName})
		}
		if !listing.HasMore || listing.LastID == "" {
			return models, nil
		}
		after = listing.LastID
	}
	return models, nil
}

// catalogListing is one page of GET /v1/models: the models on it, and
// whether another page follows the last id on this one.
type catalogListing struct {
	Data []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"data"`
	HasMore bool   `json:"has_more"`
	LastID  string `json:"last_id"`
}

func catalogPage(ctx context.Context, client *http.Client, baseURL, token, after string) (catalogListing, error) {
	endpoint, err := url.Parse(strings.TrimSuffix(baseURL, "/") + "/v1/models")
	if err != nil {
		return catalogListing{}, fmt.Errorf("building the Anthropic model listing URL: %w", err)
	}
	query := endpoint.Query()
	query.Set("limit", strconv.Itoa(catalogPageSize))
	if after != "" {
		query.Set("after_id", after)
	}
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return catalogListing{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("anthropic-version", credcheck.AnthropicVersion)
	// The one place this deployment decides how a Claude credential is
	// presented, shared with the pane that tests it: an OAuth token as a
	// bearer with its beta header, an API key as x-api-key. A picker
	// that chose differently from the check beside it would be the two
	// halves disagreeing about one secret.
	credcheck.AuthorizeAnthropic(req, token)
	resp, err := client.Do(req)
	if err != nil {
		// Not a verdict on the credential, and says so: a deployment
		// whose egress blocks Anthropic would otherwise read an empty
		// picker as a bad token.
		return catalogListing{}, fmt.Errorf("could not reach Anthropic at %s to list models: %w", endpoint.Host, err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes))
	if resp.StatusCode/100 != 2 {
		return catalogListing{}, fmt.Errorf("Anthropic refused to list models (HTTP %d%s) -- "+
			"test the Claude Code OAuth token beside this field, or type a model name",
			resp.StatusCode, credcheck.APIMessage(body))
	}
	if readErr != nil {
		return catalogListing{}, fmt.Errorf("Anthropic listed models but the answer could not be read: %w", readErr)
	}
	var listing catalogListing
	if err := json.Unmarshal(body, &listing); err != nil {
		return catalogListing{}, fmt.Errorf("Anthropic answered the model listing with something this build cannot read: %w", err)
	}
	return listing, nil
}

// VertexModels returns standard Claude models available on Google Cloud Vertex AI.
func VertexModels() []Model {
	return []Model{
		{ID: "claude-sonnet-5", Label: "Claude Sonnet 5"},
		{ID: "claude-opus-4-8", Label: "Claude Opus 4.8"},
		{ID: "claude-haiku-4-5", Label: "Claude Haiku 4.5"},
		{ID: "claude-sonnet-4-6", Label: "Claude Sonnet 4.6"},
		{ID: "claude-3-5-sonnet-v2@20241022", Label: "Claude 3.5 Sonnet v2"},
		{ID: "claude-3-5-haiku@20241022", Label: "Claude 3.5 Haiku"},
		{ID: "claude-3-opus@20240229", Label: "Claude 3 Opus"},
		{ID: "claude-3-sonnet@20240229", Label: "Claude 3 Sonnet"},
		{ID: "claude-3-haiku@20240307", Label: "Claude 3 Haiku"},
	}
}
