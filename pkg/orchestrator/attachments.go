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

package orchestrator

import (
	"context"
	"fmt"
	"path"
	"strings"
	"unicode"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// AttachmentsDir is where a dispatched run finds every file its task or
// its conversation carries (bwsalmon/agents#522), relative to the
// sandbox's own working directory -- named apart from CheckoutDir
// ("work") so a repo clone never collides with it, and fixed rather than
// configurable so BuildPrompt can tell an agent exactly where to look.
const AttachmentsDir = "attachments"

// writeFileTool picks the write_file handler out of the tools a slot's
// sandbox exposes -- runCommandTool's own counterpart (checkout.go), and
// for the same reason: it is the one call already right for both a local
// HostSandboxes directory and a GranuleSandboxes VM reached over SSH
// (mcp.NewSandboxTools and mcp.NewSSHSandboxTools both expose it under
// this name). A capability's own SideSandbox placement (applyPlacements)
// has no such reach: it only ever lands where a rootedSandbox
// hands out a local directory to write into directly (cycle.go's own
// runOne refuses a task's capability grants against anything else). An
// attachment inherits no such restriction, which is the whole reason to
// place it this way instead.
func writeFileTool(tools []mcp.Tool) (func(context.Context, map[string]any) mcp.Result, bool) {
	for _, t := range tools {
		if t.Name == "write_file" && t.Handler != nil {
			return t.Handler, true
		}
	}
	return nil, false
}

// attachmentPath is where one attachment lands inside AttachmentsDir --
// its own id first, so two attachments can never collide even when a
// human (or an earlier attempt relaying its own upload) reused a
// filename, followed by the filename itself so an agent reading a
// directory listing still sees a name that means something. The filename
// is sanitised again here (attachmentFilename) rather than trusted from
// whichever entry point stored it, since the same string becomes both the
// sandbox path and a line of the prompt.
func attachmentPath(a model.Attachment) string {
	return fmt.Sprintf("%s/%d-%s", AttachmentsDir, a.ID, attachmentFilename(a.Filename))
}

// attachmentFilename reduces a stored filename to one safe path element:
// any directory component (either separator) is dropped, and every control
// character -- a newline above all, which would otherwise break out of the
// prompt's file list into free text -- becomes '_', as do the Unicode line
// and paragraph separators. pkg/ui and pkg/mcp already strip directories on
// the way in; this is defence in depth for rows written before them, or by
// an entry point that forgot.
func attachmentFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return '_'
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." || name == "/" {
		return "attachment"
	}
	return name
}

// placeAttachments writes every one of attachments (a task's own body's,
// plus every comment's -- Store.Attachments already returns both) into
// the sandbox via the slot's own write_file tool, and returns a prompt
// section naming where each one landed -- "" if there are none. Content
// crosses this call as a plain Go string, byte for byte: unlike an
// attachment's own trip through the JSON API (where it must be base64,
// since a JSON string has to be valid UTF-8), this is an ordinary
// in-process function call with no encoding boundary of its own to cross,
// the same reasoning that lets applyPlacements write a capability's own
// Placement.Content with a bare []byte(content) conversion.
func placeAttachments(ctx context.Context, tools []mcp.Tool, attachments []model.Attachment) (string, error) {
	if len(attachments) == 0 {
		return "", nil
	}
	write, ok := writeFileTool(tools)
	if !ok {
		return "", fmt.Errorf("orchestrator: slot's sandbox exposes no write_file tool to place attachments with")
	}

	var b strings.Builder
	b.WriteString("\n\nFiles attached to this task are available in the sandbox:\n")
	for _, a := range attachments {
		p := attachmentPath(a)
		if result := write(ctx, map[string]any{"file_path": p, "content": string(a.Content)}); result.IsError {
			return "", fmt.Errorf("orchestrator: writing attachment %d (%q): %s", a.ID, a.Filename, strings.TrimSpace(result.Text))
		}
		origin := "the task description"
		if a.CommentID != nil {
			origin = fmt.Sprintf("comment #%d", *a.CommentID)
		}
		fmt.Fprintf(&b, "- ./%s (from %s)\n", p, origin)
	}
	return b.String(), nil
}
