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
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/servers"
)

// changeControlTimeout bounds one change-provider control call: a CL
// create and upload runs g4 twice.
const changeControlTimeout = 3 * time.Minute

// changeControl is change.Control over the capability server registry:
// the first attached server offering tool answers, called with its own
// data-plane token, which is what authenticates a daemon-only control
// tool (ObserveTool, changelists.md §2.5).
type changeControl struct {
	reg *servers.Registry
}

func (c changeControl) CallControl(ctx context.Context, tool string, args map[string]any) ([]byte, error) {
	for _, st := range c.reg.Statuses() {
		if st.State != servers.Attached || !slices.Contains(st.Tools, tool) {
			continue
		}
		srv, err := c.reg.Lookup(st.ID)
		if err != nil {
			return nil, err
		}
		a := maps.Clone(args)
		if a == nil {
			a = map[string]any{}
		}
		a["token"] = srv.DataPlaneToken()
		return srv.CallTool(ctx, tool, a, changeControlTimeout)
	}
	return nil, fmt.Errorf("no attached capability server offers %s: is the capability server configured and attached on this deployment?", tool)
}
