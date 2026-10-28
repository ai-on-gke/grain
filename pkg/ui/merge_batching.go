package ui

import (
	"context"
	"log"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// MergeBatching is what this deployment's merge batches have actually
// cost and saved -- model.MergeBatchStats over the bounded history grain
// keeps of finished batches (model.MergeBatchRecord, grain/task-300),
// reported beside the setting that decides how big they are.
//
// It is here rather than on a report board because it is the evidence for
// one decision, and the decision is taken in this pane: Settings ->
// Engine holds the batch size, and "three" is a number somebody has to
// choose. Without these counters the choice is made blind -- a batch's
// own row is deleted the moment it ends, so the pane could say what the
// size *is* and nothing at all about whether it is right.
//
// How to read it is in docs/merge-queue-batching.md, and the two numbers
// that decide it are FirstTry against Fresh (batches that passed first
// time, so the bet is paying and could be raised) and Split against
// Batches (batches that failed and had to be bisected, so it is too big).
// AtLimit is the third: a limit nothing ever reached is not one that is
// holding anything back, however well the batches do.
type MergeBatching struct {
	// Batches is how many finished batches the history holds and Changes
	// how many changes landed on one of their verdicts.
	Batches int `json:"batches"`
	Changes int `json:"changes"`
	// Fresh is batches formed from the queue and FirstTry the ones of
	// those that passed -- the pass rate worth quoting, since a halved
	// batch passing is the bisect working rather than a bet paying off.
	Fresh    int `json:"fresh"`
	FirstTry int `json:"firstTry"`
	// Split, Culprits and Abandoned are the three ways a batch costs
	// rather than saves: it failed and halved, it failed alone and named
	// the change that broke it, or it answered nothing at all.
	Split     int `json:"split"`
	Culprits  int `json:"culprits"`
	Abandoned int `json:"abandoned"`
	// Biggest is the largest bet made and AtLimit how many took every
	// member the size allowed while more were queued behind them.
	Biggest int `json:"biggest"`
	AtLimit int `json:"atLimit"`
	// Suites is what batching spent, SerialSuites the most a one-at-a-time
	// queue would have spent to land the same changes, and Saved the
	// difference -- negative for a deployment whose batches keep failing.
	// Both sides count the post-merge suite grain runs when a base
	// branch's tip moves, because batching moves it once per batch where
	// the serial queue moves it once per change. The most rather than the
	// count: one post-merge run goes at a time per branch, so several
	// merges on a busy branch already share one
	// (model.MergeBatchRecord.SerialSuites, model.BaseBranchCI.Covers),
	// and the pane says so rather than quoting the difference as fact.
	Suites       int `json:"suites"`
	SerialSuites int `json:"serialSuites"`
	Saved        int `json:"saved"`
	// FirstAt and LastAt bound the history: counters over an hour and
	// counters over a month read very differently, and a pane that showed
	// neither would be inviting a decision off three batches.
	FirstAt *time.Time `json:"firstAt,omitempty"`
	LastAt  *time.Time `json:"lastAt,omitempty"`
}

// mergeBatching reads this deployment's batch history and counts it, or
// answers nil where there is nothing to say -- a deployment that has
// never formed a batch, and one whose store cannot be read.
//
// Best-effort on purpose: this is a measurement beside a setting, and a
// Settings pane that failed to load because a counter could not be read
// would be trading the thing people need for the thing they are curious
// about.
func (c *Client) mergeBatching(ctx context.Context, cfg model.Config) *MergeBatching {
	if c.Store == nil {
		return nil
	}
	records, err := c.Store.MergeBatchRecords(ctx)
	if err != nil {
		log.Printf("ui: reading the merge batch history: %v", err)
		return nil
	}
	if len(records) == 0 {
		return nil
	}
	s := model.SummarizeMergeBatches(records, cfg.MergeBatchSizeOr())
	return &MergeBatching{
		Batches: s.Batches, Changes: s.Changes,
		Fresh: s.Fresh, FirstTry: s.FirstTry,
		Split: s.Split, Culprits: s.Culprits, Abandoned: s.Abandoned,
		Biggest: s.Biggest, AtLimit: s.AtLimit,
		Suites: s.Suites, SerialSuites: s.SerialSuites, Saved: s.Saved,
		FirstAt: s.FirstAt, LastAt: s.LastAt,
	}
}
