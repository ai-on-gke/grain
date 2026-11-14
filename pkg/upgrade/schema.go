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

// The step that decides whether the build being cut over to can run on
// this deployment's data at all (grain/task-59, grain/task-5594).
//
// Everything else this package checks is a property of the new build
// alone: it pulled, it runs, it names a sandbox image. That was the
// whole of it until a release changed model.SchemaVersion, and then the
// upgrade did exactly what it was told -- pulled a healthy image, ran
// `grain schema-version` in it (which touches no store, deliberately),
// repointed the ref file and restarted -- into a build whose first act
// was to refuse the store on disk. The UI never bound a port, systemd
// restarted into the identical failure, and the rollback that exists for
// a broken binary never fired, because nothing was broken: the image was
// healthy and only this host's rows were in the way.
//
// So an upgrade now asks the new build for its schema version as well as
// for proof that it runs:
//
//   - If the target build's schema is LOWER than the running store's,
//     the upgrade is refused outright at this step: no export, no ref-file
//     rewrite, no rollout and no restart. Schema downgrades are never
//     allowed through the upgrade flow (whether by switching channel, a
//     channel whose registry tag is stale, a pinned older version, or
//     rollback).
//   - If the target build's schema is HIGHER than the running store's, it
//     does two more things before the ref file is touched:
//     Prepare -- the running build writes its whole database out, at its
//     own schema, somewhere the restart cannot take it with it
//     (daemon.WriteSchemaUpgrade)
//     CheckArgs -- the *new* build is asked to import that dump at its own
//     schema, in a container that touches nothing (`grain state check
//     -migrate`).
//
// A failure in any of these leaves the deployment where it was, on the
// build that is serving it, with the ref file untouched and any prepared
// note discarded.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/version"
)

// ErrSchemaDowngrade is returned whenever an upgrade target has a lower
// schema version than the running store.
var ErrSchemaDowngrade = errors.New("refusing schema downgrade")

// BuildStamp identifies a grain build by its stamped release and commit.
type BuildStamp struct {
	Release string
	Commit  string
}

func shortCommit(rev string) string {
	rev = strings.TrimSpace(rev)
	if len(rev) > 7 && !strings.Contains(rev, "-") {
		return rev[:7]
	}
	return rev
}

// String formats the build's release and commit for error messages.
func (b BuildStamp) String() string {
	rel := strings.TrimSpace(b.Release)
	rev := shortCommit(b.Commit)
	switch {
	case rel != "" && rev != "":
		return fmt.Sprintf("%s (commit %s)", rel, rev)
	case rel != "":
		return rel
	case rev != "":
		return "commit " + rev
	default:
		return "unknown build"
	}
}

// TargetBuildInfo describes what is known about an upgrade target's schema
// and build stamp without pulling image layers or building source.
type TargetBuildInfo struct {
	Schema int
	Build  BuildStamp
	Ref    string
}

type targetSchemaCacheEntry struct {
	info      TargetBuildInfo
	immutable bool
	fetchedAt time.Time
}

const targetSchemaCacheTTL = 5 * time.Minute

// knownReleaseSchemas maps promoted vX.Y.Z releases built before the
// io.grain.schema-version OCI label to the model.SchemaVersion they carry,
// so /api/upgrade/targets and Start can reject older-schema release tags
// and stale channels labelled with one without pulling image layers.
var knownReleaseSchemas = map[string]int{
	"v2.0.0": 27,
	"v3.0.0": 58,
	"v3.0.1": 63,
	"v3.0.2": 64,
	"v3.1.0": 87,
	"v3.1.1": 87,
	"v3.2.0": 119,
	"v3.3.0": 130,
	"v3.4.0": 130,
	"v3.5.0": 136,
	"v3.5.1": 136,
	"v3.6.0": 143,
	"v3.7.0": 146,
	"v3.8.0": 146,
}

func knownReleaseSchema(tag string) (int, bool) {
	tag = strings.TrimSpace(tag)
	m := versionTag.FindStringSubmatch(tag)
	if m == nil {
		return 0, false
	}
	base := fmt.Sprintf("v%s.%s.%s", m[1], m[2], m[3])
	if s, ok := knownReleaseSchemas[base]; ok {
		return s, true
	}
	return 0, false
}

// SchemaConfig, set on Config.Schema, is what an Upgrader needs to
// notice that a build changes this deployment's schema and to carry the
// deployment forward across it -- or refuse a schema downgrade. Left nil
// -- by `grain demo`, by a test, by any deployment with no state worth
// migrating -- an upgrade behaves as before: the step is skipped.
type SchemaConfig struct {
	// Version is the schema the running build knows,
	// model.SchemaVersion at the real call site. Zero switches the
	// whole step off, since there is nothing to compare against.
	Version int
	// StoreVersion, when set, reads the running store's schema version
	// from the live database; the larger of Version and StoreVersion's
	// answer is used as the running store schema.
	StoreVersion func(ctx context.Context) (int, error)
	// RunningBuild, when set, reports the running daemon's release and
	// commit for downgrade refusal messages. Defaults to version.Get().
	RunningBuild func() BuildStamp
	// TargetBuild, when set, cheaply resolves a target branch or image
	// reference's schema and build metadata before an upgrade starts or
	// when listing /api/upgrade/targets.
	TargetBuild func(ctx context.Context, target string) (TargetBuildInfo, bool)
	// OnDowngradeRefused, when set, is invoked whenever an upgrade attempt
	// is refused because its target build has a lower schema version than
	// the running store (for example, to raise an Inbox notification).
	OnDowngradeRefused func(branch string, runningSchema, targetSchema int, err error)
	// VersionArgs asks the new build for its own -- {"schema-version"},
	// the same subcommand HealthCheckArgs runs, whose entire output is
	// the number.
	VersionArgs []string
	// CheckArgs, with the directory Prepare returned appended, asks the
	// new build to rehearse the migration -- {"state", "check",
	// "-migrate"}. Empty skips the rehearsal and cuts over on the
	// export alone.
	CheckArgs []string
	// Prepare writes this deployment's rows down for the new build to
	// come back on, and returns the directory holding them.
	Prepare func(ctx context.Context, branch string, from, to int) (string, error)
	// Discard undoes Prepare when an upgrade fails or is cancelled after
	// Prepare succeeded.
	Discard func()
}

func (u *Upgrader) runningSchemaVersion(ctx context.Context) int {
	if u.cfg.Schema == nil || u.cfg.Schema.Version <= 0 {
		return 0
	}
	v := u.cfg.Schema.Version
	if u.cfg.Schema.StoreVersion != nil {
		if sv, err := u.cfg.Schema.StoreVersion(ctx); err == nil && sv > v {
			v = sv
		}
	}
	return v
}

func (u *Upgrader) runningBuildStamp() BuildStamp {
	if u.cfg.Schema != nil && u.cfg.Schema.RunningBuild != nil {
		if b := u.cfg.Schema.RunningBuild(); b.Release != "" || b.Commit != "" {
			return b
		}
	}
	info := version.Get()
	stamp := BuildStamp{Commit: info.Revision}
	if !info.Unversioned {
		stamp.Release = info.Release
	}
	if u.cfg.RunningRelease != nil {
		if rel := strings.TrimSpace(u.cfg.RunningRelease()); rel != "" {
			stamp.Release = rel
		}
	}
	if stamp.Release == "" && stamp.Commit == "" && u.Mode() == ModeSource {
		if r, err := u.Running(); err == nil {
			if !r.Build.Unversioned && r.Build.Release != "" {
				stamp.Release = r.Build.Release
			}
			stamp.Commit = r.Build.Revision
		}
	}
	if stamp.Release == "" && (u.Mode() == ModeImage || u.Mode() == ModeKube) {
		if r, err := u.Running(); err == nil && r.Ref != "" {
			withoutDigest, _, _ := strings.Cut(r.Ref, "@")
			if slash, colon := strings.LastIndex(withoutDigest, "/"), strings.LastIndex(withoutDigest, ":"); colon > slash {
				tag := withoutDigest[colon+1:]
				if versionTag.MatchString(tag) || candidateTag.MatchString(tag) {
					stamp.Release = tag
				}
			}
		}
	}
	return stamp
}

// isChannelTarget reports whether target names a moving channel (such as
// latest, candidate, rc, main, or a release/* branch) rather than an
// immutable version, candidate, or commit tag.
func isChannelTarget(target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	if slices.Contains(standardChannels, target) || target == "rc" {
		return true
	}
	if versionTag.MatchString(target) || candidateTag.MatchString(target) {
		return false
	}
	if strings.HasPrefix(target, "sha-") || objectIDPattern.MatchString(target) {
		return false
	}
	if strings.Contains(target, "@sha256:") || strings.HasPrefix(target, "sha256:") {
		return false
	}
	return true
}

func formatSchemaDowngradeError(branch, targetRef string, runningBuild BuildStamp, runningSchema int, targetBuild BuildStamp, targetSchema int) error {
	branch = strings.TrimSpace(branch)
	targetRef = strings.TrimSpace(targetRef)
	if targetBuild.Release == "" && versionTag.MatchString(branch) {
		targetBuild.Release = branch
	}
	channel := isChannelTarget(branch)
	targetWhere := fmt.Sprintf("target %q", branch)
	switch {
	case channel && branch != "":
		targetWhere = fmt.Sprintf("channel %q", branch)
	case branch != "":
		targetWhere = fmt.Sprintf("tag %q", branch)
	case targetRef != "":
		targetWhere = fmt.Sprintf("image %s", targetRef)
	}
	if targetRef != "" && targetWhere != "image "+targetRef {
		targetWhere += fmt.Sprintf(" (%s)", targetRef)
	}
	msg := fmt.Errorf(
		"%w on %s: target build %s is at schema %d, which is older than the running store at schema %d on %s; schema downgrades are never allowed through upgrade",
		ErrSchemaDowngrade, targetWhere, targetBuild.String(), targetSchema, runningSchema, runningBuild.String(),
	)
	if channel && branch != "" {
		return fmt.Errorf("%w. Channel %q's image is older than what is running, so the channel's image is probably stale or unpublished", msg, branch)
	}
	return msg
}

func (u *Upgrader) cacheTargetSchema(key string, info TargetBuildInfo, immutable bool) {
	key = strings.TrimSpace(key)
	if key == "" || info.Schema <= 0 {
		return
	}
	u.schemaCacheMu.Lock()
	defer u.schemaCacheMu.Unlock()
	if u.schemaCache == nil {
		u.schemaCache = map[string]targetSchemaCacheEntry{}
	}
	u.schemaCache[key] = targetSchemaCacheEntry{
		info:      info,
		immutable: immutable,
		fetchedAt: time.Now(),
	}
}

func (u *Upgrader) cachedTargetSchema(key string) (TargetBuildInfo, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return TargetBuildInfo{}, false
	}
	u.schemaCacheMu.Lock()
	defer u.schemaCacheMu.Unlock()
	entry, ok := u.schemaCache[key]
	if !ok {
		return TargetBuildInfo{}, false
	}
	if !entry.immutable && time.Since(entry.fetchedAt) > targetSchemaCacheTTL {
		delete(u.schemaCache, key)
		return TargetBuildInfo{}, false
	}
	return entry.info, true
}

// cheapTargetSchema returns what can be learned cheaply (without pulling
// image layers or compiling source) about branch or imageRef's schema.
func (u *Upgrader) cheapTargetSchema(ctx context.Context, branch, imageRef string, allowRegistryProbe bool) (TargetBuildInfo, bool) {
	runningSchema := u.runningSchemaVersion(ctx)
	if runningSchema <= 0 {
		return TargetBuildInfo{}, false
	}
	branch = strings.TrimSpace(branch)
	imageRef = strings.TrimSpace(imageRef)

	if u.cfg.Schema != nil && u.cfg.Schema.TargetBuild != nil {
		if imageRef != "" {
			if info, ok := u.cfg.Schema.TargetBuild(ctx, imageRef); ok && info.Schema > 0 {
				return info, true
			}
		}
		if branch != "" {
			if info, ok := u.cfg.Schema.TargetBuild(ctx, branch); ok && info.Schema > 0 {
				return info, true
			}
		}
	}

	if imageRef != "" {
		if pinned, ok := kubeDigestRef(imageRef); ok {
			if info, hit := u.cachedTargetSchema(pinned); hit {
				return info, true
			}
		}
		if info, hit := u.cachedTargetSchema(imageRef); hit {
			return info, true
		}
	}
	if branch != "" {
		if info, hit := u.cachedTargetSchema("branch:" + branch); hit {
			return info, true
		}
	}

	// Known released vX.Y.Z tags (only applied when running a real v2+ schema
	// version >= 27, so synthetic low schema numbers in unit tests are unaffected).
	if runningSchema >= 27 {
		tagCandidate := branch
		if imageRef != "" {
			if o, err := ParseImageOverride(imageRef); err == nil && o.Digest == "" && o.Tag != "" {
				tagCandidate = o.Tag
			}
		}
		if s, ok := knownReleaseSchema(tagCandidate); ok {
			ref := imageRef
			if ref == "" && (u.Mode() == ModeImage || u.Mode() == ModeKube) {
				if r, err := u.targetImageRef(branch); err == nil {
					ref = r
				}
			}
			return TargetBuildInfo{
				Schema: s,
				Build:  BuildStamp{Release: tagCandidate},
				Ref:    ref,
			}, true
		}
	}

	// Cheap OCI manifest + config-blob probe (only fetches tiny JSON metadata,
	// never image layers) when talking to a real registry.
	if allowRegistryProbe && (u.Mode() == ModeImage || u.Mode() == ModeKube) && u.cfg.ListTags == nil {
		ref := imageRef
		if ref == "" && branch != "" {
			if r, err := u.targetImageRef(branch); err == nil {
				ref = r
			}
		}
		if ref != "" {
			if info, ok := u.probeRegistryTargetSchema(ctx, branch, ref, runningSchema); ok {
				return info, true
			}
		}
	}
	return TargetBuildInfo{}, false
}

func (u *Upgrader) targetImageRef(branch string) (string, error) {
	if u.Mode() == ModeKube {
		return u.imageRefIn(u.EffectiveKubeRepository(), branch)
	}
	if u.Mode() == ModeImage {
		return u.imageRef(branch)
	}
	return "", errors.New("not an image deployment")
}

func (u *Upgrader) probeRegistryTargetSchema(ctx context.Context, branch, ref string, runningSchema int) (TargetBuildInfo, bool) {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	pinned := ref
	if _, hasDigest := digestRef(ref); !hasDigest {
		digest, err := u.manifestDigest(probeCtx, ref)
		if err != nil {
			return TargetBuildInfo{}, false
		}
		pinned = ref + "@" + digest
	}
	if normPinned, ok := kubeDigestRef(pinned); ok {
		if cached, hit := u.cachedTargetSchema(normPinned); hit {
			cached.Ref = pinned
			if branch != "" {
				u.cacheTargetSchema("branch:"+branch, cached, false)
			}
			return cached, true
		}
	}
	labels, err := registryImageLabels(probeCtx, pinned)
	if err != nil {
		return TargetBuildInfo{}, false
	}
	schema := labels.Schema
	if schema <= 0 && runningSchema >= 27 && labels.Version != "" {
		if s, ok := knownReleaseSchema(labels.Version); ok {
			schema = s
		}
	}
	if schema <= 0 {
		return TargetBuildInfo{}, false
	}
	info := TargetBuildInfo{
		Schema: schema,
		Build: BuildStamp{
			Release: labels.Version,
			Commit:  labels.Revision,
		},
		Ref: pinned,
	}
	if normPinned, ok := kubeDigestRef(pinned); ok {
		u.cacheTargetSchema(normPinned, info, true)
	}
	u.cacheTargetSchema(ref, info, strings.Contains(ref, "@sha256:"))
	if branch != "" {
		u.cacheTargetSchema("branch:"+branch, info, versionTag.MatchString(branch))
	}
	return info, true
}

// CheckTargetSchema checks whether upgrading onto branch (or explicit imageRef)
// is already known to be a schema downgrade, returning an ErrSchemaDowngrade
// error when targetSchema < runningSchema.
func (u *Upgrader) CheckTargetSchema(ctx context.Context, branch, imageRef string) error {
	runningSchema := u.runningSchemaVersion(ctx)
	if runningSchema <= 0 {
		return nil
	}
	info, ok := u.cheapTargetSchema(ctx, branch, imageRef, true)
	if !ok || info.Schema <= 0 || info.Schema >= runningSchema {
		return nil
	}
	return formatSchemaDowngradeError(branch, info.Ref, u.runningBuildStamp(), runningSchema, info.Build, info.Schema)
}

func (u *Upgrader) notifyDowngradeRefused(branch string, runningSchema, targetSchema int, err error) {
	if u.cfg.Schema != nil && u.cfg.Schema.OnDowngradeRefused != nil {
		u.cfg.Schema.OnDowngradeRefused(branch, runningSchema, targetSchema, err)
	}
}

// schemaStep runs the schema comparison, export and rehearsal, and reports
// what to add to the upgrade's Detail line.
//
// beforePrepare, when non-nil, runs once it is established that the target
// build requires a forward schema export (to > runningSchema) and before
// Prepare snapshots the database (used by stageKube so run-host cursors are
// flushed into the store only after confirming the upgrade is not a refused
// downgrade).
func (u *Upgrader) schemaStep(ctx context.Context, p *progress, branch string, build schemaRunner) (string, bool, error) {
	return u.schemaStepWith(ctx, p, branch, build, nil)
}

func (u *Upgrader) schemaStepWith(ctx context.Context, p *progress, branch string, build schemaRunner, beforePrepare func() error) (string, bool, error) {
	cfg := u.cfg.Schema
	if cfg == nil || cfg.Version == 0 || len(cfg.VersionArgs) == 0 {
		p.skip(stepSchema, "this deployment does not check schema changes")
		return "", false, nil
	}
	runningSchema := u.runningSchemaVersion(ctx)
	p.start(stepSchema)
	to, err := u.newBuildSchema(ctx, p, build)
	if err != nil {
		return "", false, err
	}
	ref := build.targetRef()
	if to < runningSchema {
		targetStamp := build.stamp(ctx, p)
		info := TargetBuildInfo{Schema: to, Build: targetStamp, Ref: ref}
		if normPinned, ok := kubeDigestRef(ref); ok {
			u.cacheTargetSchema(normPinned, info, true)
		}
		if ref != "" {
			u.cacheTargetSchema(ref, info, strings.Contains(ref, "@sha256:"))
		}
		if branch != "" {
			u.cacheTargetSchema("branch:"+branch, info, versionTag.MatchString(branch))
		}
		refusal := formatSchemaDowngradeError(branch, ref, u.runningBuildStamp(), runningSchema, targetStamp, to)
		u.notifyDowngradeRefused(branch, runningSchema, to, refusal)
		return "", false, refusal
	}
	if normPinned, ok := kubeDigestRef(ref); ok {
		u.cacheTargetSchema(normPinned, TargetBuildInfo{Schema: to, Ref: ref}, true)
	}
	if branch != "" {
		u.cacheTargetSchema("branch:"+branch, TargetBuildInfo{Schema: to, Ref: ref}, versionTag.MatchString(branch))
	}
	if to == runningSchema {
		p.done(stepSchema, fmt.Sprintf("unchanged: both builds are at schema %d", to))
		return "", false, nil
	}
	if cfg.Prepare == nil {
		return "", false, fmt.Errorf("the build being upgraded to is at schema %d and this one at %d, and this "+
			"deployment has no way to carry its rows across -- upgrading would leave a store this build "+
			"cannot open; re-run scripts/setup.sh on the host instead", to, runningSchema)
	}
	if beforePrepare != nil {
		if err := beforePrepare(); err != nil {
			return "", false, err
		}
	}
	p.logf("%s: this build is at schema %d and the one being upgraded to is at %d -- writing this "+
		"deployment's rows out for it", stepSchema, runningSchema, to)
	dir, err := cfg.Prepare(ctx, branch, runningSchema, to)
	if err != nil {
		// No discardSchemaUpgrade here, unlike every failure below this
		// line. Discard removes the whole of what Prepare writes, and
		// this is the one point at which that is not this upgrade's to
		// remove: a Prepare that failed before it cleared the ground
		// leaves an *earlier* upgrade's note, which may still be the one
		// this deployment is waiting to restart onto, and nothing here
		// can tell that case from a half-written export of its own.
		return "", false, fmt.Errorf("writing this deployment's rows out to migrate them from schema %d to %d: %w",
			runningSchema, to, err)
	}
	if len(cfg.CheckArgs) > 0 {
		argv := append(append([]string{}, cfg.CheckArgs...), dir)
		if err := build.rehearse(ctx, p, dir, argv); err != nil {
			// Nothing has cut over: the ref file (or the installed
			// binary's health check, on the source path) is still what
			// it was, so this deployment goes on running the build that
			// wrote these rows -- which is the only build that can.
			u.discardSchemaUpgrade()
			return "", false, fmt.Errorf("the build being upgraded to is at schema %d, this deployment is at %d, "+
				"and that build could not take this deployment's rows -- so nothing has changed and it is "+
				"still running the build that can: %w", to, runningSchema, err)
		}
	} else {
		p.logf("%s: no migration check configured -- cutting over on the export alone", stepSchema)
	}
	if err := ctx.Err(); err != nil {
		u.discardSchemaUpgrade()
		return "", false, err
	}
	p.done(stepSchema, fmt.Sprintf("schema %d -> %d: this deployment's rows are in %s, and the new build "+
		"imports them at its first start", runningSchema, to, dir))
	return fmt.Sprintf(", migrating this deployment from schema %d to %d", runningSchema, to), true, nil
}

// schemaRunner is how this step reaches the build being upgraded to.
type schemaRunner interface {
	ask(ctx context.Context, p *progress, args []string) (string, error)
	rehearse(ctx context.Context, p *progress, dir string, args []string) error
	stamp(ctx context.Context, p *progress) BuildStamp
	targetRef() string
}

func parseBuildStampJSON(out string) BuildStamp {
	start := strings.Index(out, "{")
	if start < 0 {
		return BuildStamp{}
	}
	var doc struct {
		Release     string `json:"release"`
		Revision    string `json:"revision"`
		Unversioned bool   `json:"unversioned"`
	}
	if err := json.NewDecoder(strings.NewReader(out[start:])).Decode(&doc); err != nil {
		return BuildStamp{}
	}
	stamp := BuildStamp{Commit: strings.TrimSpace(doc.Revision)}
	if !doc.Unversioned {
		stamp.Release = strings.TrimSpace(doc.Release)
	}
	return stamp
}

// newBuildSchema asks the build being upgraded to which schema it knows.
func (u *Upgrader) newBuildSchema(ctx context.Context, p *progress, build schemaRunner) (int, error) {
	printed, err := build.ask(ctx, p, u.cfg.Schema.VersionArgs)
	if err != nil {
		return 0, fmt.Errorf("asking the build being upgraded to which schema it knows: %w", err)
	}
	text := strings.TrimSpace(printed)
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = strings.TrimSpace(text[:i])
	}
	version, err := strconv.Atoi(text)
	if err != nil || version <= 0 {
		return 0, fmt.Errorf("the build being upgraded to answered %q when asked which schema it knows, "+
			"which is not a schema version -- refusing to cut over to a build this upgrade cannot "+
			"reason about", strings.TrimSpace(printed))
	}
	p.logf("%s: the build being upgraded to knows schema %d", stepSchema, version)
	return version, nil
}
