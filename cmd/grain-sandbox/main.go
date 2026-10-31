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

// Command grain-sandbox is the `grain` a sandbox image carries at
// /usr/local/bin/grain (Granulefile.guest-base, Granulefile and
// Dockerfile.k8s-sandbox): only the subcommands that run inside a
// sandbox. That is `grain host-script` (pkg/hostscriptcli) and the
// sandbox CLI of docs/design/sandbox-first-agent-surface.md §4 -- `grain
// ls`, `read`, `grep`, `ci`, `pr`, `task`, `log`, `metrics` and the rest
// (pkg/sandboxcli), which answer over the sandbox's own scoped proxy. The
// full grain binary answers every one of them the same way, through the
// same packages, so a run never needs to build grain to reach its own
// deployment.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/ai-on-gke/grain/pkg/hostscriptcli"
	"github.com/ai-on-gke/grain/pkg/sandboxcli"
)

// usage is what this grain prints outside a sandbox (or with
// GRAIN_SANDBOX_CLI=0). It names the sandbox CLI's commands as well as
// host-script, because pkg/sandboximage.VerifyScript reads it to tell
// this grain from an older, host-script-only one: keep every
// sandboximage.SandboxCLICommands entry in it (TestUsageNamesTheSandboxCLI).
const usage = `usage: grain host-script <name> [arguments...]   run a granted host script (grain host-script --list)
       grain ls <uri>                               list a directory in ci://, src://, task://, config://, memory://
       grain read <uri>                             read a file from the namespace
       grain grep <pattern> <uri>                   search a file or subtree
       grain ci (wait | log | list | ...)           this task's grain CI
       grain pr (status | open | reply | ...)       this task's pull request
       grain task (wait | message | propose | ...)  other tasks
       grain api ...                                the rest of the deployment's API
       grain help                                   the sandbox CLI in full (inside a sandbox)
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "host-script" {
		return hostscriptcli.Main(args[1:], stdout, stderr)
	}
	if code, handled := sandboxcli.Main(args, stdout, stderr); handled {
		return code
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		// Only outside a sandbox: inside one, sandboxcli answers help itself.
		fmt.Fprint(stdout, usage)
		return 0
	}
	if sandboxcli.IsCommand(args) {
		fmt.Fprintf(stderr, "grain: %q answers over a sandbox's scoped proxy, and this process found none "+
			"(no proxy credential in ~/.git-credentials, and GRAIN_SANDBOX_PROXY is unset)\n", args[0])
		return 2
	}
	fmt.Fprintf(stderr, "grain: %q is not a command this sandbox's grain has\n%s", args[0], usage)
	return 2
}
