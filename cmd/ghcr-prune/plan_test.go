package main

import (
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func daysAgo(d int) time.Time  { return now.AddDate(0, 0, -d) }
func hoursAgo(h int) time.Time { return now.Add(-time.Duration(h) * time.Hour) }

// policy is the shape of the rule rather than the deployed numbers: a
// long window, so that the tests about *kinds* of tag are not really
// tests about the clock. The cases that are about the deployed window
// say 48 hours themselves.
func policy() Policy {
	return Policy{KeepFor: 90 * 24 * time.Hour, KeepCommits: 3, KeepInputs: 2, KeepBases: 2, MaxDeletes: 100}
}

// decide runs one plan and reports what happened to the version with id.
func decide(t *testing.T, versions []Version, branches map[string]bool, p Policy, id int64) Decision {
	t.Helper()
	plan := BuildPlan(versions, branches, p, now)
	for _, d := range append(append([]Decision(nil), plan.Keep...), plan.Delete...) {
		if d.Version.ID == id {
			return d
		}
	}
	t.Fatalf("version %d is in neither half of the plan", id)
	return Decision{}
}

// The failure this whole command is written around: `guest:inputs-<hash>`
// and `guest:sha-<commit>` are one version whenever a commit hit the
// guest cache, so a rule that prunes the cache tag on its own age
// deletes the sandbox image of every commit that hit it -- and a
// deployment pinned to one of those (GRAIN_IMAGE_TAG=sha-abc1234) can no
// longer pull what `grain sandbox-image` names.
func TestACacheTagNeverTakesAPinnedRollbackWithIt(t *testing.T) {
	versions := []Version{
		// The guest built six months ago, still being tagged today
		// because every commit since has hit its cache.
		{Package: "guest", ID: 1, Tags: []string{"inputs-aaaaaaaaaaaaaaaa", "sha-01d1111", "sha-abc1234"}, Created: daysAgo(180)},
		// Newer caches, enough of them to push the one above out of the
		// newest -keep-inputs.
		{Package: "guest", ID: 2, Tags: []string{"inputs-bbbbbbbbbbbbbbbb"}, Created: daysAgo(20)},
		{Package: "guest", ID: 3, Tags: []string{"inputs-cccccccccccccccc"}, Created: daysAgo(10)},
		// The grain image of the commit still pinned: fresh, and the
		// only evidence anywhere of when sha-abc1234 was written.
		{Package: "grain", ID: 4, Tags: []string{"sha-abc1234"}, Created: daysAgo(5)},
		// Newer builds still, so nothing here is held by the
		// count-based half of the commit rule.
		{Package: "grain", ID: 5, Tags: []string{"sha-1111111"}, Created: daysAgo(3)},
		{Package: "grain", ID: 6, Tags: []string{"sha-2222222"}, Created: daysAgo(2)},
		{Package: "grain", ID: 7, Tags: []string{"sha-3333333"}, Created: daysAgo(1)},
	}
	d := decide(t, versions, map[string]bool{}, policy(), 1)
	if !d.Keep {
		t.Fatalf("the guest cache version carrying a live sha- tag was planned for deletion: %s", d.Reason)
	}
	if !strings.Contains(d.Reason, "sha-abc1234") {
		t.Errorf("kept for %q, want the reason to name the pinned build it is holding", d.Reason)
	}
}

// The same fact stated the other way: a sha- tag is live in both
// packages or in neither, however old the manifest carrying it is.
// tagTimes reads the newest version carrying a tag, which is what makes
// the guest's copy inherit the grain package's honest timestamp.
func TestAGuestShaTagIsAsLiveAsTheGrainOneItGoesWith(t *testing.T) {
	versions := []Version{
		{Package: "guest", ID: 1, Tags: []string{"sha-abc1234"}, Created: daysAgo(300)},
		{Package: "grain", ID: 2, Tags: []string{"sha-abc1234"}, Created: daysAgo(2)},
		// Newer builds, so the count-based half of the rule is not what
		// is keeping either of them.
		{Package: "grain", ID: 3, Tags: []string{"sha-1111111"}, Created: daysAgo(1)},
		{Package: "grain", ID: 4, Tags: []string{"sha-2222222"}, Created: daysAgo(1)},
		{Package: "grain", ID: 5, Tags: []string{"sha-3333333"}, Created: daysAgo(1)},
	}
	if d := decide(t, versions, map[string]bool{}, policy(), 1); !d.Keep {
		t.Errorf("guest:sha-abc1234 deleted while grain:sha-abc1234 is two days old: %s", d.Reason)
	}
}

func TestAnOldBuildOfADeletedBranchIsDeleted(t *testing.T) {
	versions := []Version{
		{Package: "grain", ID: 1, Tags: []string{"sha-dead111", "grain-task-9"}, Created: daysAgo(200)},
		{Package: "grain", ID: 2, Tags: []string{"sha-1111111"}, Created: daysAgo(1)},
		{Package: "grain", ID: 3, Tags: []string{"sha-2222222"}, Created: daysAgo(1)},
		{Package: "grain", ID: 4, Tags: []string{"sha-3333333"}, Created: daysAgo(1)},
	}
	d := decide(t, versions, map[string]bool{"main": true}, policy(), 1)
	if d.Keep {
		t.Fatalf("a 200-day-old build of a branch that no longer exists was kept: %s", d.Reason)
	}
	if !strings.Contains(d.Reason, "sha-dead111") || !strings.Contains(d.Reason, "grain-task-9") {
		t.Errorf("deletion reason %q should account for every tag it is taking", d.Reason)
	}
}

func TestABranchThatStillExistsHoldsItsImage(t *testing.T) {
	versions := []Version{
		{Package: "grain", ID: 1, Tags: []string{"sha-dead111", "grain-task-9"}, Created: daysAgo(200)},
		{Package: "grain", ID: 2, Tags: []string{"sha-1111111"}, Created: daysAgo(1)},
		{Package: "grain", ID: 3, Tags: []string{"sha-2222222"}, Created: daysAgo(1)},
		{Package: "grain", ID: 4, Tags: []string{"sha-3333333"}, Created: daysAgo(1)},
	}
	if d := decide(t, versions, map[string]bool{"grain-task-9": true}, policy(), 1); !d.Keep {
		t.Errorf("the image of a live branch was deleted: %s", d.Reason)
	}
}

// The half of the retention rule that outlives the window, and the half
// the 48-hour window leans on: whatever else goes, the build a live
// branch currently names stays pullable.
//
// build-artifacts.yml puts the branch tag on the same manifest as that
// commit's sha- tag and moves it on the next build of the branch, so
// "the version carrying the branch tag" is the branch's head build --
// and an operator who pins to it (GRAIN_IMAGE_TAG=sha-<short>, or
// GRAIN_REF, which resolves to the branch tag itself) is pinning to
// something no age can take away. The commits behind it on the same
// branch are history and get the window and nothing more.
func TestTheHeadBuildOfALiveBranchOutlivesTheWindow(t *testing.T) {
	versions := []Version{
		// Yesterday's build of the branch, superseded this morning: the
		// tag has moved off it, and only the window is holding it.
		{Package: "grain", ID: 1, Tags: []string{"sha-0ldbu1d"}, Created: hoursAgo(30)},
		// The same, from a week ago: past the window, nothing else.
		{Package: "grain", ID: 2, Tags: []string{"sha-7dayo1d"}, Created: daysAgo(7)},
		// The head build, from a week ago too -- a branch nobody has
		// pushed to since -- carrying the moving tag as well as its own.
		{Package: "grain", ID: 3, Tags: []string{"sha-headbu1", "grain-task-9"}, Created: daysAgo(7)},
		{Package: "guest", ID: 4, Tags: []string{"sha-headbu1", "grain-task-9"}, Created: daysAgo(7)},
	}
	p := policy()
	p.KeepFor = 48 * time.Hour
	p.KeepCommits = 0
	branches := map[string]bool{"grain-task-9": true}

	for _, id := range []int64{3, 4} {
		d := decide(t, versions, branches, p, id)
		if !d.Keep {
			t.Errorf("version %d is the head build of a branch that still exists and was deleted: %s", id, d.Reason)
		}
	}
	if d := decide(t, versions, branches, p, 1); !d.Keep {
		t.Errorf("a build 30 hours old was deleted under a 48-hour window: %s", d.Reason)
	}
	if d := decide(t, versions, branches, p, 2); d.Keep {
		t.Errorf("a superseded build of the same branch, a week old, was kept: %s", d.Reason)
	}
}

// The window is a duration and is reported as one. It was a count of
// days, which cannot say 48 hours and answers "0d" to every question
// anyone would ask of a window this short.
func TestTheWindowIsMeasuredAndReportedInHours(t *testing.T) {
	versions := []Version{
		{Package: "grain", ID: 1, Tags: []string{"sha-1nsid31"}, Created: hoursAgo(47)},
		{Package: "grain", ID: 2, Tags: []string{"sha-0uts1d3"}, Created: hoursAgo(49)},
	}
	p := policy()
	p.KeepFor = 48 * time.Hour
	p.KeepCommits = 0

	in := decide(t, versions, map[string]bool{}, p, 1)
	if !in.Keep {
		t.Errorf("a build 47 hours old was deleted under a 48-hour window: %s", in.Reason)
	}
	if !strings.Contains(in.Reason, "47h") || !strings.Contains(in.Reason, "48h") {
		t.Errorf("kept for %q, want the age and the window said in hours", in.Reason)
	}
	out := decide(t, versions, map[string]bool{}, p, 2)
	if out.Keep {
		t.Errorf("a build 49 hours old was kept under a 48-hour window: %s", out.Reason)
	}
	if !strings.Contains(out.Reason, "49h") {
		t.Errorf("deleted for %q, want the age said in hours", out.Reason)
	}
}

// `latest` is the name a deployment resolves with nothing configured, so
// no amount of age may touch it -- and it is on the guest package too,
// where pkg/cli/sandboximage.go's unstamped fallback points.
func TestLatestSurvivesAnyAge(t *testing.T) {
	versions := []Version{
		{Package: "guest", ID: 1, Tags: []string{"latest", "sha-a0c1e17"}, Created: daysAgo(2000)},
	}
	if d := decide(t, versions, map[string]bool{}, policy(), 1); !d.Keep {
		t.Errorf("latest was deleted: %s", d.Reason)
	}
}

// A promoted release is what `latest` pointed at once, and what an
// operator pins to roll back to it, so it outlives every window too.
func TestAReleaseTagSurvivesAnyAge(t *testing.T) {
	versions := []Version{
		{Package: "grain", ID: 1, Tags: []string{"v1.5.0", "sha-cf9d615"}, Created: daysAgo(2000)},
	}
	if d := decide(t, versions, map[string]bool{}, policy(), 1); !d.Keep {
		t.Errorf("v1.5.0 was deleted: %s", d.Reason)
	}
}

func TestUntaggedVersionsAreLeftAlone(t *testing.T) {
	versions := []Version{{Package: "guest", ID: 1, Created: daysAgo(500)}}
	d := decide(t, versions, map[string]bool{}, policy(), 1)
	if !d.Keep {
		t.Fatalf("an untagged version was deleted: %s", d.Reason)
	}
	if !strings.Contains(d.Reason, "untagged") {
		t.Errorf("reason %q should say why an untagged version is not this command's business", d.Reason)
	}
}

// The count-based half of the cache rules: the newest few survive
// however old they are, so a repository that has not touched the guest
// inputs in a year still gets a cache hit rather than a nine-minute VM
// boot on its next commit.
func TestTheNewestCachesSurviveTheirOwnAge(t *testing.T) {
	versions := []Version{
		{Package: "guest", ID: 1, Tags: []string{"inputs-aaaaaaaaaaaaaaaa"}, Created: daysAgo(400)},
		{Package: "guest", ID: 2, Tags: []string{"inputs-bbbbbbbbbbbbbbbb"}, Created: daysAgo(500)},
		{Package: "guest", ID: 3, Tags: []string{"inputs-cccccccccccccccc"}, Created: daysAgo(600)},
		{Package: "guest-base", ID: 4, Tags: []string{"1111111111111111"}, Created: daysAgo(400)},
		{Package: "guest-base", ID: 5, Tags: []string{"2222222222222222"}, Created: daysAgo(500)},
		{Package: "guest-base", ID: 6, Tags: []string{"3333333333333333"}, Created: daysAgo(600)},
	}
	p := policy()
	for _, id := range []int64{1, 2, 4, 5} {
		if d := decide(t, versions, map[string]bool{}, p, id); !d.Keep {
			t.Errorf("version %d is one of the newest caches and was deleted: %s", id, d.Reason)
		}
	}
	for _, id := range []int64{3, 6} {
		if d := decide(t, versions, map[string]bool{}, p, id); d.Keep {
			t.Errorf("version %d is the oldest cache of its kind, past the age window, and was kept: %s", id, d.Reason)
		}
	}
}

func TestNothingWrittenInsideTheWindowIsDeleted(t *testing.T) {
	versions := []Version{
		{Package: "guest", ID: 1, Tags: []string{"inputs-aaaaaaaaaaaaaaaa"}, Created: daysAgo(89)},
		{Package: "guest", ID: 2, Tags: []string{"inputs-bbbbbbbbbbbbbbbb"}, Created: daysAgo(3)},
		{Package: "guest", ID: 3, Tags: []string{"inputs-cccccccccccccccc"}, Created: daysAgo(2)},
		{Package: "guest", ID: 4, Tags: []string{"inputs-dddddddddddddddd"}, Created: daysAgo(1)},
	}
	// Version 1 is past -keep-inputs (2) on rank, and inside -keep-for
	// (90 days here) on age. The window wins.
	if d := decide(t, versions, map[string]bool{}, policy(), 1); !d.Keep {
		t.Errorf("a version written 89 days ago was deleted under a 90-day floor: %s", d.Reason)
	}
}

func TestTheCapBoundsOneRunAndSpendsItOnTheOldest(t *testing.T) {
	var versions []Version
	for i := int64(1); i <= 5; i++ {
		versions = append(versions, Version{Package: "grain", ID: i, Tags: []string{"sha-000000" + string(rune('0'+i))}, Created: daysAgo(int(500 - i))})
	}
	p := policy()
	p.KeepCommits = 0
	p.MaxDeletes = 2
	plan := BuildPlan(versions, map[string]bool{}, p, now)
	if len(plan.Delete) != 2 {
		t.Fatalf("deleted %d versions under a cap of 2", len(plan.Delete))
	}
	if plan.Deferred != 3 {
		t.Errorf("deferred %d, want the 3 the cap held back", plan.Deferred)
	}
	if plan.Delete[0].Version.ID != 1 || plan.Delete[1].Version.ID != 2 {
		t.Errorf("the cap spent its deletions on %d and %d, want the two oldest (1, 2)",
			plan.Delete[0].Version.ID, plan.Delete[1].Version.ID)
	}
}

func TestTheNewestBuildsSurviveAQuietYear(t *testing.T) {
	versions := []Version{
		{Package: "grain", ID: 1, Tags: []string{"sha-1111111"}, Created: daysAgo(400)},
		{Package: "grain", ID: 2, Tags: []string{"sha-2222222"}, Created: daysAgo(401)},
		{Package: "grain", ID: 3, Tags: []string{"sha-3333333"}, Created: daysAgo(402)},
		{Package: "grain", ID: 4, Tags: []string{"sha-4444444"}, Created: daysAgo(403)},
	}
	p := policy() // KeepCommits: 3
	for _, id := range []int64{1, 2, 3} {
		if d := decide(t, versions, map[string]bool{}, p, id); !d.Keep {
			t.Errorf("version %d is one of the 3 newest builds and was deleted: %s", id, d.Reason)
		}
	}
	if d := decide(t, versions, map[string]bool{}, p, 4); d.Keep {
		t.Errorf("the fourth-newest build, over a year old, was kept: %s", d.Reason)
	}
}

func TestClassifyReadsTheTagsThisRepositoryActuallyPublishes(t *testing.T) {
	cases := []struct {
		pkg, tag string
		want     TagKind
	}{
		{"grain", "latest", KindShared},
		{"guest", "latest", KindShared},
		{"grain", "main", KindBranch},
		{"grain", "grain-task-232", KindBranch},
		{"grain", "sha-abc1234", KindCommit},
		{"guest", "sha-abc1234", KindCommit},
		{"guest", "inputs-0123456789abcdef", KindInputs},
		// The guest cache tag lives in the guest package by design, so
		// the same spelling anywhere else is not one.
		{"grain", "inputs-0123456789abcdef", KindInputs},
		{"guest-base", "0123456789abcdef", KindBase},
		{"grain/guest-base", "0123456789abcdef", KindBase},
		{"bwsalmon/grain/guest-base", "0123456789abcdef", KindBase},
		// guest-base has no `latest` and no branch tags: it is a cache
		// and nothing else. A tag on it that is not a hash is still
		// treated as a name someone may be using.
		{"guest-base", "latest", KindBranch},
		{"grain/guest-base", "latest", KindBranch},
		{"guest", "some-hand-made-name", KindBranch},
		{"grain", "v1.5.0", KindRelease},
		{"k8s-sandbox", "v1.12.3", KindRelease},
		{"grain", "v1.5.0-rc.1", KindBranch},
	}
	for _, c := range cases {
		if got := classify(c.pkg, c.tag); got != c.want {
			t.Errorf("classify(%q, %q) = %v, want %v", c.pkg, c.tag, got, c.want)
		}
	}
}

// A cosign signature (sha256-<hex>.sig, docs/image-signing.md) lives
// exactly as long as the image it signs: `latest` kept forever and its
// signature pruned at 48 hours would leave every deployment that
// verifies signatures refusing the one name it resolves by default.
func TestASignatureLivesAsLongAsTheImageItSigns(t *testing.T) {
	kept := "sha256:" + strings.Repeat("a", 64)
	gone := "sha256:" + strings.Repeat("b", 64)
	sig := func(digest string) string { return strings.Replace(digest, ":", "-", 1) + ".sig" }
	versions := []Version{
		{Package: "grain", ID: 1, Digest: kept, Tags: []string{"latest"}, Created: daysAgo(200)},
		{Package: "grain", ID: 2, Digest: "sha256:s1", Tags: []string{sig(kept)}, Created: daysAgo(200)},
		{Package: "grain", ID: 3, Digest: gone, Tags: []string{"old-branch"}, Created: daysAgo(200)},
		{Package: "grain", ID: 4, Digest: "sha256:s2", Tags: []string{sig(gone)}, Created: daysAgo(200)},
		// The same digest in another package is a different image: a
		// guest signature is held by the guest, not by a grain version.
		{Package: "guest", ID: 5, Digest: "sha256:s3", Tags: []string{sig(kept)}, Created: daysAgo(200)},
	}
	if d := decide(t, versions, map[string]bool{}, policy(), 2); !d.Keep {
		t.Errorf("the signature of a kept image was deleted: %s", d.Reason)
	}
	if d := decide(t, versions, map[string]bool{}, policy(), 4); d.Keep {
		t.Errorf("the signature of a deleted image was kept: %s", d.Reason)
	}
	if d := decide(t, versions, map[string]bool{}, policy(), 5); d.Keep {
		t.Errorf("a signature was held by a version in another package: %s", d.Reason)
	}
	if got := classify("grain", sig(kept)); got != KindSignature {
		t.Errorf("classify(%q) = %v, want KindSignature", sig(kept), got)
	}
}

// The build a deployment runs is one more superseded sha- tag once its
// branch has moved on, and its config repository's check pulls exactly
// that tag: grain:sha-7781f61 was deleted two days after candidate moved
// past it, while still deployed, and every grain/config check went red.
func TestTheBuildADeploymentRunsOutlivesTheWindow(t *testing.T) {
	p := policy()
	p.KeepFor = 48 * time.Hour
	p.KeepCommits = 1
	versions := []Version{
		{Package: "grain", ID: 1, Tags: []string{"sha-7781f61"}, Created: hoursAgo(80)},
		{Package: "guest", ID: 2, Tags: []string{"sha-7781f61"}, Created: hoursAgo(80)},
		{Package: "grain", ID: 3, Tags: []string{"sha-0ld0000"}, Created: hoursAgo(80)},
		{Package: "grain", ID: 4, Tags: []string{"sha-ffe6755", "candidate"}, Created: hoursAgo(1)},
	}
	branches := map[string]bool{"candidate": true}
	if d := decide(t, versions, branches, p, 1); d.Keep {
		t.Fatalf("with no deployment named, sha-7781f61 is an ordinary superseded build and should go: %s", d.Reason)
	}
	p.Deployed = deployedTags("ghcr.io/bwsalmon/grain/grain:sha-7781f61")
	for _, id := range []int64{1, 2} {
		d := decide(t, versions, branches, p, id)
		if !d.Keep || !strings.Contains(d.Reason, "deployment") {
			t.Fatalf("version %d carries the deployed tag and must be kept: keep=%v %s", id, d.Keep, d.Reason)
		}
	}
	if d := decide(t, versions, branches, p, 3); d.Keep {
		t.Fatalf("the deployed tag kept an unrelated build: %s", d.Reason)
	}
}

func TestDeployedTagsReadsImageReferences(t *testing.T) {
	got := deployedTags(" ghcr.io/bwsalmon/grain/grain:sha-7781f61 ,localhost:5000/grain:v1.2.3@sha256:abcd,ghcr.io/x/y@sha256:ef01,registry:5000/untagged,,")
	want := map[string]bool{"sha-7781f61": true, "v1.2.3": true}
	if len(got) != len(want) {
		t.Fatalf("deployedTags = %v, want %v", got, want)
	}
	for tag := range want {
		if !got[tag] {
			t.Fatalf("deployedTags = %v, want %v", got, want)
		}
	}
}
