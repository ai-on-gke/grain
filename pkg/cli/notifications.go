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
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// cmdNotifications is `grain notifications clear`: POST
// /api/notifications/clear, the Inbox's "Clear all" from a terminal.
func cmdNotifications(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	if len(args) == 0 || args[0] != "clear" {
		return errors.New("usage: grain notifications clear [-subject prefix] [-severity info|warning|critical] [-older-than 7d] [-ids 1,2] [-dry-run] -reason <why>")
	}
	fs := flag.NewFlagSet("grain notifications clear", flag.ContinueOnError)
	subject := fs.String("subject", "", "clear only the notifications whose subject starts with this")
	severity := fs.String("severity", "", "clear only this severity: info, warning or critical")
	olderThan := fs.String("older-than", "", "clear only those last raised longer ago than this: a duration (90m, 12h) or days (7d)")
	ids := fs.String("ids", "", "clear only these notification ids, comma-separated")
	reason := fs.String("reason", "", "why they no longer need anyone, recorded in the audit log (required unless -dry-run)")
	dryRun := fs.Bool("dry-run", false, "count what would be cleared, and how many would come back, without clearing anything")
	tail, err := parseArgs(fs, args[1:])
	if err != nil {
		return err
	}
	if err := noArgs(fs, tail); err != nil {
		return err
	}
	req := ui.ClearNotificationsRequest{Subject: *subject, Severity: *severity, OlderThan: *olderThan,
		Reason: *reason, DryRun: *dryRun}
	for _, raw := range strings.Split(*ids, ",") {
		if raw = strings.TrimSpace(raw); raw == "" {
			continue
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("-ids: %q is not a notification id", raw)
		}
		req.IDs = append(req.IDs, id)
	}
	if strings.TrimSpace(req.Reason) == "" && !req.DryRun {
		return errors.New("grain notifications clear needs -reason: say why they no longer need anyone")
	}
	resp, err := c.ClearNotifications(ctx, req)
	if err != nil {
		return err
	}
	if out.json {
		out.encode(resp)
		return nil
	}
	if resp.DryRun {
		fmt.Printf("Would clear %d notification(s).\n", resp.Matched)
	} else {
		fmt.Printf("Cleared %d notification(s).\n", resp.Cleared)
	}
	if resp.ExpectedBack > 0 {
		fmt.Printf("%d of them will be raised again while their condition holds (%s).\n",
			resp.ExpectedBack, strings.Join(resp.ExpectedBackFamilies, ", "))
	}
	return nil
}
