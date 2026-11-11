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

package staterepo

import (
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

// GrainConfigFiles are the files, relative to a state repository's root,
// that its grain_config.mcp_servers is read from: the table's own dump
// and settings/capabilities.yml, which is put over it. A caller copying
// a base revision out of git copies these for MCPServersIn to read.
func GrainConfigFiles() []string {
	return []string{DumpPath(grainConfigTable), CapabilitiesPath}
}

// MCPServersIn is grain_config.mcp_servers as the state repository at
// dir holds it, read the way an import reads it (settings/capabilities.yml
// over tables/), with no database: what `grain state check` compares a
// pull request's capability servers with, as the base held them. A
// repository with no settings row holds none.
func MCPServersIn(dir string) (map[string]protocol.ServerConfig, error) {
	rows, present, err := dumpRowsOf(dir, grainConfigTable)
	if err != nil || !present || len(rows) == 0 {
		return nil, err
	}
	text, _ := textOf(rows[0]["mcp_servers"])
	return model.DecodeMCPServers(text)
}
