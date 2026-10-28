package ui

import (
	"context"
	"log"

	"github.com/bwsalmon/grain/pkg/model"
)

// A repository that has quietly stopped shipping, said out loud
// (grain/task-511). The finding itself, and the reasoning behind when it
// is made, is model.BaseBranchPublishing's (pkg/model's
// base_branch_publishing.go): the orchestrator raises the
// `pipeline.not-publishing` notification from the same reading
// (docs/design/durable-tasks.md, DT-5), so the health page and the
// notification cannot disagree.

// BaseBranchPublishing is one base branch grain has published nothing
// from for long enough to be worth telling somebody about.
type BaseBranchPublishing = model.BaseBranchPublishing

// baseBranchPublishing is every base branch this deployment has stopped
// publishing from, for the deployment health response and through it
// read_deployment_health.
//
// nil on a UI with no store behind it and on a reading that could not be
// taken -- "no line in the pane" rather than a claim that everything is
// shipping, which is the nil-means-unavailable contract the rest of that
// response keeps.
func (c *Client) baseBranchPublishing(ctx context.Context) []BaseBranchPublishing {
	if c.Store == nil {
		return nil
	}
	out, err := c.Store.BaseBranchPublishingFindings(ctx, c.now())
	if err != nil {
		log.Printf("ui: the publishing reading: %v", err)
		return nil
	}
	return out
}

// baseBranchPublishingFor is the same reading for one repository, for its
// own release pane to put beside the switch that decides whether it
// publishes at all. nil when that repository is shipping, which is one
// alert fewer rather than a reassurance.
//
// Where a repository has several base branches the one that has been
// failing longest is reported: the pane is a paragraph beside a switch
// rather than a table.
func (c *Client) baseBranchPublishingFor(ctx context.Context, repo model.RepoRef) *BaseBranchPublishing {
	var worst *BaseBranchPublishing
	for _, finding := range c.baseBranchPublishing(ctx) {
		if finding.Repo != repo.String() {
			continue
		}
		if worst == nil || finding.FailingForMS > worst.FailingForMS {
			found := finding
			worst = &found
		}
	}
	return worst
}
