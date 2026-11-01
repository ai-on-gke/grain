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
	"net/http"
	"slices"
	"strings"

	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/model"
)

// This file is the standing interactive sessions -- the director, and
// whatever else this deployment configures (model.ConsoleSession,
// grain/task-415) -- as an API.
//
// It is deliberately not part of Settings, even though a session is
// stored in grain_config with every other setting. What a person does
// here is have a conversation and, in the same pane, decide what the
// thing they are talking to may reach; making them find that on the
// Capabilities tab of a settings dialog, for a chat that is on screen in
// every view, would be a tab strip between somebody and the answer to
// "why can it not do that?". The save underneath is an ordinary
// read-modify-write of the config row, which is why there is no second
// record and nothing to keep in step.
//
// It is also not part of /api/console. That is the pseudo-terminal
// (pkg/console): a live process, byte offsets, a session that exists
// while somebody watches it. This is a task and a conversation, which
// outlives every run of it. The two share a word and nothing else.

// ConsoleSessionSetting is one standing session as this API reports it:
// what it is, what it may reach, and which task holds its conversation.
//
// "Setting" in the name because ConsoleSession in this package is
// already taken, by the live pseudo-terminal session on /api/console
// (console.go) -- see this file's own note on why the two share a word
// and nothing else. This one is the configured session; that one is a
// process somebody is watching.
type ConsoleSessionSetting struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Team is the team this session belongs to (model.DefaultTeamID when unset).
	Team string `json:"team,omitempty"`
	// TaskID is the task this session's chat is, or "" in the moment
	// between a session being configured and the next cycle filing its
	// task (orchestrator.reconcileConsoleSessions). A client shows the
	// session either way -- it is configured, so it exists -- and has
	// nothing to open until this is filled in.
	TaskID string `json:"taskId,omitempty"`
	// Capabilities are the capability ids this session's task is granted
	// -- what the pane's own tick boxes read and write.
	Capabilities []string `json:"capabilities"`
	// NetworkAccess is "" for the deployment's own default, or one of
	// model.NetworkAccesses().
	NetworkAccess string `json:"networkAccess,omitempty"`
	// TasksApproved is whether tasks this session proposes are filed
	// approved rather than waiting for a person. False is the default --
	// see model.ConsoleSession's own doc comment on why.
	TasksApproved bool `json:"tasksApproved"`
	// KeepRunning is whether the session's task immediately restarts when
	// its run exits rather than parking until the next message.
	KeepRunning bool `json:"keepRunning"`
	// Reads is every repo this session may read but never push to --
	// model.ConsoleSession.Reads, rendered as owner/name strings.
	Reads []string `json:"reads,omitempty"`
	// Writes is every repo this session may push to
	// (model.ConsoleSession.Writes).
	Writes []WriteTarget `json:"writes,omitempty"`
	// GoogleChatSpace is the Google Chat space linked to this session
	// (model.ConsoleSession.GoogleChatSpace).
	GoogleChatSpace string `json:"googleChatSpace,omitempty"`
	// Model is the model this session's agent runs on
	// (model.ConsoleSession.Model), or "" for its team's default model.
	Model string `json:"model,omitempty"`
	// Crowded is whether this conversation has begun to cost the runs it
	// is handed to: the last dispatch's prompt past
	// model.PromptWarnTokens, or a thread grain had to cut down to fit
	// (model.PromptThreadCutNotice). It is the only thing the pane's
	// offer of a fresh conversation is shown on -- never the thread's
	// age, which says nothing about what a prompt costs (docs/
	// implementation-notes.md, "A chat that never ends is not split for
	// you").
	//
	// False for a session nothing has been said to yet, and for one
	// whose exchanges are all ordinary: there is nothing to offer, so
	// nothing is offered.
	Crowded bool `json:"crowded,omitempty"`
	// CrowdedNote is the measurement behind Crowded, in words -- what
	// the last exchange's prompt came to, or that its conversation was
	// cut. A control that appears without saying why is a control
	// nobody can judge, and the size is the half of it a person can
	// check against the window it was taken against
	// (model.PromptSize.String).
	CrowdedNote string `json:"crowdedNote,omitempty"`
}

// ConsoleSessionSettingsResponse is what GET /api/console-sessions answers
// with: this deployment's sessions, and the capabilities a client may
// offer for one.
type ConsoleSessionSettingsResponse struct {
	Sessions []ConsoleSessionSetting `json:"sessions"`
	// Created is the id of the session a POST /api/console-sessions just
	// made, and "" on every other answer. Named rather than left for
	// the client to find by diffing the list: a title need not be unique
	// (a second "Night watch" is night-watch-2), and a session started
	// from a message has a title the client never chose.
	Created string `json:"created,omitempty"`
	// Capabilities is the same listing GET /api/config carries -- this
	// deployment's own picker rows, each told whether it is ready
	// (capabilitiesWithReadiness) -- repeated here so the pane that edits
	// a session's grants can draw its list without also loading Settings.
	// Same shape, same ids, and the same rows SetCapability and
	// UpdateConsoleSession validate an id against, so a session cannot be
	// offered a grant its own save would then refuse.
	//
	// Readiness is carried rather than merely reported: the pane offers
	// only what this deployment has configured, so without it every
	// unconfigured capability would go on being tickable here after the
	// rest of the app stopped offering them (configuredCapabilities,
	// ui/src/state.js).
	Capabilities []Capability `json:"capabilities"`
	// NetworkAccessOptions is model.NetworkAccesses() as strings, for
	// the same reason Settings carries it.
	NetworkAccessOptions []string `json:"networkAccessOptions"`
}

// UpdateConsoleSessionRequest is PUT /api/console-sessions/{id}: the
// fields of a session a person may change, each a pointer so that
// leaving one out means "unchanged" rather than "cleared", exactly as
// UpdateSettingsRequest does.
//
// What is not here is as deliberate as what is: a session's id, and what
// it is told (model.DirectorSessionBody), are not editable. The id is
// the identity of the conversation, and the prompt is grain's -- see
// model.ConsoleSession on why a settings form may not rewrite what an
// agent holding this deployment's own host is for.
type UpdateConsoleSessionRequest struct {
	Title            *string        `json:"title"`
	Team             *string        `json:"team"`
	Capabilities     *[]string      `json:"capabilities"`
	NetworkAccess    *string        `json:"networkAccess"`
	TasksApproved    *bool          `json:"tasksApproved"`
	KeepRunning      *bool          `json:"keepRunning,omitempty"`
	KeepRunningSnake *bool          `json:"keep_running,omitempty"`
	Reads            *[]string      `json:"reads"`
	Writes           *[]WriteTarget `json:"writes"`
	GoogleChatSpace  *string        `json:"googleChatSpace"`
	// Model is the model the session runs on; "" puts it back on its
	// team's default model.
	Model *string `json:"model"`

	// renameByAgent marks a Title written by the session's own agent
	// (RenameConsoleSessionOfTask) rather than by a person: it is
	// refused unless the title is still the heuristic one, and recorded
	// as model.TitleSourceAgent. Unexported, so no request body can set
	// it -- PUT /api/console-sessions/{id} is always a person's edit.
	renameByAgent bool
	// firstMessage is the first thing said in the session, which
	// renameByAgent needs to judge a session saved before titles
	// recorded their source (model.ConsoleSession.ResolvedTitleSource).
	firstMessage string
}

// CreateConsoleSessionRequest is POST /api/console-sessions: the parameters
// to create a new named interactive standing session.
//
// A session is started one of two ways. Named -- Title set, as a
// configured agent with a job ("Release coordinator") is -- or from its
// first message, the way a chat app starts a chat: Message (and/or
// Attachments) set and Title left blank, in which case the session is
// titled from what the message says
// (model.ConsoleSessionTitleFromMessage) and the message is posted into
// the new conversation in the same request, so the session's first
// exchange is already under way when the answer comes back. Nothing is
// created for a chat nobody has said anything in: a request with neither
// a title nor a message is refused, which is what keeps an opened-and-
// abandoned chat out of the sessions list.
type CreateConsoleSessionRequest struct {
	ID               string        `json:"id,omitempty"`
	Title            string        `json:"title,omitempty"`
	Team             string        `json:"team,omitempty"`
	Capabilities     []string      `json:"capabilities,omitempty"`
	NetworkAccess    string        `json:"networkAccess,omitempty"`
	TasksApproved    bool          `json:"tasksApproved,omitempty"`
	KeepRunning      bool          `json:"keepRunning,omitempty"`
	KeepRunningSnake *bool         `json:"keep_running,omitempty"`
	Reads            []string      `json:"reads,omitempty"`
	Writes           []WriteTarget `json:"writes,omitempty"`
	GoogleChatSpace  string        `json:"googleChatSpace,omitempty"`
	// Model is the model the session runs on, or "" for its team's
	// default model.
	Model string `json:"model,omitempty"`
	// Message is the first thing said in the session, posted as the
	// caller's comment on the session's task once it is filed; with
	// Title blank it is also what the session is named from.
	Message string `json:"message,omitempty"`
	// Attachments are files on that first message, exactly as POST
	// /api/tasks/{id}/comments takes them.
	Attachments []AttachmentUpload `json:"attachments,omitempty"`
	// TimeZone is the zone of whoever opened the session, recorded on its
	// task so its runs tell the time in it (noteRequesterTimeZone). POST
	// /api/console-sessions fills it from TimeZoneHeader when unset.
	TimeZone string `json:"timeZone,omitempty"`
}

// consoleSessionConfig is this deployment's stored settings, or the
// shipped defaults for one that has never saved any --
// capability.NewDeploymentConfig rather than a zero Config for
// plannedConfig's own reason: PutConfig binds every column, so a first
// save made from here must not write off a setting whose default is not
// its zero value.
func (c *Client) consoleSessionConfig(ctx context.Context) (model.Config, error) {
	stored, err := c.Store.GetConfig(ctx)
	if err != nil {
		return model.Config{}, err
	}
	if stored != nil {
		if stored.ConnectionsOnly() {
			return capability.NewDeploymentConfig().WithConnectionsOf(*stored), nil
		}
		return *stored, nil
	}
	return capability.NewDeploymentConfig(), nil
}

// consoleSessionOf is the session a task holds the conversation for, as
// the task list reports it -- model.ConsoleSessionIDOf, which reads it
// off the task's tags.
func consoleSessionOf(t model.Task) string {
	id, _ := model.ConsoleSessionIDOf(t)
	return id
}

// ListConsoleSessions is this deployment's standing sessions, each with
// the task holding its conversation looked up by tag.
//
// The list is model.ConsoleSessionsOr's, so a deployment that has never
// configured any is reported as having the director rather than as
// having none -- which is what it will have as soon as the next cycle
// runs, and reporting the gap would put a pane on screen that flickers
// into existence a minute later.
func (c *Client) ListConsoleSessions(ctx context.Context) ([]ConsoleSessionSetting, error) {
	cfg, err := c.consoleSessionConfig(ctx)
	if err != nil {
		return nil, err
	}
	sessions := model.ConsoleSessionsOr(cfg.ConsoleSessions)
	out := make([]ConsoleSessionSetting, 0, len(sessions))
	for _, s := range sessions {
		taskID, err := c.Store.TaskIDWithTag(ctx, model.ConsoleSessionTag(s.ID))
		if err != nil {
			return nil, err
		}
		var openTask *model.Task
		if taskID != "" {
			obs, err := c.Store.GetObservation(ctx, taskID)
			if err != nil {
				return nil, err
			}
			if obs != nil && obs.ClosedAt != nil {
				taskID = ""
			} else {
				openTask, err = c.Store.GetTask(ctx, taskID)
				if err != nil {
					return nil, err
				}
			}
		}
		title := model.ConsoleSessionTitle(s)
		capabilities := append([]string{}, s.Capabilities...)
		networkAccess := string(s.NetworkAccess)
		tasksApproved := s.TasksApproved
		keepRunning := s.KeepRunning
		googleChatSpace := s.GoogleChatSpace
		modelChoice := s.Model
		reads := make([]string, 0, len(s.Reads))
		for _, r := range s.Reads {
			reads = append(reads, r.String())
		}
		writes := make([]WriteTarget, 0, len(s.Writes))
		for _, w := range s.Writes {
			writes = append(writes, WriteTarget{
				Repo: w.Repo.String(),
				Base: w.Base,
			})
		}
		if openTask != nil {
			prev, hasPrev := model.LastSyncedConsoleSession(c.Store, openTask.ID)
			if c.configRepoOnly(ctx) && hasPrev && strings.TrimSpace(openTask.Title) != "" &&
				(openTask.Title != prev.Title || model.ConsoleSessionTitle(s) == prev.Title) {
				title = openTask.Title
			}
			if hasPrev {
				if len(openTask.Grants) > 0 {
					capabilities = make([]string, 0, len(openTask.Grants))
					for _, g := range openTask.Grants {
						if g.Capability == "" || g.Capability == model.DurableCapability {
							continue
						}
						if g.Via == model.GrantByDefault && !slices.Contains(s.Capabilities, g.Capability) {
							continue
						}
						if !slices.Contains(capabilities, g.Capability) {
							capabilities = append(capabilities, g.Capability)
						}
					}
				}
				if openTask.NetworkAccess != "" || prev.NetworkAccess != openTask.NetworkAccess {
					if !(s.NetworkAccess == model.NetworkNone && c.Config.SandboxShapeIgnored && openTask.NetworkAccess == model.NetworkOpen) {
						networkAccess = string(openTask.NetworkAccess)
					}
				}
				tasksApproved = model.ConsoleSessionQueuesProposals(sessions, *openTask)
				keepRunning = openTask.KeepRunning
				if openTask.Reads != nil || len(s.Reads) == 0 {
					stateRepo := c.stateRepoRef()
					reads = make([]string, 0, len(openTask.Reads))
					for _, r := range openTask.Reads {
						if stateRepo.Owner != "" && r.Same(stateRepo) &&
							!slices.ContainsFunc(s.Reads, func(sr model.RepoRef) bool { return sr.Same(stateRepo) }) {
							continue
						}
						reads = append(reads, r.String())
					}
				}
				if openTask.Writes != nil || len(s.Writes) == 0 {
					writes = make([]WriteTarget, 0, len(openTask.Writes))
					for _, w := range openTask.Writes {
						writes = append(writes, WriteTarget{
							Repo: w.Repo.String(),
							Base: w.Base,
						})
					}
				}
				if openTask.Model != "" || prev.Model != openTask.Model {
					modelChoice = openTask.Model
				}
				var linkedSpace string
				for _, l := range openTask.Links {
					if l.Kind == model.LinkGoogleChat {
						linkedSpace = l.Target
						break
					}
				}
				googleChatSpace = linkedSpace
			}
		}
		crowded, note, err := c.consoleSessionCrowding(ctx, taskID)
		if err != nil {
			return nil, err
		}
		team := s.Team
		if team == "" {
			team = model.DefaultTeamID
		}
		out = append(out, ConsoleSessionSetting{
			ID:              s.ID,
			Title:           title,
			Team:            team,
			TaskID:          taskID,
			Capabilities:    capabilities,
			NetworkAccess:   networkAccess,
			TasksApproved:   tasksApproved,
			KeepRunning:     keepRunning,
			Reads:           reads,
			Writes:          writes,
			GoogleChatSpace: googleChatSpace,
			Model:           modelChoice,
			Crowded:         crowded,
			CrowdedNote:     note,
		})
	}
	return out, nil
}

// consoleSessionCrowding is ConsoleSessionSetting.Crowded and the note
// under it, measured off the last dispatch of the task holding the
// conversation (model.Store.LastPrompt).
//
// The two tests are the two ways a thread makes itself felt, and both
// are recorded facts about the last exchange rather than guesses about
// this one: a prompt past model.PromptWarnTokens is a run that started
// with a quarter of its window already spent, and a cut thread is grain
// having dropped the middle of the conversation to fit. Age is
// deliberately not a third: a week-old chat of eleven messages needs
// nothing.
//
// A session with no task yet, or whose task no run has been dispatched
// for, is not crowded -- there is no measurement, and inventing one
// would put an offer on screen before there is anything to weigh.
func (c *Client) consoleSessionCrowding(ctx context.Context, taskID string) (bool, string, error) {
	if taskID == "" {
		return false, "", nil
	}
	size, threadCut, err := c.Store.LastPrompt(ctx, taskID)
	if err != nil {
		return false, "", err
	}
	if size.Zero() {
		return false, "", nil
	}
	switch {
	case threadCut:
		return true, "This conversation is now long enough that grain had to leave the middle of it " +
			"out of the last exchange's prompt (" + size.String() + ").", nil
	case size.Large():
		return true, "The last exchange started with " + size.String() +
			" already spent on this conversation.", nil
	}
	return false, "", nil
}

// UpdateConsoleSession writes what a person changed about one session
// and answers with the whole list, the way UpdateSettings answers with
// the whole of Settings.
//
// A session this deployment has not configured is refused rather than
// created: the list is a settings row, and inventing a session because
// somebody PUT to a name is how a typo becomes a permanent agent. The
// exception that is not one is a deployment running on the shipped
// default -- it has a director whether or not the column was ever
// written, so editing that director writes the default list out with the
// change applied.
//
// Nothing here touches the session's task. The reconciler carries the
// new capabilities onto it on the next cycle
// (orchestrator.syncConsoleSessionTask), which is the same one-writer
// discipline every other setting follows.
func (c *Client) UpdateConsoleSession(ctx context.Context, id string, req UpdateConsoleSessionRequest) ([]ConsoleSessionSetting, error) {
	cfg, err := c.consoleSessionConfig(ctx)
	if err != nil {
		return nil, err
	}
	sessions := append([]model.ConsoleSession{}, model.ConsoleSessionsOr(cfg.ConsoleSessions)...)
	index := -1
	for i, s := range sessions {
		if s.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		if err := c.checkSettingsWritable(ctx, "grain_config"); err != nil {
			return nil, err
		}
		return nil, &NotFoundError{ID: id}
	}
	taskID, err := c.Store.TaskIDWithTag(ctx, model.ConsoleSessionTag(id))
	if err != nil {
		return nil, err
	}
	var openTask *model.Task
	if taskID != "" {
		obs, err := c.Store.GetObservation(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if obs != nil && obs.ClosedAt != nil {
			taskID = ""
		} else {
			openTask, err = c.Store.GetTask(ctx, taskID)
			if err != nil {
				return nil, err
			}
		}
	}
	hasParamEdit := req.Capabilities != nil || req.NetworkAccess != nil || req.TasksApproved != nil ||
		req.Reads != nil || req.Writes != nil || req.GoogleChatSpace != nil || req.Model != nil
	if openTask == nil || (!hasParamEdit && req.Title == nil) {
		if err := c.checkSettingsWritable(ctx, "grain_config"); err != nil {
			return nil, err
		}
	}
	origSession := sessions[index]
	session := origSession
	staticCaps := append([]string{}, session.Capabilities...)
	if req.Title != nil {
		switch {
		case req.renameByAgent:
			if c.configRepoOnly(ctx) && openTask != nil && strings.TrimSpace(openTask.Title) != "" && strings.TrimSpace(openTask.Title) != strings.TrimSpace(model.ConsoleSessionTitle(session)) {
				session.Title = openTask.Title
				session.TitleSource = model.TitleSourceAgent
			}
			// Checked here, against the row this write replaces, rather
			// than by the caller against a read of its own.
			if source := session.ResolvedTitleSource(req.firstMessage); source != model.TitleSourceHeuristic {
				return nil, errSessionAlreadyNamed(session, source)
			}
			session.Title = *req.Title
			session.TitleSource = model.TitleSourceAgent
		case strings.TrimSpace(*req.Title) != strings.TrimSpace(session.Title):
			// A person's title is theirs, and no agent may overwrite it.
			// Only a title that changed counts: the settings pane saves
			// every field, and ticking a capability is not choosing the
			// name the session already had.
			session.Title = *req.Title
			session.TitleSource = model.TitleSourcePerson
		}
	}
	if req.Team != nil {
		team := strings.TrimSpace(*req.Team)
		if team == "" {
			team = model.DefaultTeamID
		}
		session.Team = team
	}
	if req.Capabilities != nil {
		// Rejected rather than skipped, and duplicates dropped, for
		// UpdateSettings' own reasons about defaultCapabilities: this is
		// somebody choosing a set, in front of the thing it applies to.
		seen := map[string]bool{}
		ids := make([]string, 0, len(*req.Capabilities))
		for _, rawCap := range *req.Capabilities {
			normalizedIDs, err := c.normalizeCapabilityIDs(rawCap)
			if err != nil {
				return nil, err
			}
			for _, capability := range normalizedIDs {
				if _, ok := c.capabilityByID(capability); !ok {
					return nil, validationErrorf("capabilities: %s", capabilityRefusal(capability))
				}
				if seen[capability] {
					continue
				}
				seen[capability] = true
				ids = append(ids, capability)
			}
		}
		session.Capabilities = ids
	}
	if req.NetworkAccess != nil {
		access := model.NetworkAccess(*req.NetworkAccess)
		if access != "" && !model.ValidNetworkAccess(access) {
			return nil, validationErrorf("networkAccess must be one of %s, or empty for this deployment's own default",
				networkAccessNames())
		}
		session.NetworkAccess = access
	}
	if req.TasksApproved != nil {
		session.TasksApproved = *req.TasksApproved
	}
	if req.KeepRunning != nil {
		session.KeepRunning = *req.KeepRunning
	} else if req.KeepRunningSnake != nil {
		session.KeepRunning = *req.KeepRunningSnake
	}
	if req.Reads != nil {
		reads, err := c.parseReads(*req.Reads)
		if err != nil {
			return nil, err
		}
		session.Reads = reads
	}
	if req.Writes != nil {
		writes, err := c.parseWrites(ctx, session.Team, *req.Writes)
		if err != nil {
			return nil, err
		}
		session.Writes = writes
	}
	if req.GoogleChatSpace != nil {
		session.GoogleChatSpace = strings.TrimSpace(*req.GoogleChatSpace)
	}
	if req.Model != nil {
		// Checked the way a template's model is (declaredModelChoice):
		// a session names a model, never a connection, and which
		// connection serves its next exchange is the rotation's call.
		chosen, err := c.declaredModelChoice(ctx, *req.Model)
		if err != nil {
			return nil, err
		}
		session.Model = chosen
	}
	sessions[index] = session
	if err := model.ValidateConsoleSessions(sessions); err != nil {
		return nil, validationErrorf("%v", err)
	}
	writable := !c.configRepoOnly(ctx)
	if openTask != nil {
		stateRepo := c.stateRepoRef()
		if err := c.Store.UpdateTask(ctx, openTask.ID, func(t *model.Task) error {
			if req.Title != nil {
				t.Title = model.ConsoleSessionTitle(session)
			}
			if req.Capabilities != nil {
				existingByCap := make(map[string]bool, len(t.Grants))
				for _, g := range t.Grants {
					existingByCap[g.Capability] = true
				}
				policy, configured, err := c.capabilityPolicyFor(ctx)
				if err != nil {
					return err
				}
				for _, capID := range session.Capabilities {
					if capID == "" || existingByCap[capID] {
						continue
					}
					if model.IsCapabilityRef(capID) {
						subject := model.CapabilitySubject{Repo: t.Target, Kind: model.CapabilityRunTask}
						if err := policy.Permits(capID, subject, configured); err != nil {
							return validationErrorf("%v", err)
						}
					}
					if err := c.catalogueEditRefusal(ctx, *t, capID); err != nil {
						return err
					}
				}
				newGrants := make([]model.Grant, 0, len(session.Capabilities)+1)
				newGrants = append(newGrants, model.Grant{Capability: model.DurableCapability, Via: model.GrantByGrain})
				seen := map[string]bool{model.DurableCapability: true}
				for _, capID := range session.Capabilities {
					if capID == "" || seen[capID] {
						continue
					}
					seen[capID] = true
					via := model.GrantByLabel
					if writable || slices.Contains(staticCaps, capID) {
						via = model.GrantByGrain
					}
					newGrants = append(newGrants, model.Grant{Capability: capID, Via: via})
				}
				t.Grants = newGrants
			}
			if req.NetworkAccess != nil {
				t.NetworkAccess = model.ConsoleSessionNetworkAccess(session, !c.Config.SandboxShapeIgnored)
			}
			if req.TasksApproved != nil {
				t.AutoApproveProposals = *req.TasksApproved
				t.Tags = slices.DeleteFunc(t.Tags, func(tag string) bool {
					return tag == model.ConsoleSessionTasksApprovedTag || tag == model.ConsoleSessionTasksUnapprovedTag
				})
				if *req.TasksApproved {
					t.Tags = append(t.Tags, model.ConsoleSessionTasksApprovedTag)
				} else {
					t.Tags = append(t.Tags, model.ConsoleSessionTasksUnapprovedTag)
				}
			}
			if req.Reads != nil {
				var taskReads []model.RepoRef
				if stateRepo.Owner != "" {
					taskReads = append(taskReads, stateRepo)
				}
				for _, r := range session.Reads {
					if r.Owner != "" && r.Name != "" && !r.Same(stateRepo) {
						taskReads = append(taskReads, r)
					}
				}
				t.Reads = taskReads
			}
			if req.Writes != nil {
				t.Writes = append([]model.WriteTarget{}, session.Writes...)
			}
			if req.GoogleChatSpace != nil {
				var links []model.Link
				for _, l := range t.Links {
					if l.Kind != model.LinkGoogleChat {
						links = append(links, l)
					}
				}
				if session.GoogleChatSpace != "" {
					links = append(links, model.Link{Kind: model.LinkGoogleChat, Target: session.GoogleChatSpace})
				}
				t.Links = links
			}
			if req.Model != nil {
				t.Model = session.Model
				t.ModelSource = ""
			}
			if req.KeepRunning != nil || req.KeepRunningSnake != nil {
				t.KeepRunning = session.KeepRunning
			}
			t.Body = model.DirectorSessionBody(stateRepo, session)
			return nil
		}); err != nil {
			return nil, err
		}
		if writable {
			model.RecordSyncedConsoleSession(c.Store, openTask.ID, model.ConsoleSessionConfigSnapshot{
				Capabilities:    session.Capabilities,
				Reads:           session.Reads,
				Writes:          session.Writes,
				NetworkAccess:   model.ConsoleSessionNetworkAccess(session, !c.Config.SandboxShapeIgnored),
				Model:           strings.TrimSpace(session.Model),
				Title:           model.ConsoleSessionTitle(session),
				TasksApproved:   session.TasksApproved,
				KeepRunning:     session.KeepRunning,
				GoogleChatSpace: session.GoogleChatSpace,
			})
		} else if _, hasPrev := model.LastSyncedConsoleSession(c.Store, openTask.ID); !hasPrev {
			model.RecordSyncedConsoleSession(c.Store, openTask.ID, model.ConsoleSessionConfigSnapshot{
				Capabilities:    origSession.Capabilities,
				Reads:           origSession.Reads,
				Writes:          origSession.Writes,
				NetworkAccess:   model.ConsoleSessionNetworkAccess(origSession, !c.Config.SandboxShapeIgnored),
				Model:           strings.TrimSpace(origSession.Model),
				Title:           model.ConsoleSessionTitle(origSession),
				TasksApproved:   origSession.TasksApproved,
				KeepRunning:     origSession.KeepRunning,
				GoogleChatSpace: origSession.GoogleChatSpace,
			})
		}
	}
	if writable {
		cfg.ConsoleSessions = sessions
		if err := c.Store.PutConfig(ctx, cfg); err != nil {
			return nil, err
		}
	}
	return c.ListConsoleSessions(ctx)
}

func (c *Client) ensureConsoleSessionSnapshot(ctx context.Context, task model.Task) {
	sessionID, ok := model.ConsoleSessionIDOf(task)
	if !ok {
		return
	}
	if _, hasPrev := model.LastSyncedConsoleSession(c.Store, task.ID); hasPrev {
		return
	}
	cfg, err := c.consoleSessionConfig(ctx)
	if err != nil {
		return
	}
	session, ok := model.ConsoleSessionByID(model.ConsoleSessionsOr(cfg.ConsoleSessions), sessionID)
	if !ok {
		return
	}
	model.RecordSyncedConsoleSession(c.Store, task.ID, model.ConsoleSessionConfigSnapshot{
		Capabilities:    session.Capabilities,
		Reads:           session.Reads,
		Writes:          session.Writes,
		NetworkAccess:   model.ConsoleSessionNetworkAccess(session, !c.Config.SandboxShapeIgnored),
		Model:           strings.TrimSpace(session.Model),
		Title:           model.ConsoleSessionTitle(session),
		TasksApproved:   session.TasksApproved,
		GoogleChatSpace: session.GoogleChatSpace,
	})
}

func slugifySessionID(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if r == '-' || r == '_' || r == ' ' {
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
				b.WriteRune('-')
			}
		}
	}
	res := strings.Trim(b.String(), "-")
	if res == "" {
		return "session"
	}
	return res
}

// draftConsoleSessionID is the id the UI gives the chat it opens before
// a session exists (/session/new, NewSessionPage.jsx). No real session
// may take it: a session called "New" would otherwise slug to it, and
// opening that session would open an empty composer instead.
const draftConsoleSessionID = "new"

// CreateConsoleSession appends a new standing interactive session to
// cfg.ConsoleSessions and persists it.
func (c *Client) CreateConsoleSession(ctx context.Context, req CreateConsoleSessionRequest) ([]ConsoleSessionSetting, error) {
	_, sessions, err := c.createConsoleSession(ctx, req)
	return sessions, err
}

// createConsoleSession is CreateConsoleSession with the created
// session's id beside the list, for the handler to name in its answer.
func (c *Client) createConsoleSession(ctx context.Context, req CreateConsoleSessionRequest) (string, []ConsoleSessionSetting, error) {
	if err := c.checkSettingsWritable(ctx, "grain_config"); err != nil {
		return "", nil, err
	}
	message := req.Message
	// Decoded before anything is written, for the reason commentWithAuthor
	// decodes its own first: an attachment that does not decode must fail
	// the request with no session created and no task filed.
	now := c.now()
	attachments, err := decodeAttachments(req.Attachments, now)
	if err != nil {
		return "", nil, err
	}
	hasMessage := strings.TrimSpace(message) != "" || len(attachments) > 0
	title := strings.TrimSpace(req.Title)
	titleSource := model.TitleSourcePerson
	if title == "" {
		if !hasMessage {
			return "", nil, validationErrorf("a console session needs a title or a first message")
		}
		title = model.ConsoleSessionTitleFromMessage(message)
		titleSource = model.TitleSourceHeuristic
	}
	cfg, err := c.consoleSessionConfig(ctx)
	if err != nil {
		return "", nil, err
	}
	sessions := append([]model.ConsoleSession{}, model.ConsoleSessionsOr(cfg.ConsoleSessions)...)

	id := strings.TrimSpace(req.ID)
	if id == "" {
		baseID := slugifySessionID(title)
		if baseID == draftConsoleSessionID {
			baseID = "chat"
		}
		id = baseID
		counter := 1
		for {
			collision := false
			for _, s := range sessions {
				if s.ID == id {
					collision = true
					break
				}
			}
			if !collision {
				break
			}
			counter++
			id = fmt.Sprintf("%s-%d", baseID, counter)
		}
	} else {
		if strings.ContainsAny(id, " \t\n") {
			return "", nil, validationErrorf("console session id %q may not contain spaces", id)
		}
		if id == draftConsoleSessionID {
			return "", nil, validationErrorf("console session id %q is reserved for a chat that has not been started yet", id)
		}
		for _, s := range sessions {
			if s.ID == id {
				return "", nil, validationErrorf("a console session with id %q already exists", id)
			}
		}
	}

	var caps []string
	if req.Capabilities != nil {
		seen := map[string]bool{}
		caps = make([]string, 0, len(req.Capabilities))
		for _, rawCap := range req.Capabilities {
			normalizedIDs, err := c.normalizeCapabilityIDs(rawCap)
			if err != nil {
				return "", nil, err
			}
			for _, capability := range normalizedIDs {
				if _, ok := c.capabilityByID(capability); !ok {
					return "", nil, validationErrorf("capabilities: %s", capabilityRefusal(capability))
				}
				if seen[capability] {
					continue
				}
				seen[capability] = true
				caps = append(caps, capability)
			}
		}
	} else {
		// The director's set, plus any deployment defaults offered here so a
		// newly created session starts holding what its own proposals are
		// seeded with (grain/task-1983) while keeping session.Capabilities as
		// the single source of truth an operator can untick (grain/task-2126).
		seen := map[string]bool{}
		defs := model.DefaultConsoleSessions()
		if len(defs) > 0 {
			for _, capability := range defs[0].Capabilities {
				if capability == "" || seen[capability] {
					continue
				}
				seen[capability] = true
				caps = append(caps, capability)
			}
		}
		for _, capability := range cfg.DefaultCapabilities {
			capability, err := c.normalizeCapabilityID(capability)
			if err != nil || capability == "" || seen[capability] {
				continue
			}
			if _, ok := c.capabilityByID(capability); !ok {
				continue
			}
			seen[capability] = true
			caps = append(caps, capability)
		}
	}

	access := model.NetworkAccess(req.NetworkAccess)
	if access != "" && !model.ValidNetworkAccess(access) {
		return "", nil, validationErrorf("networkAccess must be one of %s, or empty for this deployment's own default",
			networkAccessNames())
	}
	if access == "" {
		access = model.NetworkOpen
	}

	var reads []model.RepoRef
	if req.Reads != nil {
		var err error
		reads, err = c.parseReads(req.Reads)
		if err != nil {
			return "", nil, err
		}
	}

	team := strings.TrimSpace(req.Team)
	if team == "" {
		team = model.DefaultTeamID
	}

	var writes []model.WriteTarget
	if req.Writes != nil {
		var err error
		writes, err = c.parseWrites(ctx, team, req.Writes)
		if err != nil {
			return "", nil, err
		}
	}

	chosenModel, err := c.declaredModelChoice(ctx, req.Model)
	if err != nil {
		return "", nil, err
	}

	newSession := model.ConsoleSession{
		ID:              id,
		Title:           title,
		TitleSource:     titleSource,
		Team:            team,
		Capabilities:    caps,
		NetworkAccess:   access,
		TasksApproved:   req.TasksApproved,
		KeepRunning:     req.KeepRunning || (req.KeepRunningSnake != nil && *req.KeepRunningSnake),
		Reads:           reads,
		Writes:          writes,
		GoogleChatSpace: strings.TrimSpace(req.GoogleChatSpace),
		Model:           chosenModel,
	}

	sessions = append(sessions, newSession)
	if err := model.ValidateConsoleSessions(sessions); err != nil {
		return "", nil, validationErrorf("%v", err)
	}

	cfg.ConsoleSessions = sessions
	if err := c.Store.PutConfig(ctx, cfg); err != nil {
		return "", nil, err
	}
	taskID, err := model.FileConsoleSessionTask(ctx, c.Store, newSession, model.FileConsoleSessionOptions{
		StateRepo:       c.stateRepoRef(),
		NetworkIsolates: !c.Config.SandboxShapeIgnored,
		Now:             now,
	})
	if err != nil {
		return "", nil, err
	}
	if err := c.noteRequesterTimeZone(ctx, taskID, req.TimeZone); err != nil {
		return "", nil, err
	}
	if hasMessage {
		// The first message, as the caller's own comment on the task
		// just filed -- the same write POST /api/tasks/{id}/comments
		// makes, so it lifts the opening park the task was filed on and
		// queues the session's first exchange exactly as a message typed
		// into an existing session would.
		by := model.Attribution{Actor: c.actor(ctx)}
		if err := c.addAttributedComment(ctx, taskID, message, attachments, by, now, true); err != nil {
			return "", nil, err
		}
	}
	if c.Config.Wake != nil {
		c.Config.Wake()
	}
	listed, err := c.ListConsoleSessions(ctx)
	return id, listed, err
}

// RestartConsoleSessionRequest is the body of POST /api/console-sessions/{id}/restart.
type RestartConsoleSessionRequest struct {
	// CarryHandover copies the previous task's latest standing handover
	// summary onto the fresh session task when true. False resets the
	// session completely fresh.
	CarryHandover bool `json:"carryHandover,omitempty"`
}

func (c *Client) stateRepoRef() model.RepoRef {
	if c.Config.StateRepoRef == nil {
		return model.RepoRef{}
	}
	return c.Config.StateRepoRef()
}

// stopAndCloseConsoleSessionTask cancels any in-flight run on taskID and
// closes the task. When reset is true, the closed task is tagged with
// model.ConsoleSessionResetTag so a later session with the same id will not
// inherit its handover summary or link to it.
func (c *Client) stopAndCloseConsoleSessionTask(ctx context.Context, taskID string, reset bool) error {
	if taskID == "" {
		return nil
	}
	task, err := c.Store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return nil
	}
	now := c.now()
	var cancelled bool
	if c.Config.TaskCanceller != nil {
		cancelled = c.Config.TaskCanceller.CancelTask(ctx, taskID)
	}
	if !cancelled {
		runs, err := c.Store.Runs(ctx, taskID)
		if err != nil {
			return err
		}
		for i := range runs {
			if runs[i].FinishedAt != nil {
				continue
			}
			if err := c.Store.FinishRun(ctx, runs[i].ID, now, "cancelled", "cancelled by user"); err != nil {
				return err
			}
		}
	}
	obs, err := c.Store.GetObservation(ctx, taskID)
	if err != nil {
		return err
	}
	if obs == nil || obs.ClosedAt == nil {
		if err := c.Store.ObserveField(ctx, taskID, now, func(o *model.Observation) {
			o.ClosedAt = &now
			o.PendingQuestionCommentID = nil
			o.PendingSecret = ""
		}); err != nil {
			return err
		}
	}
	if reset && !slices.Contains(task.Tags, model.ConsoleSessionResetTag) {
		if err := c.Store.UpdateTask(ctx, taskID, func(t *model.Task) error {
			if !slices.Contains(t.Tags, model.ConsoleSessionResetTag) {
				t.Tags = append(t.Tags, model.ConsoleSessionResetTag)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// RestartConsoleSession stops any in-flight run on session id's current task,
// closes that task, and synchronously files a fresh replacement task so the
// returned session list immediately carries the new taskId.
func (c *Client) RestartConsoleSession(ctx context.Context, id string, req RestartConsoleSessionRequest) ([]ConsoleSessionSetting, error) {
	cfg, err := c.consoleSessionConfig(ctx)
	if err != nil {
		return nil, err
	}
	sessions := model.ConsoleSessionsOr(cfg.ConsoleSessions)
	session, ok := model.ConsoleSessionByID(sessions, id)
	if !ok {
		return nil, &NotFoundError{ID: id}
	}
	oldTaskID, err := c.Store.TaskIDWithTag(ctx, model.ConsoleSessionTag(id))
	if err != nil {
		return nil, err
	}
	if oldTaskID != "" {
		if err := c.stopAndCloseConsoleSessionTask(ctx, oldTaskID, !req.CarryHandover); err != nil {
			return nil, err
		}
	}
	var previous string
	if req.CarryHandover && oldTaskID != "" {
		previous = oldTaskID
	}
	if _, err := model.FileConsoleSessionTask(ctx, c.Store, session, model.FileConsoleSessionOptions{
		StateRepo:            c.stateRepoRef(),
		NetworkIsolates:      !c.Config.SandboxShapeIgnored,
		PreviousTaskID:       previous,
		CarryHandoverSummary: req.CarryHandover && previous != "",
		Now:                  c.now(),
	}); err != nil {
		return nil, err
	}
	if c.Config.Wake != nil {
		c.Config.Wake()
	}
	return c.ListConsoleSessions(ctx)
}

// CancelConsoleSession stops any in-flight or queued turn on session id's
// underlying task and returns the session to awaiting_reply.
func (c *Client) CancelConsoleSession(ctx context.Context, id string) ([]ConsoleSessionSetting, error) {
	cfg, err := c.consoleSessionConfig(ctx)
	if err != nil {
		return nil, err
	}
	sessions := model.ConsoleSessionsOr(cfg.ConsoleSessions)
	if _, ok := model.ConsoleSessionByID(sessions, id); !ok {
		return nil, &NotFoundError{ID: id}
	}
	taskID, err := c.Store.TaskIDWithTag(ctx, model.ConsoleSessionTag(id))
	if err != nil {
		return nil, err
	}
	if taskID == "" {
		return nil, &NotFoundError{ID: id}
	}
	obs, err := c.Store.GetObservation(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if obs != nil && obs.ClosedAt != nil {
		return nil, &NotFoundError{ID: id}
	}
	if err := c.Stop(ctx, taskID); err != nil {
		return nil, err
	}
	return c.ListConsoleSessions(ctx)
}

// DeleteConsoleSession removes a standing interactive session from
// cfg.ConsoleSessions and stops and closes its underlying task so a session
// created with the same id later starts fresh.
func (c *Client) DeleteConsoleSession(ctx context.Context, id string) error {
	// Unrehearsed: the session's task is cancelled before the stored
	// config is written, and a refused delete must not stop it.
	if err := c.checkSettingsWritableUnrehearsed(ctx, "grain_config"); err != nil {
		return err
	}
	cfg, err := c.consoleSessionConfig(ctx)
	if err != nil {
		return err
	}
	sessions := append([]model.ConsoleSession{}, model.ConsoleSessionsOr(cfg.ConsoleSessions)...)
	index := -1
	for i, s := range sessions {
		if s.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return &NotFoundError{ID: id}
	}
	taskID, err := c.Store.TaskIDWithTag(ctx, model.ConsoleSessionTag(id))
	if err != nil {
		return err
	}
	if taskID != "" {
		if err := c.stopAndCloseConsoleSessionTask(ctx, taskID, true); err != nil {
			return err
		}
	}
	sessions = slices.Delete(sessions, index, index+1)
	if sessions == nil {
		sessions = []model.ConsoleSession{}
	}
	cfg.ConsoleSessions = sessions
	if err := c.checkSettingsWritable(ctx, "grain_config"); err != nil {
		return err
	}
	if err := c.Store.PutConfig(ctx, cfg); err != nil {
		return err
	}
	if c.Config.Wake != nil {
		c.Config.Wake()
	}
	return nil
}

// handleListConsoleSessions is GET /api/console-sessions.
func (s *Server) handleListConsoleSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.tasks.ListConsoleSessions(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	// The same two readings GET /api/config's own capability listing is
	// built from, for the same reason: this pane's checkboxes are a
	// capability picker, and a picker only offers what this deployment
	// has actually configured (configuredCapabilities, ui/src/state.js).
	// Without readiness beside each row it has nothing to filter on, and
	// would go on offering a gemini-key no session could ever use.
	s.respondWithConsoleSessions(w, r, http.StatusOK, sessions)
}

// Who may write a standing session (grain/task-1460).
//
// These are rows of the deployment-wide config, and a session's
// capabilities, network access and repo grants are what its task runs
// with -- so changing any of those is a system administrator's call, the
// same as changing defaultCapabilities in Settings is. Anything else --
// creating a plain chat, renaming one, linking a Chat space, removing
// one, and whether what it files is queued approved -- needs only the
// member or admin role on the session's team, which is what talking to
// the session asks of a person anyway (grain/task-3797). A member's new
// session starts with no capabilities rather than the director's:
// self-debug and grain-log are this host's own, and a member could not
// grant them to a task of their own.
//
// Filing approved is a member's to turn on because it grants nothing a
// member does not already hold: a member approves their team's
// proposals, and the session's own approvals reach no further than
// what it was granted -- a proposal asking for a capability or a
// repository the session does not hold is filed for a person whatever
// this says (Client.CreateTask, orchestrator.relayProposedTasks), and
// what grows below a session-queued task is bounded as self-approved
// work (model.SelfApproved).

// consoleSessionTeam is the team the configured session id belongs to,
// or a NotFoundError.
func (c *Client) consoleSessionTeam(ctx context.Context, id string) (string, error) {
	cfg, err := c.consoleSessionConfig(ctx)
	if err != nil {
		return "", err
	}
	for _, session := range model.ConsoleSessionsOr(cfg.ConsoleSessions) {
		if session.ID == id {
			return session.Team, nil
		}
	}
	return "", &NotFoundError{ID: id}
}

// respondWithConsoleSessions answers with the same body GET
// /api/console-sessions does, capability readiness included, so the
// pane's picker does not lose it on a save.
func (s *Server) respondWithConsoleSessions(w http.ResponseWriter, r *http.Request, status int, sessions []ConsoleSessionSetting) {
	s.respondWithCreatedConsoleSession(w, r, status, sessions, "")
}

// respondWithCreatedConsoleSession is respondWithConsoleSessions naming
// the session a POST just created (ConsoleSessionSettingsResponse.Created).
func (s *Server) respondWithCreatedConsoleSession(w http.ResponseWriter, r *http.Request, status int, sessions []ConsoleSessionSetting, created string) {
	cfg, err := s.tasks.Store.GetConfig(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	repoConfigs, err := s.tasks.Store.ListRepoConfigs(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, status, ConsoleSessionSettingsResponse{
		Sessions:             sessions,
		Created:              created,
		Capabilities:         s.tasks.capabilitiesWithReadiness(cfg, repoConfigs),
		NetworkAccessOptions: networkAccessNames(),
	})
}

// handleCreateConsoleSession is POST /api/console-sessions.
func (s *Server) handleCreateConsoleSession(w http.ResponseWriter, r *http.Request) {
	var req CreateConsoleSessionRequest
	// Read the way a comment is, since the request may carry one: the
	// body limit counts attachments separately from the JSON around them.
	if !readAttachmentJSON(w, r, &req, func(req *CreateConsoleSessionRequest) []AttachmentUpload { return req.Attachments }) {
		return
	}
	if strings.TrimSpace(req.TimeZone) == "" {
		req.TimeZone = requestTimeZone(r)
	}
	created, sessions, err := s.tasks.createConsoleSession(r.Context(), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithCreatedConsoleSession(w, r, http.StatusCreated, sessions, created)
}

// handleDeleteConsoleSession is DELETE /api/console-sessions/{id}.
func (s *Server) handleDeleteConsoleSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.tasks.consoleSessionTeam(r.Context(), id); err != nil {
		writeClientError(w, err)
		return
	}
	if err := s.tasks.DeleteConsoleSession(r.Context(), id); err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleUpdateConsoleSession is PUT /api/console-sessions/{id}.
func (s *Server) handleUpdateConsoleSession(w http.ResponseWriter, r *http.Request) {
	var req UpdateConsoleSessionRequest
	if !readJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	if _, err := s.tasks.consoleSessionTeam(r.Context(), id); err != nil {
		writeClientError(w, err)
		return
	}
	sessions, err := s.tasks.UpdateConsoleSession(r.Context(), id, req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithConsoleSessions(w, r, http.StatusOK, sessions)
}

// handleRestartConsoleSession is POST /api/console-sessions/{id}/restart.
func (s *Server) handleRestartConsoleSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.tasks.consoleSessionTeam(r.Context(), id); err != nil {
		writeClientError(w, err)
		return
	}
	var req RestartConsoleSessionRequest
	if r.ContentLength > 0 {
		if !readJSON(w, r, &req) {
			return
		}
	}
	sessions, err := s.tasks.RestartConsoleSession(r.Context(), id, req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithConsoleSessions(w, r, http.StatusOK, sessions)
}

// handleCancelConsoleSession is POST /api/console-sessions/{id}/cancel.
func (s *Server) handleCancelConsoleSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.tasks.consoleSessionTeam(r.Context(), id); err != nil {
		writeClientError(w, err)
		return
	}
	sessions, err := s.tasks.CancelConsoleSession(r.Context(), id)
	if err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithConsoleSessions(w, r, http.StatusOK, sessions)
}
