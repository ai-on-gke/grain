package mcp

import (
	"fmt"

	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/github"
)

// annotateChecksWithAttribution classifies failing check runs by comparing them
// against the latest check runs on the target base branch (e.g., main).
func annotateChecksWithAttribution(client PullRequestReader, scope PullRequestScope, checks []github.CheckRun) map[string]ci.JobAttribution {
	if client == nil || !anyFailed(checks) {
		return nil
	}

	base, _, err := resolveBase(client, scope)
	if err != nil || base == "" {
		return nil
	}

	baseHead, err := client.GetBranchHead(scope.Owner, scope.Repo, base)
	if err != nil || baseHead == nil {
		return nil
	}

	baseChecks, err := checkRunsForCommit(client, scope, baseHead.SHA)
	if err != nil {
		return nil
	}

	baseByJob := make(map[string]github.CheckRun)
	for _, bc := range baseChecks {
		baseByJob[bc.Name] = bc
	}

	out := make(map[string]ci.JobAttribution)
	for _, c := range checks {
		if !checkFailed(c) {
			continue
		}
		bc, ok := baseByJob[c.Name]
		if !ok {
			continue
		}

		commit := shortSHA(baseHead.SHA)
		if bc.Status == "completed" && bc.Conclusion != nil && (*bc.Conclusion == "success" || *bc.Conclusion == "neutral") {
			out[c.Name] = ci.JobAttribution{
				Kind:       ci.AttributionRegression,
				JobName:    c.Name,
				BaseRef:    base,
				BaseCommit: baseHead.SHA,
				Reason:     fmt.Sprintf("The check passed on the base branch at commit %s but failed on this commit.", commit),
				Guidance:   ci.GuidanceRegression,
			}
		} else if checkFailed(bc) {
			out[c.Name] = ci.JobAttribution{
				Kind:       ci.AttributionSharedBaseFault,
				JobName:    c.Name,
				BaseRef:    base,
				BaseCommit: baseHead.SHA,
				Reason:     fmt.Sprintf("The check failed on %s at commit %s.", base, commit),
				Guidance:   ci.GuidanceSharedBaseFault,
			}
		}
	}
	return out
}
