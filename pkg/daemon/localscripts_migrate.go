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
// gains later is picked up by the next start. A declaration that cannot
// move -- one with an approve: true script -- stays a local-scripts
// declaration, served as before, and the reason is logged.
func migrateLocalScripts(ctx context.Context, store *model.Store, local localscripts.Config) (restore func(), err error) {
	if len(localscripts.Declarations(local)) == 0 {
		return func() {}, nil
	}
	var renames map[string][]string
	err = store.UpdateConfig(ctx, func(c *model.Config) error {
		if c.MCPServers == nil {
			c.MCPServers = map[string]protocol.ServerConfig{}
		}
		var skipped []error
		var err error
		renames, skipped, err = localscripts.MigrateToHostScript(local, c.MCPServers)
		for _, why := range skipped {
			log.Printf("daemon: local-scripts: not moved to host-script: %v", why)
		}
		return err
	})
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
