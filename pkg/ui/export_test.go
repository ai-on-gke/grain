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
	"context"
	"net"
)

// What the external tests in this directory need to see of the inside.
//
// pkg/ui's tests are package ui_test -- they drive the real HTTP surface
// over a real store, which is the whole discipline of client_test.go's
// own header -- so a fact that is not on the wire is not visible to
// them. This is the way to hand one over deliberately, rather than by
// exporting something into the package's API for a test's sake.

// TaskListBuilds is how many times GET /api/tasks has actually gone to
// the store and built a response, as opposed to answering out of the
// cache in front of it. It is the only way to tell those two apart from
// outside: both produce byte-identical answers, which is the point.
func TaskListBuilds(s *Server) int { return int(s.taskList.builds.Load()) }

// TaskDetailBuilds is the equivalent count for GET /api/tasks/{id}.
func TaskDetailBuilds(s *Server) int { return int(s.taskDetails.builds.Load()) }

// httptest.NewRequest gives every request it builds a RemoteAddr in
// TEST-NET-1 (192.0.2.1:1234). The tests in this directory build
// thousands of them and mean "a caller on this machine" by every one, so
// that range is trusted as an in-process request is -- here, and only here. A test
// about an untrusted caller sets a RemoteAddr outside it.
func init() {
	_, n, _ := net.ParseCIDR("192.0.2.0/24")
	testSourceNets = []*net.IPNet{n}
}

// SetAccessCodeForTest makes code s's access code, whether or not s has
// already answered a request -- or is answering one now: it swaps the
// code localAPITokenNow reads rather than writing s's Config under a
// running server.
func SetAccessCodeForTest(s *Server, code string) {
	s.accessCode.Store(&accessCodeFile{fallback: code})
}

// RefuseConfigWriteForTest is the guard a configuration write to tables
// goes through, without a rehearsal: the refusal itself, as the caller
// on ctx would get it in external mode.
func RefuseConfigWriteForTest(c *Client, ctx context.Context, tables ...string) error {
	return c.checkSettingsWritableUnrehearsed(ctx, tables...)
}
