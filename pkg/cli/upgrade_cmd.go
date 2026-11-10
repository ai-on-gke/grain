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
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const upgradeUsage = `usage: grain upgrade [subcommand] [flags]

Manage in-place upgrades of a running grain deployment (Settings -> Upgrade).
For connected Grain Hub instances, target the instance with -context <name>.
Hosted Grain Hub instances disable in-VM self-upgrade and replace their VM
externally via "grain hub upgrade <name>" instead.

Subcommands:
  status                              show running build, scheduled upgrade, or current/last upgrade (default)
  start   [-after-tasks-finish] [-image <ref>] [<branch>]
                                      start or schedule an upgrade onto <branch> (defaults to deployment channel)
  cancel  [-reason <why>] [-scheduled]
                                      cancel an upgrade currently in progress (or a scheduled upgrade waiting to drain)
  rollback [-after-tasks-finish]      roll back to the image or commit the last completed upgrade replaced`

func cmdUpgrade(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	if len(args) == 0 {
		args = []string{"status"}
	}
	sub, subArgs := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		fmt.Println(upgradeUsage)
		return nil
	case "status":
		return cmdUpgradeStatus(ctx, c, out, subArgs)
	case "start":
		return cmdUpgradeStart(ctx, c, out, subArgs)
	case "cancel":
		return cmdUpgradeCancel(ctx, c, out, subArgs)
	case "rollback":
		return cmdUpgradeRollback(ctx, c, out, subArgs)
	default:
		return usageErrorf("unknown \"grain upgrade\" subcommand %q: one of status, start, cancel, rollback", sub)
	}
}

func cmdUpgradeStatus(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain upgrade status", flag.ContinueOnError)
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if err := noArgs(fs, rest); err != nil {
		return err
	}
	st, err := c.UpgradeStatus(ctx)
	if err != nil {
		return err
	}
	if out.json {
		out.encode(st)
		return nil
	}
	printUpgradeStatus(st)
	return nil
}

func cmdUpgradeStart(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain upgrade start", flag.ContinueOnError)
	after := fs.Bool("after-tasks-finish", false, "hold the queue and wait for running tasks and CI jobs to finish before upgrading")
	image := fs.String("image", "", "explicit image repository, repo:tag or repo@sha256:<digest> to pull (-upgrade-image deployments only)")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return usageErrorf("usage: grain upgrade start [-after-tasks-finish] [-image <ref>] [<branch>]")
	}
	branch := ""
	if len(rest) == 1 {
		branch = strings.TrimSpace(rest[0])
	}
	st, err := c.StartUpgradeImage(ctx, branch, strings.TrimSpace(*image), *after, model.Principal{})
	if err != nil {
		return err
	}
	if out.json {
		out.encode(st)
		return nil
	}
	if *after && st.Scheduled != nil {
		fmt.Printf("scheduled upgrade to %s (waiting on %d active run(s), %d CI run(s))\n",
			st.Scheduled.Branch, st.Scheduled.BusyRuns, st.Scheduled.BusyCIRuns)
		return nil
	}
	fmt.Printf("started upgrade to %s (phase=%s)\n", st.Branch, st.Phase)
	if st.GraceUntil != nil {
		if rem := time.Until(*st.GraceUntil).Round(time.Second); rem > 0 {
			fmt.Printf("grace window: %s remaining to cancel with \"grain upgrade cancel\"\n", rem)
		}
	}
	return nil
}

func cmdUpgradeCancel(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain upgrade cancel", flag.ContinueOnError)
	reason := fs.String("reason", "", "why the upgrade is being cancelled (recorded in the deployment's audit log)")
	scheduledOnly := fs.Bool("scheduled", false, "cancel only a scheduled upgrade waiting for tasks to finish")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 0 && strings.TrimSpace(*reason) == "" {
		joined := strings.TrimSpace(strings.Join(rest, " "))
		reason = &joined
	} else if len(rest) > 0 {
		return usageErrorf("usage: grain upgrade cancel [-reason <why>] [-scheduled]")
	}

	if *scheduledOnly {
		st, cancelled, err := c.CancelScheduledUpgrade(ctx)
		if err != nil {
			return err
		}
		if out.json {
			out.encode(map[string]any{"cancelled": cancelled, "scheduled": true, "status": st})
			return nil
		}
		if cancelled {
			fmt.Println("cancelled scheduled upgrade")
		} else {
			fmt.Println("no scheduled upgrade was pending")
		}
		return nil
	}

	st, cancelled, err := c.CancelCurrentUpgrade(ctx, strings.TrimSpace(*reason))
	if err != nil {
		return err
	}
	if cancelled {
		if out.json {
			out.encode(map[string]any{"cancelled": true, "status": st})
			return nil
		}
		fmt.Printf("cancelled upgrade to %s (%s)\n", st.Branch, st.Detail)
		return nil
	}
	if st.Scheduled != nil {
		schedBranch := st.Scheduled.Branch
		stSched, schedCancelled, schedErr := c.CancelScheduledUpgrade(ctx)
		if schedErr != nil {
			return schedErr
		}
		if out.json {
			out.encode(map[string]any{"cancelled": schedCancelled, "scheduled": true, "status": stSched})
			return nil
		}
		if schedCancelled {
			fmt.Printf("cancelled scheduled upgrade to %s\n", schedBranch)
			return nil
		}
	}
	if out.json {
		out.encode(map[string]any{"cancelled": false, "status": st})
		return nil
	}
	fmt.Println("no upgrade is currently in progress")
	return nil
}

func cmdUpgradeRollback(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain upgrade rollback", flag.ContinueOnError)
	after := fs.Bool("after-tasks-finish", false, "hold the queue and wait for running tasks and CI jobs to finish before rolling back (source/branch rollback only)")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if err := noArgs(fs, rest); err != nil {
		return err
	}
	cur, err := c.UpgradeStatus(ctx)
	if err != nil {
		return err
	}
	if !cur.Enabled {
		return errors.New("in-place upgrades are not enabled on this deployment")
	}
	if cur.PreviousImage != "" {
		if *after {
			return errors.New("rolling back to a pinned previous image runs immediately; omit -after-tasks-finish")
		}
		st, err := c.StartUpgradeImage(ctx, "", cur.PreviousImage, false, model.Principal{})
		if err != nil {
			return err
		}
		if out.json {
			out.encode(st)
			return nil
		}
		label := cur.PreviousImageLabel
		if label == "" {
			label = cur.PreviousImage
		}
		fmt.Printf("started rollback to %s (phase=%s)\n", label, st.Phase)
		return nil
	}
	target := cur.PreviousRelease
	if target == "" {
		target = cur.PreviousCommit
	}
	if target == "" {
		return errors.New("no previous image or build is recorded on the last upgrade to roll back to")
	}
	st, err := c.StartUpgrade(ctx, target, *after, model.Principal{})
	if err != nil {
		return err
	}
	if out.json {
		out.encode(st)
		return nil
	}
	fmt.Printf("started rollback to %s (phase=%s)\n", target, st.Phase)
	return nil
}

func printUpgradeStatus(st ui.UpgradeStatus) {
	if !st.Enabled {
		fmt.Println("upgrade: disabled or not configured on this deployment")
		return
	}
	running := st.RunningImage
	if running == "" {
		running = st.RunningCommit
	}
	if running != "" {
		fmt.Printf("running: %s (mode=%s)\n", running, st.Mode)
	} else {
		fmt.Printf("mode: %s\n", st.Mode)
	}
	if st.Scheduled != nil {
		fmt.Printf("scheduled: %s (waiting on %d active run(s), %d CI run(s))\n",
			st.Scheduled.Branch, st.Scheduled.BusyRuns, st.Scheduled.BusyCIRuns)
	}
	if st.Phase != "" {
		fmt.Printf("last/current upgrade: branch=%s phase=%s\n", st.Branch, st.Phase)
		if st.Detail != "" {
			fmt.Printf("detail: %s\n", st.Detail)
		}
		if st.Cancellable {
			if st.GraceUntil != nil {
				if rem := time.Until(*st.GraceUntil).Round(time.Second); rem > 0 {
					fmt.Printf("cancellable: yes (%s grace window remaining)\n", rem)
				} else {
					fmt.Println("cancellable: yes")
				}
			} else {
				fmt.Println("cancellable: yes")
			}
		} else if st.CancelBlockedReason != "" {
			fmt.Printf("cancellable: no (%s)\n", st.CancelBlockedReason)
		}
		if st.Phase == "ok" && !st.Unchanged && (st.PreviousImage != "" || st.PreviousRelease != "" || st.PreviousCommit != "") {
			prev := st.PreviousImageLabel
			if prev == "" {
				prev = st.PreviousImage
			}
			if prev == "" {
				prev = st.PreviousRelease
			}
			if prev == "" {
				prev = st.PreviousCommit
			}
			fmt.Printf("replaced: %s (run \"grain upgrade rollback\" to go back)\n", prev)
		}
	}
}
