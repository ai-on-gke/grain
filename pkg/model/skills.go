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
	"path"
	"regexp"
	"slices"
	"strings"
)

// DefaultSkillFolders are the repository-root-relative folders searched one
// level deep for */SKILL.md when no layer overrides them.
var DefaultSkillFolders = []string{
	".claude/skills",
	".agents/skills",
	".codex/skills",
	".gemini/skills",
	"skills",
}

// skillFolderSegmentRe restricts each path segment of a configured skill folder
// to safe repository-relative names (no shell metacharacters, spaces or control bytes).
var skillFolderSegmentRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// SkillsConfig configures repository skill discovery at a single layer
// (grain_config, repo_config / .grain/settings.yml, template, or task).
//
// A nil *SkillsConfig (or one with both Enabled and Folders nil) means unset:
// the layer defers to the one beneath it. Setting Enabled to false or Folders
// to an empty slice (folders: []) turns discovery off at that layer.
type SkillsConfig struct {
	Enabled *bool     `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Folders *[]string `json:"folders,omitempty" yaml:"folders,omitempty"`
}

// NewSkillsConfig constructs a SkillsConfig helper for callers and tests.
func NewSkillsConfig(enabled *bool, folders *[]string) *SkillsConfig {
	if enabled == nil && folders == nil {
		return nil
	}
	c := &SkillsConfig{}
	if enabled != nil {
		b := *enabled
		c.Enabled = &b
	}
	if folders != nil {
		s := slices.Clone(*folders)
		if s == nil {
			s = []string{}
		}
		c.Folders = &s
	}
	return c
}

// IsZero reports whether c carries no configuration at all.
func (c *SkillsConfig) IsZero() bool {
	return c == nil || (c.Enabled == nil && c.Folders == nil)
}

// Clone returns a deep copy of c, or nil when c.IsZero().
func (c *SkillsConfig) Clone() *SkillsConfig {
	if c.IsZero() {
		return nil
	}
	return NewSkillsConfig(c.Enabled, c.Folders)
}

// Equal reports whether c and o express the same configuration.
func (c *SkillsConfig) Equal(o *SkillsConfig) bool {
	if c.IsZero() && o.IsZero() {
		return true
	}
	if c.IsZero() != o.IsZero() {
		return false
	}
	if (c.Enabled == nil) != (o.Enabled == nil) {
		return false
	}
	if c.Enabled != nil && *c.Enabled != *o.Enabled {
		return false
	}
	if (c.Folders == nil) != (o.Folders == nil) {
		return false
	}
	if c.Folders != nil && !slices.Equal(*c.Folders, *o.Folders) {
		return false
	}
	return true
}

// CleanSkillFolder validates and normalizes a single repository-relative skill
// folder path, rejecting absolute paths, backslashes, parent traversal (".."),
// root ("."), and unsafe characters.
func CleanSkillFolder(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("skill folder path is empty")
	}
	if strings.ContainsRune(s, '\\') || strings.ContainsRune(s, 0) {
		return "", fmt.Errorf("skill folder %q must use forward slashes relative to the repository root", raw)
	}
	if strings.HasPrefix(s, "/") {
		return "", fmt.Errorf("skill folder %q must be relative to the repository root, not absolute", raw)
	}
	s = strings.TrimPrefix(s, "./")
	s = strings.TrimSuffix(s, "/")
	if s == "" || s == "." {
		return "", fmt.Errorf("skill folder %q cannot be the repository root", raw)
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("skill folder %q escapes or misnames a directory segment", raw)
		}
		if !skillFolderSegmentRe.MatchString(seg) {
			return "", fmt.Errorf("skill folder %q contains unsupported characters in %q", raw, seg)
		}
	}
	cleaned := path.Clean(s)
	if cleaned != s || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("skill folder %q escapes the repository root", raw)
	}
	return cleaned, nil
}

// NormalizeSkillsConfig validates and canonicalizes cfg, deduplicating folders
// in order while preserving an explicit empty list (folders: []).
func NormalizeSkillsConfig(cfg *SkillsConfig) (*SkillsConfig, error) {
	if cfg.IsZero() {
		return nil, nil
	}
	out := &SkillsConfig{}
	if cfg.Enabled != nil {
		b := *cfg.Enabled
		out.Enabled = &b
	}
	if cfg.Folders != nil {
		cleaned := make([]string, 0, len(*cfg.Folders))
		for _, f := range *cfg.Folders {
			c, err := CleanSkillFolder(f)
			if err != nil {
				return nil, err
			}
			if !slices.Contains(cleaned, c) {
				cleaned = append(cleaned, c)
			}
		}
		out.Folders = &cleaned
	}
	return out, nil
}

// EncodeSkillsConfig serializes cfg as compact JSON for storage, or "" when unset.
func EncodeSkillsConfig(cfg *SkillsConfig) string {
	norm, err := NormalizeSkillsConfig(cfg)
	if err != nil || norm.IsZero() {
		return ""
	}
	b, err := json.Marshal(norm)
	if err != nil {
		return ""
	}
	return string(b)
}

// DecodeSkillsConfig parses a stored JSON SkillsConfig, returning nil for empty
// or unreadable values.
func DecodeSkillsConfig(raw string) *SkillsConfig {
	s := strings.TrimSpace(raw)
	if s == "" || s == "{}" || s == "null" {
		return nil
	}
	var cfg SkillsConfig
	if err := json.Unmarshal([]byte(s), &cfg); err != nil {
		return nil
	}
	norm, err := NormalizeSkillsConfig(&cfg)
	if err != nil {
		return nil
	}
	return norm
}

// ComposeRepoSkills folds a repository's operator layer (operator) and its
// declared .grain/settings.yml layer (declared) into the effective per-repo
// SkillsConfig. An operator setting always wins where specified so an operator
// disabling discovery or choosing specific folders cannot be overridden by the
// repository's own file; when the operator leaves a field unset, the declared
// layer supplies it.
func ComposeRepoSkills(operator, declared *SkillsConfig) *SkillsConfig {
	op, _ := NormalizeSkillsConfig(operator)
	dec, _ := NormalizeSkillsConfig(declared)
	if op.IsZero() {
		return dec.Clone()
	}
	if dec.IsZero() {
		return op.Clone()
	}
	out := op.Clone()
	if out.Enabled == nil && dec.Enabled != nil {
		b := *dec.Enabled
		out.Enabled = &b
	}
	if out.Folders == nil && dec.Folders != nil {
		s := slices.Clone(*dec.Folders)
		out.Folders = &s
	}
	return out
}

// ResolveSkillFolders resolves the deployment default, per-repo configuration,
// and per-task override into the ordered list of repository-relative folders to
// scan for */SKILL.md. An empty slice means discovery is disabled.
func ResolveSkillFolders(deployment, repo, task *SkillsConfig) []string {
	enabled := true
	folders := slices.Clone(DefaultSkillFolders)

	for _, layer := range []*SkillsConfig{deployment, repo, task} {
		norm, err := NormalizeSkillsConfig(layer)
		if err != nil || norm.IsZero() {
			continue
		}
		if norm.Folders != nil {
			if len(*norm.Folders) == 0 {
				folders = nil
			} else {
				folders = slices.Clone(*norm.Folders)
				if norm.Enabled == nil {
					enabled = true
				}
			}
		}
		if norm.Enabled != nil {
			enabled = *norm.Enabled
			if enabled && norm.Folders == nil && len(folders) == 0 {
				folders = slices.Clone(DefaultSkillFolders)
			}
		}
	}

	if !enabled || len(folders) == 0 {
		return nil
	}
	return folders
}
