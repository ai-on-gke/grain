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

package model

import "strings"

// NormalizeIdentity is the form a person's identity is compared in: an
// IAP or BeyondCorp "accounts.google.com:dharb@google.com", an access
// list's "user:dharb@google.com" and a verified assertion's
// "dharb@google.com" all name the same person, and all normalise to the
// bare "dharb@google.com". Case is left alone; SameIdentity compares
// without it, as every allowlist and admin check here always has.
func NormalizeIdentity(id string) string {
	id = strings.TrimSpace(id)
	for _, prefix := range []string{"user:", "https://accounts.google.com:", "accounts.google.com:"} {
		if len(id) >= len(prefix) && strings.EqualFold(id[:len(prefix)], prefix) {
			id = strings.TrimSpace(id[len(prefix):])
		}
	}
	return id
}

// SameIdentity reports whether a and b name the same person: equal,
// ignoring case, once both are normalised (NormalizeIdentity). An empty
// identity is nobody, and is nobody's.
func SameIdentity(a, b string) bool {
	a, b = NormalizeIdentity(a), NormalizeIdentity(b)
	return a != "" && strings.EqualFold(a, b)
}
