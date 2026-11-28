//go:build ignore

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

// grain_forgecli runs core grain with the test ForgeViaCommand ambient source
// (pkg/gitproxy/gitproxytest) registered via gitproxy.RegisterAmbientSource, so
// the walkthrough can exercise a real `grain daemon` and `grain capability host-git`
// against a mock command-backed forge CLI subprocess.
package main

import (
	"github.com/ai-on-gke/grain/pkg/cli"
	"github.com/ai-on-gke/grain/pkg/gitproxy/gitproxytest"
)

func main() {
	gitproxytest.Install()
	cli.Main()
}
