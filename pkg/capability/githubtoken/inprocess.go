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

package githubtoken

import (
	"context"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

// ServerID is the git server's canonical capability_id, and MethodName
// its git-repo method, which github-credential:<name> grants became
// (schema migration 103, model.GitCredentialServerGrant).
const (
	ServerID   = model.GitCapabilityServerID
	MethodName = model.GitRepoMethod
)

// Method is git-repo {credential, repo} as grain answers it in-process:
// what capability.Register serves on ServerID.
//
// It is answered by grain itself because what it chooses is which of
// this deployment's own credentials grain's git proxy attaches to the
// run's requests for repo, and nothing outside grain holds those or sees
// those requests. So, as the retired Provider did, the mint places
// nothing and mints nothing: the proxy reads the grant off the live task
// (model.Store.GitCredentialOverride, model.GitCredentialGrantTarget) on
// every request. The mint's prompt is the Provider's own PromptSection
// for the credential named. The external `grain capability git` server,
// which clones through a per-attempt token on a proxy of its own, is no
// longer a built-in; it still serves an operator who registers it under
// an id of their own.
func Method() capability.Method {
	return capability.Method{
		Server: ServerID,
		Name:   "Git credential",
		Description: "Push and pull one repository -- or, with repo \"*\", every repository this task reaches -- " +
			"through a named GitHub credential of this deployment's instead of its default one",
		Spec: protocol.MethodSpec{
			Name: MethodName,
			Description: "Route this run's git traffic for repo through grain's git proxy with the named credential " +
				"(formerly github-credential:<name>). The token never enters the sandbox.",
			Args: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"credential", "repo"},
				"properties": map[string]any{
					"credential": map[string]any{
						"type":        "string",
						"description": "The credential's name: a file under secrets/github, without its .token or .app.json suffix.",
						"pattern":     model.GitCredentialNamePattern,
					},
					"repo": map[string]any{
						"type":        "string",
						"description": "owner/name of the repository whose git traffic uses the credential, or \"*\" for every repository the run reaches.",
						"pattern":     model.GitCredentialRepoPattern,
					},
				},
			},
			// The orchestrator set the pass's deadline from its own
			// clock, so there is no second one to hold it to (as the
			// grain server's methods).
			Now: func() time.Time { return time.Time{} },
			Mint: func(ctx context.Context, _ string, _ time.Time, args map[string]any) (*protocol.MintResponse, error) {
				name, _ := args["credential"].(string)
				prompt, err := New(name, Config{}).PromptSection(ctx, model.CapabilityContext{}, nil)
				if err != nil {
					return nil, err
				}
				return &protocol.MintResponse{Status: protocol.StatusOK, PromptSection: prompt}, nil
			},
		},
	}
}

func init() { capability.Register(Method()) }
