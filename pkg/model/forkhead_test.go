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
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestParseForkHead(t *testing.T) {
	t.Parallel()
	t.Run("a fork head label", func(t *testing.T) {
		head, ok, err := model.ParseForkHead("andrzejfudro:chore/bump-agy-1.4.9")
		if err != nil || !ok {
			t.Fatalf("ParseForkHead = %v, %v, %v; want a label", head, ok, err)
		}
		if head.Owner != "andrzejfudro" || head.Branch != "chore/bump-agy-1.4.9" {
			t.Fatalf("head = %+v", head)
		}
		if got := head.String(); got != "andrzejfudro:chore/bump-agy-1.4.9" {
			t.Fatalf("String() = %q", got)
		}
	})
	t.Run("a plain branch is not a label", func(t *testing.T) {
		// The last two have characters no label has: they are left to the
		// plain branch-name check, which refuses them as the bad refs they are.
		for _, s := range []string{"", "main", "chore/bump-agy-1.4.9", "release/1.2", "also bad ~^:", "owner:has space"} {
			if head, ok, err := model.ParseForkHead(s); ok || err != nil || head != (model.ForkHead{}) {
				t.Errorf("ParseForkHead(%q) = %v, %v, %v; want not a label", s, head, ok, err)
			}
		}
	})
	t.Run("malformed labels are errors", func(t *testing.T) {
		for _, s := range []string{
			"a:b:c", ":main", "andrzejfudro:", ":",
			"-bad:main", "bad-:main", "ba--d:main", "under_score:main",
			"andrzejfudro:bad..branch", "andrzejfudro:/lead", "andrzejfudro:trail/",
		} {
			if _, ok, err := model.ParseForkHead(s); !ok || err == nil {
				t.Errorf("ParseForkHead(%q) = ok %v, err %v; want a malformed-label error", s, ok, err)
			}
		}
	})
}
