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

// Package bootstrap is a GRANT model.CapabilityProvider: a task holding
// it gets read-only tools onto a fixed set of bootstrap playbooks --
// markdown runbooks, embedded at build time, for the setup flows
// bwsalmon/agents#620 asked an agent to be able to walk an operator
// through: minting the GCP service accounts the gcp-key/
// gemini-key capabilities need, registering grain's primary GitHub
// connection, and installing a
// GitHub App on a set of test repos.
//
// Like selfdebug, it mints nothing and writes no Placement -- Resolve,
// Materialize, PromptSection and Revoke are all model.BaseCapability's
// defaults -- because what it grants is not material moved into a
// sandbox, it is tools (PlaybookTools below).
//
// How they reach a run is `grain mcpserver -grant bootstrap-playbooks`,
// passed by a Framework only for a task holding this grant
// (agent.RunConfig.Grants, agent.GrantArgs) -- the same road self-debug
// takes, and an even shorter one, since the playbooks are embedded in
// the grain binary that subprocess already is: no daemon to ask, no
// directory to be given, nothing but the name of the grant.
// orchestrator.Config.GrantTools also adds PlaybookTools straight to a
// run's in-process tool set for an Interactive task
// (orchestrator/cycle.go's runOne), which is where this started and
// which no CLI-driving Framework can consume -- the flag is what
// actually gets these tools to an agent.
//
// A playbook only ever describes commands; it never runs one
// itself -- every command it tells the agent to run still goes through
// the self-repair grant's own run_host_command tool, which is what
// actually touches the host, gated on a human's live approval in the
// task's chat the same way any other self-repair command is. Granting
// this capability alongside self-repair is what lets an agent read a
// playbook and then act on it in the same conversation.
//
// The playbooks are the same markdown that is in grain's public source
// tree, and its tools only read them (grain/task-3779).
package bootstrap

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// CapabilityName is the grant name ui.OfferedCapabilities lists as
// "bootstrap-playbooks".
const CapabilityName = "bootstrap-playbooks"

//go:embed playbooks/*.md
var playbookFS embed.FS

const playbookDir = "playbooks"

// Provider is the bootstrap-playbooks capability. Every method but Spec
// is model.BaseCapability's default -- see selfdebug.Provider's own doc
// comment for why a GRANT capability whose only effect is which tools a
// run gets needs nothing more.
type Provider struct {
	model.BaseCapability
}

// New returns a bootstrap-playbooks Provider, ready to register.
func New() *Provider { return &Provider{} }

func (p *Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:  CapabilityName,
		Label: "Bootstrap playbooks",
		Description: "Let this task read grain's own bootstrap playbooks -- the runbooks for setting up GCP " +
			"service accounts, the primary GitHub connection, test repos, or an off-cluster GKE sandbox cluster -- so " +
			"it can walk whoever is on the other end of this chat through one of them",
		Source:    model.GrantByLabel,
		Provision: model.ProvisionGrant,
	}
}

// playbookNames lists every embedded playbook's name (its filename
// without the .md suffix), sorted -- the order both PlaybookTools'
// list_bootstrap_playbooks tool and this package's own tests rely on.
func playbookNames() ([]string, error) {
	entries, err := fs.ReadDir(playbookFS, playbookDir)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: reading embedded playbooks: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(names)
	return names, nil
}

// readPlaybook returns the embedded markdown for name, or an error
// listing what does exist if name is not one of them -- the same
// steer-back-toward-a-valid-argument shape selfdebug's own path errors
// give.
func readPlaybook(name string) (string, error) {
	data, err := playbookFS.ReadFile(playbookDir + "/" + name + ".md")
	if err != nil {
		names, listErr := playbookNames()
		if listErr != nil || len(names) == 0 {
			return "", fmt.Errorf("no bootstrap playbook named %q", name)
		}
		return "", fmt.Errorf("no bootstrap playbook named %q -- available: %s", name, strings.Join(names, ", "))
	}
	return string(data), nil
}

// PlaybookTools returns the two tools a bootstrap-playbooks grant adds
// to a run: list_bootstrap_playbooks and read_bootstrap_playbook. Both
// are read-only, so -- like selfdebug's SourceTools -- neither needs a
// confirmation step; the confirmation happens later, when the agent
// turns a playbook's steps into actual run_host_command calls.
func PlaybookTools() []mcp.Tool {
	return []mcp.Tool{listPlaybooksTool(), readPlaybookTool()}
}

func listPlaybooksTool() mcp.Tool {
	return mcp.Tool{
		Name: "list_bootstrap_playbooks",
		Description: "List the names of grain's own bootstrap playbooks -- runbooks for setting up GCP service " +
			"accounts, the primary GitHub connection, or test repos. Use " +
			"read_bootstrap_playbook to read one.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{},
		},
		Handler: func(context.Context, map[string]any) mcp.Result {
			names, err := playbookNames()
			if err != nil {
				return mcp.Result{Text: err.Error(), IsError: true}
			}
			if len(names) == 0 {
				return mcp.Result{Text: "no bootstrap playbooks are available"}
			}
			return mcp.Result{Text: strings.Join(names, "\n")}
		},
	}
}

func readPlaybookTool() mcp.Tool {
	return mcp.Tool{
		Name: "read_bootstrap_playbook",
		Description: "Read one of grain's own bootstrap playbooks by name (see list_bootstrap_playbooks). Each " +
			"one is a runbook for walking whoever is in this chat through a setup flow -- read it fully before " +
			"acting on any of it, and follow its own guidance on what to ask the human for and what to run " +
			"yourself (through run_host_command, which still needs a live approval for every command).",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "playbook name, as returned by list_bootstrap_playbooks",
				},
			},
			"required": []string{"name"},
		},
		Handler: func(_ context.Context, args map[string]any) mcp.Result {
			name, _ := args["name"].(string)
			if name == "" {
				return mcp.Result{Text: "name is required", IsError: true}
			}
			text, err := readPlaybook(name)
			if err != nil {
				return mcp.Result{Text: err.Error(), IsError: true}
			}
			return mcp.Result{Text: text}
		},
	}
}
