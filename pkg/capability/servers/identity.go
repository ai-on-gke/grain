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

package servers

import (
	"context"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// IdentitySource is what Options.Identity is read from: model.Store.
type IdentitySource interface {
	DeploymentID(ctx context.Context) (string, error)
	InstanceID(ctx context.Context) (string, error)
}

// identityTimeout bounds one read of the identity.
const identityTimeout = 5 * time.Second

// StoreIdentity is an Options.Identity reading src: the deployment id
// grain's branch names use (model.Store.DeploymentID, configured in
// Settings or else derived from the installation id) and the
// installation id. A read that fails keeps the last identity read, so a
// database hiccup does not re-attach every server.
func StoreIdentity(src IdentitySource, logf func(format string, args ...any)) func() protocol.Identity {
	var mu sync.Mutex
	var last protocol.Identity
	return func() protocol.Identity {
		ctx, cancel := context.WithTimeout(context.Background(), identityTimeout)
		defer cancel()
		mu.Lock()
		defer mu.Unlock()
		dep, err := src.DeploymentID(ctx)
		if err == nil {
			var inst string
			inst, err = src.InstanceID(ctx)
			if err == nil {
				last = protocol.Identity{DeploymentID: dep, InstanceID: inst}
				return last
			}
		}
		if logf != nil {
			logf("reading this deployment's identity for capability servers: %v", err)
		}
		return last
	}
}
