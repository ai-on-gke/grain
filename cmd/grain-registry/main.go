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

// Command grain-registry is cmd/grain-hub under its name from before the
// Grain Registry was renamed the Grain Hub, kept for one release so a
// build or deployment that still names ./cmd/grain-registry keeps working.
// It is the same program, and says once on stderr that it was renamed.
package main

import (
	"fmt"
	"os"

	"github.com/ai-on-gke/grain/pkg/hub"
	_ "github.com/ai-on-gke/grain/pkg/hub/providers/gcp"
	_ "github.com/ai-on-gke/grain/pkg/hub/providers/github"
	_ "github.com/ai-on-gke/grain/pkg/hub/reconcile"
)

func main() {
	fmt.Fprintln(os.Stderr, "grain-registry: deprecated: this command is now grain-hub (./cmd/grain-hub); grain-registry will be removed in a future release")
	hub.Main()
}
