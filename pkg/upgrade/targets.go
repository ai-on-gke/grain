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

package upgrade

// What the upgrade pane offers in its selector (grain/task-1914): the
// handful of names an operator ordinarily upgrades onto, so that picking
// one is a click rather than remembering whether the release channel is
// called "latest" or "release", and which v1.X.0 tags were ever
// published. The pane still takes any other branch typed by hand; this
// list is a convenience, not a gate -- checkBranch is the gate, and each
// target here says whether it would pass it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/installtrack"
	"github.com/ai-on-gke/grain/pkg/registryauth"
	"github.com/ai-on-gke/grain/pkg/version"
)

// AnyBranchPattern, given as one of Config.AllowedBranches, matches every
// well-formed branch. path.Match's own '*' never crosses a '/', so
// without it an operator who really does mean "anything this repository
// has" would have to spell out a pattern per depth ("*,*/*,*/*/*") and
// still miss the next one. It is opt-in like every other pattern:
// DefaultAllowedBranches does not include it.
const AnyBranchPattern = "**"

// TargetKind says where a Target came from.
type TargetKind string

const (
	// TargetChannel is one of the moving names every grain release
	// process keeps: latest (the last promoted release), candidate (the
	// release being qualified) and main.
	TargetChannel TargetKind = "channel"
	// TargetVersion is one released version, "v1.6.0", as published to
	// the image registry.
	TargetVersion TargetKind = "version"
	// TargetAllowed is a literal branch the operator listed in
	// -upgrade-allowed-branches that is neither of the above -- a
	// release branch this deployment follows, say.
	TargetAllowed TargetKind = "allowed"
)

// Target is one entry in the pane's selector.
type Target struct {
	Name string     `json:"name"`
	Kind TargetKind `json:"kind"`
	// Allowed is whether Start would accept Name on this deployment
	// (Config.AllowedBranches and the schema-downgrade check). A target
	// that is not is still listed, so an operator sees the channel or
	// version exists and why it is refused.
	Allowed bool `json:"allowed"`
	// Reason explains why Allowed is false when a target is refused (for
	// example, because its build has an older schema version than the
	// running store).
	Reason string `json:"reason,omitempty"`
	// Schema is the target build's schema version when cheaply known.
	Schema int `json:"schema,omitempty"`
}

// DiscardedRC reports when the release candidate this deployment currently
// runs (e.g. "v2.3.0-rc.1") has been discarded during soak qualification and
// will not be promoted to a release.
type DiscardedRC struct {
	// Running is the discarded RC this deployment is currently on ("v2.3.0-rc.1").
	Running string `json:"running"`
	// Latest is the newest promoted release tag in the registry ("v2.1.1"), if known.
	Latest string `json:"latest,omitempty"`
	// ReplacementRC is a newer non-discarded RC for the same minor ("v2.3.0-rc.2"), if one exists.
	ReplacementRC string `json:"replacementRc,omitempty"`
}

// RetractedRelease reports when the promoted release this deployment
// currently runs (e.g. "v3.5.0") has since been retracted
// (scripts/release-from-trunk.sh rollback, design/playbook/releases.playbook.md
// section 11): the registry carries <running>-retracted beside it and
// latest has moved back. The deployment never downgrades by itself -- an
// older release may not read the schema this one migrated to -- so it is
// told to move instead: to Latest when that is safe, or to the next
// release, which carries the fix, when it is not.
type RetractedRelease struct {
	// Running is the retracted release this deployment is on ("v3.5.0").
	Running string `json:"running"`
	// Latest is the newest release in the registry that is not
	// retracted ("v3.4.0"), if any: where latest points again.
	Latest string `json:"latest,omitempty"`
	// Newer is the newest release after Running that is not retracted
	// ("v3.6.0"), if one has been published yet: the fix-forward to move to.
	Newer string `json:"newer,omitempty"`
}

// Targets is GET /api/upgrade/targets's answer.
type Targets struct {
	Targets []Target `json:"targets"`
	// AllowedBranches is the patterns a branch has to match, for the
	// pane to say what a hand-typed branch is up against.
	AllowedBranches []string `json:"allowedBranches"`
	// VersionsError says why no versions are listed when listing them
	// was tried and failed -- a registry that wants credentials, or one
	// this host cannot reach. Empty when versions were listed, or when
	// this pipeline has no registry to ask.
	VersionsError string `json:"versionsError,omitempty"`
	// Default is the channel this deployment follows (Upgrader.Channel):
	// what the pane offers first and what an upgrade that names no
	// branch goes to, so a deployment on latest is offered the next
	// release rather than main's head. Empty when the deployment cannot
	// say -- one pinned to a single build, or the source pipeline.
	Default string `json:"default,omitempty"`
	// DiscardedRC is set when the release candidate this deployment currently
	// runs has been discarded in the registry (either marked with a
	// <running>-discarded tag or superseded by a newer vX.Y.Z-rc.M while
	// vX.Y.Z has not been promoted).
	DiscardedRC *DiscardedRC `json:"discardedRc,omitempty"`
	// RetractedRelease is set when the release this deployment currently
	// runs has been retracted: <running>-retracted is in the registry.
	RetractedRelease *RetractedRelease `json:"retractedRelease,omitempty"`
}

// standardChannels is the order the selector offers the moving names in:
// the safest first.
var standardChannels = []string{"latest", "candidate", "main"}

// versionTag is a released version's tag: v1.6.0 or edition-qualified
// v2.0.0-google, not v1.7.0-rc.1 (a candidate's tag, which is what
// "candidate" already names while it is current) and not sha-1234567.
var versionTag = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(?:-(google|oss))?$`)

var candidateTagParts = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(?:-(google|oss))?-rc\.(\d+)(?:-(google|oss))?$`)

// maxVersions is how many released versions the selector lists, newest
// first. Enough to roll back a few releases; a version older than that
// can still be typed.
const maxVersions = 10

// tagListTTL is how long a registry's tag list is reused. The list moves
// once a day at most (a promotion), and the pane is opened far more
// often than that.
const tagListTTL = 10 * time.Minute

type tagCache struct {
	mu      sync.Mutex
	repo    string
	tags    []string
	fetched time.Time
	// dedicated is whether tags came from a dedicated edition release
	// repository (dedicatedReleaseRepository), whose bare vX.Y.Z tags
	// are this edition's own releases.
	dedicated bool
}

// Targets lists what the pane's selector offers, each marked with
// whether this deployment would accept it.
//
// Versions are listed only where an upgrade could reach one: the image
// and Kubernetes pipelines pull a tag, and a release's tag is published
// alongside its branch tags (.grain/workflows/promote.yml). The source
// pipeline fetches and resets onto origin/<branch>, which a tag is not,
// so it is offered neither the versions nor "latest" (also a tag).
func (u *Upgrader) Targets(ctx context.Context) Targets {
	out := Targets{AllowedBranches: append([]string(nil), u.AllowedBranches()...), Default: u.Channel()}
	runningSchema := u.runningSchemaVersion(ctx)
	seen := map[string]bool{}
	add := func(name string, kind TargetKind) {
		if seen[name] {
			return
		}
		seen[name] = true
		allowed := u.checkBranch(name) == nil
		t := Target{Name: name, Kind: kind, Allowed: allowed}
		if allowed && runningSchema > 0 {
			// Probe registry metadata only for channels (at most 3 fast HEAD/GETs);
			// released versions use knownReleaseSchemas and cached answers so listing
			// 10 versions stays fast.
			if info, ok := u.cheapTargetSchema(ctx, name, "", kind == TargetChannel); ok && info.Schema > 0 {
				t.Schema = info.Schema
				if info.Schema < runningSchema {
					t.Allowed = false
					t.Reason = formatSchemaDowngradeError(name, info.Ref, u.runningBuildStamp(), runningSchema, info.Build, info.Schema).Error()
				}
			}
		}
		out.Targets = append(out.Targets, t)
	}

	repository := u.registryRepository()
	for _, name := range standardChannels {
		if repository == "" && name == "latest" {
			continue
		}
		add(name, TargetChannel)
	}
	if repository != "" {
		tags, dedicated, err := u.registryTags(ctx, repository)
		if err != nil {
			log.Printf("upgrade: listing released versions in %s: %v", repository, err)
			out.VersionsError = err.Error()
		}
		f := u.tagFilter(dedicated)
		for _, v := range f.releasedVersions(tags) {
			add(v, TargetVersion)
		}
		if runningRC := u.runningCandidateRelease(); runningRC != "" {
			out.DiscardedRC = discardedRCFromTags(runningRC, tags, f)
		}
		if running := u.runningPromotedRelease(); running != "" {
			out.RetractedRelease = retractedReleaseFromTags(running, tags, f)
		}
	}
	for _, pattern := range u.AllowedBranches() {
		pattern = strings.TrimSpace(pattern)
		// A channel was offered (or, on the source pipeline, deliberately
		// not offered) above; being allowed does not make "latest" a
		// branch the source pipeline can reset onto. "rc" is an alias for
		// the "candidate" channel rather than a separate literal branch.
		if slices.Contains(standardChannels, pattern) || pattern == "rc" {
			continue
		}
		if strings.ContainsAny(pattern, `*?[\`) || validateBranch(pattern) != nil {
			continue
		}
		add(pattern, TargetAllowed)
	}
	return out
}

// DiscardedRC reports whether the release candidate this deployment currently
// runs has been discarded in the registry. It returns nil when the deployment
// is not running a release candidate, when the candidate is still active or has
// been promoted, or when the registry cannot be queried.
func (u *Upgrader) DiscardedRC(ctx context.Context) *DiscardedRC {
	runningRC := u.runningCandidateRelease()
	if runningRC == "" {
		return nil
	}
	repository := u.registryRepository()
	if repository == "" && u.cfg.ListTags == nil {
		return nil
	}
	tags, dedicated, err := u.registryTags(ctx, repository)
	if err != nil {
		return nil
	}
	return discardedRCFromTags(runningRC, tags, u.tagFilter(dedicated))
}

// CachedDiscardedRC reports DiscardedRC using the cached registry tag list when
// available (or querying Config.ListTags when a caller or test has provided an
// in-process tag lister), without starting a live network fetch on an uncached
// registry.
func (u *Upgrader) CachedDiscardedRC(ctx context.Context) *DiscardedRC {
	runningRC := u.runningCandidateRelease()
	if runningRC == "" {
		return nil
	}
	repository := u.registryRepository()
	key := u.tagCacheKey(repository)
	u.tags.mu.Lock()
	cached := u.tags.repo == key && !u.tags.fetched.IsZero() && time.Since(u.tags.fetched) < tagListTTL
	tags, dedicated := u.tags.tags, u.tags.dedicated
	u.tags.mu.Unlock()
	if cached {
		return discardedRCFromTags(runningRC, tags, u.tagFilter(dedicated))
	}
	if u.cfg.ListTags != nil {
		return u.DiscardedRC(ctx)
	}
	return nil
}

// CachedRetractedRelease reports whether the release this deployment runs
// has been retracted, from the cached registry tag list (or
// Config.ListTags when a caller or test provides one), without starting a
// live network fetch on an uncached registry. Nil when the deployment does
// not run a promoted release, when that release stands, or when there is
// no tag list to read.
func (u *Upgrader) CachedRetractedRelease(ctx context.Context) *RetractedRelease {
	running := u.runningPromotedRelease()
	if running == "" {
		return nil
	}
	repository := u.registryRepository()
	key := u.tagCacheKey(repository)
	u.tags.mu.Lock()
	cached := u.tags.repo == key && !u.tags.fetched.IsZero() && time.Since(u.tags.fetched) < tagListTTL
	tags, dedicated := u.tags.tags, u.tags.dedicated
	u.tags.mu.Unlock()
	if !cached {
		if u.cfg.ListTags == nil {
			return nil
		}
		var err error
		if tags, dedicated, err = u.registryTags(ctx, repository); err != nil {
			return nil
		}
	}
	return retractedReleaseFromTags(running, tags, u.tagFilter(dedicated))
}

// runningPromotedRelease is the release (vX.Y.Z, or edition-qualified
// vX.Y.Z-google) this deployment runs, or "" when it runs anything else:
// a candidate, a branch build or a sha- pin.
func (u *Upgrader) runningPromotedRelease() string {
	if rel := strings.TrimSpace(u.runningRelease()); versionTag.MatchString(rel) {
		return rel
	}
	if r, err := u.Running(); err == nil {
		if rel := strings.TrimSpace(r.Build.Release); versionTag.MatchString(rel) {
			return rel
		}
		ref, _, _ := strings.Cut(r.Ref, "@")
		if slash, colon := strings.LastIndex(ref, "/"), strings.LastIndex(ref, ":"); colon > slash {
			if tag := ref[colon+1:]; versionTag.MatchString(tag) {
				return tag
			}
		}
	}
	return ""
}

// retractedReleaseFromTags is RetractedRelease for a deployment running
// release running, given the registry's tags: nil unless the registry
// carries running-retracted.
func retractedReleaseFromTags(running string, tags []string, f tagFilter) *RetractedRelease {
	if !slices.Contains(tags, running+"-retracted") {
		return nil
	}
	out := &RetractedRelease{Running: running}
	rels := f.releasedVersions(tags)
	if len(rels) == 0 && f.edition == "" {
		rels = releasedVersions(tags)
	}
	for _, v := range rels {
		if v == running {
			continue
		}
		if out.Latest == "" && !versionAfter(v, running) {
			out.Latest = v
		}
		if out.Newer == "" && versionAfter(v, running) {
			out.Newer = v
		}
	}
	return out
}

// versionAfter is whether released version a is newer than b (both
// vX.Y.Z, edition suffixes ignored).
func versionAfter(a, b string) bool {
	am, bm := versionTag.FindStringSubmatch(a), versionTag.FindStringSubmatch(b)
	if am == nil || bm == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(am[i])
		y, _ := strconv.Atoi(bm[i])
		if x != y {
			return x > y
		}
	}
	return false
}

func (u *Upgrader) runningCandidateRelease() string {
	if rel := strings.TrimSpace(u.runningRelease()); candidateTag.MatchString(rel) {
		return rel
	}
	if r, err := u.Running(); err == nil {
		if rel := strings.TrimSpace(r.Build.Release); candidateTag.MatchString(rel) {
			return rel
		}
		ref, _, _ := strings.Cut(r.Ref, "@")
		if slash, colon := strings.LastIndex(ref, "/"), strings.LastIndex(ref, ":"); colon > slash {
			if tag := ref[colon+1:]; candidateTag.MatchString(tag) {
				return tag
			}
		}
	}
	return ""
}

func discardedRCFromTags(running string, tags []string, f tagFilter) *DiscardedRC {
	m := candidateTagParts.FindStringSubmatch(running)
	if m == nil {
		return nil
	}
	maj, min, pat := m[1], m[2], m[3]
	rcNum, err := strconv.Atoi(m[5])
	if err != nil {
		return nil
	}
	baseVersion := fmt.Sprintf("v%s.%s.%s", maj, min, pat)
	// With no edition of its own to go by, the running candidate's
	// suffix says which tags are its siblings; with neither, any are.
	if f.edition == "" {
		f.edition = normalizeEdition(tagEdition(running))
	}
	anyEdition := f.edition == "" && !f.bareOK
	matches := func(tagEd string) bool { return anyEdition || f.accepts(tagEd) }
	for _, t := range tags {
		vm := versionTag.FindStringSubmatch(t)
		if vm == nil {
			continue
		}
		if vm[1] == maj && vm[2] == min && vm[3] == pat {
			if matches(vm[4]) {
				return nil
			}
		}
	}
	explicitlyDiscarded := slices.Contains(tags, running+"-discarded") ||
		slices.Contains(tags, fmt.Sprintf("%s-rc.%d-discarded", baseVersion, rcNum))
	bestRCNum := rcNum
	replacementRC := ""
	for _, t := range tags {
		cm := candidateTagParts.FindStringSubmatch(t)
		if cm == nil {
			continue
		}
		if cm[1] != maj || cm[2] != min || cm[3] != pat {
			continue
		}
		tEd := cm[4]
		if tEd == "" {
			tEd = cm[6]
		}
		if !matches(tEd) {
			continue
		}
		n, err := strconv.Atoi(cm[5])
		if err != nil || n <= bestRCNum {
			continue
		}
		if slices.Contains(tags, t+"-discarded") || slices.Contains(tags, fmt.Sprintf("%s-rc.%d-discarded", baseVersion, n)) {
			continue
		}
		bestRCNum = n
		replacementRC = t
	}
	if !explicitlyDiscarded && replacementRC == "" {
		return nil
	}
	var latest string
	if rels := f.releasedVersions(tags); len(rels) > 0 {
		latest = rels[0]
	}
	return &DiscardedRC{
		Running:       running,
		Latest:        latest,
		ReplacementRC: replacementRC,
	}
}

// LatestRelease is the newest promoted release tag ("v3.6.0", or
// "v3.6.0-google" on that edition) in the registry this deployment's
// upgrades pull from, filtered to its own edition the way the selector's
// versions are: what the release-triggered automatic upgrade follows
// (orchestrator's reconcileReleaseUpgrade). Release candidates are not
// releases, and a retracted release is never offered again
// (releasedVersions), so neither is ever the answer. "" with no error is
// a registry that has no release yet. The source pipeline has no
// registry to watch, and says so.
func (u *Upgrader) LatestRelease(ctx context.Context) (string, error) {
	repository := u.registryRepository()
	if repository == "" {
		return "", errors.New("this deployment upgrades from source, which publishes no release tags to follow")
	}
	tags, dedicated, err := u.registryTags(ctx, repository)
	if err != nil {
		return "", fmt.Errorf("listing released versions in %s: %w", repository, err)
	}
	if versions := u.tagFilter(dedicated).releasedVersions(tags); len(versions) > 0 {
		return versions[0], nil
	}
	return "", nil
}

// registryRepository is the image repository this deployment's upgrades
// pull from, or "" on the source pipeline.
func (u *Upgrader) registryRepository() string {
	switch u.Mode() {
	case ModeKube:
		return u.EffectiveKubeRepository()
	case ModeImage:
		return u.EffectiveRepository()
	}
	return ""
}

func (u *Upgrader) tagCacheKey(repository string) string {
	return repository
}

// registryTags lists the tags upgrades can reach from the image registry,
// and reports whether repository is a dedicated edition image repository
// (dedicatedImageRepository) -- where a bare vX.Y.Z is this edition's own
// release -- rather than a registry shared with open-source builds.
func (u *Upgrader) registryTags(ctx context.Context, repository string) (tags []string, dedicated bool, err error) {
	key := u.tagCacheKey(repository)
	u.tags.mu.Lock()
	defer u.tags.mu.Unlock()
	if u.tags.repo == key && time.Since(u.tags.fetched) < tagListTTL {
		return u.tags.tags, u.tags.dedicated, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	list := u.cfg.ListTags
	if list == nil {
		list = ListRegistryTags
	}
	tags, err = list(ctx, repository)
	if err != nil {
		return nil, false, err
	}
	dedicated = u.dedicatedImageRepository(repository)
	u.tags.repo, u.tags.tags, u.tags.fetched, u.tags.dedicated = key, tags, time.Now(), dedicated
	return tags, dedicated, nil
}

// dedicatedImageRepository reports whether repository belongs to a registered
// non-OSS install track rather than to the open-source or pre-split shared
// image repositories. A post-split edition builds from its own repository and
// tags releases in its dedicated registry as bare vX.Y.Z / vX.Y.Z-rc.N.
func (u *Upgrader) dedicatedImageRepository(repository string) bool {
	var track installtrack.Track
	switch u.Mode() {
	case ModeKube:
		track = u.cfg.Kube.Track
	case ModeImage:
		track = u.cfg.Image.Track
	}
	if track == "" || track == installtrack.TrackOSS {
		return false
	}
	repo := strings.TrimSpace(repositoryOf(strings.TrimSpace(repository)))
	if repo == "" || installtrack.IsLegacyRepository(repo) || installtrack.ResolveLegacyRepository(repo) == installtrack.DefaultOSSGrainRepository {
		return false
	}
	return installtrack.ValidateTargetRepository(installtrack.DefaultRepository(track), repo) == nil
}

// tagFilter is which editions' tags a deployment accepts from one tag
// list.
type tagFilter struct {
	// edition is the deployment's own edition, normalized ("" is core).
	edition string
	// bareOK also accepts unsuffixed tags: the list came from a dedicated
	// edition release repository, where vX.Y.Z is the edition's release.
	bareOK bool
}

// tagFilter is the filter for a tag list from a dedicated edition release
// repository (dedicated) or from a possibly shared one.
func (u *Upgrader) tagFilter(dedicated bool) tagFilter {
	ed := normalizeEdition(u.edition())
	return tagFilter{edition: ed, bareOK: dedicated && ed != ""}
}

// accepts is whether a tag carrying edition suffix tagEd ("" for none) is
// one of this deployment's.
func (f tagFilter) accepts(tagEd string) bool {
	tagEd = normalizeEdition(tagEd)
	return tagEd == f.edition || (f.bareOK && tagEd == "")
}

// releasedVersions is releasedVersions filtered by f.
func (f tagFilter) releasedVersions(tags []string) []string {
	return releasedVersionsMatching(tags, f.accepts)
}

func (u *Upgrader) edition() string {
	if u.cfg.Edition != "" {
		return u.cfg.Edition
	}
	if u.Mode() != ModeSource {
		if r, err := u.Running(); err == nil {
			if ed := tagEdition(r.Ref); ed != "" {
				return ed
			}
		}
	}
	if u.cfg.RunningRelease != nil {
		if ed := tagEdition(u.cfg.RunningRelease()); ed != "" {
			return ed
		}
	}
	info := version.Get()
	if ed := tagEdition(info.Release); ed != "" {
		return ed
	}
	return info.Edition
}

func normalizeEdition(ed string) string {
	ed = strings.ToLower(strings.TrimSpace(ed))
	if ed == "oss" || ed == "core" {
		return ""
	}
	return ed
}

// releasedVersions picks the released versions out of a registry's
// tags, newest first, at most maxVersions of them. When an edition is
// given, only tags compatible with that edition are included.
func releasedVersions(tags []string, edition ...string) []string {
	if len(edition) == 0 {
		return releasedVersionsMatching(tags, nil)
	}
	return tagFilter{edition: normalizeEdition(edition[0])}.releasedVersions(tags)
}

// releasedVersionsMatching is releasedVersions keeping only the tags whose
// edition suffix ("" for none) accept takes; a nil accept keeps every one.
func releasedVersionsMatching(tags []string, accept func(tagEd string) bool) []string {
	type version struct {
		tag string
		n   [3]int
	}
	var vs []version
	for _, t := range tags {
		m := versionTag.FindStringSubmatch(t)
		if m == nil {
			continue
		}
		// A retracted release (scripts/release-from-trunk.sh rollback)
		// is never offered as a target again.
		if slices.Contains(tags, t+"-retracted") {
			continue
		}
		if accept != nil && !accept(m[4]) {
			continue
		}
		var v version
		v.tag = t
		for i := range v.n {
			v.n[i], _ = strconv.Atoi(m[i+1])
		}
		vs = append(vs, v)
	}
	sort.Slice(vs, func(i, j int) bool {
		for k := range vs[i].n {
			if vs[i].n[k] != vs[j].n[k] {
				return vs[i].n[k] > vs[j].n[k]
			}
		}
		return vs[i].tag < vs[j].tag
	})
	var out []string
	for i, v := range vs {
		if i == maxVersions {
			break
		}
		out = append(out, v.tag)
	}
	return out
}

// registryClient is what ListRegistryTags talks to the registry with.
var registryClient = &http.Client{Timeout: 15 * time.Second}

// ListRegistryTags lists repository's tags ("docker.io/grainai/grain")
// through the registry's own v2 API, anonymously: a public package
// answers a pull-scoped token to anybody, which is all a tag list needs,
// and the pull credentials scripts/setup.sh gives docker are docker's,
// not this process's. A private registry therefore lists nothing, and
// the pane says so and still takes a typed version. The exception is
// Artifact Registry and GCR, which take this host's own Google access
// token when it has one (registryauth.TokenFor) -- the same credential
// setup.sh logs docker in with.
func ListRegistryTags(ctx context.Context, repository string) ([]string, error) {
	host, name := registryLocation(repository)
	next := "https://" + host + "/v2/" + name + "/tags/list?n=1000"
	token := registryauth.TokenFor(ctx, host)
	exchanged := false
	var tags []string
	for page := 0; next != "" && page < 20; page++ {
		resp, err := registryGet(ctx, next, token)
		if err != nil && page == 0 && errors.Is(err, http.ErrSchemeMismatch) && loopbackRegistry(host) {
			// Docker's own rule for a registry on this host: it may
			// speak plain HTTP, and docker and cosign both pull and
			// verify from it that way, so the selector lists its
			// versions that way too rather than calling it unreachable.
			next = "http://" + strings.TrimPrefix(next, "https://")
			resp, err = registryGet(ctx, next, token)
		}
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && !exchanged {
			exchanged = true
			challenge := resp.Header.Get("WWW-Authenticate")
			resp.Body.Close()
			if token, err = registryToken(ctx, challenge); err != nil {
				return nil, err
			}
			page--
			continue
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		err = decodeRegistry(resp, &body)
		if err != nil {
			return nil, err
		}
		tags = append(tags, body.Tags...)
		next = nextPage(next, resp.Header.Get("Link"))
	}
	return tags, nil
}

// registryLocation splits repository into the registry host its v2 API
// answers on and the repository's name there.
func registryLocation(repository string) (host, name string) {
	host, name, ok := strings.Cut(repository, "/")
	if !ok || !strings.ContainsAny(host, ".:") && host != "localhost" {
		// A Docker Hub short name ("grainai/grain", "grain"): its
		// registry host is not in the name.
		host, name = "registry-1.docker.io", repository
		if !strings.Contains(name, "/") {
			name = "library/" + name
		}
		return host, name
	}
	if host == "docker.io" || host == "index.docker.io" || host == "registry-1.docker.io" {
		host = "registry-1.docker.io"
		if !strings.Contains(name, "/") {
			name = "library/" + name
		}
	}
	return host, name
}

// loopbackRegistry is whether host ("localhost:5000", "127.0.0.1") is
// one docker treats as insecure without being told to: a registry on the
// machine itself.
func loopbackRegistry(host string) bool {
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	if name == "localhost" {
		return true
	}
	ip := net.ParseIP(name)
	return ip != nil && ip.IsLoopback()
}

func registryGet(ctx context.Context, target, token string) (*http.Response, error) {
	return registryDo(ctx, http.MethodGet, target, token, nil)
}

func registryDo(ctx context.Context, method, target, token string, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, err
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return registryClient.Do(req)
}

func decodeRegistry(resp *http.Response, v any) error {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("%s answered %s", resp.Request.URL.Host, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
}

// registryToken answers a `Bearer realm=...,service=...,scope=...`
// challenge with an anonymous token.
func registryToken(ctx context.Context, challenge string) (string, error) {
	scheme, params, _ := strings.Cut(challenge, " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return "", errors.New("the registry wants credentials to list tags")
	}
	fields := map[string]string{}
	for _, part := range splitChallenge(params) {
		k, v, ok := strings.Cut(part, "=")
		if ok {
			fields[strings.ToLower(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	realm, err := url.Parse(fields["realm"])
	if err != nil || realm.Scheme != "https" && realm.Scheme != "http" {
		return "", fmt.Errorf("the registry's token challenge names no usable realm: %q", challenge)
	}
	q := realm.Query()
	for _, k := range []string{"service", "scope"} {
		if fields[k] != "" {
			q.Set(k, fields[k])
		}
	}
	realm.RawQuery = q.Encode()
	resp, err := registryGet(ctx, realm.String(), "")
	if err != nil {
		return "", err
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := decodeRegistry(resp, &body); err != nil {
		return "", err
	}
	if body.Token != "" {
		return body.Token, nil
	}
	if body.AccessToken != "" {
		return body.AccessToken, nil
	}
	return "", errors.New("the registry issued no token to list tags with")
}

// splitChallenge splits a challenge's parameters on the commas between
// them, not the ones inside a quoted value (a scope can carry several
// actions: "repository:x:pull,push").
func splitChallenge(s string) []string {
	var parts []string
	quoted, start := false, 0
	for i, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
		case r == ',' && !quoted:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

// nextPage resolves an RFC 5988 `Link: <...>; rel="next"` header against
// the page it came with, or "" when there is no next page.
func nextPage(current, link string) string {
	if link == "" {
		return ""
	}
	ref, _, _ := strings.Cut(link, ";")
	ref = strings.Trim(strings.TrimSpace(ref), "<>")
	if !strings.Contains(link, `rel="next"`) || ref == "" {
		return ""
	}
	base, err := url.Parse(current)
	if err != nil {
		return ""
	}
	next, err := base.Parse(ref)
	if err != nil || next.Host != base.Host {
		return ""
	}
	return next.String()
}
