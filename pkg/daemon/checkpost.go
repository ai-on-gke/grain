package daemon

import (
	"fmt"
	"os"
	"strings"

	"github.com/ai-on-gke/grain/pkg/checkpost"
	"github.com/ai-on-gke/grain/pkg/forge"
	"github.com/ai-on-gke/grain/pkg/granule"
)

// checkpostSecretEnv is where the daemon reads the secret it signs reads
// to the reporting service with -- the same variable that service reads
// to verify them, and the same one the workflow step signs with.
//
// The environment rather than a flag or the secrets store: a flag is in
// the process table, and the secrets store is grain's own encrypted
// directory, which this service deliberately shares nothing else with.
const checkpostSecretEnv = "CHECKPOST_SECRET"

// checkpostClient builds the verdict source from this deployment's
// flags, or nil where there is none.
//
// Both halves are required together. A url with nothing expected would
// read nothing and quietly cost a flag; an expectation with no url is an
// operator who meant to turn this on and did not finish. Neither is
// worth guessing at.
func checkpostClient(cfg Config) (*checkpost.Client, error) {
	if cfg.CheckpostURL == "" && cfg.CheckpostExpect == "" {
		return nil, nil
	}
	if cfg.CheckpostURL == "" || cfg.CheckpostExpect == "" {
		return nil, fmt.Errorf("-checkpost-url and -checkpost-expect go together: one without " +
			"the other is a deployment that meant to stop polling GitHub and did not")
	}
	expect, err := checkpost.ParseExpect(cfg.CheckpostExpect)
	if err != nil {
		return nil, err
	}
	secret := os.Getenv(checkpostSecretEnv)
	if secret == "" {
		return nil, fmt.Errorf("%s is not set, so reads to %s could not be signed",
			checkpostSecretEnv, cfg.CheckpostURL)
	}
	return &checkpost.Client{BaseURL: cfg.CheckpostURL, Secret: secret, Expect: expect}, nil
}

// verdictSource is checkpostClient's answer as the interface Config
// holds, with nil staying nil.
//
// The dance is Go's typed-nil trap and nothing more: assigning a nil
// *checkpost.Client straight into an interface field produces an
// interface that is not nil, and forge.CheckRuns would then call
// through it on every read for every deployment that has no service at
// all.
func verdictSource(c *checkpost.Client) forge.Verdicts {
	if c == nil {
		return nil
	}
	return c
}

// splitGranuleCommand turns -granule-command into the argv prefix
// granule.Command is. Empty means granule is on this machine's PATH, which
// granule.Command spells as its zero value.
//
// strings.Fields rather than a shell parse: the wrappers this exists for --
// `limactl shell <vm> -- granule`, `ssh <host> granule`,
// `docker exec <c> granule` -- are all plain words, and accepting quoting
// here would promise a shell that never runs. A path with a space in it
// needs a wrapper script instead, which is the same answer the exec'd-not-
// shelled rule gives everywhere else in this daemon.
func SplitGranuleCommand(flag string) granule.Command {
	fields := strings.Fields(flag)
	if len(fields) == 0 {
		return nil
	}
	return granule.Command(fields)
}
