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
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
)

// CLIRunSettings is GET and PUT /api/settings/cli-run's answer: where each
// agent CLI is set to run (Settings -> Models, model.Config.CLIRun), and
// where it will run on its next dispatch and why, as the daemon resolves
// it (daemon.CLIRunResolution, design/design/cli-run-location.design.md).
type CLIRunSettings struct {
	Settings map[string]model.CLIRunSetting `json:"settings"`
	// Resolved is nil where no daemon is colocated with this UI to say
	// (Config.CLIRuns).
	Resolved any `json:"resolved"`
	// HostExecutor is what "host" means for this deployment, and whether
	// grain hostd answers when that is what it is (Config.HostExecutor);
	// nil where no daemon is colocated with this UI.
	HostExecutor any `json:"hostExecutor,omitempty"`
}

// cliRunSettings reads the stored setting and resolves it.
func (c *Client) cliRunSettings(ctx context.Context) (CLIRunSettings, error) {
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		return CLIRunSettings{}, err
	}
	out := CLIRunSettings{Settings: map[string]model.CLIRunSetting{}}
	if cfg != nil {
		for k, v := range cfg.CLIRun {
			out.Settings[k] = v
		}
	}
	// The pane shows a dialing-in hostd that is not connected at once,
	// rather than after the wait a run would give it (hostd.ForDisplay).
	ctx = hostd.ForDisplay(ctx)
	if c.Config.CLIRuns != nil {
		out.Resolved = c.Config.CLIRuns(ctx, out.Settings)
	}
	if c.Config.HostExecutor != nil {
		out.HostExecutor = c.Config.HostExecutor(ctx)
	}
	return out, nil
}

// UpdateCLIRunSettings replaces the stored run locations with settings,
// which applies to each CLI's next run.
func (c *Client) UpdateCLIRunSettings(ctx context.Context, settings map[string]model.CLIRunSetting) (CLIRunSettings, error) {
	// Where a CLI runs decides what runs with that CLI's credential --
	// on the machine as the operator, or in the daemon's container -- so
	// it is a person's to choose, never a run's.
	if p, ok := PrincipalFromContext(ctx); ok && p.Kind == model.PrincipalAgent {
		return CLIRunSettings{}, forbiddenErrorf("%q is not a person, so it may not change where agent CLIs run", p.ID)
	}
	if err := c.checkBootstrapWritable(ctx, "grain_config"); err != nil {
		return CLIRunSettings{}, err
	}
	check := model.ValidateCLIRun
	if c.Config.CLIRunCheck != nil {
		check = c.Config.CLIRunCheck
	}
	if err := check(settings); err != nil {
		return CLIRunSettings{}, validationErrorf("%v", err)
	}
	var before map[string]model.CLIRunSetting
	if err := c.Store.UpdateConfig(ctx, func(cfg *model.Config) error {
		before = cfg.CLIRun
		cfg.CLIRun = settings
		return nil
	}); err != nil {
		return CLIRunSettings{}, err
	}
	c.auditCLIRunChanges(ctx, before, settings)
	return c.cliRunSettings(ctx)
}

// auditCLIRunChanges records every agent CLI whose run location differs
// between before and after, the way auditCLIPathChanges records a
// connection's CLI path: where a CLI runs is what the daemon runs with
// the CLI's credential, so choosing it is recorded as a use of that
// credential is. Best effort: the save it follows has already landed.
func (c *Client) auditCLIRunChanges(ctx context.Context, before, after map[string]model.CLIRunSetting) {
	if c.Store == nil {
		return
	}
	user := c.actor(ctx).ID
	for _, name := range model.CLIRunFrameworks {
		was, now := trimCLIRun(before[name]), trimCLIRun(after[name])
		if was == now {
			continue
		}
		action := "set cli run"
		if now == (model.CLIRunSetting{}) {
			action = "clear cli run"
		}
		outcome := fmt.Sprintf("location %q -> %q", was.Location, now.Location)
		if user != "" {
			outcome += " by " + user
		}
		log.Printf("grain ui: agent CLI %s: %s %s", name, action, outcome)
		if err := c.Store.RecordCredentialAccess(ctx, model.CredentialAccessEntry{
			Kind:       model.CredentialAccessConnection,
			Resource:   "cli-run/" + name,
			Action:     action,
			Credential: name,
			Outcome:    outcome,
		}); err != nil {
			log.Printf("grain ui: auditing agent CLI %s's run location: %v", name, err)
		}
	}
}

func trimCLIRun(s model.CLIRunSetting) model.CLIRunSetting {
	return model.CLIRunSetting{Location: strings.TrimSpace(s.Location)}
}

func (s *Server) handleGetCLIRunSettings(w http.ResponseWriter, r *http.Request) {
	out, err := s.tasks.cliRunSettings(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// updateCLIRunSettingsRequest is PUT /api/settings/cli-run's body.
type updateCLIRunSettingsRequest struct {
	Settings map[string]model.CLIRunSetting `json:"settings"`
}

func (s *Server) handleUpdateCLIRunSettings(w http.ResponseWriter, r *http.Request) {
	var req updateCLIRunSettingsRequest
	if !readJSON(w, r, &req) {
		return
	}
	out, err := s.tasks.UpdateCLIRunSettings(r.Context(), req.Settings)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
