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

package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const qaUsage = "usage: grain qa <status|enable|disable> [flags]"

type qaStatusView struct {
	Enabled         bool     `json:"enabled"`
	FlowID          string   `json:"flowId"`
	AvailableFlows  []string `json:"availableFlows"`
	RolloutPct      int      `json:"rolloutPct"`
	ShadowOnControl bool     `json:"shadowOnControl"`
	Epoch           int      `json:"epoch"`
	RegimeID        int      `json:"regimeId"`
}

func qaStatusFromSettings(s ui.Settings) qaStatusView {
	flows := s.QAFlowIDs
	if len(flows) == 0 {
		flows = model.QAFlowIDs()
	}
	flow := s.QAFlowID
	if strings.TrimSpace(flow) == "" {
		flow = model.DefaultQAFlowID
	}
	epoch := s.QAEpoch
	if epoch < 1 {
		epoch = 1
	}
	regime := s.QARegimeID
	if regime < 1 {
		regime = 1
	}
	return qaStatusView{
		Enabled:         s.QAEnabled,
		FlowID:          flow,
		AvailableFlows:  flows,
		RolloutPct:      s.QARolloutPct,
		ShadowOnControl: s.QAShadowOnControl,
		Epoch:           epoch,
		RegimeID:        regime,
	}
}

func printQAStatus(out *printer, s ui.Settings) {
	v := qaStatusFromSettings(s)
	if out.json {
		out.encode(v)
		return
	}
	stateLabel := "off"
	if v.Enabled {
		stateLabel = fmt.Sprintf("enabled (%d%% PR rollout)", v.RolloutPct)
	}
	fmt.Printf("quality control: %s\n", stateLabel)
	fmt.Printf("  flow:              %s\n", v.FlowID)
	fmt.Printf("  rollout:           %d%%\n", v.RolloutPct)
	fmt.Printf("  shadow on control: %v\n", v.ShadowOnControl)
	fmt.Printf("  epoch / regime:    %d / %d\n", v.Epoch, v.RegimeID)
}

func cmdQA(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	sub := "status"
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		rest = args[1:]
	}

	switch sub {
	case "status":
		fs := flag.NewFlagSet("grain qa status", flag.ContinueOnError)
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if err := noArgs(fs, tail); err != nil {
			return err
		}
		s, err := c.GetSettings(ctx)
		if err != nil {
			return err
		}
		printQAStatus(out, s)
		return nil

	case "enable":
		fs := flag.NewFlagSet("grain qa enable", flag.ContinueOnError)
		flow := fs.String("flow", "", "quality flow package ("+strings.Join(model.QAFlowIDs(), ", ")+")")
		rollout := fs.Int("rollout", 30, "percentage of work-tree PRs assigned to treatment (0..100); defaults to 30 when unconfigured")
		shadow := fs.Bool("shadow-control", true, "run passive shadow checks on control-arm PRs")
		epoch := fs.Int("epoch", 0, "experiment epoch (>=1) to re-randomize sticky work-tree assignments")
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if err := noArgs(fs, tail); err != nil {
			return err
		}
		setFlags := map[string]bool{}
		fs.Visit(func(f *flag.Flag) {
			setFlags[f.Name] = true
		})

		enabled := true
		req := ui.UpdateSettingsRequest{
			QAEnabled: &enabled,
		}
		if setFlags["flow"] {
			req.QAFlowID = flow
		}
		if setFlags["rollout"] {
			req.QARolloutPct = rollout
		} else {
			current, err := c.GetSettings(ctx)
			if err != nil {
				return err
			}
			if current.QARolloutPct == 0 && !current.QAEnabled {
				defaultRollout := 30
				req.QARolloutPct = &defaultRollout
			}
		}
		if setFlags["shadow-control"] {
			req.QAShadowOnControl = shadow
		}
		if setFlags["epoch"] {
			req.QAEpoch = epoch
		}

		updated, err := c.UpdateSettings(ctx, req)
		if err != nil {
			return err
		}
		printQAStatus(out, updated)
		return nil

	case "disable":
		fs := flag.NewFlagSet("grain qa disable", flag.ContinueOnError)
		tail, err := parseArgs(fs, rest)
		if err != nil {
			return err
		}
		if err := noArgs(fs, tail); err != nil {
			return err
		}
		enabled := false
		updated, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{QAEnabled: &enabled})
		if err != nil {
			return err
		}
		printQAStatus(out, updated)
		return nil

	default:
		return usageErrorf("%s", qaUsage)
	}
}
