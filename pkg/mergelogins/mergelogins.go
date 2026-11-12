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

// Package mergelogins checks the logins a reviewed-merge gate trusts as
// grain's own merges (pkg/reviewedmerge): the repository variable
// GRAIN_MERGE_LOGINS that release-images.sh reads, and the daemon's
// -upgrade-reviewed-merge-logins. A merge by one of those logins passes
// the gate without a review, so a login somebody signs in to -- the
// operator's own account, which grain merged as while its git connection
// was the operator's token -- lets that person's hand merges through
// too, which is the insider risk the gate was built against (task 4386).
// `grain health` says so, with the remedy: merge through a GitHub App
// (a git connection's merge_credential) and trust its `<slug>[bot]`.
package mergelogins

import (
	"fmt"
	"log"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Variable is the repository variable release-images.sh's reviewed-merge
// check reads its trusted logins from.
const Variable = "GRAIN_MERGE_LOGINS"

// Forge is the two reads Check makes; github.RESTClient has both.
type Forge interface {
	RepoVariable(owner, repo, name string) (string, bool, error)
	AccountType(owner, repo, login string) (string, error)
}

// Setting is one place logins are trusted as grain's merges.
type Setting struct {
	// Repo is the repository whose merges the logins vouch for.
	Repo model.RepoRef
	// Name is how a person finds the setting: "vars.GRAIN_MERGE_LOGINS"
	// or "-upgrade-reviewed-merge-logins".
	Name string
	// Logins are the logins themselves. nil reads Repo's Variable.
	Logins []string
}

// Warning is one trusted login that belongs to a user account.
type Warning struct {
	Repo    string `json:"repo"`
	Setting string `json:"setting"`
	Login   string `json:"login"`
	Message string `json:"message"`
}

// SplitLogins is a logins setting as cmd/reviewed-merge reads it: comma
// or space separated, blanks dropped.
func SplitLogins(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
}

// Check is every login in settings that GitHub says is a user account
// ("User"): one a person can sign in to and merge as by hand. An App's
// `<slug>[bot]` is never one, and is not asked about. A read that fails
// -- a token without the variables permission, a login GitHub does not
// know -- is logged and passed over: health reports what it could read,
// and an unreadable setting is not evidence of a person.
func Check(f Forge, settings []Setting) []Warning {
	if f == nil {
		return nil
	}
	var out []Warning
	types := map[string]string{}
	for _, s := range settings {
		logins := s.Logins
		if logins == nil {
			v, ok, err := f.RepoVariable(s.Repo.Owner, s.Repo.Name, Variable)
			if err != nil {
				log.Printf("mergelogins: reading %s's %s: %v", s.Repo, Variable, err)
				continue
			}
			if !ok {
				continue
			}
			logins = SplitLogins(v)
		}
		for _, login := range logins {
			if strings.HasSuffix(login, "[bot]") {
				continue
			}
			key := strings.ToLower(login)
			kind, seen := types[key]
			if !seen {
				var err error
				kind, err = f.AccountType(s.Repo.Owner, s.Repo.Name, login)
				if err != nil {
					log.Printf("mergelogins: reading the account type of %s: %v", login, err)
				}
				types[key] = kind
			}
			if kind != "User" {
				continue
			}
			out = append(out, Warning{Repo: s.Repo.String(), Setting: s.Name, Login: login,
				Message: fmt.Sprintf("%s trusts %s's merges as grain's, and %s is a user account: every merge "+
					"its owner makes by hand passes the reviewed-merge gate unreviewed. Give the repository's git "+
					"connection a merge_credential (a GitHub App), then name the App's <slug>[bot] here instead "+
					"of %s (docs/github-actions-ci.md#a-merge-identity-of-grains-own)", s.Name, login, login, login)})
		}
	}
	return out
}
