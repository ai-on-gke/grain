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

package security

import (
	"net/http"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// identityProxy is how this suite signs a person in to a multi-tenant
// daemon now that grain keeps no sign-in accounts or sessions
// (docs/single-tenant-reset.md, stage 3d): a real ES256 assertion naming
// them, signed by a local stand-in for IAP (assertionSigner) and verified
// by the daemon against the issuer, audience and key set configure gives
// it. A verified assertion is what the session token this suite used to
// sign stood for: an identity the daemon checked itself, not one a header
// merely claims.
type identityProxy struct {
	signer *assertionSigner
}

// sharedIdentityProxy is one identityProxy for the whole run: every
// fixture that signs people in uses it, so its key set is fetched and
// cached once rather than once per daemon. Its key endpoint lives as long
// as the test binary does.
var sharedIdentityProxy = sync.OnceValue(func() *identityProxy {
	s, _, err := startAssertionSigner()
	if err != nil {
		panic(err)
	}
	return &identityProxy{signer: s}
})

// configure points cfg at p's issuer, audience and keys.
func (p *identityProxy) configure(cfg *ui.Config) {
	cfg.IAPAudience = assertionAudience
	cfg.IAPIssuer = assertionIssuer
	cfg.IAPKeysURL = p.signer.keysURL
}

// header is the request header that signs user in, or none for "".
func (p *identityProxy) header(t *testing.T, user string) http.Header {
	t.Helper()
	header := http.Header{}
	if user != "" {
		c := validAssertionClaims()
		c.Email = "accounts.google.com:" + user
		header.Set(ui.IAPAssertionHeader, p.signer.sign(t, c))
	}
	return header
}

// sessionHeader signs user in through sharedIdentityProxy, to a daemon
// configured with it.
func sessionHeader(t *testing.T, user string) http.Header {
	t.Helper()
	return sharedIdentityProxy().header(t, user)
}
