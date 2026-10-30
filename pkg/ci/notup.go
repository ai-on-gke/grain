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

package ci

import "errors"

// ErrNotUp is a Runner that exists but cannot answer yet: a daemon whose
// UI/API server and git proxy came up before its reconcile loop built
// the CI service (pkg/daemon's CiGate), which on a restart that adopts a
// lot of runs is a minute or more. It is a reason to ask again shortly,
// not an answer -- the API serves it as 503, and a wait that meets it
// keeps waiting rather than reporting the run as forgotten.
var ErrNotUp = errors.New("this daemon's CI service is not up yet: its reconcile loop has not started " +
	"(or has failed -- check the daemon log)")

// Readiness is what a Runner that can be not up yet adds, so that
// Get's false can be told apart from "no such run". A Runner without it
// is always up.
type Readiness interface {
	Ready() bool
}

// IsUp reports whether r can answer: false only for a Runner that says
// it is not ready.
func IsUp(r any) bool {
	ready, ok := r.(Readiness)
	return !ok || ready.Ready()
}

// IsNotUp reports whether err says CI is not up yet -- ErrNotUp itself,
// or an error from across a wire that says it was refused as
// unavailable (a 503, which is how the API carries ErrNotUp).
func IsNotUp(err error) bool {
	if errors.Is(err, ErrNotUp) {
		return true
	}
	var unavailable interface{ Unavailable() bool }
	return errors.As(err, &unavailable) && unavailable.Unavailable()
}
