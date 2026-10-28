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
	"fmt"

	"github.com/ai-on-gke/grain/pkg/model"
)

// refuseDeclaredEdit refuses to change or delete a template, suite or
// schedule a repository declares in its own `.grain` directory
// (grain/task-60, model's repo_declaration.go), and says where the file
// is instead.
//
// It refuses the whole edit rather than splitting content from cadence
// the way refuseSystemScheduleContentEdit does for one of grain's own
// jobs. The reasoning that makes that split right is exactly what makes
// it wrong here: a system schedule's *content* ships in grain's code
// while the deployment owns its cadence, whereas a declared row has one
// author for the whole of it -- the repository -- and the next poll
// would put back anything changed here, within a couple of minutes,
// with no record of what happened to the edit.
//
// So the answer is the file. It is not a worse answer than the form:
// the file is in a repository whoever is looking at this row can open,
// it changes under review with the code it describes, and it is live the
// moment it merges. What this deployment does keep is the switch that
// says whether it reads that repository at all (RepoConfig.Declarations)
// -- named here, because an operator who wants a repository's standing
// work to stop needs to be told where the stop is.
func (c *Client) refuseDeclaredEdit(ctx context.Context, id, verb string) error {
	if !model.IsRepoDeclaredID(id) {
		return nil
	}
	declared, err := c.Store.GetRepoDeclaration(ctx, id)
	if err != nil {
		return err
	}
	if declared == nil {
		// An id in the declared namespace with no provenance row: a
		// repository whose switch was turned off between the two reads,
		// or a row restored from a dump older than its own declaration.
		// Nothing is going to overwrite an edit to it, so there is
		// nothing to refuse.
		return nil
	}
	return validationErrorf(
		"%s is declared by %s in %s, so it is not this deployment's to %s -- change the file there and "+
			"grain picks it up within a couple of minutes. To stop reading %s's declarations "+
			"altogether, turn them off in its repository settings",
		id, declared.Repo, declared.Path, verb, declared.Repo)
}

// declaredIn is the provenance of every declared id, for a listing that
// wants to mark its declared rows -- one query rather than one per row,
// and nil-safe for a deployment where nothing declares anything.
func (c *Client) declaredIn(ctx context.Context) (map[string]model.RepoDeclaration, error) {
	rows, err := c.Store.ListRepoDeclarations(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing declarations: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	out := make(map[string]model.RepoDeclaration, len(rows))
	for _, row := range rows {
		out[row.ID] = row
	}
	return out, nil
}

// Declaration is where a template, suite or schedule came from, on the
// wire: the repository whose `.grain` directory declares it and the file
// it is in.
//
// A UI needs both halves. The first is what lets it show a row as
// belonging to a repository rather than to this deployment's settings,
// which is the difference between "somebody here set this up" and "this
// repository asks for this"; the second is the answer to the only
// question anybody has next, which is where to go and edit it.
type Declaration struct {
	Repo string `json:"repo"`
	Path string `json:"path"`
}

// DeclaredPush is one declared template's push set on the wire: the
// refs, in Repo, that the runs of tasks filed from it may push beyond
// their own branch, and the file and commit that say so.
type DeclaredPush struct {
	Repo   string          `json:"repo"`
	Path   string          `json:"path"`
	Commit string          `json:"commit,omitempty"`
	Refs   []model.RefPush `json:"refs"`
}

// declaredPushOf renders a declaration's push set, or nil for one with
// none.
func declaredPushOf(row *model.RepoDeclaration) *DeclaredPush {
	if row == nil || row.Kind != model.DeclaredTemplate || len(row.Push) == 0 {
		return nil
	}
	return &DeclaredPush{Repo: row.Repo.String(), Path: row.Path, Commit: row.Commit, Refs: row.Push}
}

// declarationOf renders one row's provenance, or nil for a row nothing
// declared -- which is every row somebody created here.
func declarationOf(declared map[string]model.RepoDeclaration, id string) *Declaration {
	row, ok := declared[id]
	if !ok {
		return nil
	}
	return &Declaration{Repo: row.Repo.String(), Path: row.Path}
}
