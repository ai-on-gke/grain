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

package orchestrator

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// mergeIdentifier is github.RESTClient.MergeIdentity, which test fakes
// may not have.
type mergeIdentifier interface {
	MergeIdentity(owner, repo string) string
}

// mergedAsDetail is what a TaskEventMergedAs says: the login the forge
// recorded as merging ref, and the merge credential grain merged through
// when the repository has one of its own. "" when the forge has not said
// who merged it yet -- a pull request handed to GitHub's merge queue,
// which merges it later.
func mergedAsDetail(client github.Client, ref model.PullRequestRef, detail github.PullRequestDetail) string {
	if !detail.Merged || detail.MergedBy == "" {
		return ""
	}
	out := fmt.Sprintf("%s merged by %s", ref, detail.MergedBy)
	if m, ok := client.(mergeIdentifier); ok {
		if identity := m.MergeIdentity(ref.Repo.StoredOwner(), ref.Repo.Name); identity != "" {
			out += " through " + identity
		} else {
			out += " through the repository's push credential"
		}
	}
	return out
}

// recordMergedAs writes which identity merged a task's pull request
// (mergedAsDetail) as a TaskEventMergedAs, so a reader auditing the
// reviewed-merge gate (pkg/reviewedmerge, vars.GRAIN_MERGE_LOGINS) can
// tell from grain's own record which merges were grain's and as whom. A
// failure is logged: the merge has happened, and it must not leave the
// task un-closed-out over its note.
func recordMergedAs(ctx context.Context, store *model.Store, client github.Client, taskID string,
	ref model.PullRequestRef, detail github.PullRequestDetail, now time.Time) {
	if store == nil || taskID == "" {
		return
	}
	note := mergedAsDetail(client, ref, detail)
	if note == "" {
		return
	}
	if _, err := store.RecordTaskEvent(ctx, model.TaskEvent{TaskID: taskID, Kind: model.TaskEventMergedAs,
		At: now, By: model.GrainPrincipal, Detail: note}); err != nil {
		log.Printf("orchestrator: recording who merged %s: %v", ref, err)
	}
}
