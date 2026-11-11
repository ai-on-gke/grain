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

	"github.com/ai-on-gke/grain/pkg/ui"
)

// cmdCompact is `grain compact <id>`: fold a task's conversation into a
// handover so later prompts carry the handover and only what comes after
// it (design/design/durable-tasks.design.md#DT-15). With -handover-file
// the handover is the file's and the task is compacted now; without, the
// task's next run is asked to compact before anything else.
func cmdCompact(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain compact", flag.ContinueOnError)
	file := fs.String("handover-file", "", "compact now, to the handover in this file (- for stdin); without it, the task's next run is asked to compact first")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := atMostArgs(fs, fs.Args(), 1); err != nil {
		return err
	}
	id, err := taskID(fs.Arg(0))
	if err != nil {
		return err
	}
	var req ui.CompactRequest
	if *file != "" {
		body, err := readBodyFile(*file)
		if err != nil {
			return err
		}
		req.Handover = body
	}
	view, err := c.CompactTask(ctx, id, req)
	if err != nil {
		return err
	}
	out.taskContext(id, view)
	return nil
}

// cmdBrief is `grain brief <id>`: show a task's context -- its current
// brief, handover age and compaction cursor -- or, with -file and
// -reason, replace its current brief.
func cmdBrief(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain brief", flag.ContinueOnError)
	file := fs.String("file", "", "replace the current brief with this file (- for stdin); an empty file reverts to the original description")
	reason := fs.String("reason", "", "why the brief changed (required with -file)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := atMostArgs(fs, fs.Args(), 1); err != nil {
		return err
	}
	id, err := taskID(fs.Arg(0))
	if err != nil {
		return err
	}
	if *file == "" {
		view, err := c.TaskContext(ctx, id)
		if err != nil {
			return err
		}
		out.taskContext(id, view)
		return nil
	}
	if strings.TrimSpace(*reason) == "" {
		return usageErrorf("-reason is required with -file: say why the brief changed")
	}
	body, err := readBodyFile(*file)
	if err != nil {
		return err
	}
	view, err := c.SetTaskBrief(ctx, id, ui.BriefRequest{Brief: body, Reason: *reason})
	if err != nil {
		return err
	}
	out.taskContext(id, view)
	return nil
}

// cmdRollover is `grain rollover <id>`: file a successor to a durable
// task whose record has grown too large, carrying its current brief,
// handover and settings, and close the task as rolled over to it
// (design/design/durable-tasks.design.md#DT-15).
func cmdRollover(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain rollover", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := atMostArgs(fs, fs.Args(), 1); err != nil {
		return err
	}
	id, err := taskID(fs.Arg(0))
	if err != nil {
		return err
	}
	result, err := c.RolloverTask(ctx, id)
	if err != nil {
		return err
	}
	if out.json {
		out.encode(result)
		return nil
	}
	fmt.Printf("task %s rolled over to task %s\n", id, result.Successor)
	if len(result.Moved) > 0 {
		fmt.Printf("now waiting on task %s instead: %s\n", result.Successor, strings.Join(result.Moved, ", "))
	}
	for _, k := range result.Kept {
		fmt.Printf("task %s left waiting on task %s: %s\n", k.ID, id, k.Reason)
	}
	return nil
}

func (p *printer) taskContext(id string, v ui.TaskContextView) {
	if p.json {
		p.encode(v)
		return
	}
	fmt.Printf("task %s\n", id)
	if v.CurrentBrief != "" {
		by := v.BriefUpdatedBy
		fmt.Printf("current brief (by %s, %d edit(s); replaces the original description in prompts):\n%s\n",
			by, len(v.BriefEdits), v.CurrentBrief)
	} else {
		fmt.Println("current brief: none (prompts carry the original description)")
	}
	if v.HandoverAge.Exists {
		stale := ""
		if v.HandoverStale {
			stale = " -- stale"
		}
		fmt.Printf("handover: version %d, %d run(s) and %d comment(s) old%s\n",
			v.HandoverVersion, v.HandoverAge.Runs, v.HandoverAge.Comments, stale)
	} else {
		fmt.Println("handover: none")
	}
	if v.CompactedThrough > 0 {
		fmt.Printf("compacted through comment %d by %s: %d comment(s) summarised\n",
			v.CompactedThrough, v.CompactedBy, v.Summarised)
	}
	if v.CompactRequestedAt != nil {
		fmt.Printf("compaction requested by %s at %s: the next run compacts first\n",
			v.CompactRequestedBy, v.CompactRequestedAt.Format("2006-01-02 15:04 MST"))
	}
}
