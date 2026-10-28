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

package cli

import (
	"flag"

	"github.com/ai-on-gke/grain/pkg/daemon"
)

// localFlags defines the daemon's direct local-configuration flags, which
// are aliases over settings it already has (daemon.LocalSettings), and
// returns what folds them into the parsed config.
func localFlags(fs *flag.FlagSet) func(daemon.Config, map[string]bool) (daemon.Config, error) {
	var repos stringSliceFlag
	fs.Var(&repos, "repo", "a repository this workstation works on, as a local checkout's path or owner/name or URL; repeatable. "+
		"Each is added to the -policy file's repos, added to the default team's target repositories when "+
		"that list restricts them, and the first is -default-target-repo unless that is set")
	sandboxBackend := fs.String("sandbox-backend", "", "container, granule, kubernetes, kubernetes-granule, host or none: "+
		"the same as -container-sandboxes, -granule-sandboxes, -k8s-sandboxes, -k8s-granule-sandboxes, -host-sandboxes or -disable-native-sandboxes")
	modelBackend := fs.String("model-backend", "", "agy, claude or codex: the same as -agent-framework (agy is antigravity)")
	return func(cfg daemon.Config, passed map[string]bool) (daemon.Config, error) {
		return daemon.ApplyLocalSettings(cfg, daemon.LocalSettings{
			Repos: repos, SandboxBackend: *sandboxBackend, ModelBackend: *modelBackend,
		}, passed)
	}
}
