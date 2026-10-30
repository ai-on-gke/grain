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

package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// unlistableRunStore is a run store whose live runs cannot be listed, and
// which records what it was asked to recover.
type unlistableRunStore struct {
	ci.RunStore
	recovered int
	kept      []string
}

func (s *unlistableRunStore) LiveRuns(context.Context) ([]ci.Run, error) {
	return nil, errors.New("database is locked")
}

func (s *unlistableRunStore) RecoverUnattachable(_ context.Context, _ time.Time, keep []string) (int, error) {
	s.recovered++
	s.kept = keep
	return 1, nil
}

// TestAdoptCIRunsStillRecoversWhenLiveRunsCannotBeListed: before adoption,
// every start resolved the runs a previous process left in flight. A run
// left unconcluded is the one Start answers every later request for the
// same plan with, so a failure to list them must still resolve them all
// rather than leave them standing.
func TestAdoptCIRunsStillRecoversWhenLiveRunsCannotBeListed(t *testing.T) {
	runs := &unlistableRunStore{}
	svc := &ci.Service{Runs: runs, Engine: &ci.Engine{}}
	adopted, lost := AdoptCIRuns(context.Background(), svc)
	if len(adopted) != 0 || len(lost) != 0 {
		t.Fatalf("adopted %v, lost %v; want neither", adopted, lost)
	}
	if runs.recovered != 1 || len(runs.kept) != 0 {
		t.Fatalf("RecoverUnattachable called %d time(s) keeping %v; want once, keeping none", runs.recovered, runs.kept)
	}
}
