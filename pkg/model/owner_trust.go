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
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// OwnerTrustAnyRepo is OwnerTrustRule.Repo for a rule covering every
// repository, no-repo work included.
const OwnerTrustAnyRepo = "*"

// OwnerTrustPrincipal is who approves an external agent's filing that the
// deployment's owner-trust policy queues (Config.OwnerTrust): grain, on
// behalf of the credential's owner.
var OwnerTrustPrincipal = Principal{Kind: PrincipalAutomation, ID: "owner-trust"}

// OwnerTrustRule is one entry of the deployment's owner-trust policy
// (design/spec/external-cli/external-cli.spec.md#XC-17): work an
// external agent credential owned by Owner files unapproved against Repo
// (or anywhere, for OwnerTrustAnyRepo) is queued as if Owner had approved
// it, unless a person-only hold applies.
type OwnerTrustRule struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
}

// NormalizeOwnerTrust validates rules and returns them trimmed, with each
// repository in its canonical owner/name form and duplicates dropped. An
// empty list is nil.
func NormalizeOwnerTrust(rules []OwnerTrustRule) ([]OwnerTrustRule, error) {
	var out []OwnerTrustRule
	for i, r := range rules {
		owner := strings.TrimSpace(r.Owner)
		if owner == "" {
			return nil, fmt.Errorf("ownerTrust[%d]: owner is required", i)
		}
		if containsSpaceOrControl(owner) {
			return nil, fmt.Errorf("ownerTrust[%d]: owner %q may not contain whitespace or control characters", i, owner)
		}
		repo := strings.TrimSpace(r.Repo)
		switch repo {
		case "":
			return nil, fmt.Errorf("ownerTrust[%d]: repo is required (owner/name, or %q for every repository)", i, OwnerTrustAnyRepo)
		case OwnerTrustAnyRepo:
		default:
			ref, err := ParseRepo(repo)
			if err != nil {
				return nil, fmt.Errorf("ownerTrust[%d]: %w", i, err)
			}
			repo = ref.String()
		}
		rule := OwnerTrustRule{Owner: owner, Repo: repo}
		dup := false
		for _, o := range out {
			if strings.EqualFold(o.Owner, rule.Owner) && strings.EqualFold(o.Repo, rule.Repo) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, rule)
		}
	}
	return out, nil
}

// OwnerTrusted reports whether rules trust owner's external agents to
// queue work against repo ("" for work with no repository, which only an
// OwnerTrustAnyRepo rule covers). An empty owner is never trusted.
func OwnerTrusted(rules []OwnerTrustRule, owner, repo string) bool {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return false
	}
	repo = strings.TrimSpace(repo)
	for _, r := range rules {
		if !strings.EqualFold(strings.TrimSpace(r.Owner), owner) {
			continue
		}
		rr := strings.TrimSpace(r.Repo)
		if rr == OwnerTrustAnyRepo {
			return true
		}
		if repo != "" && SameRepoText(rr, repo) {
			return true
		}
	}
	return false
}

// OwnerTrustedForTask reports whether rules trust owner's external agents to
// queue work targeting repo and reaching reads and writes: every repository
// the run clones or pushes to must be covered, not only its primary target.
func OwnerTrustedForTask(rules []OwnerTrustRule, owner, repo string, reads []RepoRef, writes []WriteTarget) bool {
	if !OwnerTrusted(rules, owner, repo) {
		return false
	}
	for _, w := range writes {
		if !OwnerTrusted(rules, owner, w.Repo.String()) {
			return false
		}
	}
	for _, r := range reads {
		if !OwnerTrusted(rules, owner, r.String()) {
			return false
		}
	}
	return true
}

func containsSpaceOrControl(s string) bool {
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// EncodeOwnerTrust is the stored form of Config.OwnerTrust: JSON, or ""
// for none.
func EncodeOwnerTrust(rules []OwnerTrustRule) (string, error) {
	if len(rules) == 0 {
		return "", nil
	}
	b, err := json.Marshal(rules)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// DecodeOwnerTrust reads EncodeOwnerTrust's form back.
func DecodeOwnerTrust(raw string) ([]OwnerTrustRule, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var rules []OwnerTrustRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil, errors.Join(errors.New("decoding grain_config.owner_trust"), err)
	}
	return rules, nil
}
