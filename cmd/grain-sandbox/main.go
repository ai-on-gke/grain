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
// /usr/local/bin/grain (Granulefile.guest-base): only the subcommands
// that run inside a sandbox, built from standard-library packages alone
// so the guest image needs none of the rest of grain. Today that is
// `grain host-script` (pkg/hostscriptcli); the full grain binary answers
// the same subcommand the same way.
package main

import (
	"fmt"
	"os"

	"github.com/ai-on-gke/grain/pkg/hostscriptcli"
)

const usage = `usage: grain host-script <name> [arguments...]   run a granted host script (grain host-script --list)
`

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "host-script" {
		os.Exit(hostscriptcli.Main(args[1:], os.Stdout, os.Stderr))
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(usage)
		return
	}
	fmt.Fprintf(os.Stderr, "grain: %q is not a command this sandbox's grain has\n%s", args[0], usage)
	os.Exit(2)
}
