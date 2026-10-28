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

package intree

import (
	"context"
	"fmt"
	"log"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
)

// host-git is not a grant: it has no mint_ method and no sandbox ever
// sees it. It is the host side of an ambient git credential for a
// daemon in the release container -- the host account's gh (or registered source tool) login,
// run on the host by grain hostd from the capability entry of the same
// name (gitproxy.HostGitEntry, hostgit.go there) -- and serves the tools
// gitproxy's HostGitClient calls, one process per call.
func init() {
	register(Builder{
		Name:     gitproxy.HostGitEntry,
		Internal: true,
		Summary:  "the host account's forge CLI login, for an ambient git credential behind grain hostd",
		Build: func(ctx context.Context, args []string, env Env, logger *log.Logger) (*Server, error) {
			fs := newFlags(gitproxy.HostGitEntry, logOutput(logger))
			if err := fs.Parse(args); err != nil {
				return nil, err
			}
			if fs.NArg() > 0 {
				return nil, fmt.Errorf("%s takes no arguments, got %q", gitproxy.HostGitEntry, fs.Args())
			}
			s, err := newServer(gitproxy.HostGitEntry, logger)
			if err != nil {
				return nil, err
			}
			if err := gitproxy.ServeHostGit(s.CapabilityServer); err != nil {
				return nil, err
			}
			return s, nil
		},
	})
}
