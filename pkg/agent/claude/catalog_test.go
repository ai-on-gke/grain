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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The picker behind this reads what a dispatch could really be asked
// for, so the listing has to go out as the credential a dispatch would
// authenticate with, and come back as ids paired with Anthropic's own
// display names -- across as many pages as the listing takes.
func TestCatalogListsEveryPageAsTheCredential(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-ant-oat-token" {
			t.Errorf("Authorization = %q, want an OAuth bearer", got)
		}
		if got := r.Header.Get("anthropic-beta"); got == "" {
			t.Errorf("anthropic-beta header missing on an OAuth listing")
		}
		if got := r.Header.Get("anthropic-version"); got == "" {
			t.Errorf("anthropic-version header missing")
		}
		seen = append(seen, r.URL.Query().Get("after_id"))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("after_id") == "" {
			w.Write([]byte(`{"data":[{"id":"claude-opus-5","display_name":"Claude Opus 5"}],"has_more":true,"last_id":"claude-opus-5"}`))
			return
		}
		w.Write([]byte(`{"data":[{"id":"claude-sonnet-5","display_name":"Claude Sonnet 5"}],"has_more":false,"last_id":"claude-sonnet-5"}`))
	}))
	defer srv.Close()

	models, err := CatalogFrom(context.Background(), srv.Client(), srv.URL, "sk-ant-oat-token")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "claude-opus-5" || models[1].ID != "claude-sonnet-5" {
		t.Fatalf("models = %+v, want both pages in the order Anthropic listed them", models)
	}
	if models[0].Label != "Claude Opus 5" {
		t.Errorf("models[0].Label = %q, want Anthropic's own display name", models[0].Label)
	}
	if len(seen) != 2 || seen[0] != "" || seen[1] != "claude-opus-5" {
		t.Errorf("requests = %q, want the second one to follow the first page's last id", seen)
	}
}

// A plain Anthropic API key is a different credential to that API, and
// the field this fills is typed by hand: both spellings have to list.
func TestCatalogPresentsAnAPIKeyAsAKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "sk-ant-api-key" {
			t.Errorf("x-api-key = %q, want the pasted key", got)
		}
		w.Write([]byte(`{"data":[{"id":"claude-haiku-4-5"}],"has_more":false}`))
	}))
	defer srv.Close()

	models, err := CatalogFrom(context.Background(), srv.Client(), srv.URL, "sk-ant-api-key")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "claude-haiku-4-5" || models[0].Label != "" {
		t.Fatalf("models = %+v, want the one model and no invented label", models)
	}
}

// A refusal has to carry Anthropic's own sentence: the field stays
// typable either way, and this is what says whether to paste a new token
// or simply type the model name.
func TestCatalogReportsARefusalInAnthropicsOwnWords(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"type":"authentication_error","message":"OAuth access token is invalid"}}`))
	}))
	defer srv.Close()

	_, err := CatalogFrom(context.Background(), srv.Client(), srv.URL, "sk-ant-oat-stale")
	if err == nil {
		t.Fatal("listing succeeded against a 401")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "OAuth access token is invalid") {
		t.Errorf("error = %q, want the status and Anthropic's own message", err)
	}
}

func TestCatalogRefusesWithNoToken(t *testing.T) {
	if _, err := CatalogFrom(context.Background(), nil, DefaultCatalogBaseURL, "  "); err == nil {
		t.Fatal("listed models with no credential")
	}
}
