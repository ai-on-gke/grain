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

	"github.com/ai-on-gke/grain/pkg/capability/localscripts"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

// migrateLocalScripts retires the local-scripts declarations local finds
// onto the host-script capability server (docs/local-scripts.md,
// "Moving to host-script"): it adds their scripts to the deployment's
// host-script server, rewrites every stored local-scripts:<name> grant
// to the host-script grants it became, and sets the same renames as
// runtime expansions, so local-scripts:<name> keeps granting them
// wherever it is still written. It runs at every start, and what it has
// already done it leaves alone, so a declaration the state repository
// gains later is picked up by the next start, and the scripts an earlier
// start converted are converted again from the declarations as they are
// now, so one removed or edited in the declaration updates on host-script.
// A declaration that cannot move -- such as one that cannot be read or
// whose target name collides with a hand-configured script -- is skipped
// and the reason is logged.
func migrateLocalScripts(ctx context.Context, store *model.Store, local localscripts.Config) (restore func(), err error) {
	// No early return when local finds no declaration: scripts an
	// earlier start converted are still pruned (MigrateToHostScript).
	var renames map[string][]string
	err = store.UpdateConfig(ctx, func(c *model.Config) error {
		before, err := model.EncodeMCPServers(c.MCPServers)
		if err != nil {
			return err
		}
		if c.MCPServers == nil {
			c.MCPServers = map[string]protocol.ServerConfig{}
		}
		var skipped []error
		renames, skipped, err = localscripts.MigrateToHostScript(local, c.MCPServers)
		for _, why := range skipped {
			log.Printf("daemon: local-scripts: not moved to host-script: %v", why)
		}
		if err != nil {
			return err
		}
		if after, err := model.EncodeMCPServers(c.MCPServers); err == nil && after == before {
			// Every start after the first: leave the row as it is.
			return errMCPServersUnchanged
		}
		return nil
	})
	if errors.Is(err, errMCPServersUnchanged) {
		err = nil
	}
	if err != nil {
		return func() {}, err
	}
	if err := store.RewriteRetiredGrants(ctx, renames); err != nil {
		return func() {}, err
	}
	for from, tos := range renames {
		log.Printf("daemon: local-scripts: %s is now %d host-script grant(s): %v", from, len(tos), tos)
	}
	return model.SetRuntimeGrantExpansions(renames), nil
}

// errMCPServersUnchanged aborts migrateLocalScripts' config write when it
// would write back what is already stored.
var errMCPServersUnchanged = errors.New("mcp_servers unchanged")
