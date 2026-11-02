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

package ci

import (
	"context"
	"strconv"
	"strings"
)

// ExternalRuns is CI some other provider ran, read the way grain's own
// RunStore is: what the sandbox namespace (ci://) and the sandbox CLI
// (grain ci list, grain ci log) serve beside grain's runs. The one
// provider today is GitHub Actions, served by pkg/ui over the daemon's
// own forge connection (pkg/ui/ci_actions.go); a run's id says which
// provider and repository it is ("gha:<owner>:<name>:<run id>").
//
// Every method answers for repositories the caller has already checked
// the reader may see: it does no scoping of its own.
type ExternalRuns interface {
	// FindExternalRuns is f.Repo's runs matching f, newest first, at
	// most limit of them, and a note saying why they are fewer than
	// asked (no forge connection, a low API budget) when it has one. A
	// repository whose CI is not the provider's answers none.
	FindExternalRuns(ctx context.Context, f RunFilter, limit int) (runs []Run, note string)
	// ExternalRun is one run in full, jobs and steps. ok is false for an
	// id that is not this source's.
	ExternalRun(ctx context.Context, id string) (run Run, ok bool, err error)
	// ExternalJobLog is one job's log, by name or id ("" for the first
	// that failed).
	ExternalJobLog(ctx context.Context, id, job string) (string, error)
}

// ActionsRunIDPrefix starts every GitHub Actions run id ci:read serves.
const ActionsRunIDPrefix = "gha:"

// ParseActionsRunID reads a GitHub Actions run id
// ("gha:<owner>:<name>:<run id>") back into its repository and GitHub
// run id. The repository may be written with its slashes
// ("gha:owner/name:42") as well as with colons. It is what a reader's
// repository scope is checked against before anything is asked of the
// forge for that run.
func ParseActionsRunID(id string) (repo string, runID int64, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(id), ActionsRunIDPrefix)
	if !found {
		return "", 0, false
	}
	at := strings.LastIndexByte(rest, ':')
	if at <= 0 {
		return "", 0, false
	}
	n, err := strconv.ParseInt(rest[at+1:], 10, 64)
	if err != nil || n <= 0 {
		return "", 0, false
	}
	repo = strings.ReplaceAll(rest[:at], ":", "/")
	if strings.Count(repo, "/") < 1 {
		return "", 0, false
	}
	return repo, n, true
}
