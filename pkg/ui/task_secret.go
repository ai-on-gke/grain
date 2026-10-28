package ui

import (
	"context"
	"fmt"
	"net/http"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/secrets"
)

// A task parked on mcp's request_secret escape hatch is a task waiting
// for one value, and this is the whole of what grain does with it: a
// box in the task pane, a PUT that lands in the secrets store, and the
// same un-parking a reply would have done.
//
// The value never touches the task. It is not a comment, so it is not in
// the conversation, not in the state repository's plaintext rows and not
// in the prompt the next run is handed (orchestrator's
// commentThreadSection reads comments). It is not returned by anything
// here either -- this package can write a secret and report that one
// exists, never read one back (see secrets.go and the secrets package's
// own doc comment), so "set it here" adds no way to get it out.
//
// What the run that asked gets is the effect rather than the material:
// its next attempt resolves the name it asked for -- through a
// capability's credential, or grain's own -- where the attempt that
// asked resolved nothing.

// pendingSecretFor is TaskDetail.PendingSecret: where the credential a
// parked run asked for would be written, and whether this deployment
// already holds one under that name.
//
// It reuses CapabilitySecret rather than declaring a second four-field
// shape, because it is the same question that type already answers --
// given a credential *name*, which secret and key does a write address,
// and is it set? -- asked about a task's pending request instead of a
// capability's declared one. capabilitySecretsFor is the one
// implementation of the answer, so the two cannot disagree about where a
// bare "<secret>" name lands.
//
// A UI with no secrets store to write to (Config.Secrets nil -- `grain
// demo`) still gets the row, with Set false: the request is a real fact
// about the task either way, and hiding it would leave such a deployment
// showing a task parked on nothing anyone could see. Writing is what
// fails there, in handleSetTaskSecret, with errSecretsUnavailable saying
// why.
func (c *Client) pendingSecretFor(obs *model.Observation) *CapabilitySecret {
	if obs == nil || obs.PendingSecret == "" {
		return nil
	}
	var list []secrets.SecretInfo
	if c.Config.Secrets != nil {
		stored, err := c.Config.Secrets.List()
		if err == nil {
			list = stored
		}
	}
	secret := capabilitySecretsFor([]string{obs.PendingSecret}, list)[0]
	return &secret
}

// SetPendingSecret stores the value a human typed for the credential a
// parked run asked for, and un-parks the task.
//
// The task's own observation is what says which secret this is -- never
// the request -- so a caller cannot address this at some other name:
// the only value writable through this endpoint is the one a run
// actually asked for, on a task that is actually parked on it. Anything
// else is a 404 or a 400 rather than a write.
//
// The three effects happen in the order that is safe to interrupt.
// First the value lands in the secrets store, which is the part the
// human came to do. Then the task is told, with a comment naming the
// secret and no value in it, which is also what clears
// PendingQuestionCommentID and requeues the task -- AddComment's own
// "reply reopens" rule, reused rather than reimplemented, so a secret
// set and a question answered un-park a task by exactly the same path.
// Then PendingSecret is cleared, so the box stops being offered: that
// same AddComment clears it whenever it un-parks a task, and clearing it
// again here is what makes it unconditional rather than contingent on
// the task having been parked in the state this expects.
//
// A failure after the first step leaves a set secret on a still-parked
// task: the human sees the box again, and setting it a second time
// overwrites the same key with the same value. That is the direction
// worth failing in -- the alternative, clearing the request first, ends
// with a task nobody is being asked anything about and a run that will
// fail again for want of the credential.
func (c *Client) SetPendingSecret(ctx context.Context, id, value string) error {
	store := c.Config.Secrets
	if store == nil {
		return &NotFoundError{message: errSecretsUnavailable.Error()}
	}
	if value == "" {
		return validationErrorf("value is required")
	}
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return &NotFoundError{ID: id}
	}
	obs, err := c.Store.GetObservation(ctx, id)
	if err != nil {
		return err
	}
	if obs == nil || obs.PendingSecret == "" {
		return validationErrorf("task %s is not waiting on a secret", id)
	}
	target := c.pendingSecretFor(obs)
	if err := store.Set(target.Secret, target.Key, []byte(value)); err != nil {
		return validationErrorf("%s", err)
	}
	// The comment says the secret was set and nothing about what it is.
	// It is the human's own, not grain relaying anybody: they are the one
	// who acted, and the next run reads this thread to find out what
	// happened while it was parked.
	body := fmt.Sprintf("Set `%s`. Its value is in grain's secret store, not in this "+
		"conversation -- resolve it by name.", target.Name)
	if err := c.AddComment(ctx, id, body, nil); err != nil {
		return err
	}
	return c.Store.ObserveField(ctx, id, c.now(), func(o *model.Observation) {
		o.PendingSecret = ""
	})
}

type setPendingSecretRequest struct {
	Value string `json:"value"`
}

func (s *Server) handleSetTaskSecret(w http.ResponseWriter, r *http.Request) {
	var req setPendingSecretRequest
	if !readJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	if err := s.tasks.SetPendingSecret(r.Context(), id, req.Value); err != nil {
		writeClientError(w, err)
		return
	}
	// The task, not the secrets listing: this is a task endpoint, the
	// caller is the task pane, and what changed for it is that the task
	// is queued again with no secret pending. respondWithTask is what
	// every other mutation on a task already answers with.
	s.respondWithTask(w, r, id)
}
