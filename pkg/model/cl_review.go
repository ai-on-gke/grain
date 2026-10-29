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

package model

import (
	"fmt"
	"strconv"
	"strings"
)

// ReviewOfArg is the grant argument a change provider's read-only review
// grant names the reviewed task by (changelists.md §5, G3-5). Only grain
// files a grant carrying it, on a review it filed itself
// (LinkCLReviewOf); a grant an agent asks for with it is refused
// (RefuseReviewOfGrants), since it would let any run pass itself off as a
// required review.
const ReviewOfArg = "review_of"

// GrantNamesReviewOf reports whether grant string s is a capability
// reference whose with: arguments carry ReviewOfArg.
func GrantNamesReviewOf(s string) bool {
	if !IsCapabilityRef(s) {
		return false
	}
	spec, err := CapabilityGrantSpec(s)
	if err != nil {
		return false
	}
	_, ok := spec.With[ReviewOfArg]
	return ok
}

// RefuseReviewOfGrants is the refusal for a list of grants an agent asked
// for (propose_task's capabilities) when any carries ReviewOfArg.
func RefuseReviewOfGrants(grants []string) error {
	for _, g := range grants {
		if GrantNamesReviewOf(g) {
			return fmt.Errorf("capability %s: %q is grain's to give, on a review it files itself; "+
				"a task may not ask for it", CapabilityGrantRef(g), ReviewOfArg)
		}
	}
	return nil
}

// CLReviewTarget is the target LinkCLReviewFiled, LinkCLReviewOf and
// LinkCLReviewCleared share: change ("cl/<number>"), the snapshot reviewed
// and the template reviewing it.
func CLReviewTarget(change string, snapshot int, template string) string {
	return fmt.Sprintf("%s@%d:%s", change, snapshot, template)
}

// ParseCLReviewTarget reads CLReviewTarget back.
func ParseCLReviewTarget(s string) (change string, snapshot int, template string, ok bool) {
	at := strings.LastIndex(s, "@")
	if at <= 0 {
		return "", 0, "", false
	}
	snap, tmpl, found := strings.Cut(s[at+1:], ":")
	if !found || tmpl == "" {
		return "", 0, "", false
	}
	n, err := strconv.Atoi(snap)
	if err != nil {
		return "", 0, "", false
	}
	return s[:at], n, tmpl, true
}
