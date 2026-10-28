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

package apierror

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
)

func TestBodyKeepsClientErrors(t *testing.T) {
	t.Parallel()
	got := Body(400, errors.New("a head branch is required"))
	if got.Error != "a head branch is required" || got.Code != "" || got.Ref != "" {
		t.Fatalf("Body(400) = %+v", got)
	}
}

func TestBodyWithholdsServerErrors(t *testing.T) {
	t.Parallel()
	secret := `pq: password authentication failed for user "grain" at 10.0.0.7:5432`
	got := Body(500, fmt.Errorf("loading task: %w", errors.New(secret)))
	if strings.Contains(got.Error, "pq:") || strings.Contains(got.Error, "10.0.0.7") {
		t.Fatalf("Body(500) leaked the internal error: %+v", got)
	}
	if got.Code != CodeInternal || got.Ref == "" || !strings.Contains(got.Error, got.Ref) {
		t.Fatalf("Body(500) = %+v, want an internal code and a reference", got)
	}
}

func TestBodyPassesPublicErrors(t *testing.T) {
	t.Parallel()
	got := Body(501, Public("this deployment does not run a proxy cache"))
	if got.Error != "this deployment does not run a proxy cache" || got.Ref != "" {
		t.Fatalf("Body(501, Public) = %+v", got)
	}
}

func TestClassify(t *testing.T) {
	t.Parallel()
	dial := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	cases := []struct {
		name   string
		status int
		err    error
		want   string
	}{
		{"deadline", 500, fmt.Errorf("query: %w", context.DeadlineExceeded), CodeTimeout},
		{"canceled", 500, context.Canceled, CodeCanceled},
		{"bad conn", 500, fmt.Errorf("exec: %w", driver.ErrBadConn), CodeStorage},
		{"dial", 500, fmt.Errorf("fetch: %w", dial), CodeUpstream},
		{"bad gateway", 502, errors.New("GitHub said 422"), CodeUpstream},
		{"unavailable", 503, errors.New("x"), CodeUnavailable},
		{"plain", 500, errors.New("x"), CodeInternal},
	}
	for _, tc := range cases {
		if got, _ := Classify(tc.status, tc.err); got != tc.want {
			t.Errorf("%s: Classify = %q, want %q", tc.name, got, tc.want)
		}
	}
}
