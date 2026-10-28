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
