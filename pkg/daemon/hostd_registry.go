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
	"log"

	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
)

// newHostdHub is the Remote every grain hostd dials in to: cfg's
// HostdRemote when the deprecated shared token configured one (so that
// hostd keeps its name and stays the default), or a new one, with the
// hostd registry in store as the way any other token is named.
func newHostdHub(cfg Config, store *model.Store) *hostd.Remote {
	hub := cfg.HostdRemote
	if hub == nil {
		hub = &hostd.Remote{Logf: log.Printf}
	}
	if store != nil {
		hub.Resolve = registryResolver(store)
	}
	return hub
}

// registryResolver names a hostd token from the registry: the live hostd
// it belongs to, and that hostd's owner.
func registryResolver(store *model.Store) func(ctx context.Context, token string) (string, string, bool, error) {
	return func(ctx context.Context, token string) (string, string, bool, error) {
		h, err := store.HostdForToken(ctx, token)
		if err != nil || h == nil {
			return "", "", false, err
		}
		if h.Name == model.LocalHostd {
			// local is the socket's; nothing dials in as it.
			return "", "", false, nil
		}
		return h.Name, h.Owner, true, nil
	}
}
