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

package model_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A release run a newer merge superseded is not a red branch
// (grain/task-3334), but any other cancellation still is.
func TestBaseBranchReleaseSuperseded(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  model.BaseBranchRelease
		want bool
	}{
		{"superseded", model.BaseBranchRelease{Conclusion: "cancelled",
			Detail: model.ReleaseSupersededDetailPrefix + "abc123 (run 7): a newer merge's release replaced this one"}, true},
		{"cancelled by hand", model.BaseBranchRelease{Conclusion: "cancelled", Detail: "cancelled"}, false},
		{"failed", model.BaseBranchRelease{Conclusion: "failure",
			Detail: model.ReleaseSupersededDetailPrefix + "abc123"}, false},
		{"published", model.BaseBranchRelease{Conclusion: "success"}, false},
	} {
		if got := tc.row.Superseded(); got != tc.want {
			t.Errorf("%s: Superseded() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A branch whose every release since its last publish was superseded is
// reported only once nothing has published for BaseBranchNotPublishingFor.
func TestOnlySupersededReleasesAreReportedOnlyAfterTheQuietPeriod(t *testing.T) {
	since := time.Date(2026, 11, 6, 5, 42, 0, 0, time.UTC)
	row := model.BaseBranchRelease{
		Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Branch: "main",
		Conclusion: "cancelled", Detail: model.ReleaseSupersededDetailPrefix + "abc123",
		FailingSince: &since,
	}
	if _, behind := model.BaseBranchPublishingOf(row, since.Add(time.Hour)); behind {
		t.Errorf("an hour of superseded releases was reported as not publishing")
	}
	if _, behind := model.BaseBranchPublishingOf(row, since.Add(model.BaseBranchNotPublishingFor+time.Minute)); !behind {
		t.Errorf("nothing published for longer than %v, yet it was not reported", model.BaseBranchNotPublishingFor)
	}
}
