// Command ghcr-prune deletes the container package versions this
// repository's CI has stopped needing, and nothing else.
//
// # What grows
//
// ../../.github/workflows/build-artifacts.yml publishes three packages,
// every one of them multi-gigabyte per version and every one of them
// monotonic until something removes a version:
//
//	grain        one version per commit per branch (sha-<short>), plus
//	             the moving <branch>, `latest` and v<x>.<y>.<z> names
//	guest        the sandbox image, the same three names -- and
//	             inputs-<hash>, one per distinct set of guest inputs,
//	             which is the cache that keeps most commits from booting
//	             a VM for nine minutes
//	guest-base   <hash>, one per vendored granule tree: the granule base
//	             image that guest cache is built on top of
//
// Nothing deleted any of it. This command is what the scheduled
// prune-packages.yml runs.
//
// # The rule, and the one thing it has to get right
//
// A version is deleted only when no tag on it is a name something may
// still resolve. That is not a stylistic preference: GHCR has no
// operation that deletes a *tag*. The only mutation is "delete this
// version", and it takes every tag pointing at that version with it --
// while build-artifacts.yml deliberately puts several names on one
// manifest. On a guest cache hit, `guest:inputs-<hash>` and
// `guest:sha-<commit>` are the same version, for as many commits as have
// hit that cache; that sharing is exactly what makes a hit cost a
// manifest push rather than 8GB of layers. Pruning the cache tag on its
// own age would therefore delete the sandbox image of every commit that
// hit it, and a deployment pinned to one of those (scripts/setup.sh's
// GRAIN_IMAGE_TAG=sha-abc1234, which is what a rollback looks like here)
// would find `grain sandbox-image` naming a tag that no longer exists.
// So BuildPlan decides liveness per tag and deletes per version, and
// plan.go's own comments carry the rest of that reasoning.
//
// What counts as live, by kind of tag:
//
//   - `latest` -- always. It is the one name a deployment resolves with
//     nothing configured (pkg/cli/sandboximage.go's stamped fallback),
//     and it names the last promoted release.
//   - v<x>.<y>.<z> -- always. A promoted release, retagged from its
//     sha- build when `latest` moves onto it (.grain/workflows/promote.yml):
//     what an operator pins to stay on, or roll back to, a release.
//   - a branch tag -- while that branch still exists on the remote. It
//     is what pkg/upgrade's TagForBranch pulls when an operator upgrades
//     onto a branch, and what setup.sh pulls for GRAIN_REF. Since
//     build-artifacts.yml re-points that name at each new build of the
//     branch, this is the rule that keeps the most recent build of every
//     live branch at any age -- the half of the policy that outlives the
//     window, and at a 48-hour window the half that matters most. Once
//     the branch is deleted nothing offers the name any more, and what
//     was its head build is held by the age window alone.
//   - sha-<short> -- for -keep-for, and the newest -keep-commits
//     whatever their age. This is the pin-and-rollback window: how far
//     back into a branch's *history* anyone can pin, as against its head
//     build, which the rule above keeps regardless. It is one window
//     across both packages by construction: `guest:sha-x` is live
//     exactly as long as `grain:sha-x` is, because they are the same tag
//     written in the same run and tagTimes reads the newer of the two.
//   - the tag of every image a deployment is running -- always. grain
//     hands a release job those images as GRAIN_DEPLOYED_IMAGES
//     (-keep-images), and the release workflow is what runs this. A
//     deployment's build is an ordinary sha- tag once its branch has
//     moved on, and its config repository's check pulls exactly that
//     tag, so pruning it after the window turned every check red
//     (grain:sha-adea62d; grain:sha-7781f61).
//   - inputs-<hash> and guest-base's <hash> -- the newest few, plus the
//     age window. Nothing outside CI resolves either; the cost of
//     deleting one too eagerly is one slower build.
//
// Untagged versions are never deleted, which is the opposite of what
// most prune actions default to. An untagged manifest here is either
// debris or a child of a tagged multi-platform index (or an attestation
// attached to one), and deleting one of those breaks the tagged image
// that references it while leaving the tag in place. The pile this
// command exists for is tagged, so there is nothing to gain by touching
// them.
//
// # Why a Go program rather than a marketplace action
//
// actions/delete-package-versions and its relatives all delete by count
// or by age *per version*, with an ignore list matched against the
// version name -- which for a container package is its digest. None of
// them can express "keep this version because one of its five tags is
// still a rollback target", which is the only rule that is safe here.
// The rule is also worth testing rather than reviewing: plan_test.go
// pins the cases that would cost a deployment its image.
//
// It is not in the Makefile's CMDS and is not built into the deployment
// image. Nothing on a host runs it -- `go run ./cmd/ghcr-prune` from the
// scheduled workflow is the whole of how it is invoked.
//
// # Dry run by default
//
// Deleting a package version cannot be undone, so -apply is required to
// delete anything. Without it this prints exactly the plan it would have
// executed, which is what a workflow_dispatch with dry_run left on
// gives you, and what the first run of any change to these numbers
// should be.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/packagepublish"
	"github.com/ai-on-gke/grain/pkg/github"
)

var (
	sandboxTokenPath = "/home/debian/.github-sandbox-token"
	publishTokenPath = "/home/debian/.package-publish-token"
	sandboxRepoPath  = "/home/debian/.github-sandbox-repo"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "::error title=ghcr-prune::%v\n", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("ghcr-prune", flag.ContinueOnError)
	owner := fs.String("owner", "", "account the packages belong to (default: from GITHUB_REPOSITORY or GRAIN_CI_REPO)")
	repo := fs.String("repo", "", "repository the packages are scoped to, and the first segment of their names (default: from GITHUB_REPOSITORY or GRAIN_CI_REPO)")
	packages := fs.String("packages", "grain,google-grain,guest,guest-base,k8s-sandbox,k8s-sandbox-base", "comma-separated package names, relative to the repository")
	tokenFlag := fs.String("token", "", "GitHub token (default: GITHUB_TOKEN, PACKAGE_PUBLISH_TOKEN, GHCR_TOKEN, or placed CI capability tokens)")
	// 48 hours -- and, on top of it, the most recent build of every
	// branch that still exists, which is what a branch tag amounts to
	// (BuildPlan's liveBranches rule) and which is the half of the
	// policy that outlives any window. Both halves were decided by a
	// human on 2026-09-08, and deliberately not derived here: what the
	// window answers is how far back anyone may ever want to roll a
	// deployment back, and no build rate can answer that.
	//
	// What the build rate does decide is what each candidate costs, and
	// at ~450 versions a day (measured -- prune-packages.yml's schedule
	// comment has the counts) that had made the previous 90 days
	// unaffordable rather than merely generous. A run costs one request
	// per hundred versions listed plus one per deletion, against
	// GITHUB_TOKEN's 1,000 an hour for the whole repository: 90 days
	// settles at ~40,000 versions, which is ~400 requests of listing
	// before a single deletion and ~850 all told. 48 hours settles at
	// ~900 versions -- ~10 of listing, ~450 of deletion -- which fits
	// with room to spare, and retires GHCR_PRUNE_TOKEN (a PAT whose
	// 5,000 an hour is its own budget) as something anyone has to set.
	//
	// The rollback story this leaves is per-branch rather than per-day,
	// which is the point of the second half. The head build of main and
	// of every live branch stays pullable for as long as the branch
	// exists, so GRAIN_IMAGE_TAG=sha-<short> of what a branch currently
	// is never stops resolving; it is only a commit already superseded
	// on its own branch that has 48 hours to be pinned in. If that turns
	// out to be too short, this is the number to raise, and the request
	// budget above is what it is spent against.
	keepFor := fs.Duration("keep-for", 48*time.Hour, "never delete anything written inside this window")
	// The floor under a quiet stretch: a repository with no pushes for
	// four months still has thirty builds to roll back to. At the rate
	// this one builds, thirty commits is under two hours and this number
	// decides nothing -- the window and the live-branch heads are what
	// hold everything. It is kept for the repository that has gone
	// quiet, where they do not.
	keepCommits := fs.Int("keep-commits", 30, "keep this many of the newest sha- builds whatever their age")
	// Ten and five: one entry is all a cache hit needs, and the extras
	// are what make a branch that reverts a guest-input change (or a
	// resync that gets backed out) hit rather than boot a VM.
	//
	// They were five and three under a 90-day window, where the window
	// was doing the work and the counts were a formality. At 48 hours
	// the counts are the whole of the rule: a cache tag is written once,
	// when the cache is built, and a *hit* does not touch it, so main's
	// own guest cache is two days old within two days of the last change
	// to scripts/granule and is then held by nothing but its rank. Losing
	// it costs a nine-minute VM boot on the next commit and re-fills
	// itself, so this is a slow build rather than a broken one -- but
	// five distinct guest-input changes in flight is an ordinary week
	// here, and ten is a handful of images against that.
	keepInputs := fs.Int("keep-inputs", 10, "keep this many of the newest guest inputs- caches")
	keepBases := fs.Int("keep-bases", 5, "keep this many of the newest guest-base caches")
	// One busy day's builds, with room to catch up on top of it. The cap
	// is a bound on how much a bug in the rule can cost, but a cap below
	// what CI creates between two runs is not a bound on anything -- it
	// is a backlog that grows for ever, and the registry goes on growing
	// at very nearly the rate it did before this ran at all. On the
	// daily schedule prune-packages.yml keeps, one run's steady-state
	// work is one day's builds: 135 to 433 of them over the first week
	// the workflow existed, ~450 to size against, and rising -- 2026-09-08
	// was at a ~640/day pace by 03:00 UTC.
	//
	// 700 covers that pace, and it is about as large as this can sensibly
	// be: one deletion is one request, so a cap of 700 is ~715 requests
	// of the 1,000 an hour GITHUB_TOKEN gets for the whole repository,
	// which the ~19 builds an hour that create these versions are drawing
	// on at the same time. If CI's rate ever outgrows it, the answer is a
	// second run in a *different hour* -- each hour has its own budget --
	// rather than a cap that goes on climbing towards 1,000 inside one.
	//
	// There is a backlog to work off the moment this window lands, which
	// the previous 90-day one had deferred to 2026-11-30: at 48 hours
	// every version published before the day before yesterday is
	// deletable at once, ~1,000 of them today. So the first applying runs
	// delete their 700, report the rest as Deferred, and converge inside
	// a few days; Deferred still non-zero after that is what the warning
	// in prune() is for.
	maxDeletes := fs.Int("max-deletes", 700, "delete at most this many versions in one run")
	// Defaulted from what grain tells a release job, so the release
	// workflow's prune step needs no argument for it. Additive rather
	// than a replacement in effect: an empty list keeps nothing extra.
	keepImages := fs.String("keep-images", os.Getenv("GRAIN_DEPLOYED_IMAGES"), "comma-separated image references whose tags are kept whatever their age (default: GRAIN_DEPLOYED_IMAGES)")
	apply := fs.Bool("apply", false, "actually delete; without it this prints the plan and changes nothing")
	fs.SetOutput(out)
	if err := fs.Parse(args); err != nil {
		return err
	}

	// A negative window puts the cutoff in the future, which turns the
	// age protection off altogether; a negative cap is no cap anyone
	// meant. Neither is a typo this should act on.
	if *keepFor < 0 {
		return fmt.Errorf("-keep-for must not be negative (got %s)", *keepFor)
	}
	if *maxDeletes < 0 {
		return fmt.Errorf("-max-deletes must not be negative (got %d)", *maxDeletes)
	}

	resolvedOwner, resolvedRepo, err := resolveRepo(*owner, *repo)
	if err != nil {
		return err
	}
	token := resolveToken(*tokenFlag)
	if token == "" {
		return fmt.Errorf("GITHUB_TOKEN is empty: listing package versions needs read:packages and deleting them needs packages: write")
	}

	api := &API{
		Transport: github.NewRealTransport("github.com"),
		Token:     token,
		Owner:     resolvedOwner,
		Repo:      resolvedRepo,
	}
	policy := Policy{
		KeepFor:     *keepFor,
		KeepCommits: *keepCommits,
		KeepInputs:  *keepInputs,
		KeepBases:   *keepBases,
		MaxDeletes:  *maxDeletes,
		Deployed:    deployedTags(*keepImages),
	}
	return prune(api, strings.Split(*packages, ","), policy, *apply, time.Now().UTC(), out)
}

func resolveRepo(ownerFlag, repoFlag string) (string, string, error) {
	owner := ownerFlag
	repo := repoFlag
	if owner == "" || repo == "" {
		repoEnv := os.Getenv("GITHUB_REPOSITORY")
		if repoEnv == "" {
			repoEnv = os.Getenv("GRAIN_CI_REPO")
		}
		if repoEnv == "" {
			if b, err := os.ReadFile(sandboxRepoPath); err == nil {
				repoEnv = strings.TrimSpace(string(b))
			}
		}
		o, r, ok := strings.Cut(repoEnv, "/")
		if !ok || o == "" || r == "" {
			return "", "", fmt.Errorf("-owner and -repo are required when GITHUB_REPOSITORY and GRAIN_CI_REPO are not set")
		}
		if owner == "" {
			owner = o
		}
		if repo == "" {
			repo = r
		}
	}
	return owner, repo, nil
}

func resolveToken(tokenFlag string) string {
	if tokenFlag != "" {
		return tokenFlag
	}
	for _, env := range []string{"GITHUB_TOKEN", "PACKAGE_PUBLISH_TOKEN", "GHCR_TOKEN", "REGISTRY_PUSH_TOKEN"} {
		if val := strings.TrimSpace(os.Getenv(env)); val != "" {
			return val
		}
	}
	for _, path := range []string{sandboxTokenPath, publishTokenPath, packagepublish.TokenPath, "/home/debian/.registry-push-token"} {
		if data, err := os.ReadFile(path); err == nil {
			if val := strings.TrimSpace(string(data)); val != "" {
				return val
			}
		}
	}
	return ""
}

// deleter is what prune needs of the API, so a test can drive the whole
// command -- the reporting and the apply/dry-run split included --
// against a fake.
type deleter interface {
	ListVersions(pkg string) ([]Version, error)
	ListBranchTags() (map[string]bool, error)
	DeleteVersion(pkg string, id int64) error
}

func prune(api deleter, packages []string, policy Policy, apply bool, now time.Time, out io.Writer) error {
	branches, err := api.ListBranchTags()
	if err != nil {
		return err
	}
	// A repository always has at least one branch, so an empty listing
	// is an API or credential problem rather than a fact -- and it is
	// the one wrong answer that would make every branch tag in the
	// registry look dead at once. Refusing here costs a run; believing
	// it would cost every branch image older than the window.
	if len(branches) == 0 {
		return fmt.Errorf("the branch listing came back empty, which no repository is: refusing to prune, since every branch tag would look dead")
	}
	var versions []Version
	for _, pkg := range packages {
		pkg = strings.TrimSpace(pkg)
		if pkg == "" {
			continue
		}
		got, err := api.ListVersions(pkg)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: %d versions\n", pkg, len(got))
		versions = append(versions, got...)
	}

	plan := BuildPlan(versions, branches, policy, now)
	report(plan, apply, out)

	// A cap that defers is a cap that has been outgrown, and this is the
	// only line saying so where anyone will see it. Deferred falls to
	// zero over a few runs when it was a backlog -- which the first runs
	// under the 48-hour window are, since it opened over months of builds
	// at once -- and a number that is *not* falling run over run is CI
	// creating versions faster than -max-deletes removes them, which no
	// later run clears and which nothing else reports. report() prints
	// it, but a few hundred lines into a log nobody opens until they are
	// already wondering; that is how this workflow was sized for a build
	// rate a tenth of the real one and would have gone on being. An
	// annotation is what turns the next one into something somebody
	// notices.
	if plan.Deferred > 0 {
		fmt.Fprintf(out, "\n::warning title=ghcr-prune::%d deletable versions were held back by -max-deletes (%d). That should fall run over run while a backlog is worked off; a number that holds steady or climbs is the cap sitting below what CI publishes between two runs, and the registry is still growing.\n",
			plan.Deferred, policy.MaxDeletes)
	}

	if !apply {
		fmt.Fprintf(out, "\n::notice title=ghcr-prune::dry run: %d versions would be deleted, %d kept\n", len(plan.Delete), len(plan.Keep))
		return nil
	}

	deleted, refused, gone := 0, 0, 0
	for _, d := range plan.Delete {
		if err := api.DeleteVersion(d.Version.Package, d.Version.ID); err != nil {
			var already *ErrAlreadyGone
			if errors.As(err, &already) {
				// A retry after a lost response: the deletion this run
				// asked for has happened, so it is counted and the run
				// goes on rather than stopping partway through its plan.
				gone++
				fmt.Fprintf(out, "already gone %s %s (%s)\n", d.Version.Package, d.Version.Digest, strings.Join(d.Version.Tags, ", "))
				continue
			}
			var refusal *ErrRefused
			if errors.As(err, &refusal) {
				// Permanent, and not about this credential: a public
				// version with more than 5,000 downloads is one GitHub
				// will not delete for anybody. Stepping over it keeps
				// the schedule green for a condition no later run can
				// clear, and the annotation is how anyone finds out.
				refused++
				fmt.Fprintf(out, "::warning title=ghcr-prune::%s %s (%s): %v\n", d.Version.Package, d.Version.Digest, strings.Join(d.Version.Tags, ", "), err)
				continue
			}
			return fmt.Errorf("deleting %s %s (%s): %w", d.Version.Package, d.Version.Digest, strings.Join(d.Version.Tags, ", "), err)
		}
		deleted++
		fmt.Fprintf(out, "deleted %s %s (%s)\n", d.Version.Package, d.Version.Digest, strings.Join(d.Version.Tags, ", "))
	}
	fmt.Fprintf(out, "\n::notice title=ghcr-prune::deleted %d versions, already gone %d, refused %d, kept %d, deferred %d to the next run\n",
		deleted, gone, refused, len(plan.Keep), plan.Deferred)
	return nil
}

// report prints the whole plan, kept versions included.
//
// All of it, not just the deletions: a scheduled job that says only what
// it removed leaves nobody able to answer "why is that still there" or
// "what is holding this cache alive" without going to the registry and
// working it out by hand. It is a few hundred lines a week in a log
// nobody reads until they need it, which is the right trade for the one
// week somebody needs it.
func report(plan Plan, apply bool, out io.Writer) {
	verb := "would delete"
	if apply {
		verb = "deleting"
	}
	fmt.Fprintf(out, "\nkeeping %d versions:\n", len(plan.Keep))
	for _, d := range sorted(plan.Keep) {
		fmt.Fprintf(out, "  keep   %-12s %-20s %s -- %s\n", d.Version.Package, tagList(d.Version), d.Version.Created.Format(time.DateOnly), d.Reason)
	}
	fmt.Fprintf(out, "\n%s %d versions:\n", verb, len(plan.Delete))
	for _, d := range sorted(plan.Delete) {
		fmt.Fprintf(out, "  delete %-12s %-20s %s -- %s\n", d.Version.Package, tagList(d.Version), d.Version.Created.Format(time.DateOnly), d.Reason)
	}
	if plan.Deferred > 0 {
		fmt.Fprintf(out, "\n%d more are deletable and held back by the per-run cap; the next run takes them.\n", plan.Deferred)
	}
}

func sorted(in []Decision) []Decision {
	out := append([]Decision(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Version.Package != out[j].Version.Package {
			return out[i].Version.Package < out[j].Version.Package
		}
		return out[i].Version.Created.Before(out[j].Version.Created)
	})
	return out
}

func tagList(v Version) string {
	if len(v.Tags) == 0 {
		return "<untagged>"
	}
	return strings.Join(v.Tags, ",")
}
