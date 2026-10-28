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
	// (Config.AllowedBranches). A target that is not is still listed,
	// so an operator sees the channel exists and what to configure to
	// reach it, rather than wondering why it is missing.
	Allowed bool `json:"allowed"`
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
	seen := map[string]bool{}
	add := func(name string, kind TargetKind) {
		if seen[name] {
			return
		}
		seen[name] = true
		out.Targets = append(out.Targets, Target{Name: name, Kind: kind, Allowed: u.checkBranch(name) == nil})
	}

	repository := u.registryRepository()
	for _, name := range standardChannels {
		if repository == "" && name == "latest" {
			continue
		}
		add(name, TargetChannel)
	}
	if repository != "" {
		tags, err := u.registryTags(ctx, repository)
		if err != nil {
			log.Printf("upgrade: listing released versions in %s: %v", repository, err)
			out.VersionsError = err.Error()
		}
		ed := u.edition()
		for _, v := range releasedVersions(tags, ed) {
			add(v, TargetVersion)
		}
		if runningRC := u.runningCandidateRelease(); runningRC != "" {
			out.DiscardedRC = discardedRCFromTags(runningRC, tags, ed)
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
	tags, err := u.registryTags(ctx, repository)
	if err != nil {
		return nil
	}
	return discardedRCFromTags(runningRC, tags, u.edition())
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
	u.tags.mu.Lock()
	cached := u.tags.repo == repository && !u.tags.fetched.IsZero() && time.Since(u.tags.fetched) < tagListTTL
	tags := u.tags.tags
	u.tags.mu.Unlock()
	if cached {
		return discardedRCFromTags(runningRC, tags, u.edition())
	}
	if u.cfg.ListTags != nil {
		return u.DiscardedRC(ctx)
	}
	return nil
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

func discardedRCFromTags(running string, tags []string, edition string) *DiscardedRC {
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
	wantEd := normalizeEdition(edition)
	if wantEd == "" {
		wantEd = normalizeEdition(tagEdition(running))
	}
	for _, t := range tags {
		vm := versionTag.FindStringSubmatch(t)
		if vm == nil {
			continue
		}
		if vm[1] == maj && vm[2] == min && vm[3] == pat {
			if wantEd == "" || normalizeEdition(vm[4]) == wantEd {
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
		if wantEd != "" && normalizeEdition(tEd) != wantEd {
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
	if rels := releasedVersions(tags, edition); len(rels) > 0 {
		latest = rels[0]
	}
	return &DiscardedRC{
		Running:       running,
		Latest:        latest,
		ReplacementRC: replacementRC,
	}
}

// registryRepository is the image repository this deployment's upgrades
// pull from, or "" on the source pipeline.
func (u *Upgrader) registryRepository() string {
	switch u.Mode() {
	case ModeKube:
		return u.cfg.Kube.Repository
	case ModeImage:
		return u.EffectiveRepository()
	}
	return ""
}

func (u *Upgrader) registryTags(ctx context.Context, repository string) ([]string, error) {
	u.tags.mu.Lock()
	defer u.tags.mu.Unlock()
	if u.tags.repo == repository && time.Since(u.tags.fetched) < tagListTTL {
		return u.tags.tags, nil
	}
	list := u.cfg.ListTags
	if list == nil {
		list = ListRegistryTags
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tags, err := list(ctx, repository)
	if err != nil {
		return nil, err
	}
	u.tags.repo, u.tags.tags, u.tags.fetched = repository, tags, time.Now()
	return tags, nil
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
	type version struct {
		tag string
		n   [3]int
	}
	filterByEdition := len(edition) > 0
	var wantEd string
	if filterByEdition {
		wantEd = normalizeEdition(edition[0])
	}
	var vs []version
	for _, t := range tags {
		m := versionTag.FindStringSubmatch(t)
		if m == nil {
			continue
		}
		tagEd := normalizeEdition(m[4])
		if filterByEdition && tagEd != wantEd {
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

// ListRegistryTags lists repository's tags ("ghcr.io/bwsalmon/grain/grain")
// through the registry's own v2 API, anonymously: a public package
// answers a pull-scoped token to anybody, which is all a tag list needs,
// and the pull credentials scripts/setup.sh gives docker are docker's,
// not this process's. A private registry therefore lists nothing, and
// the pane says so and still takes a typed version.
func ListRegistryTags(ctx context.Context, repository string) ([]string, error) {
	host, name := registryLocation(repository)
	next := "https://" + host + "/v2/" + name + "/tags/list?n=1000"
	token := ""
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
		if resp.StatusCode == http.StatusUnauthorized && token == "" {
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
		// A Docker Hub short name ("library/grain", "grain"): its
		// registry host is not in the name.
		host, name = "registry-1.docker.io", repository
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
