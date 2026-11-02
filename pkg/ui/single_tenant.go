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

package ui

import (
	"net/http"
)

// singleTenantRefusal is what a caller this daemon does not trust as
// itself hears: there is nothing it could sign in to.
const singleTenantRefusal = "this daemon is single-tenant: it keeps no accounts, and only its access code reaches its API " +
	"(`grain access-code` prints it); work from anybody else is judged by the local policy docs/local-policy.md describes"

// presentsAccess reports whether r carries this single-tenant daemon's
// access code, as a bearer token or an access session.
func (s *Server) presentsAccess(r *http.Request) bool {
	cfg, _ := IAPConfigFromContext(r.Context())
	if presentsAccessCode(r, cfg) {
		return true
	}
	_, ok := accessSessionIssued(r, cfg)
	return ok
}
