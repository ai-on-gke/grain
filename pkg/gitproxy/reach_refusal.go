package gitproxy

// Reach's answer turned into the one sentence every filing path gives a
// task its team cannot reach: ui.Client.CreateTask refuses a person's
// task with it, and every path automation files through -- CreateTask
// for a caller acting as grain, the end-of-run replay of propose_task,
// a schedule's firing, a suite's passes -- files the task and parks it
// on ReachParkComment (grain/task-1850, grain/task-1857). One home, so
// the operator is told the same thing whichever way the task came in.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TeamCredentials is which named credential a team uses for a
// repository -- model.Store.TeamGitCredential.
type TeamCredentials interface {
	TeamGitCredential(ctx context.Context, team, owner, repo string) (string, error)
}

// HostTeamCredentials is TeamCredentials for a repository on a git host
// other than the deployment's default -- model.Store.TeamGitCredentialOn.
// Refusal asks it for a repository named with its host, so the team's
// connection on that host is the one checked, not its default host's.
type HostTeamCredentials interface {
	TeamGitCredentialOn(ctx context.Context, team, host, owner, repo string) (string, error)
}

// reachNeed is one repository a task needs and the access it needs there.
type reachNeed struct {
	repo model.RepoRef
	push bool
	role string
}

// Refusal is the sentence a task is refused (or parked) with when its
// team's credential cannot reach one of the repositories it names --
// push for its target and every extra write, read for every read -- and
// "" when it can, or when nothing can say: a nil Reach, a listing GitHub
// would not give, a config repository the controller serves itself.
// Unknown never refuses; the dispatch-side stop
// (orchestrator.stopOnAccessRefused) is still there behind it.
func (r *Reach) Refusal(ctx context.Context, teams TeamCredentials, team string, target *model.RepoRef,
	reads []model.RepoRef, writes []model.WriteTarget) (string, error) {

	if r == nil {
		return "", nil
	}
	// Whichever team filed it, the task reaches its repositories with the
	// deployment's git connections, which the default team's row holds
	// (model.Store.GitConnectionOn).
	team = model.DefaultTeamID
	var needs []reachNeed
	if target != nil {
		needs = append(needs, reachNeed{repo: *target, push: true, role: "target"})
	}
	for _, w := range writes {
		needs = append(needs, reachNeed{repo: w.Repo, push: true, role: "write"})
	}
	for _, rd := range reads {
		needs = append(needs, reachNeed{repo: rd, push: false, role: "read"})
	}
	var problems []string
	var needAccess, onGitHub bool
	for _, need := range needs {
		if model.IsConfigRepo(need.repo) {
			continue
		}
		var name string
		var err error
		onHost, hostAware := teams.(HostTeamCredentials)
		if need.repo.Host != "" && hostAware {
			name, err = onHost.TeamGitCredentialOn(ctx, team, need.repo.Host, need.repo.Owner, need.repo.Name)
		} else {
			name, err = teams.TeamGitCredential(ctx, team, need.repo.Owner, need.repo.Name)
		}
		if errors.Is(err, model.ErrNoTeamGitCredential) {
			if need.repo.Host != "" {
				problems = append(problems, fmt.Sprintf("the deployment has no git connection on %s, so it cannot reach %s",
					need.repo.Host, need.repo))
				continue
			}
			if r.hostKind("", "") == model.GitHostGit {
				problems = append(problems, fmt.Sprintf("the deployment has no git connection, so it cannot reach %s", need.repo))
				continue
			}
			problems = append(problems, fmt.Sprintf("the deployment has no GitHub credential, so it cannot reach %s", need.repo))
			continue
		}
		if err != nil {
			return "", err
		}
		got, err := r.Check(name, need.repo.Owner, need.repo.Name, need.push)
		if err != nil {
			log.Printf("gitproxy: checking whether team %s's credential %q reaches %s: %v", team, name, need.repo, err)
			continue
		}
		if got != Unreachable {
			continue
		}
		access := "read"
		if need.push {
			access = "push to"
		}
		needAccess = true
		if r.hostKind(name, need.repo.Host) == model.GitHostGit {
			problems = append(problems, fmt.Sprintf("the deployment's git connection %q cannot %s %s (its %s repository)",
				name, access, need.repo, need.role))
		} else {
			onGitHub = true
			problems = append(problems, fmt.Sprintf("the deployment's GitHub credential %q cannot %s %s (its %s repository)",
				name, access, need.repo, need.role))
		}
	}
	if len(problems) == 0 {
		return "", nil
	}
	if !needAccess {
		return strings.Join(problems, "; ") + " -- add one in Settings → Git (/settings/git), then file this again.", nil
	}
	if onGitHub {
		return strings.Join(problems, "; ") + " -- give the credential access on GitHub, or choose another in " +
			"Settings → Git (/settings/git), then file this again.", nil
	}
	return strings.Join(problems, "; ") + " -- give the credential access, or choose another in " +
		"Settings → Git (/settings/git), then file this again.", nil
}

// hostKind is the kind of host (model.GitHostGitHub or model.GitHostGit)
// for a repository on repoHost (or the default host when empty), read
// from the credential name that covers it or any credential recorded on
// repoHost. A non-default host with no record yet is treated as a plain
// git host until a connection says otherwise.
func (r *Reach) hostKind(name, repoHost string) model.GitHostKind {
	if r != nil && r.Credentials != nil {
		if name != "" {
			if h := r.Credentials.HostOf(name); h.Recorded || repoHost == "" {
				return h.Kind
			}
		}
		if repoHost != "" {
			if h := r.Credentials.HostOf(repoHost); h.Recorded {
				return h.Kind
			}
		}
		if repoHost == "" {
			return r.Credentials.HostOf(r.Host).Kind
		}
	}
	if repoHost != "" && (r == nil || !model.SameGitHost(repoHost, r.Host, model.DefaultGitHost)) {
		return model.GitHostGit
	}
	return model.GitHostGitHub
}

// TaskRefusal is Refusal for a task already stored: its own target,
// reads and writes.
func (r *Reach) TaskRefusal(ctx context.Context, teams TeamCredentials, task model.Task) (string, error) {
	return r.Refusal(ctx, teams, model.DefaultTeamID, task.Target, task.Reads, task.Writes)
}

// ReachParkComment is what a task filed by automation is parked on when
// its team cannot reach a repository it names: the refusal a person
// filing it would have been given, and what a reply does.
func ReachParkComment(refusal string) string {
	return "This task was filed, but it cannot be dispatched yet: " + refusal +
		"\n\nOnce the deployment's credential can reach it, reply here to have it dispatched."
}
