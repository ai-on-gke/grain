package model

import "strings"

// The prompt extension: standing instructions an operator adds to every
// dispatch's prompt, on top of what orchestrator.BuildPrompt already
// says (grain/task-114).
//
// BuildPrompt is deliberately only the facts that are grain's own -- the
// task, the branch, the repo, the push/check/repair loop -- because
// everything it says has to be true of every deployment there will ever
// be. A deployment has facts of its own that grain cannot know and a
// task author should not have to retype on every task: a house style, a
// test command, a review convention, "this repo's migrations live
// elsewhere, read docs/x.md before touching them." Without somewhere to
// put those, the only place they fit is the body of each task, which is
// the same sentence written again per task and drifting one task at a
// time.
//
// Three layers say it, and PromptExtensionFor is the whole of how they
// compose:
//
//   - Config.PromptExtension, the deployment's own, on every dispatch.
//   - RepoConfig.PromptExtension, appended after it for a task that
//     targets that repo.
//   - Task.PromptExtension, which *replaces* both for that one task.
//
// Deployment and repo compose by appending, the same "a repo adds to the
// deployment and never subtracts from it" rule
// RepoConfig.DefaultCapabilities already holds to, for the same reason:
// two people write these at two different times, and a repo silently
// discarding what the deployment said would be a setting that fails
// where nobody is looking.
//
// A task, by contrast, replaces. "Overridable for specific tasks" is
// what this was asked for, and an override that could only append would
// leave no way to run one task without instructions that are wrong for
// it -- a repo-wide "never touch the generated client" is exactly the
// thing a task regenerating that client has to be exempt from. The cost
// is that a task that wants to keep the deployment's text and add a line
// has to restate it; the UI shows what it is replacing while that choice
// is being made, which is where restating it is cheap.
//
// What it does not offer is a way to say "this task gets no extension at
// all": empty means "no override" here, the same "zero means unset"
// every other per-task override uses (Task.AgentFramework,
// Task.SandboxCPUs), and a task that needs silence is one nobody has
// needed yet. A single space would not do it either -- everything here
// is trimmed.

// PromptExtensionFor resolves those three layers into the one block of
// text a dispatch is actually given, or "" when no layer has anything to
// say.
//
// Every layer is trimmed before it is read, so a box someone typed a
// newline into is the empty setting it looks like rather than a layer
// that silently suppresses the ones beside it. Trimming happens on the
// way in as well (ui.UpdateSettings, ui.SetRepoPromptExtension,
// ui.CreateTask); doing it here too is what makes the rule hold for a
// row written by an older build, by hand, or by a client that did not.
func PromptExtensionFor(deployment, repo, task string) string {
	if t := strings.TrimSpace(task); t != "" {
		return t
	}
	var parts []string
	for _, layer := range []string{deployment, repo} {
		if v := strings.TrimSpace(layer); v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, "\n\n")
}
