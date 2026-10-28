package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Version is one package version as GHCR's packages API reports it: a
// manifest, its id, and the tags currently pointing at it.
//
// Tags are a property of the *version*, which is the fact this whole
// file is organised around -- see BuildPlan. The API has no endpoint
// that deletes a tag; the only mutation is "delete this version", which
// takes every tag on it down at once.
type Version struct {
	// Package is the package this version belongs to -- "grain",
	// "guest", "guest-base" -- carried on the version itself because
	// the plan reasons across all three at once (a `guest` tag's age is
	// often only knowable from the `grain` package; see tagTimes).
	Package string
	ID      int64
	// Digest is the version's "name" in the API: "sha256:...". Printed,
	// and read for one decision: a signature version is kept while the
	// version whose Digest it signs is (KindSignature).
	Digest  string
	Tags    []string
	Created time.Time
}

// Policy is the retention rule, in the five numbers that express it.
// Defaults live on the flags in main.go, and the reasoning for each is
// in that file's header.
type Policy struct {
	// KeepFor is the age floor. Nothing written inside this window is
	// deleted, whatever any other number says -- neither a version
	// pushed inside it nor a version whose only tags were written
	// inside it.
	//
	// A duration rather than a count of days, because the window this
	// repository settled on is 48 hours (main.go's flag has the
	// reasoning) and a day is too coarse a unit either to say that in
	// or to tune it by.
	KeepFor time.Duration
	// KeepCommits is how many of the newest sha-<short> tags survive
	// regardless of age: the floor under a quiet month, so a repository
	// nobody has pushed to in a year still has rollback targets.
	KeepCommits int
	// KeepInputs is how many of the newest guest inputs-<hash> cache
	// tags survive regardless of age, and KeepBases the same for
	// guest-base's own hash tags. One is enough for the cache to hit;
	// more than one is what makes a branch that reverts a guest-input
	// change hit rather than rebuild.
	KeepInputs int
	KeepBases  int
	// MaxDeletes caps one run. A rule with a bug in it should cost a
	// bounded number of versions and be visible in the next run's plan,
	// not empty the registry between two nightly reports.
	MaxDeletes int
	// Deployed holds the tags of the images a deployment is running --
	// deployedTags' reading of GRAIN_DEPLOYED_IMAGES, which grain hands
	// a release job -- and every one of them is live whatever its kind
	// or age.
	//
	// A deployment runs the sha- build it upgraded to, and nothing in
	// the registry says so: once its branch tag has moved on, that build
	// is one more superseded commit, held by the window alone. Deleting
	// it does not stop the deployment, but its config repository's check
	// runs that exact tag (pkg/daemon's ConfigRepoCISource), so every
	// change to the configuration goes red the moment the window closes
	// (grain:sha-adea62d, grain/task-3926; grain:sha-7781f61, task 4544).
	Deployed map[string]bool
}

// TagKind is what a tag on one of this repository's packages is for.
// Everything in the retention rule is decided per kind, so classify is
// the single place that reads a tag's spelling.
type TagKind int

const (
	// KindShared is `latest`: the one name every deployment resolves
	// without being told anything (pkg/cli/sandboximage.go's fallback),
	// which names the last promoted release. Never deleted.
	KindShared TagKind = iota
	// KindBranch is a branch tag -- the branch name with '/' replaced by
	// "..", which is what pkg/upgrade's TagForBranch resolves and what
	// scripts/setup.sh pulls for GRAIN_REF. Live while the branch is.
	KindBranch
	// KindCommit is sha-<short>: the immutable per-commit tag, the thing
	// an operator pins to (GRAIN_IMAGE_TAG=sha-abc1234) and rolls back
	// to. Kept by age and by count.
	KindCommit
	// KindInputs is inputs-<hash> on the guest package: the
	// content-addressed guest cache build-artifacts.yml looks up before
	// it boots a VM. Nothing outside CI resolves one.
	KindInputs
	// KindBase is guest-base's <hash>: the granule base image cache, the
	// same idea one layer down. Nothing outside CI resolves one either.
	KindBase
	// KindRelease is v<major>.<minor>.<patch>: a promoted release, retagged
	// from its sha-<short> build by scripts/promote-release-images.sh at the
	// same time `latest` is moved onto it. There is no branch by that
	// name, so as KindBranch it would live for the age window alone, and a
	// deployment pinned to a release (GRAIN_IMAGE_TAG=v1.5.0) would lose
	// it two days later. Never deleted, like `latest`.
	KindRelease
	// KindSignature is sha256-<hex>.sig: the cosign signature of the
	// version whose digest is <hex>, which scripts/release-images.sh
	// pushes beside every image it publishes (docs/image-signing.md). It
	// is live exactly as long as the version it signs is kept -- see
	// BuildPlan -- since a deployment verifying signatures refuses a
	// kept image whose signature was pruned out from under it.
	KindSignature
)

// BasePackage is the package whose every tag is a cache key. It is named
// here because that is the one place the classification differs per
// package: a bare hex tag means "granule base built from this vendored
// tree" there and would be an unrecognised tag anywhere else.
const BasePackage = "guest-base"

var (
	commitTag = regexp.MustCompile(`^sha-[0-9a-f]{7,40}$`)
	inputsTag = regexp.MustCompile(`^inputs-[0-9a-f]{8,64}$`)
	hashTag   = regexp.MustCompile(`^[0-9a-f]{8,64}$`)
	sigTag    = regexp.MustCompile(`^sha256-([0-9a-f]{64})\.sig$`)
)

// releaseTag is v<major>.<minor>.<patch> exactly: an rc (v1.6.0-rc.1) is
// not a release, and nothing retags one.
var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// classify says what a tag is for, given the package it is on.
//
// Anything unrecognised comes back KindBranch, which is the kind nothing
// deletes while its name is live -- and, since a tag nobody can account
// for is not in the branch list either, it is then held by the age floor
// alone. That is the deliberate direction to be wrong in: a hand-made
// tag (someone pinning a build they care about, a mirror's own name)
// outlives a rule that has never heard of it, and the cost of the
// mistake is disk rather than an image somebody was relying on.
func classify(pkg, tag string) TagKind {
	if pkg == BasePackage || strings.HasSuffix(pkg, "/"+BasePackage) {
		if hashTag.MatchString(tag) {
			return KindBase
		}
		return KindBranch
	}
	switch {
	case sigTag.MatchString(tag):
		return KindSignature
	case tag == "latest":
		return KindShared
	case releaseTag.MatchString(tag):
		return KindRelease
	case commitTag.MatchString(tag):
		return KindCommit
	case inputsTag.MatchString(tag):
		return KindInputs
	default:
		return KindBranch
	}
}

// Decision is what the plan does with one version, and why. The reason
// is written for the run's log rather than parsed by anything: a person
// reading a scheduled job's summary should be able to see which tag
// saved a version without going to the registry.
type Decision struct {
	Version Version
	Keep    bool
	Reason  string
}

// Plan is one run's decisions, split into what it will delete (oldest
// first, already capped at Policy.MaxDeletes) and what it will not.
type Plan struct {
	Delete []Decision
	Keep   []Decision
	// Deferred is how many deletable versions the cap held back. They
	// are deleted by later runs; a number that never falls is a cap set
	// too low rather than a failure.
	Deferred int
}

// tagTimes maps each tag to the newest creation time of any version
// carrying it, across every package.
//
// This is the awkward half of the whole rule, and it is worth being
// explicit about why a version's own created_at will not do.
//
// A guest cache hit adds this commit's `sha-<short>` and branch tags to
// a manifest built weeks or months ago, and adding a tag does not move
// the manifest's created_at. So the guest package holds versions that
// are genuinely old and carry tags written this morning: a rule reading
// the version's age would delete today's `guest:sha-abc1234` -- the
// sandbox image today's `grain:sha-abc1234` has stamped into it -- as
// six months stale.
//
// The grain package has no such sharing: it publishes a fresh manifest
// per commit per branch, so the version carrying `grain:sha-abc1234` was
// created when that commit was built. Taking the newest version carrying
// a tag therefore gets the real answer for every tag that exists in both
// packages (every sha- and branch tag), and the only available answer
// for the cache tags, which live in one package and are written with the
// version they name.
func tagTimes(versions []Version) map[string]time.Time {
	times := map[string]time.Time{}
	for _, v := range versions {
		for _, tag := range v.Tags {
			if t, ok := times[tag]; !ok || v.Created.After(t) {
				times[tag] = v.Created
			}
		}
	}
	return times
}

// short prints a duration the way this command's log wants to read it:
// hours while that is still a number a person can hold, days after
// that. The window is 48 hours, so a report that counted whole days
// would answer "0d" for very nearly everything it decides, and "is this
// build an hour outside the window or a day inside it" is exactly the
// question somebody reads this log to answer.
func short(d time.Duration) string {
	h := int(d.Round(time.Hour).Hours())
	if h > -72 && h < 72 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dd", h/24)
}

// newestByRank returns the `keep` newest tags of kind, by the times
// tagTimes worked out -- the count-based half of the rule for the three
// kinds that have one (commit, inputs, base).
func newestByRank(versions []Version, times map[string]time.Time, kind TagKind, keep int) map[string]bool {
	seen := map[string]bool{}
	var tags []string
	for _, v := range versions {
		for _, tag := range v.Tags {
			if classify(v.Package, tag) != kind || seen[tag] {
				continue
			}
			seen[tag] = true
			tags = append(tags, tag)
		}
	}
	sort.Slice(tags, func(i, j int) bool {
		if !times[tags[i]].Equal(times[tags[j]]) {
			return times[tags[i]].After(times[tags[j]])
		}
		// Deterministic on a tie: two tags written in the same second
		// must not rank differently between two runs of the same plan.
		return tags[i] < tags[j]
	})
	if keep > len(tags) {
		keep = len(tags)
	}
	if keep < 0 {
		keep = 0
	}
	kept := map[string]bool{}
	for _, tag := range tags[:keep] {
		kept[tag] = true
	}
	return kept
}

// BuildPlan decides which versions this run deletes.
//
// The shape of it, and the one rule everything else hangs off: a version
// is deleted only when *no* tag on it is live. The registry has no
// "delete this tag" operation -- deleting a version takes every tag
// pointing at it -- and this repository deliberately puts several names
// on one manifest. `guest:inputs-<hash>` is the same version as
// `guest:sha-<commit>` for every commit that hit that cache, which is
// what makes a cache hit cost a manifest push instead of 8GB of layers;
// pruning the cache tag by its own age would take the sandbox image of
// every one of those commits with it, and a deployment pinned to one of
// them (GRAIN_IMAGE_TAG=sha-abc1234) would find its rollback no longer
// pullable. So each kind's rule below decides whether a *tag* is live,
// and a version survives if any of its tags is.
//
// liveBranches holds the branch tags -- '/' already replaced by "..", the
// spelling the registry uses -- of branches that still exist on the
// remote. A branch tag whose branch is gone is dead: nothing resolves it
// (pkg/upgrade only ever offers a branch that exists, scripts/setup.sh
// pulls the tag for the ref it was given), and it is held only by the
// age floor, which is what covers a host still running the image it
// upgraded to from a branch someone has since deleted.
//
// That rule is also the half of the retention policy that outlives the
// window, and since the window is now 48 hours it is worth saying what
// it amounts to: the most recent build of every branch that still
// exists is kept for as long as the branch does, at any age. A branch
// tag is a moving name that build-artifacts.yml re-points at each new
// build of that branch (its Push step tags the same manifest as
// `sha-<short>` and as the branch), so the version carrying it is the
// branch's head build and the versions it moved off are the branch's
// history. The head is held here; the history is held by the window
// and by nothing else.
func BuildPlan(versions []Version, liveBranches map[string]bool, p Policy, now time.Time) Plan {
	times := tagTimes(versions)
	cutoff := now.Add(-p.KeepFor)
	rankedCommits := newestByRank(versions, times, KindCommit, p.KeepCommits)
	rankedInputs := newestByRank(versions, times, KindInputs, p.KeepInputs)
	rankedBases := newestByRank(versions, times, KindBase, p.KeepBases)

	// live reports whether one tag is a name something may still
	// resolve, and says why in words meant for the run's log.
	live := func(pkg, tag string) (bool, string) {
		age := short(now.Sub(times[tag]))
		if p.Deployed[tag] {
			return true, fmt.Sprintf("%q is the build a deployment is running", tag)
		}
		switch classify(pkg, tag) {
		case KindShared:
			return true, fmt.Sprintf("%q is the name every deployment resolves", tag)
		case KindRelease:
			return true, fmt.Sprintf("%q is a promoted release", tag)
		case KindBranch:
			if liveBranches[tag] {
				return true, fmt.Sprintf("%q is a branch that still exists, and this is the build it names", tag)
			}
		case KindCommit:
			if rankedCommits[tag] {
				return true, fmt.Sprintf("%q is one of the %d newest builds", tag, p.KeepCommits)
			}
		case KindInputs:
			if rankedInputs[tag] {
				return true, fmt.Sprintf("%q is one of the %d newest guest caches", tag, p.KeepInputs)
			}
		case KindBase:
			if rankedBases[tag] {
				return true, fmt.Sprintf("%q is one of the %d newest granule base caches", tag, p.KeepBases)
			}
		}
		if times[tag].After(cutoff) {
			return true, fmt.Sprintf("%q was written %s ago, inside the %s window", tag, age, short(p.KeepFor))
		}
		return false, fmt.Sprintf("%s (%s)", tag, age)
	}

	// Oldest first, so the cap below spends this run's deletions on the
	// versions that have been deletable longest rather than on whatever
	// order the API happened to answer in.
	ordered := append([]Version(nil), versions...)
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].Created.Equal(ordered[j].Created) {
			return ordered[i].Created.Before(ordered[j].Created)
		}
		return ordered[i].ID < ordered[j].ID
	})

	// Signatures are decided after everything they could sign, since
	// whether one is live is whether its image is kept (signedBy).
	var signatures []Version
	var plan Plan
	for _, v := range ordered {
		if signedDigest(v) != "" {
			signatures = append(signatures, v)
			continue
		}
		// An untagged version is left alone, always. It is either debris
		// this rule did not create or -- the case that matters -- a
		// child manifest of a tagged multi-platform index or an
		// attestation attached to one, and deleting those breaks the
		// tagged image that references them while leaving its tag in
		// place, which is a worse failure than any amount of disk.
		if len(v.Tags) == 0 {
			plan.Keep = append(plan.Keep, Decision{Version: v, Keep: true, Reason: "untagged: left alone (it may be part of a tagged image)"})
			continue
		}
		// The age floor applies to the version as well as to its tags.
		// Nothing pushed inside the window is deleted inside it,
		// whatever the tags on it are called -- the belt to the braces
		// above, and what keeps a bug in classify from reaching a build
		// somebody is mid-way through publishing.
		if v.Created.After(cutoff) {
			plan.Keep = append(plan.Keep, Decision{Version: v, Keep: true,
				Reason: fmt.Sprintf("pushed %s ago, inside the %s window", short(now.Sub(v.Created)), short(p.KeepFor))})
			continue
		}
		var dead []string
		kept := ""
		for _, tag := range v.Tags {
			ok, why := live(v.Package, tag)
			if ok {
				kept = why
				break
			}
			dead = append(dead, why)
		}
		if kept != "" {
			plan.Keep = append(plan.Keep, Decision{Version: v, Keep: true, Reason: kept})
			continue
		}
		if len(plan.Delete) >= p.MaxDeletes {
			plan.Deferred++
			plan.Keep = append(plan.Keep, Decision{Version: v, Keep: true,
				Reason: fmt.Sprintf("deletable (%s) but over this run's cap of %d", strings.Join(dead, ", "), p.MaxDeletes)})
			continue
		}
		plan.Delete = append(plan.Delete, Decision{Version: v,
			Reason: "no live tag: " + strings.Join(dead, ", ")})
	}

	kept := map[string]bool{}
	for _, d := range plan.Keep {
		kept[d.Version.Package+"@"+d.Version.Digest] = true
	}
	for _, v := range signatures {
		target := signedDigest(v)
		switch {
		case kept[v.Package+"@"+target]:
			plan.Keep = append(plan.Keep, Decision{Version: v, Keep: true,
				Reason: fmt.Sprintf("signs %s, which this run keeps", target)})
		case v.Created.After(cutoff):
			plan.Keep = append(plan.Keep, Decision{Version: v, Keep: true,
				Reason: fmt.Sprintf("pushed %s ago, inside the %s window", short(now.Sub(v.Created)), short(p.KeepFor))})
		case len(plan.Delete) >= p.MaxDeletes:
			plan.Deferred++
			plan.Keep = append(plan.Keep, Decision{Version: v, Keep: true,
				Reason: fmt.Sprintf("deletable (signs %s, which is not kept) but over this run's cap of %d", target, p.MaxDeletes)})
		default:
			plan.Delete = append(plan.Delete, Decision{Version: v,
				Reason: fmt.Sprintf("signs %s, which is not kept", target)})
		}
	}
	return plan
}

// signedDigest is the digest v is the cosign signature of -- "sha256:..."
// -- when every tag on it is a signature tag, and "" otherwise. A version
// carrying any other tag as well is decided by that tag like any other,
// which can only keep it longer.
func signedDigest(v Version) string {
	digest := ""
	for _, tag := range v.Tags {
		m := sigTag.FindStringSubmatch(tag)
		if m == nil {
			return ""
		}
		digest = "sha256:" + m[1]
	}
	return digest
}

// deployedTags reads a comma-separated list of image references --
// GRAIN_DEPLOYED_IMAGES, or -keep-images -- into the tags they name, for
// Policy.Deployed. A digest suffix is dropped and a reference with no
// tag names nothing to keep, since this rule keeps versions by their
// tags. The registry and repository are not compared with the packages
// being pruned: a deployment running somebody else's image can only
// keep a tag here that happens to share its name, which costs disk.
func deployedTags(list string) map[string]bool {
	tags := map[string]bool{}
	for _, ref := range strings.Split(list, ",") {
		ref = strings.TrimSpace(ref)
		if at := strings.Index(ref, "@"); at >= 0 {
			ref = ref[:at]
		}
		name := ref[strings.LastIndex(ref, "/")+1:]
		if colon := strings.LastIndex(name, ":"); colon >= 0 && colon < len(name)-1 {
			tags[name[colon+1:]] = true
		}
	}
	return tags
}
