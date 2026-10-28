package ui

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// StagedAttachment is POST /api/tasks/{id}/staged-attachments' answer.
// Live is false when the task had no run in flight to hold the file
// against, which is a 200 rather than an error for the reason Activity's
// own Live is: the run did nothing wrong, and there is no finish left to
// relay what it sent.
type StagedAttachment struct {
	Live bool `json:"live"`
}

// stagedAttachmentRequest is POST /api/tasks/{id}/staged-attachments'
// body: the path the run named, as it named it, and the file read from
// there.
type stagedAttachmentRequest struct {
	Path       string           `json:"path"`
	Attachment AttachmentUpload `json:"attachment"`
}

// StageAttachment holds a file id's live run attached by path to
// comment_on_issue or ask_question until that run is finished and its
// words relayed (model.Store.StageAttachment).
//
// This exists for the agent, the way SetTaskActivity does: the run's MCP
// server reads the file out of the sandbox when the tool is called
// (mcp.ResponseAttachments), and this is the only route it has back to
// the daemon that will relay it. The finish path finds the file again by
// the run and the path, since the call's arguments -- which are all the
// agent's transcript gives back -- carry the path and not the bytes.
func (c *Client) StageAttachment(ctx context.Context, id, path string, upload AttachmentUpload) (StagedAttachment, error) {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return StagedAttachment{}, err
	}
	if task == nil {
		return StagedAttachment{}, &NotFoundError{ID: id}
	}
	if strings.TrimSpace(path) == "" {
		return StagedAttachment{}, validationErrorf("a staged attachment needs the path it was read from")
	}
	now := time.Now().UTC()
	decoded, err := decodeAttachments([]AttachmentUpload{upload}, now)
	if err != nil {
		return StagedAttachment{}, err
	}
	a := decoded[0]
	live, err := c.Store.StageAttachment(ctx, id, model.StagedAttachment{
		Path: path, Filename: a.Filename, ContentType: a.ContentType, Content: a.Content, CreatedAt: now,
	})
	if err != nil {
		return StagedAttachment{}, err
	}
	return StagedAttachment{Live: live}, nil
}

// handleStageAttachment answers POST /api/tasks/{id}/staged-attachments.
func (s *Server) handleStageAttachment(w http.ResponseWriter, r *http.Request) {
	var req stagedAttachmentRequest
	if !readAttachmentJSON(w, r, &req, func(req *stagedAttachmentRequest) []AttachmentUpload {
		return []AttachmentUpload{req.Attachment}
	}) {
		return
	}
	staged, err := s.tasks.StageAttachment(r.Context(), r.PathValue("id"), req.Path, req.Attachment)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, staged)
}
