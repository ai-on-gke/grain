package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/localscripts"
	"github.com/ai-on-gke/grain/pkg/localexec"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// LocalScriptsFlags is the forked mcpserver's -local-scripts list: one
// NAME=PATH per declaration this run is served (agent.GrantArgs).
type LocalScriptsFlags []LocalScriptsFlag

// LocalScriptsFlag is one -local-scripts entry.
type LocalScriptsFlag struct{ Name, Path string }

func (l *LocalScriptsFlags) String() string {
	var parts []string
	for _, e := range *l {
		parts = append(parts, e.Name+"="+e.Path)
	}
	return strings.Join(parts, " ")
}

func (l *LocalScriptsFlags) Set(value string) error {
	var cfg localscripts.Config
	if err := cfg.ParseHost(value); err != nil {
		return err
	}
	name, path, _ := strings.Cut(value, "=")
	*l = append(*l, LocalScriptsFlag{Name: name, Path: path})
	return nil
}

// daemonLocalScriptApprover is the run's localexec.Approver: each call
// is posted to the daemon, which shows it on the task for a person to
// approve or deny, and the call waits on the answer for as long as ctx
// (the script's approval timeout) allows.
type daemonLocalScriptApprover struct {
	client *ui.HTTPClient
	taskID string
}

func (d daemonLocalScriptApprover) Approve(ctx context.Context, r localexec.Request) (bool, error) {
	approval, err := d.client.RequestLocalScriptApproval(ctx, d.taskID, r)
	if err != nil {
		return false, fmt.Errorf("asking for approval: %w", err)
	}
	for {
		switch approval.Status {
		case ui.LocalScriptApproved:
			return true, nil
		case ui.LocalScriptDenied:
			return false, nil
		case ui.LocalScriptExpired:
			return false, errors.New("nobody answered before the call stopped waiting")
		}
		if ctx.Err() != nil {
			break
		}
		next, err := d.client.WaitLocalScriptApproval(ctx, d.taskID, approval.ID)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			// A failed poll is retried after a moment rather than
			// ending the call: the daemon restarting under a waiting
			// call is what this most likely is, and the deadline still
			// bounds the wait.
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
			continue
		}
		approval = next
	}
	// The call's own deadline has passed (or the run is ending): say so,
	// so the pane stops offering buttons nobody's answer can reach.
	expire, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = d.client.ExpireLocalScriptApproval(expire, d.taskID, approval.ID)
	return false, ctx.Err()
}

// localScriptTools snapshots the declarations a run is served and
// returns their tools and the function that closes their executors when
// the run ends. A declaration whose capability the task does not hold
// (grants, when the daemon said which it holds) is not served; nor is any
// declaration at all when one fails its snapshot, which is said on
// stderr, where the daemon's subprocess plumbing carries it to an
// operator, rather than taking the run's other tools down with it.
func localScriptTools(list LocalScriptsFlags, envFile string, grants GrantNames, grantsKnown bool,
	client *ui.HTTPClient, taskID string) ([]mcp.Tool, func()) {
	if len(list) == 0 {
		return nil, func() {}
	}
	var paths []string
	for _, l := range list {
		if grantsKnown && !grants.Has(localscripts.CapabilityName(l.Name)) {
			fmt.Fprintf(os.Stderr, "grain mcpserver: -local-scripts %s: task %s does not hold %s; not serving it\n",
				l.Name, taskID, localscripts.CapabilityName(l.Name))
			continue
		}
		paths = append(paths, l.Path)
	}
	creds := localscripts.Credentials{}
	if envFile != "" {
		var err error
		if creds, err = localscripts.ReadCredentialsFile(envFile); err != nil {
			fmt.Fprintf(os.Stderr, "grain mcpserver: -local-scripts-env-file: %v; not serving local scripts\n", err)
			return nil, func() {}
		}
	}
	var approver localexec.Approver
	if client != nil {
		approver = daemonLocalScriptApprover{client: client, taskID: taskID}
	}
	execs, err := localscripts.Open(paths, creds, approver)
	if err != nil {
		fmt.Fprintf(os.Stderr, "grain mcpserver: not serving local scripts: %v\n", err)
		return nil, func() {}
	}
	var tools []mcp.Tool
	for _, e := range execs {
		tools = append(tools, e.Tools()...)
	}
	return tools, func() {
		for _, e := range execs {
			e.Close()
		}
	}
}
