package mcp

import (
	"context"
	"fmt"
	"strings"
)

// SandboxRecreationReport is what one recreate_sandbox call did: the
// sandbox that was rebuilt, and how much of the setup grain had put into
// the old one it managed to put back into the new one.
//
// Restored and Warnings are prose, one entry per step, for the same
// reason renderPullRequestReport prints lines rather than JSON: the
// reader is a model deciding what to do next, and "your repo is cloned
// again but your git credentials are not" is the useful shape of that
// answer, not a status code.
type SandboxRecreationReport struct {
	Sandbox     string
	CheckoutDir string
	Restored    []string
	Warnings    []string
}

// SandboxRecreator destroys the sandbox this server's run is working in
// and builds an empty one under the same name, putting back what grain
// itself had set up in it.
//
// It is an interface here, and one that takes no arguments, for the same
// reason PullRequestOpener next door is. Creating and destroying
// sandboxes is grain's: this process holds nothing but a transport into
// a guest, and the shape of the VM to build, the token to mint for it
// and the repo to clone into it are all facts about the run that live in
// the daemon. So the real implementation (pkg/cli/mcpserver.go's
// daemonSandbox) asks the daemon over its REST API, which rebuilds the
// sandbox of whichever run that task currently has. Nothing an agent can
// put in a tool call chooses which sandbox is destroyed -- it can only
// ever be its own.
//
// It is also the one tool here that destroys something, which is why it
// takes the same route as open_pull_request rather than being something
// a run does for itself: a write, made where grain already makes writes.
type SandboxRecreator interface {
	RecreateSandbox(ctx context.Context) (SandboxRecreationReport, error)
}

// NewRecreateSandboxTools returns the one tool a run gets when its
// dispatch can rebuild its own sandbox: recreate_sandbox.
//
// This is the tool for the failure an agent otherwise cannot do anything
// about. Every other tool here runs *inside* the sandbox, so a sandbox
// that has been broken badly enough -- a filesystem left in a state no
// command can untangle, a full disk, a wedged process, a guest that has
// simply stopped answering -- takes every one of them down with it, and
// the run spends the rest of its turns failing at things that have
// nothing to do with its task. Until now the only way out was for the
// run to end and the whole task to be redispatched, which throws away
// the agent's context along with the broken sandbox.
//
// Like open_pull_request, and unlike NewMockTools' escape hatches, its
// effect is real and immediate rather than deferred to the end of the
// run: the entire point is that the agent carries on working
// afterwards, in the sandbox this call built.
//
// recreator nil returns the tool anyway, refusing every call -- what
// lets each agent framework's allowedTools enumerate the names this
// package registers without holding a live recreator, exactly as
// NewOpenPullRequestTools does.
func NewRecreateSandboxTools(recreator SandboxRecreator) []Tool {
	return []Tool{recreateSandboxTool(recreator)}
}

func recreateSandboxTool(recreator SandboxRecreator) Tool {
	return Tool{
		Name: "recreate_sandbox",
		Description: "Destroy the sandbox you are working in and build a fresh, empty " +
			"one in its place, then carry on working in it. This is the escape " +
			"hatch for a sandbox you cannot repair from inside it, because every " +
			"other tool you have runs inside it: a filesystem an interrupted " +
			"install or build has left in a state you cannot untangle, a full " +
			"disk, a process you cannot kill, or a machine that has started " +
			"failing commands for reasons that have nothing to do with your task. " +
			"EVERYTHING IN THE SANDBOX IS DESTROYED, including every uncommitted " +
			"change you have made -- commit and push anything you want to keep " +
			"before you call this, because a pushed branch lives on the remote " +
			"and is cloned back for you. grain puts back what it set up for you " +
			"in the first place: your git credentials, a fresh clone of your repo " +
			"with your branch checked out (including commits you had already " +
			"pushed), your task's attachments, and any credential files your " +
			"capabilities placed. Anything you installed or built yourself is " +
			"gone and you will have to do it again. Do not reach for this over an " +
			"ordinary failing command or a test you cannot get to pass: it costs " +
			"you a machine rebuild and a fresh clone, and it fixes nothing that " +
			"is wrong with your code. It takes no arguments -- it can only ever " +
			"rebuild your own run's sandbox.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{},
		},
		Handler: func(ctx context.Context, _ map[string]any) Result {
			if recreator == nil {
				return Result{
					Text: "This run cannot rebuild its own sandbox (no route back to the " +
						"grain daemon that owns it). Work with the sandbox you have, and " +
						"if it is genuinely unusable, say so in a comment before you " +
						"finish so a human knows why the run got nowhere.",
					IsError: true,
				}
			}
			report, err := recreator.RecreateSandbox(ctx)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			return Result{Text: renderSandboxRecreation(report)}
		},
	}
}

// renderSandboxRecreation is what the agent actually reads back. Three
// things, in the order they matter to whoever is about to take their
// next turn in this sandbox: that it really is gone, what is in the new
// one, and what grain could not put back.
//
// The warnings are last and named as such rather than folded in with the
// rest, because they are the half that changes what the agent should do
// next: a run whose credentials did not come back cannot push, and one
// whose repo did not clone has an empty directory rather than the
// checkout everything else it was told assumes.
func renderSandboxRecreation(r SandboxRecreationReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Sandbox %s has been destroyed and rebuilt. Everything that was in it "+
		"is gone, including anything you had not pushed.\n", r.Sandbox)

	if len(r.Restored) == 0 {
		b.WriteString("\ngrain had nothing of its own to put back, so this is a bare, empty sandbox.\n")
	} else {
		b.WriteString("\ngrain has put back:\n")
		for _, s := range r.Restored {
			fmt.Fprintf(&b, "- %s\n", s)
		}
	}

	if len(r.Warnings) > 0 {
		b.WriteString("\nWhat it could not put back -- read these before you carry on:\n")
		for _, w := range r.Warnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
	}

	if r.CheckoutDir != "" {
		fmt.Fprintf(&b, "\nStart again from ./%s. Anything you installed or built in the old "+
			"sandbox you will have to do again.\n", r.CheckoutDir)
	}
	return strings.TrimRight(b.String(), "\n")
}
