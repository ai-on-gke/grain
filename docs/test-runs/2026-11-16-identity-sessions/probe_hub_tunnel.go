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

//go:build ignore

// Command probe_hub_tunnel mounts ui.Server.HubTunnelHandler() on a local
// listener with a counting "registry" IdentityVerifier so curl can probe the
// hub tunnel path against real embedded static-next bundles (Item 3).
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"sync/atomic"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/webauth"
)

type countingRegistryVerifier struct {
	verifies atomic.Int64
	admit    atomic.Bool
}

func (v *countingRegistryVerifier) Name() string      { return string(ui.AuthMethodHub) }
func (v *countingRegistryVerifier) Headers() []string { return webauth.HubAssertionHeaders }

func (v *countingRegistryVerifier) Verify(r *http.Request) (ui.VerifiedIdentity, bool, error) {
	v.verifies.Add(1)
	if v.admit.Load() && r.Header.Get(webauth.RegistryAssertionHeader) == "admit-alice" {
		return ui.VerifiedIdentity{Email: "alice@example.com", Kind: model.PrincipalHuman}, true, nil
	}
	for _, h := range webauth.HubAssertionHeaders {
		if r.Header.Get(h) != "" {
			return ui.VerifiedIdentity{}, false, errors.New("hub assertion refused")
		}
	}
	return ui.VerifiedIdentity{}, false, nil
}

func main() {
	addr := flag.String("addr", "127.0.0.1:4296", "hub tunnel HTTP listener")
	ctlAddr := flag.String("ctl", "127.0.0.1:4297", "control listener")
	flag.Parse()

	rv := &countingRegistryVerifier{}
	ui.RegisterIdentityVerifier(rv)

	srv := ui.NewServer(ui.Config{
		Actor:             ui.DefaultActor("alice@example.com"),
		AllowedPrincipals: []string{"user:alice@example.com"},
	}, nil)

	ctl := http.NewServeMux()
	ctl.HandleFunc("/stats", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"verifies": rv.verifies.Load()})
	})
	ctl.HandleFunc("/reset", func(w http.ResponseWriter, r *http.Request) {
		rv.verifies.Store(0)
		rv.admit.Store(r.URL.Query().Get("admit") == "1")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"reset":true}` + "\n"))
	})
	go func() {
		if err := http.ListenAndServe(*ctlAddr, ctl); err != nil {
			log.Fatalf("ctl: %v", err)
		}
	}()

	if err := http.ListenAndServe(*addr, srv.HubTunnelHandler()); err != nil {
		log.Fatalf("tunnel: %v", err)
	}
}
