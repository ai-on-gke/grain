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

// sandbox_budget_cmd.go is `grain sandbox-budget`: whether one sandbox of the
// deployment's own shape fits inside its sandbox budget, the same check
// grain init makes (hostinit.CheckSandboxBudget). scripts/setup.sh's
// readiness report runs it, which is how a host too small to admit any
// run is told so by setup.sh as well as by grain init.
package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ai-on-gke/grain/pkg/hostinit"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func cmdSandboxBudget(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain sandbox-budget", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noArgs(fs, fs.Args()); err != nil {
		return err
	}
	v, err := sandboxBudget(ctx, c)
	if err != nil {
		return err
	}
	if out.json {
		out.encode(sandboxBudgetJSON(v))
		return nil
	}
	printSandboxBudget(os.Stdout, v)
	return nil
}

// sandboxBudget asks the daemon for its settings and checks them. The
// settings go back through JSON because that is the shape grain init's
// check reads them in, off the same endpoint.
func sandboxBudget(ctx context.Context, c *ui.HTTPClient) (hostinit.SandboxBudgetVerdict, error) {
	settings, err := c.GetSettings(ctx)
	if err != nil {
		return hostinit.SandboxBudgetVerdict{}, err
	}
	body, err := json.Marshal(settings)
	if err != nil {
		return hostinit.SandboxBudgetVerdict{}, err
	}
	return hostinit.CheckSandboxBudget(body)
}

// sandboxBudgetView is `grain sandbox-budget -json`.
type sandboxBudgetView struct {
	Verdict string         `json:"verdict"`
	Detail  string         `json:"detail"`
	Fix     map[string]any `json:"fix,omitempty"`
	Command string         `json:"command,omitempty"`
}

func sandboxBudgetJSON(v hostinit.SandboxBudgetVerdict) sandboxBudgetView {
	return sandboxBudgetView{Verdict: sandboxBudgetWord(v), Detail: v.Detail, Fix: v.Fix, Command: v.SettingsCommand()}
}

// sandboxBudgetWord is the verdict's first word: "fits", "unchecked" or
// "admits-nothing". setup.sh's report_readiness matches on it, so it is
// part of this command's contract.
func sandboxBudgetWord(v hostinit.SandboxBudgetVerdict) string {
	switch {
	case !v.Checked:
		return "unchecked"
	case v.Fits:
		return "fits"
	default:
		return "admits-nothing"
	}
}

// printSandboxBudget prints the verdict as "<word>: <detail>", then
// "fix: <grain settings command>" when there are settings that would fit.
func printSandboxBudget(w io.Writer, v hostinit.SandboxBudgetVerdict) {
	fmt.Fprintf(w, "%s: %s\n", sandboxBudgetWord(v), v.Detail)
	if cmd := v.SettingsCommand(); cmd != "" {
		fmt.Fprintf(w, "fix: %s\n", cmd)
	}
}
