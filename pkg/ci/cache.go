package ci

import (
	"context"
)

// noCache is what an Engine with no Cache uses, so the engine has one
// code path rather than a nil check at each of the two call sites.
type noCache struct{}

func (noCache) Lookup(context.Context, Key) (JobRun, bool, error) { return JobRun{}, false, nil }
func (noCache) Save(context.Context, Key, JobRun) error           { return nil }
