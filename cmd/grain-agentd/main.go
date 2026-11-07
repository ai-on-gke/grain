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

// Command grain-agentd is a lightweight, rootless grain hostd for a machine
// that is not the daemon's: it runs the agent CLIs a grain daemon asks for,
// as the user who starts it, and nothing else. See pkg/agentd and
// docs/grain-agentd.md.
//
// A build that reaches its daemons some other way, or admits host tools
// of its own in the agy hook, keeps a main of its own that registers them
// (agentd.RegisterTransport, hook.RegisterHostToolGrant) and calls
// agentd.Main.
package main

import (
	"os"

	"github.com/ai-on-gke/grain/pkg/agentd"
)

func main() {
	os.Exit(agentd.Main(os.Args))
}
