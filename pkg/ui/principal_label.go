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

import (
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// PrincipalLabel is how p reads to a person (operator-attribution spec
// §1.2.2), computed here so that the UI, the CLI and MCP all say the same
// thing. behalf is whose request p relayed, nil for none; it is named
// only when it is verified.
//
// Only a verified principal (model.Principal.Verified) is shown as a
// person. The operator fallback, and a row written before sources
// existed that names the deployment's default actor, read as the
// operator, never as a person; any other unsourced human is shown as
// unverified.
func PrincipalLabel(p model.Principal, behalf *model.Principal) string {
	label := principalLabelOf(p)
	if behalf != nil && behalf.Verified() {
		label += " for " + principalLabelOf(*behalf)
	}
	return label
}

func principalLabelOf(p model.Principal) string {
	id := strings.TrimSpace(p.ID)
	switch p.Kind {
	case model.PrincipalAutomation:
		return "grain"
	case model.PrincipalAgent:
		switch p.Source {
		case model.SourceExternalToken:
			return "external agent " + strings.TrimPrefix(id, "external:")
		case model.SourceSession:
			return "session " + id
		}
		if name, ok := strings.CutPrefix(id, "external:"); ok {
			return "external agent " + name
		}
		return "task " + id
	}
	if p.Verified() {
		return id
	}
	switch p.Source {
	case model.SourceAccessCode:
		return "operator (access code)"
	case model.SourceLoopback, model.SourceInProcess:
		return "operator (local)"
	case "":
		if id == DefaultActor("").ID {
			return "operator (access code)"
		}
	}
	return id + " (unverified)"
}
