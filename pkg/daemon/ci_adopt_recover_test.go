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

func (s *unlistableRunStore) RecoverInterruptedExcept(_ context.Context, _ time.Time, keep []string) (int, error) {
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
		t.Fatalf("RecoverInterruptedExcept called %d time(s) keeping %v; want once, keeping none", runs.recovered, runs.kept)
	}
}
