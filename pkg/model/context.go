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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// ContextAnyRepo is the Config.AutoGrantContextReads entry that auto-grants
// read access for any non-target committed-context repository a task names.
const ContextAnyRepo = "*"

// ContextRef is one committed-context reference attached to a task: a folder
// or file of approved specifications (design docs, product briefs, mock HTML,
// pinned data snapshots), either in the task's own target repository or in a
// separate context repository read via Task.Reads.
//
// Both placements share this shape: switching between a folder in the target
// repo and a dedicated context repo is changing Repo (and granting -read when
// not auto-granted), not switching mechanisms.
type ContextRef struct {
	// Repo is the repository holding Path. Zero means the task's own target
	// repository (see ResolvedRepo).
	Repo RepoRef
	// Path is the clean, repository-relative path to the context folder or file.
	Path string
	// Rev is the commit SHA (or git ref before filing-time resolution) Path is
	// pinned to; empty when unpinned.
	Rev string
	// Manifest is the optional context.yml metadata discovered in Path. Display
	// and prompt pass-through only: grain never enforces workflow gates from it.
	Manifest *ContextManifest
}

// ContextManifest is the optional context.yml convention inside a committed
// context folder: an inventory of files, their kinds, human-readable titles,
// and upstream source URLs (for example in an external planning tool).
type ContextManifest struct {
	Title       string                `json:"title,omitempty" yaml:"title,omitempty"`
	Description string                `json:"description,omitempty" yaml:"description,omitempty"`
	Files       []ContextManifestFile `json:"files,omitempty" yaml:"files,omitempty"`
}

// ContextManifestFile describes one file listed in a ContextManifest.
type ContextManifestFile struct {
	Path      string `json:"path" yaml:"path"`
	Kind      string `json:"kind,omitempty" yaml:"kind,omitempty"`
	Title     string `json:"title,omitempty" yaml:"title,omitempty"`
	SourceURL string `json:"source_url,omitempty" yaml:"source_url,omitempty"`
}

// ParseContextRef parses a committed-context reference in the form
// `[owner/name:]path[@rev]` (or `[host/owner/name:]path[@rev]`), where a bare
// `path[@rev]` defaults to the task's own target repository.
func ParseContextRef(raw string) (ContextRef, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ContextRef{}, fmt.Errorf("context reference is empty")
	}
	for _, r := range trimmed {
		if unicode.IsControl(r) {
			return ContextRef{}, fmt.Errorf("context reference %q may not contain control characters", raw)
		}
	}

	beforeAt := trimmed
	rev := ""
	lastColon := strings.LastIndex(trimmed, ":")
	lastAt := strings.LastIndex(trimmed, "@")
	if lastAt > lastColon {
		beforeAt = trimmed[:lastAt]
		revPart := trimmed[lastAt+1:]
		if revPart == "" {
			return ContextRef{}, fmt.Errorf("context reference %q has an empty @rev suffix", raw)
		}
		cleanRev, err := ValidateContextRev(revPart)
		if err != nil {
			return ContextRef{}, fmt.Errorf("context reference %q: %w", raw, err)
		}
		rev = cleanRev
	}

	var repo RepoRef
	pathPart := beforeAt
	if colon := strings.LastIndex(beforeAt, ":"); colon >= 0 {
		repoPart := beforeAt[:colon]
		pathPart = beforeAt[colon+1:]
		parsedRepo, err := ParseRepo(repoPart)
		if err != nil {
			return ContextRef{}, fmt.Errorf("context reference %q: invalid repo before ':' (%w); write [owner/name:]path[@rev]", raw, err)
		}
		repo = parsedRepo
	}

	cleanPath, err := CleanContextPath(pathPart)
	if err != nil {
		return ContextRef{}, fmt.Errorf("context reference %q: %w", raw, err)
	}

	return ContextRef{
		Repo: repo,
		Path: cleanPath,
		Rev:  rev,
	}, nil
}

// CleanContextPath validates and normalizes a repository-relative context path.
func CleanContextPath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	for strings.HasPrefix(p, "./") {
		p = strings.TrimPrefix(p, "./")
	}
	p = strings.TrimSuffix(p, "/")
	if p == "" || p == "." {
		return "", fmt.Errorf("path %q must be a non-empty repository-relative path such as design/area/id", raw)
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("path %q must be repository-relative, not absolute", raw)
	}
	if strings.HasPrefix(p, "-") {
		return "", fmt.Errorf("path %q may not start with '-'", raw)
	}
	if strings.ContainsAny(p, `\:@`) {
		return "", fmt.Errorf("path %q may not contain '\\', ':' or '@'", raw)
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("path %q may not contain control characters", raw)
		}
	}
	parts := strings.Split(p, "/")
	for _, seg := range parts {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("path %q may not contain empty, '.' or '..' segments", raw)
		}
	}
	return p, nil
}

// ValidateContextRev validates a commit SHA or git ref on a ContextRef and
// lowercases hexadecimal commit SHAs.
func ValidateContextRev(raw string) (string, error) {
	rev := strings.TrimSpace(raw)
	if rev == "" {
		return "", fmt.Errorf("revision is empty")
	}
	if strings.HasPrefix(rev, "-") {
		return "", fmt.Errorf("revision %q may not start with '-'", raw)
	}
	if strings.Contains(rev, "..") || strings.Contains(rev, "@{") || strings.HasSuffix(rev, ".lock") || strings.HasSuffix(rev, "/") || strings.HasPrefix(rev, "/") || strings.Contains(rev, "//") {
		return "", fmt.Errorf("revision %q is not a single commit SHA or ref name", raw)
	}
	for _, r := range rev {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", fmt.Errorf("revision %q may not contain whitespace or control characters", raw)
		}
		switch r {
		case '\\', ':', '?', '*', '[', '~', '^':
			return "", fmt.Errorf("revision %q contains invalid git ref character %q", raw, r)
		}
	}
	if IsCommitSHA(rev) {
		return strings.ToLower(rev), nil
	}
	return rev, nil
}

// IsCommitSHA reports whether s is a 7..64-character hexadecimal commit SHA
// (abbreviated or full SHA-1 / SHA-256).
func IsCommitSHA(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// IsFullCommitSHA reports whether s is a full 40-char (SHA-1) or 64-char
// (SHA-256) hexadecimal commit hash.
func IsFullCommitSHA(s string) bool {
	return (len(s) == 40 || len(s) == 64) && IsCommitSHA(s)
}

// ResolvedRepo returns the repository r lives in given a task's target
// repository: r.Repo when explicitly set, else *target when target != nil.
func (r ContextRef) ResolvedRepo(target *RepoRef) (RepoRef, bool) {
	if !r.Repo.IsZero() {
		return r.Repo, true
	}
	if target != nil && !target.IsZero() {
		return *target, true
	}
	return RepoRef{}, false
}

// TargetsOtherRepo reports whether r lives in a repository other than target.
func (r ContextRef) TargetsOtherRepo(target *RepoRef) bool {
	if r.Repo.IsZero() {
		return false
	}
	if target == nil || target.IsZero() {
		return true
	}
	return !r.Repo.Same(*target)
}

// StringRelativeTo formats r in canonical LinkDesign form relative to target:
// omitting `<repo>:` when r lives in target (preserving bare `path` and
// `path@rev` compatibility), and qualifying `<repo>:path[@rev]` otherwise.
func (r ContextRef) StringRelativeTo(target *RepoRef) string {
	var b strings.Builder
	if !r.Repo.IsZero() && (target == nil || target.IsZero() || !r.Repo.Same(*target)) {
		b.WriteString(r.Repo.String())
		b.WriteByte(':')
	}
	b.WriteString(r.Path)
	if r.Rev != "" {
		b.WriteByte('@')
		b.WriteString(r.Rev)
	}
	return b.String()
}

// CanonicalKey returns a stable key identifying (repo, path, rev) relative to target.
func (r ContextRef) CanonicalKey(target *RepoRef) string {
	repoStr := ""
	if repo, ok := r.ResolvedRepo(target); ok {
		repoStr = strings.ToLower(repo.String())
	}
	key := repoStr + ":" + r.Path
	if r.Rev != "" {
		key += "@" + strings.ToLower(r.Rev)
	}
	return key
}

type rawContextManifest struct {
	Title       string                   `yaml:"title"`
	Description string                   `yaml:"description"`
	Files       []rawContextManifestFile `yaml:"files"`
}

type rawContextManifestFile struct {
	Path         string `yaml:"path"`
	Kind         string `yaml:"kind"`
	Title        string `yaml:"title"`
	SourceURL    string `yaml:"source_url"`
	SourceURLAlt string `yaml:"sourceUrl"`
}

// ParseContextManifest parses a context.yml document from a context directory.
// Grain displays this metadata and passes it through to the agent prompt; it
// never enforces workflow gates based on manifest contents.
func ParseContextManifest(data []byte) (*ContextManifest, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return &ContextManifest{}, nil
	}
	var raw rawContextManifest
	dec := yaml.NewDecoder(bytes.NewReader(trimmed))
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parsing context.yml: %w", err)
	}
	out := &ContextManifest{
		Title:       sanitizeManifestText(raw.Title),
		Description: sanitizeManifestText(raw.Description),
	}
	for i, f := range raw.Files {
		cleanPath, err := CleanContextPath(f.Path)
		if err != nil {
			return nil, fmt.Errorf("context.yml files[%d]: %w", i, err)
		}
		src := strings.TrimSpace(f.SourceURL)
		if src == "" {
			src = strings.TrimSpace(f.SourceURLAlt)
		}
		cleanURL, err := sanitizeManifestURL(src)
		if err != nil {
			return nil, fmt.Errorf("context.yml files[%d].source_url: %w", i, err)
		}
		out.Files = append(out.Files, ContextManifestFile{
			Path:      cleanPath,
			Kind:      sanitizeManifestText(f.Kind),
			Title:     sanitizeManifestText(f.Title),
			SourceURL: cleanURL,
		})
	}
	return out, nil
}

func sanitizeManifestText(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

func sanitizeManifestURL(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", fmt.Errorf("URL %q may not contain whitespace or control characters", raw)
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid URL %q: %w", raw, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("URL %q must use http or https", raw)
	}
	return u.String(), nil
}

type storedContextManifest struct {
	Repo     string          `json:"repo,omitempty"`
	Path     string          `json:"path"`
	Rev      string          `json:"rev,omitempty"`
	Manifest ContextManifest `json:"manifest"`
}

func encodeStoredContextManifest(target *RepoRef, ref ContextRef, m ContextManifest) (string, error) {
	repoStr := ""
	if repo, ok := ref.ResolvedRepo(target); ok {
		repoStr = repo.String()
	}
	rec := storedContextManifest{
		Repo:     repoStr,
		Path:     ref.Path,
		Rev:      ref.Rev,
		Manifest: m,
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func decodeStoredContextManifest(raw string) (storedContextManifest, bool) {
	var rec storedContextManifest
	if err := json.Unmarshal([]byte(raw), &rec); err != nil || rec.Path == "" {
		return storedContextManifest{}, false
	}
	return rec, true
}

func storedManifestMatches(rec storedContextManifest, target *RepoRef, ref ContextRef) bool {
	if rec.Path != ref.Path {
		return false
	}
	refRepo, refHasRepo := ref.ResolvedRepo(target)
	switch {
	case rec.Repo == "" && !refHasRepo:
	case rec.Repo != "" && refHasRepo && SameRepoText(rec.Repo, refRepo.String()):
	default:
		return false
	}
	if rec.Rev == "" || ref.Rev == "" {
		return rec.Rev == ref.Rev
	}
	lr, rr := strings.ToLower(rec.Rev), strings.ToLower(ref.Rev)
	return lr == rr || (IsCommitSHA(lr) && IsCommitSHA(rr) && (strings.HasPrefix(lr, rr) || strings.HasPrefix(rr, lr)))
}

// ContextRefs returns t's committed-context references in declaration order,
// pairing each LinkDesign link with any discovered LinkContextManifest record.
func (t Task) ContextRefs() []ContextRef {
	var manifests []storedContextManifest
	for _, l := range t.Links {
		if l.Kind != LinkContextManifest {
			continue
		}
		if rec, ok := decodeStoredContextManifest(l.Target); ok {
			manifests = append(manifests, rec)
		}
	}
	var out []ContextRef
	for _, l := range t.Links {
		if l.Kind != LinkDesign {
			continue
		}
		ref, err := ParseContextRef(l.Target)
		if err != nil {
			p := strings.TrimSpace(l.Target)
			if p == "" {
				continue
			}
			ref = ContextRef{Path: p}
		}
		for i := range manifests {
			if storedManifestMatches(manifests[i], t.Target, ref) {
				mCopy := cloneContextManifest(manifests[i].Manifest)
				ref.Manifest = &mCopy
				break
			}
		}
		out = append(out, ref)
	}
	return out
}

func cloneContextManifest(m ContextManifest) ContextManifest {
	out := ContextManifest{
		Title:       m.Title,
		Description: m.Description,
	}
	if len(m.Files) > 0 {
		out.Files = append([]ContextManifestFile(nil), m.Files...)
	}
	return out
}

// SetTaskContextManifest attaches or updates the stored ContextManifest on t
// for ref (and upgrades an abbreviated SHA on t's matching LinkDesign link when
// ref.Rev is the resolved full commit SHA). It reports whether t.Links changed.
func SetTaskContextManifest(t *Task, ref ContextRef, m *ContextManifest) bool {
	if t == nil {
		return false
	}
	changed := false
	if IsFullCommitSHA(ref.Rev) {
		for i, l := range t.Links {
			if l.Kind != LinkDesign {
				continue
			}
			parsed, err := ParseContextRef(l.Target)
			if err != nil || parsed.Path != ref.Path {
				continue
			}
			pRepo, pOK := parsed.ResolvedRepo(t.Target)
			rRepo, rOK := ref.ResolvedRepo(t.Target)
			if pOK != rOK || (pOK && !pRepo.Same(rRepo)) {
				continue
			}
			if parsed.Rev != "" && IsCommitSHA(parsed.Rev) && strings.HasPrefix(ref.Rev, strings.ToLower(parsed.Rev)) && parsed.Rev != ref.Rev {
				parsed.Rev = ref.Rev
				nextTarget := parsed.StringRelativeTo(t.Target)
				if t.Links[i].Target != nextTarget {
					t.Links[i].Target = nextTarget
					changed = true
				}
			}
		}
	}
	if m == nil {
		return changed
	}
	encoded, err := encodeStoredContextManifest(t.Target, ref, *m)
	if err != nil {
		return changed
	}
	kept := make([]Link, 0, len(t.Links)+1)
	replaced := false
	for _, l := range t.Links {
		if l.Kind == LinkContextManifest {
			if rec, ok := decodeStoredContextManifest(l.Target); ok && storedManifestMatches(rec, t.Target, ref) {
				if !replaced {
					if l.Target != encoded {
						changed = true
					}
					kept = append(kept, Link{Kind: LinkContextManifest, Target: encoded})
					replaced = true
				} else {
					changed = true
				}
				continue
			}
		}
		kept = append(kept, l)
	}
	if !replaced {
		kept = append(kept, Link{Kind: LinkContextManifest, Target: encoded})
		changed = true
	}
	t.Links = kept
	return changed
}

// InheritContext copies source's committed-context references (and the -read
// repository grants any non-target context references require) onto derived,
// unless derived was already filed with its own LinkDesign context references.
//
// When source's context lives in source.Target and derived targets a different
// repository (or has no target repository), InheritContext qualifies the
// inherited reference with source.Target and grants derived read access to
// source.Target so the child sees the exact same pinned files.
func InheritContext(derived *Task, source Task) {
	if derived == nil {
		return
	}
	for _, l := range derived.Links {
		if l.Kind == LinkDesign {
			return
		}
	}
	refs := source.ContextRefs()
	if len(refs) == 0 {
		return
	}
	for _, ref := range refs {
		childRef := ContextRef{
			Path:     ref.Path,
			Rev:      ref.Rev,
			Manifest: ref.Manifest,
		}
		if repo, ok := ref.ResolvedRepo(source.Target); ok {
			if derived.Target == nil || derived.Target.IsZero() || !derived.Target.Same(repo) {
				childRef.Repo = repo
				appendUniqueRepo(&derived.Reads, repo)
			}
		}
		targetStr := childRef.StringRelativeTo(derived.Target)
		hasDesign := false
		for _, l := range derived.Links {
			if l.Kind == LinkDesign && l.Target == targetStr {
				hasDesign = true
				break
			}
		}
		if !hasDesign {
			derived.Links = append(derived.Links, Link{Kind: LinkDesign, Target: targetStr})
		}
		if childRef.Manifest != nil {
			SetTaskContextManifest(derived, childRef, childRef.Manifest)
		}
	}
}

func appendUniqueRepo(reads *[]RepoRef, repo RepoRef) {
	for _, existing := range *reads {
		if existing.Same(repo) {
			return
		}
	}
	*reads = append(*reads, repo)
}

// NormalizeAutoGrantContextReads validates Config.AutoGrantContextReads entries
// ("*" or owner/name / host/owner/name) and returns them deduplicated in
// canonical form.
func NormalizeAutoGrantContextReads(entries []string) ([]string, error) {
	var out []string
	for i, raw := range entries {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return nil, fmt.Errorf("autoGrantContextReads[%d]: repo is required (owner/name, or %q for every context repository)", i, ContextAnyRepo)
		}
		var normalized string
		if trimmed == ContextAnyRepo {
			normalized = ContextAnyRepo
		} else {
			ref, err := ParseRepo(trimmed)
			if err != nil {
				return nil, fmt.Errorf("autoGrantContextReads[%d]: %w", i, err)
			}
			normalized = ref.String()
		}
		dup := false
		for _, existing := range out {
			if existing == ContextAnyRepo && normalized == ContextAnyRepo {
				dup = true
				break
			}
			if existing != ContextAnyRepo && normalized != ContextAnyRepo && SameRepoText(existing, normalized) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, normalized)
		}
	}
	return out, nil
}

// ContextReadAutoGranted reports whether entries auto-grant read access when a
// task's committed context names repo.
func ContextReadAutoGranted(entries []string, repo RepoRef) bool {
	if repo.IsZero() {
		return false
	}
	repoText := repo.String()
	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		if trimmed == ContextAnyRepo {
			return true
		}
		if trimmed != "" && SameRepoText(trimmed, repoText) {
			return true
		}
	}
	return false
}

// EncodeAutoGrantContextReads encodes Config.AutoGrantContextReads for
// grain_config.auto_grant_context_reads: JSON, or "" when empty.
func EncodeAutoGrantContextReads(entries []string) (string, error) {
	if len(entries) == 0 {
		return "", nil
	}
	b, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// DecodeAutoGrantContextReads decodes EncodeAutoGrantContextReads's stored form.
func DecodeAutoGrantContextReads(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, errors.Join(errors.New("decoding grain_config.auto_grant_context_reads"), err)
	}
	return out, nil
}
