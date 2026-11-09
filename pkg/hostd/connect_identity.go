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

package hostd

import (
	"context"
	"fmt"
	"strings"
)

// ConnectIdentity is who a connection to RemotePath came in as, which the
// daemon's HTTP layer (pkg/ui) works out from the request before handing
// it to Remote.ServeHTTP: the hostd token says which hostd it is, this
// says who is connecting.
//
// A remote hostd -- one that dials in through the IAP Gateway or another
// identity verifier's proxy -- needs both, and its verified identity must
// be its registry row's owner (authentication.design.md, grain hostd). A
// local one (loopback with no forwarding headers and no assertion, such
// as a kubectl port-forward) is named by its token alone, as is any
// connection to a daemon with no verifier configured.
type ConnectIdentity struct {
	// Remote is a connection that came through a verifying proxy, or from
	// off this host to a daemon that has a verifier configured.
	Remote bool
	// Principal is the verified identity, a lowercased email; "" for none.
	Principal string
	// Via is the verifier that vouched for Principal ("iap", or a
	// wrapper's proxy).
	Via string
	// DelegatedBy is the delegating service-account email when the request
	// arrived on behalf of Principal; any non-empty value is refused by
	// ownerRefusal (AU-25.4).
	DelegatedBy string
}

type connectIdentityKey struct{}

// WithConnectIdentity returns ctx carrying id, for Remote.ServeHTTP.
func WithConnectIdentity(ctx context.Context, id ConnectIdentity) context.Context {
	return context.WithValue(ctx, connectIdentityKey{}, id)
}

// ConnectIdentityFrom is the ConnectIdentity WithConnectIdentity put on
// ctx; a local connection's zero value when none did.
func ConnectIdentityFrom(ctx context.Context) ConnectIdentity {
	id, _ := ctx.Value(connectIdentityKey{}).(ConnectIdentity)
	return id
}

// ownerRefusal is why a remote connection to hostd name, whose registry
// owner is owner, is refused for the identity it came in as, or "" when
// it is admitted. shared says name is the deprecated shared token's
// hostd, whose owner is a daemon flag rather than a registry row.
func ownerRefusal(id ConnectIdentity, name, owner string, shared bool) string {
	if !id.Remote {
		return ""
	}
	if id.DelegatedBy != "" {
		return fmt.Sprintf("hostd %q dialed in via a delegated identity (%s acting for %s): a remote hostd must connect directly as its owner, never via delegation",
			name, id.DelegatedBy, id.Principal)
	}
	setOwner := fmt.Sprintf("set it in Settings → Hosts, `grain host set-owner %s <email>`, or PATCH /api/hostds/%s {\"owner\": \"<email>\"}", name, name)
	if shared {
		setOwner = "start the daemon with -hostd-token-owner=<email>, or register this hostd in the hostd registry (/api/hostds) with an owner"
	}
	if id.Principal == "" {
		return fmt.Sprintf("hostd %q dialed in through a proxy with no verified identity: a remote hostd must present its owner's identity "+
			"(grain hostd -iap-audience, with its service account or the owner's login)", name)
	}
	if owner == "" {
		return fmt.Sprintf("hostd %q has no owner, and a remote hostd's verified identity must be its owner: it connected as %q; %s",
			name, id.Principal, setOwner)
	}
	if !strings.EqualFold(strings.TrimSpace(id.Principal), strings.TrimSpace(owner)) {
		return fmt.Sprintf("hostd %q connected as %q, but its owner is %q: a remote hostd must connect as its owner "+
			"(run it as %s, or %s)", name, id.Principal, owner, owner, strings.Replace(setOwner, "set it", "change the owner", 1))
	}
	return ""
}
