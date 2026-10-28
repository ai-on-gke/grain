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

// InstallNotice is something about how this deployment is installed that
// its operator has to fix, on GET /api/config as installNotice: today an
// install still in host mode, which v2.3.0 removed (pkg/daemon's
// installnotice.go).
type InstallNotice struct {
	// Kind names the case: "host-mode".
	Kind string `json:"kind"`
	// Message is the notice itself, the sentence the daemon logs.
	Message string `json:"message"`
	// Command is what fixes it.
	Command string `json:"command,omitempty"`
	// Signals are what the daemon saw that says so.
	Signals []string `json:"signals,omitempty"`
	// AffectedCLIs are the agent CLIs that cannot run because of it, one
	// line each: "claude: no host binary, ...".
	AffectedCLIs []string `json:"affectedClis,omitempty"`
	// DocsURL is where the fix is written up.
	DocsURL string `json:"docsUrl,omitempty"`
}
