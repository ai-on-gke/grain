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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

type unavailableForTest struct{}

func (unavailableForTest) Error() string     { return "backend not reachable yet" }
func (unavailableForTest) Unavailable() bool { return true }

// A hostd whose identity header could not be checked yet is refused with
// 503, to retry, and one whose header did not verify with 401.
func TestHostdConnectIdentityRefusesUnavailableVerifierWith503(t *testing.T) {
	for _, c := range []struct {
		err  error
		want int
	}{
		{fmt.Errorf("checking: %w", unavailableForTest{}), http.StatusServiceUnavailable},
		{errors.New("signature verification failed"), http.StatusUnauthorized},
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/hostd/connect", nil)
		r = r.WithContext(withCallerVerification(r.Context(), &callerVerification{present: true, method: "corp-proxy", err: c.err}))
		id, code, err := (&Server{}).hostdConnectIdentity(r)
		if err == nil || code != c.want || id.Remote || id.Principal != "" {
			t.Errorf("%v: got %+v, %d, %v; want refused with %d", c.err, id, code, err, c.want)
		}
	}
}
