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
	"log"
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

// literalSecrets is what migrateLiteralCredentials needs of the secrets
// store (*secrets.Store).
type literalSecrets interface {
	Has(secret, key string) (bool, error)
	Resolve(ctx context.Context, name string) (string, error)
	Set(secret, key string, value []byte) error
}

// migrateLiteralCredentials moves every credential typed into a stored
// capability server's env or headers (protocol.ServerConfig.LiteralCredentials,
// the credential-exposure audit's CE-2) into the secrets store, as
// capability-server-<id>/<field> (protocol.LiteralSecretName), and
// rewrites the entry to reference it as $secret:<name>, so the server
// resolves exactly what it did before and grain_config.mcp_servers -- and
// with it the state repository's next export -- holds no such value.
//
// Saves have refused a new one since task 3983 and `grain state check`
// refuses one a grain/config pull request adds, so this is in effect a
// one-time move of what was stored before them. It runs at every start
// and is a no-op once nothing is left, so an entry it could not move
// (the store unreadable, its name already holding a different value) is
// tried again at the next. What it moves is logged by where and by the
// name it now has, never by value; one it cannot move at all -- in args
// or serverUrl, where grain resolves no reference -- is logged for the
// operator to edit by hand.
//
// It is deliberately not ServerConfig.Validate's job: that runs on load,
// and refusing these there would detach live servers on upgrade.
//
// The value stays in the state repository's history whatever this does,
// so every credential it moves has to be rotated
// (docs/capabilities.md, "Typed-in credentials").
func migrateLiteralCredentials(ctx context.Context, store *model.Store, secretStore literalSecrets) error {
	var moved []string
	err := store.UpdateConfig(ctx, func(c *model.Config) error {
		moved = moved[:0]
		ids := make([]string, 0, len(c.MCPServers))
		for id := range c.MCPServers {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		changed := false
		for _, id := range ids {
			out, moves, stuck := c.MCPServers[id].MoveLiteralCredentials(id)
			for _, where := range stuck {
				log.Printf("daemon: capability server %s: %s holds a typed-in credential grain cannot move into the secrets store "+
					"(grain resolves no $secret: reference there): edit the entry by hand, then rotate the credential", id, where)
			}
			if len(moves) == 0 || !storeLiteralMoves(ctx, secretStore, id, moves) {
				continue
			}
			c.MCPServers[id] = out
			changed = true
			for _, m := range moves {
				moved = append(moved, "capability server "+id+": "+m.Where+" is now "+protocol.SecretRef(m.Secret))
			}
		}
		if !changed {
			return errMCPServersUnchanged
		}
		return nil
	})
	if errors.Is(err, errMCPServersUnchanged) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, line := range moved {
		log.Printf("daemon: moved a typed-in credential into the secrets store: %s -- "+
			"the old value is still in the state repository's history, so rotate it", line)
	}
	return nil
}

// storeLiteralMoves stores each move's value under its name, and says
// whether every one of them is now there to resolve. A name that already
// holds the same value -- from an earlier start that stored it but did
// not get as far as rewriting the entry -- counts; one holding a
// different value is never overwritten, and leaves the entry as it is.
func storeLiteralMoves(ctx context.Context, secretStore literalSecrets, id string, moves []protocol.LiteralMove) bool {
	for _, m := range moves {
		secret, key, _ := strings.Cut(m.Secret, "/")
		has, err := secretStore.Has(secret, key)
		if err != nil {
			log.Printf("daemon: capability server %s: %s not moved into the secrets store: %v", id, m.Where, err)
			return false
		}
		if has {
			held, err := secretStore.Resolve(ctx, m.Secret)
			if err == nil && held == m.Value {
				continue
			}
			log.Printf("daemon: capability server %s: %s not moved into the secrets store: %s already holds a different value",
				id, m.Where, m.Secret)
			return false
		}
		if err := secretStore.Set(secret, key, []byte(m.Value)); err != nil {
			log.Printf("daemon: capability server %s: %s not moved into the secrets store: %v", id, m.Where, err)
			return false
		}
	}
	return true
}
