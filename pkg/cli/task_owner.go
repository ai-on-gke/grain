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

package cli

import (
	"context"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// normalizeOwnerConditions resolves the "me" shorthand and "user:<email>"
// spelling on any owner filter condition before matching tasks client-side.
func normalizeOwnerConditions(ctx context.Context, c *ui.HTTPClient, q *taskQuery, viewerOwner string) {
	if q == nil || len(q.filters) == 0 {
		return
	}
	resolvedMe := ""
	resolveMe := func() string {
		if resolvedMe != "" {
			return resolvedMe
		}
		if norm, err := model.NormalizeTaskOwner(viewerOwner); err == nil {
			resolvedMe = norm
			return resolvedMe
		}
		if c != nil {
			if norm, err := model.NormalizeTaskOwner(c.User); err == nil {
				resolvedMe = norm
				return resolvedMe
			}
			if who, err := c.AuthWhoami(ctx); err == nil {
				for _, candidate := range []string{who.Owner, strings.TrimPrefix(who.Principal, "human:"), who.Principal} {
					if norm, nerr := model.NormalizeTaskOwner(candidate); nerr == nil {
						resolvedMe = norm
						return resolvedMe
					}
				}
			}
		}
		resolvedMe = model.DefaultTaskOwner
		return resolvedMe
	}

	for i := range q.filters {
		cond := &q.filters[i]
		if cond.attr.id != "owner" {
			continue
		}
		for j, raw := range cond.values {
			trimmed := strings.TrimSpace(raw)
			if strings.EqualFold(trimmed, "me") {
				cond.values[j] = resolveMe()
				continue
			}
			if norm, err := model.NormalizeTaskOwner(trimmed); err == nil {
				cond.values[j] = norm
			}
		}
	}
}
