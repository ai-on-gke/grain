package ci

import (
	"context"
	"fmt"
	"strings"
)

// gatesOnBase reports whether r is one this check applies to: a caller
// asked for it, and the run is the complete suite rather than a narrowed
// one -- a selected job or workflow never stands as the required verdict,
// so it has nothing to be compared against.
func gatesOnBase(r Request) bool {
	return r.GateOnBase && len(r.Jobs) == 0 && r.Workflow == "" &&
		!strings.EqualFold(r.BaseRef, r.Ref)
}

// checkBaseGate reads the base's workflows and refuses head when it drops
// any job the base requires. The error it returns for a refusal is a
// workflow-definition error, so Start records it as a failed run on this
// commit; failing to read the base at all is not, and is retried like any
// other source failure.
func (s *Service) checkBaseGate(ctx context.Context, req Request, head []Workflow) error {
	base, _, err := s.Source.Workflows(ctx, req.Repo, req.BaseRef)
	if err != nil && !isWorkflowDefinitionError(err) {
		return fmt.Errorf("ci: reading %s's workflows on its base %s: %w", req.Repo, baseName(req.BaseRef), err)
	}
	// A workflow file that does not parse on the base is the base's own
	// breakage, not the head's (head's files were already parsed above):
	// refusing head for it would deadlock the branch that repairs the file
	// on base. The base workflows that did parse are still checked below so
	// a broken file on base does not let a branch drop jobs from the rest.
	removed := RemovedGateJobs(req, base, head)
	if len(removed) == 0 {
		return nil
	}
	return markWorkflowDefinitionError(fmt.Errorf(
		"ci: %s does not run %d job(s) its base %s requires for %s on %s: %s. "+
			"A branch cannot remove checks from its own merge gate; restore them, "+
			"or have a person merge the change to the gate",
		req.Ref, len(removed), baseName(req.BaseRef), req.Event, req.Ref, strings.Join(removed, ", ")))
}

func baseName(ref string) string {
	if ref == "" {
		return "(the default branch)"
	}
	return ref
}
