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
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TestValidReleaseNameAgreesWithGit checks ValidReleaseName against git
// itself rather than against an argument about git's rules: a release's
// name is its own prod branch and the stem of its .latest and .rc.N
// branches, so a name is valid exactly when git check-ref-format --branch
// accepts all three and the name is not itself one of those derived
// shapes -- plus grain's own one rule stricter than git's, that a name
// starts with a letter or digit (git would also take a leading '_').
//
// The corpus is every one-character string in the charset, and every
// two- and three-character string over one representative of each class
// git's rules can tell apart (a lower and an upper letter, two digits,
// '.', '_' and '-') -- git treats every letter and digit alike except in
// the few words it reserves, and those are listed as targeted cases --
// so it stays a few hundred git invocations, not a quarter of a million.
func TestValidReleaseNameAgreesWithGit(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}

	full := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-"
	reps := "aZ09._-"
	var corpus []string
	for _, c := range full {
		corpus = append(corpus, string(c))
	}
	for _, a := range reps {
		for _, b := range reps {
			corpus = append(corpus, string(a)+string(b))
			for _, c := range reps {
				corpus = append(corpus, string(a)+string(b)+string(c))
			}
		}
	}
	corpus = append(corpus,
		"..", ".lock", "a.lock", "v3.lock", "v3.0..x", "v3.", "v3.0-lock", "a-lock", "lock",
		"a.b.c", ".latest", "a.latest", "latest", "a-latest", "a.latest.x",
		".rc.7", "a.rc.7", "rc.7", "a.rc.", "a.rc.x", "a.rc.7x", "a.rc.07",
		"HEAD", "head", "HEAD-", "HEAD.x", "FETCH_HEAD", "a.lock.b", "a..b", "a._.b",
		"0", "9", "007", "v1.2.3", "release_2026-10-07",
		strings.Repeat("a", 63), strings.Repeat("a", 64), strings.Repeat("9", 200),
		strings.Repeat("a-", 40)+"z", strings.Repeat("_", 1), "a"+strings.Repeat("_", 70),
		"a"+strings.Repeat("-", 70), strings.Repeat("a.", 50)+"b",
	)

	branchOK := func(name string) bool {
		return exec.Command(git, "check-ref-format", "--branch", name).Run() == nil
	}
	derived := regexp.MustCompile(`(\.latest|\.rc\.[0-9]+)$`)
	startsAlnum := regexp.MustCompile(`^[A-Za-z0-9]`)

	type mismatch struct {
		name      string
		got, want bool
	}
	var (
		mu  sync.Mutex
		bad []mismatch
		wg  sync.WaitGroup
	)
	names := make(chan string)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range names {
				want := branchOK(n) && branchOK(n+".latest") && branchOK(model.RCBranch(n, 1)) &&
					!derived.MatchString(n) && startsAlnum.MatchString(n)
				if got := model.ValidReleaseName(n); got != want {
					mu.Lock()
					bad = append(bad, mismatch{n, got, want})
					mu.Unlock()
				}
			}
		}()
	}
	for _, n := range corpus {
		names <- n
	}
	close(names)
	wg.Wait()

	for _, m := range bad {
		t.Errorf("ValidReleaseName(%q) = %v, git says %v", m.name, m.got, m.want)
	}
}
