package mcp

import (
	"context"
	"encoding/base64"
	"fmt"
	"mime"
	"net/http"
	"path"
	"strings"
)

const (
	maxResponseAttachmentSize  = 10 * 1024 * 1024
	maxResponseAttachmentCount = 10
)

// ResponseAttachment is one file an agent sends with something it says to a
// task. On the wire it is either base64 content, or the path of a file in
// the run's sandbox that grain reads when the tool is called
// (ResolveResponseAttachments) -- either way the bytes are held here, so
// they survive the sandbox disappearing.
type ResponseAttachment struct {
	Filename    string
	ContentType string
	Content     []byte
}

// AttachmentFiles is how an attachment given by path reaches the file it
// names: Read reads it out of the run's sandbox, and Stage holds what was
// read until the run's finish relays it -- the finish path gets the
// call's arguments back from the agent's transcript, which name the path
// and carry no bytes, and by then the sandbox may be gone
// (orchestrator.afterAgentRun reads them back by path). Stage is nil for
// a tool that delivers its attachments itself as it is called (chat).
// A nil Read refuses attachments by path, saying to send content; so
// must a caller with nowhere to stage what it reads, rather than read a
// file nothing will relay.
type AttachmentFiles struct {
	Read  SandboxFileReader
	Stage func(ctx context.Context, path string, a ResponseAttachment) error
}

func responseAttachmentsSchema() map[string]any {
	return map[string]any{
		"type": "array", "maxItems": maxResponseAttachmentCount,
		"description": "Optional images or files to attach to this response. Give each one as `path`, a file in your " +
			"sandbox (relative to where run_command starts, or absolute), which grain reads when you make this " +
			"call -- the way to attach a screenshot or anything else past a few kilobytes -- or as `content`, the " +
			"file's bytes encoded as base64 on one line (`base64 -w0`). Up to 10 MB each. Images are previewed in " +
			"the conversation (and, on a run that pushed a branch, shown in its pull request's description) and " +
			"other files are downloadable.",
		"items": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"filename": map[string]any{"type": "string", "description": "The name to show; defaults to the last element of path."},
				"content_type": map[string]any{"type": "string", "description": "MIME type, such as image/png; " +
					"defaults to one worked out from the filename's extension or the bytes themselves."},
				"path":    map[string]any{"type": "string", "description": "A file in your sandbox to attach. Give this or content, not both."},
				"content": map[string]any{"type": "string", "description": "The file bytes encoded as base64. Give this or path, not both."},
			},
		},
	}
}

// ResponseAttachments validates and decodes the optional attachments argument
// shared by ask_question, comment_on_issue, and chat, reading any given
// by path through files. Each one read by path is also staged, when files
// has a Stage, under the path exactly as the call wrote it.
func ResponseAttachments(ctx context.Context, args map[string]any, files AttachmentFiles) ([]ResponseAttachment, error) {
	items, err := responseAttachmentItems(args)
	if err != nil || items == nil {
		return nil, err
	}
	if len(items) > maxResponseAttachmentCount {
		return nil, fmt.Errorf("at most %d attachments are allowed", maxResponseAttachmentCount)
	}
	out := make([]ResponseAttachment, 0, len(items))
	for i, rawItem := range items {
		attachment, err := readResponseAttachment(ctx, i, rawItem, files)
		if err != nil {
			if attachment.Filename != "" {
				return nil, fmt.Errorf("attachment %q %w", attachment.Filename, err)
			}
			return nil, err
		}
		out = append(out, attachment)
	}
	return out, nil
}

// readResponseAttachment is decodeResponseAttachment for a call being
// made, while the sandbox is there to read: an item given by path is read
// through files and staged.
func readResponseAttachment(ctx context.Context, i int, rawItem any, files AttachmentFiles) (ResponseAttachment, error) {
	p, byPath, err := responseAttachmentPath(i, rawItem)
	if err != nil || !byPath {
		return decodeResponseAttachment(i, rawItem, nil)
	}
	item := rawItem.(map[string]any)
	filename := responseAttachmentFilename(item)
	if files.Read == nil {
		return ResponseAttachment{Filename: filename}, fmt.Errorf("cannot be attached by path on this run, " +
			"which has no way to read it out of the sandbox and keep it: send its bytes as base64 content instead")
	}
	content, err := files.Read(ctx, p, maxResponseAttachmentSize)
	if err != nil {
		if strings.HasPrefix(err.Error(), "is ") {
			return ResponseAttachment{Filename: filename}, err
		}
		return ResponseAttachment{Filename: filename}, fmt.Errorf("could not be read from %s: %w", p, err)
	}
	attachment := ResponseAttachment{Filename: filename, ContentType: responseAttachmentType(item, filename, content), Content: content}
	if files.Stage != nil {
		if err := files.Stage(ctx, p, attachment); err != nil {
			return ResponseAttachment{Filename: filename}, fmt.Errorf("was read from %s but could not be held for relay: %w", p, err)
		}
	}
	return attachment, nil
}

// responseAttachmentPath is the path an attachment item names, and
// whether it names one at all. An item that names both a path and
// content is refused: which one was meant is a guess.
func responseAttachmentPath(i int, rawItem any) (string, bool, error) {
	item, ok := rawItem.(map[string]any)
	if !ok {
		return "", false, fmt.Errorf("attachment %d must be an object", i+1)
	}
	p, _ := argString(item, "path")
	p = strings.TrimSpace(p)
	if p == "" {
		return "", false, nil
	}
	if content, _ := argString(item, "content"); content != "" {
		return "", false, fmt.Errorf("gives both path and content: give one")
	}
	return p, true, nil
}

// DroppedAttachment is one attachment ResponseAttachmentsTolerant could
// not decode: the name it went by (as best it could be read) and why.
type DroppedAttachment struct {
	Filename string
	Reason   string
}

// ResponseAttachmentsTolerant is ResponseAttachments for a run that has
// already finished, where an attachment that will not decode is no longer
// something the agent can fix: rather than failing the whole call, it
// keeps every attachment that decodes and reports the rest as dropped, so
// the words they came with are still relayed (grain/task-1064).
//
// staged is what the run's calls read by path (AttachmentFiles.Stage),
// keyed by the path as the call wrote it; an item naming a path nothing
// was staged under is dropped, saying so.
func ResponseAttachmentsTolerant(args map[string]any, staged map[string]ResponseAttachment) ([]ResponseAttachment, []DroppedAttachment) {
	items, err := responseAttachmentItems(args)
	if err != nil {
		return nil, []DroppedAttachment{{Filename: "attachments", Reason: err.Error()}}
	}
	var out []ResponseAttachment
	var dropped []DroppedAttachment
	for i, rawItem := range items {
		if i >= maxResponseAttachmentCount {
			dropped = append(dropped, DroppedAttachment{
				Filename: responseAttachmentFilename(rawItem),
				Reason:   fmt.Sprintf("at most %d attachments are allowed", maxResponseAttachmentCount),
			})
			continue
		}
		attachment, err := decodeResponseAttachment(i, rawItem, staged)
		if err != nil {
			dropped = append(dropped, DroppedAttachment{Filename: responseAttachmentFilename(rawItem), Reason: strings.TrimPrefix(err.Error(), "is ")})
			continue
		}
		out = append(out, attachment)
	}
	return out, dropped
}

// responseAttachmentItems is the attachments argument as a list, nil when
// there is none.
func responseAttachmentItems(args map[string]any) ([]any, error) {
	raw, exists := args["attachments"]
	if !exists || raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("attachments must be an array")
	}
	return items, nil
}

// responseAttachmentFilename is the filename an attachment item gives,
// stripped of any directory and capped in length -- or, when it gives
// none worth keeping, the last element of the path it names, or
// "attachment".
func responseAttachmentFilename(rawItem any) string {
	item, _ := rawItem.(map[string]any)
	if filename := cleanAttachmentFilename(item, "filename"); filename != "" {
		return filename
	}
	if filename := cleanAttachmentFilename(item, "path"); filename != "" {
		return filename
	}
	return "attachment"
}

// cleanAttachmentFilename is item[key]'s last path element, capped in
// length, or "" when there is none worth keeping.
func cleanAttachmentFilename(item map[string]any, key string) string {
	filename, ok := argString(item, key)
	filename = strings.TrimSpace(filename)
	filename = strings.ReplaceAll(filename, "\\", "/")
	if slash := strings.LastIndex(filename, "/"); slash >= 0 {
		filename = filename[slash+1:]
	}
	if !ok || filename == "" || filename == "." || filename == ".." {
		return ""
	}
	if len(filename) > 200 {
		filename = filename[len(filename)-200:]
	}
	return filename
}

// responseAttachmentType is the content type an item gives, or failing
// that the one its filename's extension names, or failing that the one
// its bytes look like (http.DetectContentType, which ends at
// application/octet-stream).
func responseAttachmentType(item map[string]any, filename string, content []byte) string {
	contentType, _ := argString(item, "content_type")
	if contentType = strings.TrimSpace(contentType); contentType != "" {
		return contentType
	}
	if byExt := mime.TypeByExtension(path.Ext(filename)); byExt != "" {
		return byExt
	}
	return http.DetectContentType(content)
}

// decodeResponseAttachment validates and decodes the i'th (zero-based)
// attachment item, finding one given by path in staged. An item that is
// not an object fails with a whole sentence; any other failure returns
// the filename alone, with an error that reads as the rest of a sentence
// about it ("is not valid base64").
func decodeResponseAttachment(i int, rawItem any, staged map[string]ResponseAttachment) (ResponseAttachment, error) {
	p, byPath, err := responseAttachmentPath(i, rawItem)
	item, _ := rawItem.(map[string]any)
	if err != nil {
		if item == nil {
			return ResponseAttachment{}, err
		}
		return ResponseAttachment{Filename: responseAttachmentFilename(item)}, err
	}
	filename := responseAttachmentFilename(item)
	if byPath {
		held, ok := staged[p]
		if !ok {
			return ResponseAttachment{Filename: filename}, fmt.Errorf("was not read from %s while the run was live", p)
		}
		// The call's own filename and content_type, as it gave them;
		// what was staged was worked out from those same arguments.
		held.Filename = filename
		return held, nil
	}
	encoded, ok := argString(item, "content")
	if !ok || encoded == "" {
		return ResponseAttachment{Filename: filename}, fmt.Errorf("needs a path or base64 content")
	}
	content, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return ResponseAttachment{Filename: filename}, fmt.Errorf("is not valid base64 "+
			"(encode it on one line with `base64 -w0`, or give the file's path instead): %w", err)
	}
	if len(content) > maxResponseAttachmentSize {
		return ResponseAttachment{Filename: filename}, fmt.Errorf("is over the %d byte limit", maxResponseAttachmentSize)
	}
	return ResponseAttachment{Filename: filename, ContentType: responseAttachmentType(item, filename, content), Content: content}, nil
}
