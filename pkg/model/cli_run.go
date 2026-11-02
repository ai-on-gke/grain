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

package model

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Where an agent CLI runs (design/design/cli-run-location.design.md).
const (
	// CLIRunHost runs the CLI on the machine: through grain hostd for a
	// daemon in the release container, or as the daemon's own child for
	// a development daemon on the bare machine.
	CLIRunHost = "host"
	// CLIRunContainer runs it as a subprocess in the daemon's own
	// container, from the CLI the release image ships.
	CLIRunContainer = "container"
	// CLIRunDetached runs the image's own CLI on the machine through
	// grain hostd, detached, from its container-equivalent allowlist
	// entry ("claude-detached"): the deployment's login and the
	// container's HOME, so it is a container run that outlives a daemon
	// upgrade (design/design/non-disruptive-upgrade.design.md, "Which substrate carries
	// which CLI"). Only a daemon in the release container with grain
	// hostd has it, and only for a CLI whose runs can be adopted.
	CLIRunDetached = "detached"
	// cliRunAuto is the location releases before v2.3.0 also accepted:
	// host when the host's binary answered --version, container
	// otherwise. It is no longer one, and DecodeCLIRun drops a stored
	// one, leaving that CLI to the daemon's default.
	cliRunAuto = "auto"
)

// ValidCLIRunLocation reports whether v is a location, "" (unset)
// included.
func ValidCLIRunLocation(v string) bool {
	switch v {
	case "", CLIRunHost, CLIRunContainer, CLIRunDetached:
		return true
	}
	return false
}

// CLIRunSetting is one agent CLI's run location as Settings -> Models
// stores it.
type CLIRunSetting struct {
	// Location is CLIRunHost, CLIRunContainer or CLIRunDetached; "" leaves the choice to
	// the daemon's flags and defaults.
	Location string `json:"location,omitempty"`
}

// CLIRunFrameworks are the frameworks a run location can be set for, in
// the order Settings shows them.
var CLIRunFrameworks = []string{AgentFrameworkClaude, AgentFrameworkCodex, AgentFrameworkAntigravity, AgentFrameworkCustom}

// ValidateCLIRun refuses a setting for a framework that has none, or a
// location that is not one, whatever the deployment. Which of the two
// locations a deployment offers is the daemon's to decide
// (daemon.Config.CheckCLIRunSettings), on top of this.
func ValidateCLIRun(m map[string]CLIRunSetting) error {
	for _, name := range slices.Sorted(maps.Keys(m)) {
		s := m[name]
		if !slices.Contains(CLIRunFrameworks, name) {
			return fmt.Errorf("run location: unknown agent CLI %q: expected one of %s", name, strings.Join(CLIRunFrameworks, ", "))
		}
		if !ValidCLIRunLocation(s.Location) {
			return fmt.Errorf("run location for %s: %q is not one of host, container, detached", name, s.Location)
		}
	}
	return nil
}

// EncodeCLIRun is grain_config.cli_run's text: "" for no settings,
// otherwise JSON with empty entries dropped.
func EncodeCLIRun(m map[string]CLIRunSetting) (string, error) {
	kept := map[string]CLIRunSetting{}
	for _, name := range slices.Sorted(maps.Keys(m)) {
		s := CLIRunSetting{Location: strings.TrimSpace(m[name].Location)}
		if s != (CLIRunSetting{}) {
			kept[name] = s
		}
	}
	if len(kept) == 0 {
		return "", nil
	}
	b, err := json.Marshal(kept)
	if err != nil {
		return "", fmt.Errorf("encoding cli_run: %w", err)
	}
	return string(b), nil
}

// DecodeCLIRun reads EncodeCLIRun's text back. What releases before
// v2.3.0 also stored is dropped as it is read, so the next save writes
// it out: an "auto" location, which leaves that CLI to the daemon's
// default, and an image, which only the per-run docker launcher those
// releases had ever used.
func DecodeCLIRun(s string) (map[string]CLIRunSetting, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var m map[string]CLIRunSetting
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, fmt.Errorf("decoding cli_run: %w", err)
	}
	for name, setting := range m {
		if strings.TrimSpace(setting.Location) == cliRunAuto {
			setting.Location = ""
		}
		if setting == (CLIRunSetting{}) {
			delete(m, name)
			continue
		}
		m[name] = setting
	}
	if len(m) == 0 {
		return nil, nil
	}
	return m, nil
}
