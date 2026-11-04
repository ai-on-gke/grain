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
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// An access request is what a run's request_access call becomes: the
// repositories, capability grants and network access the run says its
// work needs and its task does not hold, with the run's reason. The
// task parks on it the way it parks on a request_secret (it sits beside
// Observation.PendingQuestionCommentID), and a person grants some or all
// of it, or denies it with a rationale, from the card on the task
// (ui's POST /api/tasks/{id}/access-request/grant and /deny). Either
// decision posts into the conversation, which is what re-queues the task
// and what the next run reads.

// The kinds of thing an access request can ask for.
const (
	AccessRead    = "read"
	AccessWrite   = "write"
	AccessGrant   = "grant"
	AccessNetwork = "network"
)

// AccessRequestItem is one thing asked for.
type AccessRequestItem struct {
	Kind string `json:"kind"`
	// Repo is a read's or a write's repository, owner/name; Base a
	// write's branch, "" for the repository's default.
	Repo string `json:"repo,omitempty"`
	Base string `json:"base,omitempty"`
	// Capability and With are a grant's reference and its with:
	// arguments, as ui.GrantRequest takes them.
	Capability string         `json:"capability,omitempty"`
	With       map[string]any `json:"with,omitempty"`
	// Access is a network item's: "open".
	Access string `json:"access,omitempty"`
}

// Key identifies an item for comparing two requests: equal keys ask for
// the same thing.
func (it AccessRequestItem) Key() string {
	switch it.Kind {
	case AccessRead:
		return "read:" + strings.ToLower(it.Repo)
	case AccessWrite:
		return "write:" + strings.ToLower(it.Repo) + "@" + it.Base
	case AccessNetwork:
		return "network:" + it.Access
	default:
		with := ""
		if len(it.With) > 0 {
			data, _ := json.Marshal(it.With) // map keys marshal sorted
			with = string(data)
		}
		return "grant:" + it.Capability + "=" + with
	}
}

// Label is the item as a sentence names it: "read acme/lib",
// "write acme/lib (branch dev)", "grant gcp#gcp-key {...}", "network open".
func (it AccessRequestItem) Label() string {
	switch it.Kind {
	case AccessRead:
		return "read " + it.Repo
	case AccessWrite:
		if it.Base != "" {
			return fmt.Sprintf("write %s (branch %s)", it.Repo, it.Base)
		}
		return "write " + it.Repo
	case AccessNetwork:
		return "network access " + it.Access
	default:
		if len(it.With) > 0 {
			data, _ := json.Marshal(it.With)
			return fmt.Sprintf("grant %s %s", it.Capability, data)
		}
		return "grant " + it.Capability
	}
}

// PendingAccessRequest is a request_access call waiting on a person.
type PendingAccessRequest struct {
	Summary   string              `json:"summary"`
	Reason    string              `json:"reason"`
	Items     []AccessRequestItem `json:"items"`
	RunID     string              `json:"runId,omitempty"`
	CreatedAt time.Time           `json:"createdAt"`
}

// Key is the request's items' keys, sorted and joined: two requests
// with equal keys ask for exactly the same things.
func (r PendingAccessRequest) Key() string {
	keys := make([]string, 0, len(r.Items))
	for _, it := range r.Items {
		keys = append(keys, it.Key())
	}
	sort.Strings(keys)
	return strings.Join(keys, "\n")
}

// AccessDecision is a person's answer to one access request, kept on the
// task so a later identical request shows the earlier answer beside it.
type AccessDecision struct {
	Request PendingAccessRequest `json:"request"`
	// Granted and Declined partition Request.Items by the person's
	// choice; a denial declines every item.
	Granted  []AccessRequestItem `json:"granted,omitempty"`
	Declined []AccessRequestItem `json:"declined,omitempty"`
	Denied   bool                `json:"denied,omitempty"`
	Reason   string              `json:"reason,omitempty"`
	By       Principal           `json:"by"`
	At       time.Time           `json:"at"`
}

// accessDecisionsKept bounds Observation.AccessDecisions: what a card
// needs is the latest answers, not every one a long task ever had.
const accessDecisionsKept = 20

// AppendAccessDecision is decisions with d added, the oldest dropped
// past accessDecisionsKept.
func AppendAccessDecision(decisions []AccessDecision, d AccessDecision) []AccessDecision {
	out := append(append([]AccessDecision(nil), decisions...), d)
	if len(out) > accessDecisionsKept {
		out = out[len(out)-accessDecisionsKept:]
	}
	return out
}

// ParseAccessRequestItems reads request_access's requests argument as a
// tool call carries it (decoded JSON), checking each item's shape: the
// kind is one of the four, and each kind names what it needs and
// nothing it does not. What the items name -- whether the repository is
// one this deployment works with, whether the capability exists and its
// arguments validate -- is the daemon's to check (ui's
// CheckAccessRequest), not this function's.
func ParseAccessRequestItems(raw any) ([]AccessRequestItem, error) {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, fmt.Errorf("requests is required: a non-empty list of {kind: read|write|grant|network, ...}")
	}
	items := make([]AccessRequestItem, 0, len(list))
	seen := map[string]bool{}
	for i, entry := range list {
		m, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("requests[%d] is not an object", i)
		}
		str := func(key string) string {
			s, _ := m[key].(string)
			return strings.TrimSpace(s)
		}
		allowed := map[string]bool{"kind": true}
		it := AccessRequestItem{Kind: str("kind")}
		switch it.Kind {
		case AccessRead:
			allowed["repo"] = true
			it.Repo = str("repo")
			if it.Repo == "" {
				return nil, fmt.Errorf("requests[%d]: a read needs repo (owner/name)", i)
			}
		case AccessWrite:
			allowed["repo"], allowed["base"] = true, true
			it.Repo, it.Base = str("repo"), str("base")
			if it.Repo == "" {
				return nil, fmt.Errorf("requests[%d]: a write needs repo (owner/name)", i)
			}
		case AccessGrant:
			allowed["capability"], allowed["with"] = true, true
			it.Capability = str("capability")
			if it.Capability == "" {
				return nil, fmt.Errorf("requests[%d]: a grant needs capability", i)
			}
			if w, present := m["with"]; present && w != nil {
				with, ok := w.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("requests[%d]: with must be an object of the method's arguments", i)
				}
				it.With = with
			}
		case AccessNetwork:
			allowed["access"] = true
			it.Access = str("access")
			if it.Access != "open" {
				return nil, fmt.Errorf("requests[%d]: network access can only be requested as \"open\"", i)
			}
		case "":
			return nil, fmt.Errorf("requests[%d]: kind is required (read, write, grant or network)", i)
		default:
			return nil, fmt.Errorf("requests[%d]: kind %q is not read, write, grant or network", i, it.Kind)
		}
		for key := range m {
			if !allowed[key] {
				return nil, fmt.Errorf("requests[%d]: a %s request takes no %q", i, it.Kind, key)
			}
		}
		if seen[it.Key()] {
			return nil, fmt.Errorf("requests[%d] repeats an earlier item (%s)", i, it.Label())
		}
		seen[it.Key()] = true
		items = append(items, it)
	}
	return items, nil
}
