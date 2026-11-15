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

	"github.com/ai-on-gke/grain/pkg/model"
)

// adoptDefaultEnvironmentName gives the deployment the environment name
// a Grain Hub supplied -- a hosted instance's -default-environment-name,
// or a connected entry's heartbeat reply -- unless its operator has named
// it something else (model.Config.AdoptHubEnvironmentName). A failure is
// logged, not returned: what a tab title says is no reason to stop a
// daemon starting or a heartbeat succeeding.
func adoptDefaultEnvironmentName(ctx context.Context, store *model.Store, name string) {
	if store == nil || name == "" {
		return
	}
	changed, err := store.AdoptHubEnvironmentName(ctx, name)
	if err != nil {
		log.Printf("grain daemon: applying the hub's environment name %q: %v", name, err)
		return
	}
	if changed {
		log.Printf("grain daemon: environment name is now %q, as its Grain Hub entry names it (Settings overrides it)", name)
	}
}
