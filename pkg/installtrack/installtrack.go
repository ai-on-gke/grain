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

// Package installtrack classifies a Grain deployment into an install
// track from host, systemd unit, and store signals -- falling back to the
// edition linked into the binary (SetBuildTrack) -- and provides
// repository transition rules for shifting pre-split deployments to their
// post-split image repositories.
//
// Core grain knows one track, TrackOSS. An edition linked into the binary
// registers its own (RegisterTrack) from init(): the signals that mark a
// deployment as one of its installs, the image repository it upgrades
// from, and the repositories it may move between. It may also name its
// track as the build's own (SetBuildTrack), so a deployment running that
// edition's binary on a host showing none of its signals -- a CI runner,
// a plain VM -- is still that edition's install rather than TrackOSS.
package installtrack

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Track identifies which release/repository track a Grain deployment belongs to.
type Track string

const (
	// TrackOSS is a standard open-source Grain deployment, and the track of
	// any deployment no registered track claims.
	TrackOSS Track = "oss"
)

const (
	// LegacyGrainRepository is the pre-Docker-Hub container image repository
	// shared by open-source and pre-split deployments on GHCR.
	LegacyGrainRepository          = "ghcr.io/bwsalmon/grain/grain"
	LegacyGuestRepository          = "ghcr.io/bwsalmon/grain/guest"
	LegacyGuestBaseRepository      = "ghcr.io/bwsalmon/grain/guest-base"
	LegacyGranuleSandboxRepository = "ghcr.io/bwsalmon/grain/granule-sandbox"
	LegacyK8sSandboxRepository     = "ghcr.io/bwsalmon/grain/k8s-sandbox"
	LegacyK8sSandboxBaseRepository = "ghcr.io/bwsalmon/grain/k8s-sandbox-base"

	// DefaultOSSGrainRepository is the default container image repository for
	// open-source deployments on Docker Hub.
	DefaultOSSGrainRepository          = "docker.io/grainai/grain"
	DefaultOSSSandboxRepository        = "docker.io/grainai/sandbox"
	DefaultOSSSandboxBaseRepository    = "docker.io/grainai/sandbox-base"
	DefaultOSSK8sSandboxRepository     = "docker.io/grainai/sandbox-k8s"
	DefaultOSSK8sSandboxBaseRepository = "docker.io/grainai/sandbox-k8s-base"
	DefaultOSSHubRepository            = "docker.io/grainai/hub"
	// DefaultOSSRegistryRepository is DefaultOSSHubRepository's name
	// from before the Grain Registry was renamed the Grain Hub; releases
	// publish both, with the same tags, for one release.
	DefaultOSSRegistryRepository = "docker.io/grainai/registry"

	// OSSImageRepoEnv overrides DefaultOSSGrainRepository.
	OSSImageRepoEnv = "GRAIN_OSS_IMAGE_REPO"
)

type ossImageRepoAlias struct {
	oss    string
	legacy []string
}

// ossImageRepositories is the single Go source of truth for open-source Docker
// Hub image repositories and the legacy GHCR repositories that transition onto
// them. Matches scripts/release-image-repos.sh.
var ossImageRepositories = []ossImageRepoAlias{
	{oss: DefaultOSSGrainRepository, legacy: []string{LegacyGrainRepository, "ghcr.io/ai-on-gke/grain/grain"}},
	{oss: DefaultOSSSandboxRepository, legacy: []string{LegacyGuestRepository, LegacyGranuleSandboxRepository, "ghcr.io/ai-on-gke/grain/guest"}},
	{oss: DefaultOSSSandboxBaseRepository, legacy: []string{LegacyGuestBaseRepository, "ghcr.io/ai-on-gke/grain/guest-base"}},
	{oss: DefaultOSSK8sSandboxRepository, legacy: []string{LegacyK8sSandboxRepository, "ghcr.io/ai-on-gke/grain/k8s-sandbox"}},
	{oss: DefaultOSSK8sSandboxBaseRepository, legacy: []string{LegacyK8sSandboxBaseRepository, "ghcr.io/ai-on-gke/grain/k8s-sandbox-base"}},
	{oss: DefaultOSSHubRepository},
	{oss: DefaultOSSRegistryRepository},
}

var (
	legacyToOSSRepositories map[string]string
	ossToLegacyRepositories map[string]string
)

func init() {
	legacyToOSSRepositories = make(map[string]string, len(ossImageRepositories)*2)
	ossToLegacyRepositories = make(map[string]string, len(ossImageRepositories))
	for _, entry := range ossImageRepositories {
		if len(entry.legacy) > 0 {
			ossToLegacyRepositories[entry.oss] = entry.legacy[0]
		}
		for _, leg := range entry.legacy {
			legacyToOSSRepositories[leg] = entry.oss
		}
	}
}

func normalizeOSSRepo(repo string) string {
	trimmed := strings.TrimSpace(repo)
	if rest, ok := strings.CutPrefix(trimmed, "index.docker.io/"); ok {
		return "docker.io/" + rest
	}
	if rest, ok := strings.CutPrefix(trimmed, "registry-1.docker.io/"); ok {
		return "docker.io/" + rest
	}
	if strings.HasPrefix(trimmed, "grainai/") {
		return "docker.io/" + trimmed
	}
	return trimmed
}

// ResolveLegacyRepository returns the canonical Docker Hub repository for a
// legacy GHCR open-source Grain image repository, or repo (with Docker Hub
// grainai/* short/alias prefixes normalized) when it is not a legacy GHCR name.
func ResolveLegacyRepository(repo string) string {
	norm := normalizeOSSRepo(repo)
	if oss, ok := legacyToOSSRepositories[norm]; ok {
		return oss
	}
	return norm
}

// IsLegacyRepository reports whether repo is one of the legacy GHCR open-source
// Grain image repositories that moved to Docker Hub.
func IsLegacyRepository(repo string) bool {
	_, ok := legacyToOSSRepositories[strings.TrimSpace(repo)]
	return ok
}

// LegacyRepositoryFor returns the primary legacy GHCR repository that ossRepo
// replaced, when one exists.
func LegacyRepositoryFor(ossRepo string) (string, bool) {
	leg, ok := ossToLegacyRepositories[normalizeOSSRepo(ossRepo)]
	return leg, ok
}

// ResolveLegacyImageRef rewrites an unpinned legacy GHCR Grain image reference
// (e.g. "ghcr.io/bwsalmon/grain/guest:latest") onto its Docker Hub counterpart
// ("docker.io/grainai/sandbox:latest"). Digest-pinned references (`@sha256:...`)
// are returned unchanged unless stripDigest is true, since pre-cutover GHCR
// digests may not exist in Docker Hub.
func ResolveLegacyImageRef(ref string) (resolved string, changed bool) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" || strings.Contains(trimmed, "@") {
		return ref, false
	}
	repo := trimmed
	suffix := ""
	if idx := strings.LastIndex(trimmed, ":"); idx != -1 && idx > strings.LastIndex(trimmed, "/") {
		repo = trimmed[:idx]
		suffix = trimmed[idx:]
	}
	oss, ok := legacyToOSSRepositories[repo]
	if !ok {
		return ref, false
	}
	return oss + suffix, true
}

// DefaultUnitFile is the standard systemd unit path written by scripts/setup.sh.
const DefaultUnitFile = "/etc/systemd/system/grain-daemon.service"

// Definition is an install track an edition registers.
type Definition struct {
	// Track is the track's name, reported as Detection.Track.
	Track Track
	// DefaultRepository is the image repository a deployment on the
	// track upgrades from when it names none.
	DefaultRepository string
	// RepositoryEnv, if set, names an environment variable overriding
	// DefaultRepository; its value is also a known successor repository.
	RepositoryEnv string
	// MovesFrom are repositories a deployment on this track still naming
	// one of moves to DefaultRepository (RepositoryForTrack): the build
	// it was already running, published somewhere new.
	MovesFrom []string
	// Successors are further repositories a deployment may opt into
	// explicitly and so may transition to, but never by default.
	Successors []string
	// Detect returns the signals in f marking a deployment as one of
	// this track's installs; none means it is not.
	Detect func(f Facts) []string
	// SwitchAwayLoses names what a deployment on this track gives up by
	// running another edition's build -- the capability servers and
	// other integrations the edition links in, say -- in words the
	// upgrade pane's "switch edition" confirmation shows the operator.
	// Empty leaves the pane's generic wording.
	SwitchAwayLoses []string
}

// RepositoryTrack reports which track's builds the grain image
// repository repo carries, and false when this build cannot say (a
// repository no track names, or a pre-split one no track claims).
//
// Open-source repositories are checked first and always belong to
// TrackOSS: Docker Hub's grainai/grain (however it is spelled), the
// repository GRAIN_OSS_IMAGE_REPO names, and the legacy GHCR
// repositories that now carry the core build -- even when a registered
// track lists one among the repositories its deployments move off
// (Definition.MovesFrom) or may opt into (Definition.Successors). A
// registered track then claims its DefaultRepository, the value of its
// RepositoryEnv, its Successors and its MovesFrom.
//
// It is what keeps an upgrade from crossing editions: a deployment on
// one track pulling another's build would restart as that other edition,
// without the integrations its own edition links in (grain/task-4153).
func RepositoryTrack(repo string) (Track, bool) {
	trimmed := strings.TrimSpace(repo)
	if trimmed == "" {
		return "", false
	}
	norm := ResolveLegacyRepository(trimmed)
	if norm == DefaultOSSGrainRepository {
		return TrackOSS, true
	}
	if env := strings.TrimSpace(os.Getenv(OSSImageRepoEnv)); env != "" && (env == trimmed || normalizeOSSRepo(env) == norm) {
		return TrackOSS, true
	}
	for _, d := range registered() {
		if trimmed == strings.TrimSpace(d.DefaultRepository) ||
			slices.Contains(d.Successors, trimmed) || slices.Contains(d.MovesFrom, trimmed) {
			return d.Track, true
		}
		if d.RepositoryEnv != "" {
			if env := strings.TrimSpace(os.Getenv(d.RepositoryEnv)); env != "" && env == trimmed {
				return d.Track, true
			}
		}
	}
	return "", false
}

// SwitchAwayLoses is the registered track's Definition.SwitchAwayLoses,
// or nil for TrackOSS and any track that names none.
func SwitchAwayLoses(track Track) []string {
	if d, ok := lookup(track); ok {
		return slices.Clone(d.SwitchAwayLoses)
	}
	return nil
}

var (
	tracksMu sync.RWMutex
	tracks   []Definition
)

// RegisterTrack adds def, replacing any registered track of the same
// name. A deployment matching several tracks' signals is classified as
// the first one registered.
func RegisterTrack(def Definition) {
	tracksMu.Lock()
	defer tracksMu.Unlock()
	for i, d := range tracks {
		if d.Track == def.Track {
			tracks[i] = def
			return
		}
	}
	tracks = append(tracks, def)
}

var buildTrack Track

// SetBuildTrack names track as the edition linked into this binary: the
// track Detect settles on when no registered track's signals match,
// instead of TrackOSS. Without it a signal-less deployment running an
// edition's build was classified open source, and then refused upgrades
// onto its own edition's images as another edition's (grain/task-4512).
// track must be registered (RegisterTrack) for Detect to use it; ""
// clears it. It returns a func restoring the previous value, for tests.
// Call it from the edition's init(), after RegisterTrack.
func SetBuildTrack(track Track) (restore func()) {
	tracksMu.Lock()
	defer tracksMu.Unlock()
	prev := buildTrack
	buildTrack = track
	return func() {
		tracksMu.Lock()
		defer tracksMu.Unlock()
		buildTrack = prev
	}
}

// BuildTrack is the track SetBuildTrack named, or "" when none was.
func BuildTrack() Track {
	tracksMu.RLock()
	defer tracksMu.RUnlock()
	return buildTrack
}

func registered() []Definition {
	tracksMu.RLock()
	defer tracksMu.RUnlock()
	return slices.Clone(tracks)
}

func lookup(t Track) (Definition, bool) {
	for _, d := range registered() {
		if d.Track == t {
			return d, true
		}
	}
	return Definition{}, false
}

// Tracks lists every track this build knows: TrackOSS first, then the
// registered ones in registration order.
func Tracks() []Track {
	out := []Track{TrackOSS}
	for _, d := range registered() {
		out = append(out, d.Track)
	}
	return out
}

// Detection is the result of classifying an installed deployment.
type Detection struct {
	Track   Track    `json:"track"`
	Signals []string `json:"signals,omitempty"`
}

// DefaultRepository returns the default post-split container image repository
// for the given install track.
func DefaultRepository(track Track) string {
	if d, ok := lookup(track); ok && d.DefaultRepository != "" {
		return d.DefaultRepository
	}
	return DefaultOSSGrainRepository
}

// ImageRepository is DefaultRepository(track) unless the track's
// override environment variable names another.
func ImageRepository(track Track) string {
	env := OSSImageRepoEnv
	if d, ok := lookup(track); ok {
		env = d.RepositoryEnv
	}
	if env != "" {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v
		}
	}
	return DefaultRepository(track)
}

// RepositoryForTrack is the repository a deployment configured with repo
// upgrades from, given its install track. A registered track may name
// repositories its deployments move off (Definition.MovesFrom) onto its
// DefaultRepository -- the build they were already running -- rather
// than stay on one that now carries another build. Any other repository
// is returned unchanged.
func RepositoryForTrack(repo string, track Track) string {
	trimmed := strings.TrimSpace(repo)
	if d, ok := lookup(track); ok && d.DefaultRepository != "" && slices.Contains(d.MovesFrom, trimmed) {
		return d.DefaultRepository
	}
	if track == TrackOSS && (trimmed == LegacyGrainRepository || trimmed == "ghcr.io/ai-on-gke/grain/grain") {
		return ImageRepository(TrackOSS)
	}
	return repo
}

// KnownSuccessorRepositories returns the built-in, registered and
// caller-supplied successor repositories that a pre-split deployment on
// LegacyGrainRepository may shift to.
func KnownSuccessorRepositories(extra ...string) []string {
	repos := []string{LegacyGrainRepository, "ghcr.io/ai-on-gke/grain/grain", DefaultOSSGrainRepository}
	envs := []string{OSSImageRepoEnv}
	for _, d := range registered() {
		repos = append(repos, d.MovesFrom...)
		repos = append(repos, d.DefaultRepository)
		repos = append(repos, d.Successors...)
		if d.RepositoryEnv != "" {
			envs = append(envs, d.RepositoryEnv)
		}
	}
	for _, name := range envs {
		if env := strings.TrimSpace(os.Getenv(name)); env != "" {
			repos = append(repos, env)
		}
	}
	if env := strings.TrimSpace(os.Getenv("GRAIN_ALLOWED_IMAGE_REPOS")); env != "" {
		for _, part := range strings.FieldsFunc(env, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t' || r == '\n'
		}) {
			if part = strings.TrimSpace(part); part != "" {
				repos = append(repos, part)
			}
		}
	}
	for _, r := range extra {
		for _, part := range strings.FieldsFunc(r, func(ch rune) bool {
			return ch == ',' || ch == ' ' || ch == '\t' || ch == '\n'
		}) {
			if part = strings.TrimSpace(part); part != "" {
				repos = append(repos, part)
			}
		}
	}
	var out []string
	for _, r := range repos {
		if r != "" && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// CanTransitionRepository reports whether an install currently configured for
// fromRepo is permitted to transition to (or accept an image ref from) toRepo.
//
// A transition is permitted when:
//   - fromRepo and toRepo are identical and non-empty, or
//   - toRepo is explicitly listed in extraAllowed, or
//   - fromRepo is one of the known Grain repositories (e.g. LegacyGrainRepository)
//     and toRepo is also one of the known successor repositories
//     (KnownSuccessorRepositories: core's, every registered track's, their
//     environment overrides, GRAIN_ALLOWED_IMAGE_REPOS, or extraAllowed).
func CanTransitionRepository(fromRepo, toRepo string, extraAllowed ...string) bool {
	fromRepo = strings.TrimSpace(fromRepo)
	toRepo = strings.TrimSpace(toRepo)
	if fromRepo == "" || toRepo == "" {
		return false
	}
	if fromRepo == toRepo {
		return true
	}
	for _, extra := range extraAllowed {
		for _, part := range strings.FieldsFunc(extra, func(ch rune) bool {
			return ch == ',' || ch == ' ' || ch == '\t' || ch == '\n'
		}) {
			if strings.TrimSpace(part) == toRepo {
				return true
			}
		}
	}
	known := KnownSuccessorRepositories(extraAllowed...)
	return slices.Contains(known, fromRepo) && slices.Contains(known, toRepo)
}

// ValidateTargetRepository returns nil when a deployment configured for
// fromRepo may upgrade onto an image in toRepo (CanTransitionRepository),
// and an error naming both and the repositories that would be accepted
// otherwise. An empty fromRepo is a deployment with nothing configured to
// transition from, which may only name a known successor repository.
func ValidateTargetRepository(fromRepo, toRepo string, extraAllowed ...string) error {
	fromRepo = strings.TrimSpace(fromRepo)
	toRepo = strings.TrimSpace(toRepo)
	if toRepo == "" {
		return fmt.Errorf("no image repository given")
	}
	if fromRepo == "" {
		if slices.Contains(KnownSuccessorRepositories(extraAllowed...), toRepo) {
			return nil
		}
	} else if CanTransitionRepository(fromRepo, toRepo, extraAllowed...) {
		return nil
	}
	return fmt.Errorf("image repository %q is not one this deployment (on %q) may upgrade onto; known repositories are %s (add others to GRAIN_ALLOWED_IMAGE_REPOS)",
		toRepo, fromRepo, strings.Join(KnownSuccessorRepositories(extraAllowed...), ", "))
}

// Probe holds the host, systemd unit, and store inputs used to classify an
// install.
//
// A zero-value Probe is hermetic and inspects only the fields explicitly set
// on it; call DefaultProbe().Detect() to include the live host's hostname,
// directories, and /etc/systemd/system/grain-daemon.service.
type Probe struct {
	// IncludeHost, when true, reads the live host's os.Hostname(), lets a
	// track check whether a directory exists on disk, and reads
	// DefaultUnitFile if UnitFile and UnitText are both empty.
	IncludeHost bool

	// Host signals. Dirs answers whether a directory exists, ahead of
	// Stat (os.Stat when IncludeHost).
	Hostname string
	Dirs     map[string]bool
	Stat     func(string) (os.FileInfo, error)

	// Systemd unit / daemon flag signals.
	UnitFile       string
	UnitText       string
	AgyPath        string
	UIAllowedHosts []string

	// Store / configuration signals.
	Config         *model.Config
	RepoConfigs    []model.RepoConfig
	GitHosts       []string
	AmbientSources []string
	SecretsDir     string
	DataDir        string
}

// DefaultProbe returns a Probe configured to inspect the current host and its
// installed systemd unit file.
func DefaultProbe() Probe {
	return Probe{IncludeHost: true}
}

// Facts are what a Probe found, for a registered track's Detect to read.
type Facts struct {
	Hostname       string
	AgyPath        string
	UIAllowedHosts []string
	Config         *model.Config
	RepoConfigs    []model.RepoConfig
	GitHosts       []string
	AmbientSources []string
	// AmbientSecrets are the host and source of each ambient git
	// credential in the secrets directory, and SecretHosts the hosts
	// its hosts.json names.
	AmbientSecrets []AmbientSecret
	SecretHosts    []string
	// StateRepoConfig is the state repository's settings/config.yml.
	StateRepoConfig string

	probe Probe
}

// AmbientSecret is one *.ambient.json in the secrets directory.
type AmbientSecret struct {
	Host   string `json:"host"`
	Source string `json:"source"`
}

// HasDir reports whether path is a directory on the probed host.
func (f Facts) HasDir(path string) bool {
	if v, ok := f.probe.Dirs[path]; ok {
		return v
	}
	stat := f.probe.Stat
	if stat == nil {
		if !f.probe.IncludeHost {
			return false
		}
		stat = os.Stat
	}
	info, err := stat(path)
	return err == nil && info != nil && info.IsDir()
}

// BuildSignal is the signal Detection.Signals reports when Detect
// settled on the build's own track (SetBuildTrack) for want of any
// other.
func BuildSignal(track Track) string { return "build:" + string(track) }

// Detect classifies the deployment p describes: the first registered
// track whose signals match, with them; else the build's own track
// (SetBuildTrack), with BuildSignal; else TrackOSS.
func (p Probe) Detect() Detection {
	defs := registered()
	if len(defs) == 0 {
		return Detection{Track: TrackOSS}
	}
	f := p.facts()
	for _, d := range defs {
		if d.Detect == nil {
			continue
		}
		var signals []string
		for _, sig := range d.Detect(f) {
			if !slices.Contains(signals, sig) {
				signals = append(signals, sig)
			}
		}
		if len(signals) > 0 {
			return Detection{Track: d.Track, Signals: signals}
		}
	}
	if bt := BuildTrack(); bt != "" && bt != TrackOSS {
		if _, ok := lookup(bt); ok {
			return Detection{Track: bt, Signals: []string{BuildSignal(bt)}}
		}
	}
	return Detection{Track: TrackOSS}
}

func (p Probe) facts() Facts {
	f := Facts{
		Hostname:       strings.TrimSpace(p.Hostname),
		AgyPath:        strings.TrimSpace(p.AgyPath),
		UIAllowedHosts: append([]string(nil), p.UIAllowedHosts...),
		Config:         p.Config,
		RepoConfigs:    p.RepoConfigs,
		GitHosts:       p.GitHosts,
		AmbientSources: p.AmbientSources,
		probe:          p,
	}
	if f.Hostname == "" && p.IncludeHost {
		if h, err := os.Hostname(); err == nil {
			f.Hostname = h
		}
	}
	unitText := p.UnitText
	if unitText == "" {
		unitFile := strings.TrimSpace(p.UnitFile)
		if unitFile == "" && p.IncludeHost {
			unitFile = DefaultUnitFile
		}
		if unitFile != "" {
			if data, err := os.ReadFile(unitFile); err == nil {
				unitText = string(data)
			}
		}
	}
	if unitText != "" {
		unitAgy, unitHosts := parseUnitSignals(unitText)
		if f.AgyPath == "" {
			f.AgyPath = unitAgy
		}
		f.UIAllowedHosts = append(f.UIAllowedHosts, unitHosts...)
	}
	dataDir := strings.TrimSpace(p.DataDir)
	secretsDir := strings.TrimSpace(p.SecretsDir)
	if secretsDir == "" && dataDir != "" {
		secretsDir = filepath.Join(dataDir, "secrets", "github")
	}
	if secretsDir != "" {
		f.AmbientSecrets, f.SecretHosts = readSecretsDir(secretsDir)
	}
	if dataDir != "" {
		if data, err := os.ReadFile(filepath.Join(dataDir, "state-repo", "settings", "config.yml")); err == nil {
			f.StateRepoConfig = string(data)
		}
	}
	return f
}

func readSecretsDir(dir string) (ambient []AmbientSecret, hosts []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		isAmbient := strings.HasSuffix(name, ".ambient.json")
		if !isAmbient && name != "hosts.json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		if isAmbient {
			var amb AmbientSecret
			if json.Unmarshal(data, &amb) == nil {
				ambient = append(ambient, amb)
			}
		} else if name == "hosts.json" {
			var m map[string]any
			if json.Unmarshal(data, &m) == nil {
				for h := range m {
					hosts = append(hosts, h)
				}
				slices.Sort(hosts)
			}
		}
	}
	return ambient, hosts
}

// parseUnitSignals extracts -agy-path and -ui-allowed-hosts values from a
// systemd unit file's contents.
func parseUnitSignals(unitText string) (agyPath string, allowedHosts []string) {
	// Normalize line continuations so multi-line ExecStart blocks tokenize cleanly.
	normalized := strings.ReplaceAll(unitText, "\\\r\n", " ")
	normalized = strings.ReplaceAll(normalized, "\\\n", " ")
	tokens := strings.Fields(normalized)
	for i := 0; i < len(tokens); i++ {
		tok := strings.Trim(tokens[i], `"'`)
		switch {
		case tok == "-agy-path" || tok == "--agy-path":
			if i+1 < len(tokens) {
				agyPath = strings.Trim(tokens[i+1], `"'`)
				i++
			}
		case strings.HasPrefix(tok, "-agy-path="):
			agyPath = strings.Trim(strings.TrimPrefix(tok, "-agy-path="), `"'`)
		case strings.HasPrefix(tok, "--agy-path="):
			agyPath = strings.Trim(strings.TrimPrefix(tok, "--agy-path="), `"'`)
		case tok == "-ui-allowed-hosts" || tok == "--ui-allowed-hosts":
			if i+1 < len(tokens) {
				allowedHosts = append(allowedHosts, strings.Trim(tokens[i+1], `"'`))
				i++
			}
		case strings.HasPrefix(tok, "-ui-allowed-hosts="):
			allowedHosts = append(allowedHosts, strings.Trim(strings.TrimPrefix(tok, "-ui-allowed-hosts="), `"'`))
		case strings.HasPrefix(tok, "--ui-allowed-hosts="):
			allowedHosts = append(allowedHosts, strings.Trim(strings.TrimPrefix(tok, "--ui-allowed-hosts="), `"'`))
		}
	}
	return agyPath, allowedHosts
}
