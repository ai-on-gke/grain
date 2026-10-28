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
	"log"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// MergeGathering is what this deployment's gather holds have actually
// done -- model.MergeGatherStats over the bounded history grain keeps of
// ended holds (model.MergeGatherRecord, grain/task-440), reported beside
// the setting that decides how long one may last.
//
// It is here for MergeBatching's reason, one field further down: the
// window above it is a duration somebody has to choose, and until this
// the choice was made with no evidence whatever about the thing being
// chosen. A hold lives in the daemon's memory and its only visible trace
// is the row on the change it is holding, which is overwritten the
// moment the wait ends -- so the batch counters could say a batch had
// formed, and nothing anywhere could say what the waits that produced no
// batch had cost.
//
// How to read it is in docs/merge-queue-batching.md. The number the
// window is tuned on is Expired against Holds, read with
// ExpiredWaitSeconds beside BatchedWaitSeconds: holds that routinely run
// the full window and expire mean the run being waited for is slower
// than the window, and a window much longer than the waits that do pay
// off is buying nothing but those expiries.
type MergeGathering struct {
	// Holds is how many ended holds the history keeps, and Batched,
	// Released and Expired partition it: the wait paid off and a batch
	// formed, the wait was given up on early because what it was waiting
	// for stopped being worth waiting for, or the wait ran its whole
	// window and bought nothing.
	Holds    int `json:"holds"`
	Batched  int `json:"batched"`
	Released int `json:"released"`
	Expired  int `json:"expired"`
	// BatchedWaitSeconds and ExpiredWaitSeconds are the mean wait of each
	// of those two, and ExpiredTotalSeconds the latency the expiries cost
	// altogether -- the one number here that is purely a loss. Means are
	// computed here rather than in the pane so that a count of zero is
	// zero seconds rather than a division nobody wanted.
	BatchedWaitSeconds  float64 `json:"batchedWaitSeconds"`
	ExpiredWaitSeconds  float64 `json:"expiredWaitSeconds"`
	ExpiredTotalSeconds float64 `json:"expiredTotalSeconds"`
	// WindowSeconds is the longest window any of these holds was armed
	// under, which is how a reader tells a history taken under the
	// current setting from one taken before it was changed.
	WindowSeconds float64 `json:"windowSeconds"`
	// FirstAt and LastAt bound the history, for MergeBatching's reason:
	// counters over an hour and counters over a week read very
	// differently.
	FirstAt *time.Time `json:"firstAt,omitempty"`
	LastAt  *time.Time `json:"lastAt,omitempty"`
}

// mergeGathering reads this deployment's gather history and counts it,
// or answers nil where there is nothing to say -- a deployment whose
// queue has never held anything, one with the wait turned off, and one
// whose store cannot be read.
//
// Best-effort like mergeBatching beside it: a Settings pane that failed
// to load because a measurement could not be read would be trading the
// thing people need for the thing they are curious about.
func (c *Client) mergeGathering(ctx context.Context) *MergeGathering {
	if c.Store == nil {
		return nil
	}
	records, err := c.Store.MergeGatherRecords(ctx)
	if err != nil {
		log.Printf("ui: reading the merge gather history: %v", err)
		return nil
	}
	if len(records) == 0 {
		return nil
	}
	s := model.SummarizeMergeGathers(records)
	return &MergeGathering{
		Holds: s.Holds, Batched: s.Batched, Released: s.Released, Expired: s.Expired,
		BatchedWaitSeconds:  meanSeconds(s.BatchedWaited, s.Batched),
		ExpiredWaitSeconds:  meanSeconds(s.ExpiredWaited, s.Expired),
		ExpiredTotalSeconds: s.ExpiredWaited.Seconds(),
		WindowSeconds:       s.LongestWindow.Seconds(),
		FirstAt:             s.FirstAt, LastAt: s.LastAt,
	}
}

// meanSeconds is total/n in seconds, and zero for the n a history with
// none of that outcome in it has.
func meanSeconds(total time.Duration, n int) float64 {
	if n <= 0 {
		return 0
	}
	return total.Seconds() / float64(n)
}
