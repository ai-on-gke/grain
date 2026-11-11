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
	"testing"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
)

// A repository whose connection names a merge credential hands that
// credential's token out for merges and keeps its push credential for
// everything else; one whose merge credential is not in the store hands
// out none, which refuses the merge rather than making it as the pusher.
func TestCredentialTokenSourceHandsOutTheMergeCredentialForMerges(t *testing.T) {
	credentials, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := credentials.SetToken("person", "person-token"); err != nil {
		t.Fatal(err)
	}
	if err := credentials.SetToken("merger", "merger-token"); err != nil {
		t.Fatal(err)
	}
	merges := map[string]string{"acme/widgets": "merger", "acme/lost": "missing"}
	src := credentialTokenSource{credentials: credentials,
		teamCredential: func(host, owner, repo string) (string, bool) { return "person", true },
		mergeCredential: func(host, owner, repo string) (string, bool) {
			name, ok := merges[owner+"/"+repo]
			return name, ok
		}}
	if tok := src.TokenFor("acme", "widgets"); tok == nil || *tok != "person-token" {
		t.Errorf("TokenFor = %v, want the push credential's", tok)
	}
	if tok, ok := src.MergeTokenFor("acme", "widgets"); !ok || tok == nil || *tok != "merger-token" {
		t.Errorf("MergeTokenFor = %v, %v, want the merge credential's", tok, ok)
	}
	if got := src.MergeIdentityFor("acme", "widgets"); got != "merge credential merger" {
		t.Errorf("MergeIdentityFor = %q", got)
	}
	if tok, ok := src.MergeTokenFor("acme", "gears"); ok || tok != nil {
		t.Errorf("MergeTokenFor(no merge credential) = %v, %v, want none configured", tok, ok)
	}
	if tok, ok := src.MergeTokenFor("acme", "lost"); !ok || tok != nil {
		t.Errorf("MergeTokenFor(missing credential) = %v, %v, want configured with no token", tok, ok)
	}
}
