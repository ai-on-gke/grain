package ui

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/bwsalmon/grain/pkg/capability/teambroadcast"
	"github.com/bwsalmon/grain/pkg/model"
)

type teamNotFoundError struct{ ID string }

func (e *teamNotFoundError) Error() string { return "no team " + e.ID }

// TeamRepoAssociation describes a code repository associated with a team.
type TeamRepoAssociation struct {
	Repo          string `json:"repo"`
	CIMode        string `json:"ciMode"`
	ActiveWorkers int    `json:"activeWorkers"`
	Health        string `json:"health"`
}

// Team is the deployment as GET /api/deployment shows it: the default
// team's shape, which the frontend still reads, built from grain_config
// since schema 24 dropped the team table (deploymentTeam).
type Team struct {
	ID string `json:"id"`
	// Name is what the deployment is called (model.Config.
	// EnvironmentName), empty for an unnamed one. It was the default
	// team's name, "admin", until the single-tenant reset.
	Name             string   `json:"name,omitempty"`
	ConfigRepo       string   `json:"configRepo,omitempty"`
	ConfigCloneURL   string   `json:"configCloneUrl,omitempty"`
	CloneCommand     string   `json:"cloneCommand,omitempty"`
	MergeBatchSize   int      `json:"mergeBatchSize"`
	RepairTemplateID string   `json:"repairTemplateID,omitempty"`
	AgentFramework   string   `json:"agentFramework,omitempty"`
	AgentBackendID   string   `json:"agentBackendId,omitempty"`
	DefaultModel     string   `json:"defaultModel,omitempty"`
	DefaultModels    []string `json:"defaultModels,omitempty"`
	// ModelSettings is the deployment's per-model settings
	// (model.Config.ModelSettings, docs/connections-and-models.md).
	ModelSettings  []model.ModelSetting      `json:"modelSettings,omitempty"`
	GitCredential  string                    `json:"gitCredential,omitempty"`
	GitConnections []model.TeamGitConnection `json:"gitConnections,omitempty"`
	// HasGitCredential is whether the deployment holds any git
	// connection on its default host -- GitConnections or the legacy
	// GitCredential (model.Config.HasGitConnectionOn). It is what the
	// UI's "has no GitHub credential" warnings and hostinit read, since
	// adding or deleting a connection blanks GitCredential
	// (grain/task-3079).
	HasGitCredential bool                  `json:"hasGitCredential"`
	AgentGitName     string                `json:"agentGitName,omitempty"`
	AgentGitEmail    string                `json:"agentGitEmail,omitempty"`
	TargetRepos      []string              `json:"targetRepos,omitempty"`
	Repos            []TeamRepoAssociation `json:"repos,omitempty"`
	AssociatedRepos  []TeamRepoAssociation `json:"associatedRepos,omitempty"`
}

// gitDefaultHost is the deployment's default git host as the store
// resolves team connections against it, empty for DefaultGitHost.
func (c *Client) gitDefaultHost() string {
	if c == nil || c.Store == nil {
		return ""
	}
	return c.Store.GitDefaultHost
}

// deploymentSettings is the deployment's stored configuration, a zero
// Config when nothing has stored one yet: where its git connections,
// default model, pinned connection and per-model settings are kept since
// schema 22 (docs/single-tenant-reset.md, 6-ii-b2).
func (c *Client) deploymentSettings(ctx context.Context) (model.Config, error) {
	if c == nil || c.Store == nil {
		return model.Config{}, nil
	}
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil || cfg == nil {
		return model.Config{}, err
	}
	return *cfg, nil
}

// teamFrom is the deployment as the API shows it, out of cfg, where its
// settings are kept (deploymentSettings). defaultHost is the
// deployment's default git host (model.Store.GitDefaultHost), empty for
// DefaultGitHost.
func teamFrom(cfg model.Config, defaultHost string) Team {
	configRepo := model.ConfigRepo().String()
	configCloneURL := "/" + configRepo + ".git"
	cloneCommand := "git clone " + configCloneURL
	return Team{
		ID:               model.DefaultTeamID,
		Name:             cfg.EnvironmentName,
		ConfigRepo:       configRepo,
		ConfigCloneURL:   configCloneURL,
		CloneCommand:     cloneCommand,
		MergeBatchSize:   cfg.MergeBatchSize,
		RepairTemplateID: cfg.RepairTemplateID,
		AgentFramework:   cfg.AgentFramework,
		AgentBackendID:   cfg.AgentBackendID,
		DefaultModel:     model.NormalizeDefaultModel(cfg.DefaultModel),
		DefaultModels:    cfg.DefaultModels(),
		ModelSettings:    cfg.ModelSettings,
		GitCredential:    cfg.GitCredential,
		GitConnections:   cfg.GitConnections,
		HasGitCredential: cfg.HasGitConnectionOn("", defaultHost),
		AgentGitName:     cfg.AgentGitName,
		AgentGitEmail:    cfg.AgentGitEmail,
		TargetRepos:      cfg.TargetRepos,
		Repos:            []TeamRepoAssociation{},
		AssociatedRepos:  []TeamRepoAssociation{},
	}
}

// teamRepoSources is what populateTeamRepos derives a team's repos from:
// every task (with its state), template and schedule, read once. Listing
// teams reads them once for the whole list rather than once per team --
// GET /api/config lists every team, and the per-team reads made it take
// teams x (a full task listing), over a second on a deployment of a few
// thousand tasks, with the connection Save button waiting on it
// (grain/task-3903).
type teamRepoSources struct {
	tasks     []model.TaskRepo
	states    map[string]model.State
	templates []model.Template
	schedules []model.Schedule
	// workflows caches GetRepoWorkflows per repo across teams.
	workflows map[string]bool
}

// loadTeamRepoSources reads what populateTeamReposFrom needs. A read that
// fails leaves its part empty, as each one always has: a team's repo list
// is a summary, not something to fail the team over.
func (c *Client) loadTeamRepoSources(ctx context.Context) *teamRepoSources {
	src := &teamRepoSources{workflows: map[string]bool{}}
	if c.Store == nil {
		return src
	}
	// A task's repo and state are all this needs, so not
	// ListTasks: its full rows and the reads hydrating them were most of
	// the cost.
	if tasks, err := c.Store.TaskRepos(ctx); err == nil {
		if states, err := c.ReadStates(ctx); err == nil {
			src.tasks, src.states = tasks, states
		}
	}
	if templates, err := c.Store.ListTemplates(ctx); err == nil {
		src.templates = templates
	}
	if schedules, err := c.Store.ListSchedules(ctx); err == nil {
		src.schedules = schedules
	}
	return src
}

func (c *Client) populateTeamRepos(ctx context.Context, team *Team) {
	c.populateTeamReposFrom(ctx, c.loadTeamRepoSources(ctx), team)
}

func (c *Client) populateTeamReposFrom(ctx context.Context, src *teamRepoSources, team *Team) {
	if remote, _ := c.externalConfigRepo(ctx); remote != "" {
		team.ConfigRepo = ""
		if c.Config.StateRepoRef != nil {
			team.ConfigRepo = c.Config.StateRepoRef().String()
		}
		if team.ConfigRepo == "" {
			team.ConfigRepo = remote
		}
		team.ConfigCloneURL = remote
		team.CloneCommand = "git clone " + remote
	} else {
		if team.ConfigRepo == "" {
			team.ConfigRepo = model.ConfigRepo().String()
		}
		if team.ConfigCloneURL == "" {
			team.ConfigCloneURL = "/" + team.ConfigRepo + ".git"
		}
		if team.CloneCommand == "" {
			team.CloneCommand = "git clone " + team.ConfigCloneURL
		}
	}

	reposMap := make(map[string]*TeamRepoAssociation)

	if c.Store != nil {
		for _, task := range src.tasks {
			if task.Target == nil || !strings.EqualFold(model.DefaultTeamID, team.ID) {
				continue
			}
			repoName := task.Target.String()
			if repoName == "" {
				continue
			}
			assoc, ok := reposMap[repoName]
			if !ok {
				assoc = &TeamRepoAssociation{
					Repo:   repoName,
					CIMode: "Grain CI",
					Health: "healthy",
				}
				reposMap[repoName] = assoc
			}
			switch src.states[task.ID] {
			case model.StateRunning:
				assoc.ActiveWorkers++
			case model.StateFailed:
				assoc.Health = "failing"
			}
		}

		for _, tmpl := range src.templates {
			// Every template is the deployment's, the default team's.
			if team.ID != model.DefaultTeamID || tmpl.Target == nil || tmpl.Target.Owner == "" || tmpl.Target.Name == "" {
				continue
			}
			repoName := tmpl.Target.String()
			if _, ok := reposMap[repoName]; !ok {
				reposMap[repoName] = &TeamRepoAssociation{
					Repo:   repoName,
					CIMode: "Grain CI",
					Health: "healthy",
				}
			}
		}

		for _, sched := range src.schedules {
			if team.ID != model.DefaultTeamID || sched.Target.Owner == "" || sched.Target.Name == "" {
				continue
			}
			repoName := sched.Target.String()
			if _, ok := reposMap[repoName]; !ok {
				reposMap[repoName] = &TeamRepoAssociation{
					Repo:   repoName,
					CIMode: "Grain CI",
					Health: "healthy",
				}
			}
		}

		for repoName, assoc := range reposMap {
			parsed, err := model.ParseRepo(repoName)
			if err == nil {
				hasWorkflows, ok := src.workflows[repoName]
				if !ok {
					workflows, _ := c.Store.GetRepoWorkflows(ctx, parsed)
					hasWorkflows = len(workflows) > 0
					src.workflows[repoName] = hasWorkflows
				}
				if hasWorkflows {
					assoc.CIMode = "Grain CI"
				} else {
					assoc.CIMode = "GitHub CI"
				}
			}
		}
	}

	assocs := make([]TeamRepoAssociation, 0, len(reposMap))
	for _, assoc := range reposMap {
		assocs = append(assocs, *assoc)
	}
	sort.Slice(assocs, func(i, j int) bool { return assocs[i].Repo < assocs[j].Repo })
	team.Repos = assocs
	team.AssociatedRepos = assocs
}

// UpdateTeamRequest is what a write to the deployment's settings says
// about them, each field replacing the stored one when present:
// PUT /api/deployment's and the git connection editor's.
type UpdateTeamRequest struct {
	AgentBackendID *string   `json:"agentBackendId,omitempty"`
	DefaultModel   *string   `json:"defaultModel,omitempty"`
	DefaultModels  *[]string `json:"defaultModels,omitempty"`
	// ModelSettings replaces model.Config.ModelSettings when present.
	ModelSettings  *[]model.ModelSetting      `json:"modelSettings,omitempty"`
	GitCredential  *string                    `json:"gitCredential,omitempty"`
	GitConnections *[]model.TeamGitConnection `json:"gitConnections,omitempty"`
	AgentGitName   *string                    `json:"agentGitName,omitempty"`
	AgentGitEmail  *string                    `json:"agentGitEmail,omitempty"`
	TargetRepos    *[]string                  `json:"targetRepos,omitempty"`
	User           string                     `json:"user,omitempty"`
}

// GetTeam is the deployment in the team shape, for id the default team's
// and a teamNotFoundError for any other.
func (c *Client) GetTeam(ctx context.Context, id string) (Team, error) {
	team, err := c.existingTeam(ctx, id)
	if err != nil {
		return Team{}, err
	}
	c.populateTeamRepos(ctx, team)
	return *team, nil
}

// UpdateTeam writes req onto the deployment's settings (grain_config)
// and is the deployment as it then stands. id must be the default team's.
func (c *Client) UpdateTeam(ctx context.Context, id string, req UpdateTeamRequest) (Team, error) {
	if err := c.checkSettingsWritable(ctx, "grain_config"); err != nil {
		return Team{}, err
	}
	if _, err := c.existingTeam(ctx, id); err != nil {
		return Team{}, err
	}
	settings, err := c.deploymentSettings(ctx)
	if err != nil {
		return Team{}, err
	}
	var targetRepos []string
	if req.TargetRepos != nil {
		seen := make(map[string]bool)
		for _, r := range *req.TargetRepos {
			trimmed := strings.TrimSpace(r)
			if trimmed == "" {
				continue
			}
			ref, err := model.ParseRepo(trimmed)
			if err != nil {
				return Team{}, validationErrorf("invalid target repo %q: %w", trimmed, err)
			}
			canon := ref.String()
			if !seen[canon] {
				seen[canon] = true
				targetRepos = append(targetRepos, canon)
			}
		}
	}
	if err := c.Store.UpdateConfig(ctx, func(cfg *model.Config) error {
		if err := c.applyDeploymentSettings(cfg, req); err != nil {
			return err
		}
		if req.TargetRepos != nil {
			cfg.TargetRepos = targetRepos
		}
		settings = *cfg
		return nil
	}); err != nil {
		return Team{}, err
	}
	c.triggerIAPSync()
	res := teamFrom(settings, c.gitDefaultHost())
	c.populateTeamRepos(ctx, &res)
	return res, nil
}

// applyDeploymentSettings is what req says about the deployment's own
// settings -- its git connections, default model, pinned connection and
// per-model settings -- written onto cfg, or the validation error that
// refuses it.
func (c *Client) applyDeploymentSettings(cfg *model.Config, req UpdateTeamRequest) error {
	if req.AgentBackendID != nil {
		cfg.AgentBackendID = strings.TrimSpace(*req.AgentBackendID)
	}
	if req.DefaultModel != nil {
		cfg.DefaultModel = model.NormalizeDefaultModel(*req.DefaultModel)
	}
	if req.DefaultModels != nil {
		cfg.DefaultModel = model.JoinDefaultModels(*req.DefaultModels)
	}
	if req.ModelSettings != nil {
		cfg.ModelSettings = cleanModelSettings(*req.ModelSettings)
	}
	if req.AgentGitName != nil {
		cfg.AgentGitName = strings.TrimSpace(*req.AgentGitName)
	}
	if req.AgentGitEmail != nil {
		cfg.AgentGitEmail = strings.TrimSpace(*req.AgentGitEmail)
	}
	if req.GitCredential != nil {
		gitCred := strings.TrimSpace(*req.GitCredential)
		if gitCred != "" {
			if !c.isGitHubCredentialSelector(gitCred) {
				return validationErrorf(
					"gitCredential %q is not a GitHub credential on this deployment (%s)",
					gitCred, strings.Join(c.gitHubCredentialNames(), ", "))
			}
		}
		cfg.GitCredential = gitCred
	}
	if req.GitConnections != nil {
		var conns []model.TeamGitConnection
		for _, raw := range *req.GitConnections {
			norm, err := model.NormalizeTeamGitConnection(raw)
			if err != nil {
				return validationErrorf("invalid git connection: %w", err)
			}
			if !c.isGitHubCredentialSelector(norm.Credential) {
				return validationErrorf(
					"git connection credential %q is not a GitHub credential on this deployment (%s)",
					norm.Credential, strings.Join(c.gitHubCredentialNames(), ", "))
			}
			conns = append(conns, norm)
		}
		// Migrate the legacy Config.GitCredential into connections on
		// first save
		if legacy := strings.TrimSpace(cfg.GitCredential); legacy != "" {
			hasLegacy := false
			for _, conn := range conns {
				if conn.Credential == legacy {
					hasLegacy = true
					break
				}
			}
			if !hasLegacy {
				conns = append([]model.TeamGitConnection{{Credential: legacy}}, conns...)
			}
			cfg.GitCredential = ""
		}
		cfg.GitConnections = conns
	}
	return nil
}

// teamActingUser is who ctx acts as for a team or system-admin check:
// its verified human principal (a session, a verified IAP assertion),
// and nobody otherwise. claimed -- the "user" a request body or an
// in-process caller names -- is never believed: a request with no
// principal got past ServeHTTP only as a trusted source, and a trusted
// source acts as the deployment's own actor (Client.Config.Actor, which
// every caller of this falls back to on ""), never as a person it names
// (grain#2379, grain/task-2078). Anything able to reach the daemon over
// loopback -- every user onboarded through an SSH port-forward -- could
// otherwise be any team admin or system admin by typing their name.
func teamActingUser(ctx context.Context, claimed string) string {
	_ = claimed
	if p, ok := PrincipalFromContext(ctx); ok && p.Kind == model.PrincipalHuman && strings.TrimSpace(p.ID) != "" {
		return strings.TrimSpace(p.ID)
	}
	return ""
}

func (c *Client) triggerIAPSync() {
	if c.Config.TriggerIAPSync != nil {
		c.Config.TriggerIAPSync()
	}
}

func requestUser(r *http.Request) string {
	if p, ok := PrincipalFromContext(r.Context()); ok && p.Kind == model.PrincipalHuman && strings.TrimSpace(p.ID) != "" {
		return strings.TrimSpace(p.ID)
	}
	if p, ok := ExtractPrincipal(r); ok && p.Kind == model.PrincipalHuman && strings.TrimSpace(p.ID) != "" {
		return strings.TrimSpace(p.ID)
	}
	return ""
}

// actingUser is who r acts as for a team or attribution check: its
// verified human principal, and "" otherwise. The user its body claims
// is ignored, whoever sent it: a signed-in person, or a run with its own
// agent principal, may not claim to be somebody else by naming them, and
// neither may a request with no principal at all. That one got past
// ServeHTTP's access gate only as a caller this daemon already trusts
// (the access code, an in-process hop), and it
// acts as the deployment's own actor, which "" falls back to -- not as
// whichever team admin or system admin it names (grain/task-2078).
func actingUser(r *http.Request, claimed string) string {
	_ = claimed
	return requestUser(r)
}

// BroadcastTeamRequest carries the payload for POST /api/deployment/broadcast.
type BroadcastTeamRequest struct {
	Message string `json:"message"`
	Author  string `json:"author,omitempty"`
	// Task is the task whose run is broadcasting -- cmd/grain's
	// daemonTasks, for a run holding the team-broadcast grant. Set, the
	// broadcast is that task's to make (authorizeBroadcast) rather than
	// Author's. Believed only from a caller with no principal at all
	// (a hop ServeHTTP already trusts); a run with an agent principal is
	// that task whatever the body says, and a signed-in person is never
	// one.
	Task string `json:"task,omitempty"`
	// RunningOnly limits delivery to task agents currently in StateRunning
	// (excluding standing console sessions), so a notice meant for running
	// agents does not un-park tasks awaiting a reply or comment on queued
	// work.
	RunningOnly bool `json:"running_only,omitempty"`
}

// BroadcastTeamResponse reports the outcome of a broadcast to the
// deployment's active tasks.
type BroadcastTeamResponse struct {
	DeliveredCount int      `json:"delivered_count"`
	TaskIDs        []string `json:"task_ids"`
}

func isBroadcastState(st model.State) bool {
	switch st {
	case model.StateRunning, model.StateAwaitingReply, model.StateQueued, model.StateAwaitingSubmit:
		return true
	default:
		return false
	}
}

// BroadcastTeamMessage delivers a comment to every active task of the
// deployment.
func (c *Client) BroadcastTeamMessage(ctx context.Context, req BroadcastTeamRequest) (BroadcastTeamResponse, error) {
	if err := c.authorizeBroadcast(ctx, req.Task); err != nil {
		return BroadcastTeamResponse{}, err
	}
	if strings.TrimSpace(req.Message) == "" {
		return BroadcastTeamResponse{}, validationErrorf("a broadcast message is required")
	}

	tasks, err := c.ListTasks(ctx)
	if err != nil {
		return BroadcastTeamResponse{}, err
	}

	// A run's broadcast is that task's words, relayed by grain -- the same
	// attribution a run's own comment_on_issue gets -- and says so in its
	// first line. Left to AddCommentWithAuthor it would be signed by
	// c.actor, which for the daemon's own hop is the operator: a human
	// instruction, as far as every task it reached could tell
	// (grain/task-1705).
	var from *model.Attribution
	body := req.Message
	if sender := strings.TrimSpace(req.Task); sender != "" {
		from = &model.Attribution{
			Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
			OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: sender},
		}
		body = fmt.Sprintf("Broadcast from task %s:\n\n%s", sender, req.Message)
	}

	deliveredIDs := make([]string, 0)
	for _, t := range tasks {
		if t.ConsoleSession != "" {
			continue
		}
		if req.RunningOnly {
			if t.State != model.StateRunning {
				continue
			}
		} else if !isBroadcastState(t.State) {
			continue
		}
		if from == nil {
			err = c.commentWithAuthor(ctx, t.ID, body, nil, req.Author, false)
		} else {
			err = c.addAttributedComment(ctx, t.ID, body, nil, *from, c.now(), false)
		}
		if err != nil {
			return BroadcastTeamResponse{}, err
		}
		deliveredIDs = append(deliveredIDs, t.ID)
	}

	return BroadcastTeamResponse{
		DeliveredCount: len(deliveredIDs),
		TaskIDs:        deliveredIDs,
	}, nil
}

// authorizeBroadcast refuses a broadcast unless whoever is making it may.
// A broadcast comments on every active task -- un-parking the ones
// awaiting a reply, and delivering into the sessions of the ones running
// -- so it is an instruction to all of the deployment's work, and two
// kinds of caller may give one:
//
//   - with taskID, a run of that task, which must hold the team-broadcast
//     grant (pkg/capability/teambroadcast);
//   - without one, whoever ServeHTTP let in: every such caller is the
//     deployment's own actor.
//
// Before grain/task-1568 neither was asked: every run was offered
// broadcast_team_message and the daemon delivered whatever it was given.
func (c *Client) authorizeBroadcast(ctx context.Context, taskID string) error {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil
	}
	if c.Store == nil {
		return forbiddenErrorf("no task store to check task %s against, so it may not broadcast", taskID)
	}
	task, err := c.Store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return forbiddenErrorf("task %s does not exist, so it may not broadcast", taskID)
	}
	for _, g := range task.Grants {
		if g.Capability == teambroadcast.CapabilityName {
			return nil
		}
	}
	return forbiddenErrorf("task %s does not hold the %q capability, so its run may not broadcast",
		taskID, teambroadcast.CapabilityName)
}

// DeploymentRepoConfigResponse is the response body of
// GET and PUT /api/deployment/repos/{owner}/{name}/config: the
// repository's configuration as it resolves over the deployment's
// defaults (model.EffectiveRepoConfig).
type DeploymentRepoConfigResponse struct {
	Repo      string                    `json:"repo"`
	Effective model.EffectiveRepoConfig `json:"effective"`
}

// SetDeploymentRepoConfigRequest carries the fields PUT
// /api/deployment/repos/{owner}/{name}/config changes on the repository's
// repo_config row. A field left out keeps what the row holds.
type SetDeploymentRepoConfigRequest struct {
	SetupCommand                 *string   `json:"setupCommand,omitempty"`
	PromptExtension              *string   `json:"promptExtension,omitempty"`
	DefaultCapabilities          *[]string `json:"defaultCapabilities,omitempty"`
	CICapabilities               *[]string `json:"ciCapabilities,omitempty"`
	CIMode                       *string   `json:"ciMode,omitempty"`
	RepairTemplateID             *string   `json:"repairTemplateId,omitempty"`
	CloneURL                     *string   `json:"cloneUrl,omitempty"`
	GitCredential                *string   `json:"gitCredential,omitempty"`
	RequireBranchUpToDate        *bool     `json:"requireBranchUpToDate,omitempty"`
	WatchDefaultBranch           *bool     `json:"watchDefaultBranch,omitempty"`
	MergeOnGreen                 *bool     `json:"mergeOnGreen,omitempty"`
	BaseRepairBypassesHumanGates *bool     `json:"baseRepairBypassesHumanGates,omitempty"`
	ReleaseSecrets               *[]string `json:"releaseSecrets,omitempty"`
	User                         string    `json:"user,omitempty"`
}

// existingTeam is the deployment in the team shape when id names the
// default team, the only one there is since schema 24, and a
// teamNotFoundError otherwise.
func (c *Client) existingTeam(ctx context.Context, id string) (*Team, error) {
	if id != model.DefaultTeamID {
		return nil, &teamNotFoundError{ID: id}
	}
	settings, err := c.deploymentSettings(ctx)
	if err != nil {
		return nil, err
	}
	team := teamFrom(settings, c.gitDefaultHost())
	return &team, nil
}

// teamRepoTarget is existingTeam for a team repo configuration action on
// repo: one the team does not target is refused, unless a person -- the
// deployment's owner -- asks.
func (c *Client) teamRepoTarget(ctx context.Context, teamID string, repo model.RepoRef) (*Team, error) {
	team, err := c.existingTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
	if len(team.TargetRepos) == 0 || model.RepoAllowed(team.TargetRepos, repo) {
		return team, nil
	}
	// A person in the context, or -- with nobody in it at all, the
	// trusted in-process caller -- the daemon's own actor when that is a
	// person. An agent in the context never falls back to the daemon's
	// actor: it is no owner, so it gets no exception.
	actingUser := teamActingUser(ctx, "")
	if _, named := PrincipalFromContext(ctx); !named && actingUser == "" && c.Config.Actor.Kind == model.PrincipalHuman {
		actingUser = strings.TrimSpace(c.Config.Actor.ID)
	}
	if actingUser != "" {
		return team, nil
	}
	return nil, validationErrorf("repository %q is not in this deployment's target repos", repo.String())
}

// GetDeploymentRepoConfigWithUser returns repo's configuration as it
// resolves over the deployment's defaults. A repository outside the
// deployment's target repos is refused, unless a person asks.
func (c *Client) GetDeploymentRepoConfigWithUser(ctx context.Context, repo, user string) (DeploymentRepoConfigResponse, error) {
	if c.Store == nil {
		return DeploymentRepoConfigResponse{}, nil
	}
	parsedRepo, err := c.parseRepo(repo)
	if err != nil {
		return DeploymentRepoConfigResponse{}, validationErrorf("invalid repo: %v", err)
	}
	if _, err := c.teamRepoTarget(ctx, model.DefaultTeamID, parsedRepo); err != nil {
		return DeploymentRepoConfigResponse{}, err
	}
	return c.deploymentRepoConfigResponse(ctx, parsedRepo)
}

func (c *Client) deploymentRepoConfigResponse(ctx context.Context, repo model.RepoRef) (DeploymentRepoConfigResponse, error) {
	eff, err := c.Store.EffectiveRepoConfig(ctx, repo)
	if err != nil {
		return DeploymentRepoConfigResponse{}, err
	}
	return DeploymentRepoConfigResponse{Repo: repo.String(), Effective: eff}, nil
}

// SetDeploymentRepoConfigWithUser changes the fields req names on repo's
// repo_config row, creating the row when there is none. Until schema 23
// these were the default team's team_repo_config overrides, layered over
// the row; that table is folded into repo_config now
// (docs/single-tenant-reset.md, 6-ii-b3).
func (c *Client) SetDeploymentRepoConfigWithUser(ctx context.Context, repo string, req SetDeploymentRepoConfigRequest, user string) (DeploymentRepoConfigResponse, error) {
	if c.Store == nil {
		return DeploymentRepoConfigResponse{}, nil
	}
	parsedRepo, err := c.parseRepo(repo)
	if err != nil {
		return DeploymentRepoConfigResponse{}, validationErrorf("invalid repo: %v", err)
	}
	if _, err := c.teamRepoTarget(ctx, model.DefaultTeamID, parsedRepo); err != nil {
		return DeploymentRepoConfigResponse{}, err
	}
	if err := c.checkSettingsWritable(ctx, "repo_config"); err != nil {
		return DeploymentRepoConfigResponse{}, err
	}

	existing, err := c.Store.GetRepoConfig(ctx, parsedRepo)
	if err != nil {
		return DeploymentRepoConfigResponse{}, err
	}
	cfg := model.RepoConfig{Repo: parsedRepo}
	if existing != nil {
		cfg = *existing
	}

	if req.SetupCommand != nil {
		cfg.SetupCommand = strings.TrimSpace(*req.SetupCommand)
	}
	if req.PromptExtension != nil {
		cfg.PromptExtension = strings.TrimSpace(*req.PromptExtension)
	}
	if req.DefaultCapabilities != nil {
		// Unknown capabilities and hand-attached grain-ci are refused
		// with capabilityRefusal, matching repo and settings defaults.
		// Ids already stored are let through, as for ciCapabilities below.
		clean := make([]string, 0, len(*req.DefaultCapabilities))
		seen := make(map[string]bool, len(*req.DefaultCapabilities))
		for _, capID := range *req.DefaultCapabilities {
			t := strings.TrimSpace(capID)
			if t != "" && !seen[t] {
				seen[t] = true
				if !slices.Contains(cfg.DefaultCapabilities, t) {
					if _, ok := c.capabilityByID(t); !ok {
						return DeploymentRepoConfigResponse{}, validationErrorf("defaultCapabilities: %s", capabilityRefusal(t))
					}
				}
				clean = append(clean, t)
			}
		}
		cfg.DefaultCapabilities = clean
	}
	if req.CICapabilities != nil {
		// Capabilities not supported for CI (not in the CI catalogue) are
		// refused, matching SetRepoCICapabilities. One already stored is
		// let through, so a save that does not touch it never makes the
		// rest of the form unsaveable.
		allowedCI := c.ciCapabilityIDs()
		clean := make([]string, 0, len(*req.CICapabilities))
		seen := make(map[string]bool, len(*req.CICapabilities))
		for _, capID := range *req.CICapabilities {
			t := strings.TrimSpace(capID)
			if t != "" && !seen[t] {
				canonicalID := t
				if target, ok := model.RepoReadTarget(t); ok {
					canonicalID = model.RepoReadCapability(target)
				} else if target, ok := model.RepoWriteTarget(t); ok {
					canonicalID = model.RepoWriteCapability(target)
				} else if !slices.Contains(allowedCI, t) {
					if !slices.Contains(cfg.CICapabilities, t) {
						return DeploymentRepoConfigResponse{}, validationErrorf("ciCapabilities: capability %q is not supported for CI", t)
					}
				}
				if !seen[canonicalID] {
					seen[canonicalID] = true
					clean = append(clean, canonicalID)
				}
			}
		}
		cfg.CICapabilities = clean
	}
	if req.CIMode != nil {
		mode := strings.ToLower(strings.TrimSpace(*req.CIMode))
		if mode != "" && mode != string(model.CIModeGrain) && mode != string(model.CIModeGitHub) && mode != string(model.CIModeNone) {
			return DeploymentRepoConfigResponse{}, validationErrorf("invalid ciMode %q", *req.CIMode)
		}
		cfg.CIMode = model.CIMode(mode)
	}
	if req.RepairTemplateID != nil {
		cfg.RepairTemplateID = strings.TrimSpace(*req.RepairTemplateID)
	}
	if req.CloneURL != nil {
		cfg.CloneURL = strings.TrimSpace(*req.CloneURL)
	}
	if req.GitCredential != nil {
		gitCred := strings.TrimSpace(*req.GitCredential)
		cfg.GitCredential = gitCred
	}
	if req.RequireBranchUpToDate != nil {
		cfg.RequireBranchUpToDate = *req.RequireBranchUpToDate
	}
	if req.WatchDefaultBranch != nil {
		cfg.WatchDefaultBranch = *req.WatchDefaultBranch
	}
	if req.MergeOnGreen != nil {
		cfg.MergeOnGreen = *req.MergeOnGreen
	}
	if req.BaseRepairBypassesHumanGates != nil {
		cfg.BaseRepairBypassesHumanGates = *req.BaseRepairBypassesHumanGates
	}
	if req.ReleaseSecrets != nil {
		clean := make([]string, 0, len(*req.ReleaseSecrets))
		seen := make(map[string]bool, len(*req.ReleaseSecrets))
		for _, s := range *req.ReleaseSecrets {
			t := strings.TrimSpace(s)
			if t != "" && !seen[t] {
				seen[t] = true
				clean = append(clean, t)
			}
		}
		cfg.ReleaseSecrets = clean
	}

	if err := c.Store.PutRepoConfig(ctx, cfg); err != nil {
		return DeploymentRepoConfigResponse{}, err
	}
	return c.deploymentRepoConfigResponse(ctx, parsedRepo)
}

// ListTeamSecretAuditWithUser lists recent secret resolution audit entries for a team.
func (c *Client) ListTeamSecretAuditWithUser(ctx context.Context, teamID, user string, limit int) ([]model.SecretAuditEntry, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		teamID = model.DefaultTeamID
	}
	if _, err := c.existingTeam(ctx, teamID); err != nil {
		return nil, err
	}
	return c.Store.ListSecretAudit(ctx, limit)
}

// ListTeamAuditLogsWithUser is teamID's audit trail -- the team's secret
// resolutions and its credentials' accesses through the git proxy,
// newest first.
func (c *Client) ListTeamAuditLogsWithUser(ctx context.Context, teamID, user string, limit int) ([]model.TeamAuditEvent, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		teamID = model.DefaultTeamID
	}
	if _, err := c.existingTeam(ctx, teamID); err != nil {
		return nil, err
	}
	return c.Store.ListTeamAuditEvents(ctx, limit)
}

// maxTeamAuditLogs caps ?limit= on GET /api/deployment/audit-logs.
const maxTeamAuditLogs = 1000

// cleanModelSettings trims each setting and keeps one per model, the
// last stated winning, dropping any that name no model.
func cleanModelSettings(in []model.ModelSetting) []model.ModelSetting {
	var out []model.ModelSetting
	for _, s := range in {
		s.Model, s.Effort = strings.TrimSpace(s.Model), strings.TrimSpace(s.Effort)
		if s.Model == "" {
			continue
		}
		out = slices.DeleteFunc(out, func(have model.ModelSetting) bool { return strings.EqualFold(have.Model, s.Model) })
		out = append(out, s)
	}
	return out
}
