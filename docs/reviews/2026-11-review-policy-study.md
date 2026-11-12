# Which pull requests should get security and targeted reviews? (2026-11-12)

Task 5053. Report only: this recommends a policy and changes none. The
operator decides, and a follow-up task makes the change.

**Status: part 1 of 2.** This part measures, from git history alone, what
the current policy fires on. Part 2 (yield, cost, overlap of findings)
needs grain's task records, metrics and database. The run that wrote this
part could not read any of them. It got `forbidden` on `grain task read`,
`grain metrics` and `grain db query`, so #4353, #4357 and #4359 could not
be read either. The numbers below therefore don't yet reuse or check that
study's numbers. Part 2 will.

## Method (part 1)

- **Window:** pull requests merged to `main` from 2026-10-29 to
  2026-11-12 (14 days, 1099 merges), and the last 2 days on their own
  (2026-11-10 17:00 to 2026-11-12 17:00 UTC, 162 merges). The operator
  counted 164 merges in their window.
- **Files:** each pull request's files are `git diff --name-only M^1 M` of
  its merge commit `M`. They are classified with today's
  `trustboundary.Classify` (trust-boundary/v28) and today's
  `.grain/triggers/*`. Trigger paths are matched with
  `trustboundary.MatchStatePath` (`**`-aware), and `ignore:` is honoured.
  Pull requests merged before v28 or before a trigger existed are
  classified as if today's policy had applied. This is a simulation of the
  current policy, not a record of what was filed.
- **Heads:** a branch's heads are approximated by its first-parent commits
  `M^1..M^2`, split into its own commits and merges of the base. Pushes
  often carry several commits, so this is an upper bound on heads.
- **Test/doc files:** `*_test.go`, `*/testdata/*` and `*.md` files.
- **Script:** the classifier is in the appendix.

## What fires today

### Most merged pull requests get a security review

| window | merged PRs | security review (any TB category) | sensitive trigger | every-pr |
|---|---|---|---|---|
| 2 days | 162 | 121 (75%) | 30 | 162 |
| 14 days | 1099 | 729 (66%) | 138 | 1099 |

Every path a sensitive trigger watches is also on the trust-boundary list,
with one exception: `agentd-contract`'s design doc, `tests/agentdcompat/**`
and `scripts/agentd-compat.sh`. 5 pull requests in 2 days (5 in 14)
touched an agentd-contract path the list doesn't cover. So the security
review set *is* the trigger set, plus everything else.

### Trust-boundary categories (14 days; 2-day figures in brackets)

"Sole" means it was the only category the pull request touched, so
removing the category would remove the review. "Test/doc only" means every
path that matched the category was a test or doc file.

| category | PRs hit | sole | test/doc only |
|---|---|---|---|
| exec-sandbox | 382 (65) | 125 (16) | 50 (7) |
| auth-routes | 258 (46) | 54 (9) | 1 (0) |
| capabilities-secrets | 154 (24) | 34 (9) | 7 (1) |
| credential-proxy | 120 (20) | 35 (2) | 14 (3) |
| security-tests | 88 (21) | 1 (1) | 88 (21) |
| state-repo | 83 (10) | 8 (1) | 38 (3) |
| merge-gate | 67 (9) | 8 (0) | 9 (1) |
| grain-config | 53 (11) | 13 (1) | 0 |
| github-workflows | 51 (16) | 13 (3) | 0 |
| release-machinery | 51 (10) | 11 (2) | 1 (1) |
| release-promotion | 50 (9) | 7 (0) | 0 |
| hold-policy | 42 (3) | 7 (0) | 4 (1) |
| setup-scripts | 32 (6) | 3 (0) | 0 |
| upgrade-images | 29 (8) | 10 (4) | 2 |
| sandbox-images | 29 (5) | 8 (1) | 2 |
| deploy | 27 (3) | 2 (0) | 5 (1) |
| ci-suppressions | 16 (5) | 1 (0) | 0 |
| path-triggers | 11 (2) | 0 | 0 |

What drives the counts:

- **exec-sandbox through `pkg/orchestrator/`:** this path alone puts 296 of
  1099 merged PRs (27%) on the list. 81 PRs in 14 days (7 in 2 days) reach
  review *only* through it.
- **auth-routes through the route table:** `agent_api_tools.go` (77),
  `agent_api_gate.go` (76), `server.go` (68) and `route_scopes.go` (52).
  These are touched by nearly every feature that adds a route. 18 PRs in 14
  days reached review only through these four files.

### Narrowing categories saves little

- **Excluding test and doc files** (`*_test.go`, `testdata/`, `*.md`) from
  every category except `security-tests` and `hold-policy` takes 35 of 729
  PRs (5%) out of security review over 14 days, and 3 of 121 over 2 days.
  Dropping `security-tests` as well adds one more PR. Its 88 hits almost
  always come with another category.
- **Dropping `pkg/orchestrator/` from exec-sandbox** would take 81 PRs
  (11%) out. Without yield data I can't tell whether those reviews found
  anything, so I don't recommend it yet (part 2).
- **Pull requests that are entirely test or doc files:** 162 in 14 days, 18
  in 2 days. Most of them never touch the list.

### The multiplier: targeted reviews, then heads

**Security reviews.** In the 2-day window, grain filed 223 security
reviews against the 121 pull requests the list classifies: about 1.8 per
merged PR. Those PRs had 646 first-parent commits, of which 385 were their
own commits and 261 were merges of `main`. 219 of the 385 own commits
touched a trust-boundary path.

- Every review should be a full read at a head, or an incremental re-read
  of a push that touched a classified path.
- 223 reviews is about the number of commits (219) that touched one, so
  pushes and the debounce coalesce about as many heads as unchanged heads
  add.
- How many of the 223 read a head whose new commits touched no classified
  path can only be counted from the review records (part 2).
- Rejected and unmerged PRs are in the 223 but not in this count.

**Targeted reviews.** 99 were filed against the **30** pull requests that
touched a sensitive trigger: **about 3.3 per PR**. Those pull requests had
260 heads between them. Two things should prevent nearly all of these:

- every one of those trigger paths, except agentd-contract's design doc,
  `tests/agentdcompat/**` and `scripts/agentd-compat.sh`, is also a
  trust-boundary path;
- `pathtriggerhold.go` already reuses the security review when it covers
  every path the trigger matched (`allPathsCovered`, since 183623b9f,
  2026-11-10).

So either that reuse isn't taking effect, or the filed targeted reviews
are not what this simulation predicts. Only 5 PRs in the window touched
an uncovered agentd-contract path. Part 2 reads the `Review: <task>`
records to find out which.

**Code reviews.** The `every-pr` code review has run since 2026-11-11
13:41. 139 were filed over the 85 merges since then: about 1.6 per merged
PR, plus reviews of PRs that never merged.

## Recommendation so far (part 1)

These follow from the counts alone. Part 2 confirms them, or drops any that
yield data contradicts.

1. **Re-review a new head only when it changes a path the review is
   for.** A security re-review should be filed only when the new commits
   (the incremental delta) touch a trust-boundary path or a sensitive
   trigger's path. Otherwise the existing clearance carries, as
   `carryClearance` already does for "no trust-boundary patch changed".
   - #4808 (head moves that leave the diff unchanged) covers the merges
     of `main`. This covers own commits that touch nothing on the list.
   - The saving can't be sized from git alone: 223 reviews against 219
     classified-path commits says the two effects roughly cancel. Part 2
     counts how many filed reviews read a delta with no classified path.
2. **Never file a targeted review alongside a security review of the same
   head.** Where a trigger's paths are all on the trust-boundary list,
   append the trigger's brief to the security review instead of filing a
   second review. If the reuse at `pathtriggerhold.go:213` already intends
   this, find out why ~99 still ran. Expected effect: targeted reviews fall
   from ~99 per 2 days to the handful for `agentd-contract`'s uncovered
   paths, and to zero under item 3.
3. **Per trigger:**
   - **release-machinery.yml:** it is the trust-boundary category
     `release-machinery` again: same paths, same brief
     (`Category.Review`), no `human:`. Delete the trigger, or keep it only
     as documentation with no gate. **Keep the category**, because it
     guards release publishing and DOCKER_PAT.
   - **release-promotion.yml:** keep `human: required`, since it guards
     release promotion. Set `template: none`. The category already files
     the security review, and a person is the gate.
   - **agentd-contract.yml:** keep `human: required`. Its question is wire
     compatibility, not security, and grain's security review brief
     doesn't check the design doc's extensibility rules. Either give it a
     focused template (`agentd-contract-review`: optional fields only, new
     behaviour behind a feature flag, nothing removed inside the
     deprecation window, contract block regenerated) or set
     `template: none`. `contract_test.go` and `agentd-compat.sh` already
     enforce the mechanical part.
   - **grain-declarations.yml:** 74 PRs in 14 days waited on a person's
     Submit. By directory: `.grain/workflows` 36 (CI and the gate it
     reads), `.grain/templates` 27, `.grain/triggers` 11, `.grain/baselines`
     10, `.grain/schedules` 7, others 5.
     - Triggers, baselines, merge rules and template push sets are already
       human-only (and owner-only) trust-boundary categories.
     - The candidate to relax is template prose and `.grain/suites`: let a
       clean security review release them.
     - Keep `human: required` on `.grain/workflows/**`, `.grain/schedules/**`
       and `.grain/settings.yml`. Part 2 should check whether any template
       or suite edit got a real finding first.
4. **Test and doc files:** stop classifying `*_test.go`, `testdata/` and
   `*.md` under the prefix categories, except `security-tests` and
   `hold-policy`. Expected saving: about 5% of security reviews. It is
   cheap and low-risk because executable code and declarations stay
   classified. It is worth doing only if part 2 shows those reviews found
   nothing.
5. **Code review (every-pr):** part 2 decides on a size or risk threshold
   from its yield. As a data point, 48 of 1099 PRs changed 20 lines or
   fewer, and 162 were test/doc only.

Unchanged, whatever part 2 finds: everything guarding credentials and
grants (`capabilities-secrets`, `credential-proxy`, `auth-routes`), the
merge gate (`merge-gate`, `ci-suppressions`, `github-workflows`,
`path-triggers`, `merge-rules`, `hold-policy`) and release promotion
(`release-promotion`, `human: required`).

## Part 2 (to do, needs task and metrics read access)

For each review kind (security; targeted, per trigger; code), over 7–14
days:

- reviews filed, per merged PR and per head;
- run time and tokens;
- the share that left findings or questions, and of what kind;
- the share of those that were real (fixed, or accepted by the owner);
- misses: what a later review or CI caught;
- findings only one kind caught, and the same issue raised by two kinds at
  one head;
- trust-boundary categories and triggers with zero real findings;

and a comparison with #4353, #4357 and #4359.

## Appendix: classifier

Run from the repository root as `go run ./.studytool 2026-10-29T00:00:00Z`.
It writes one JSON line per merged PR, with its files, categories,
triggers, commit counts and lines changed.

<details>

```go
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ai-on-gke/grain/pkg/trustboundary"
	"gopkg.in/yaml.v3"
)

type trig struct {
	Name   string
	Paths  []string `yaml:"paths"`
	Ignore []string `yaml:"ignore"`
}

type pr struct {
	Merge, Date, Num, Branch string
	Files                    []string
	Commits, Pushes, Lines   int
	Cats, Trigs              map[string][]string
}

func git(args ...string) string {
	out, _ := exec.Command("git", args...).Output()
	return string(out)
}

func main() {
	var trigs []trig
	for _, n := range []string{"agentd-contract", "grain-declarations", "release-machinery", "release-promotion", "every-pr"} {
		b, _ := os.ReadFile(".grain/triggers/" + n + ".yml")
		t := trig{Name: n}
		if err := yaml.Unmarshal(b, &t); err != nil {
			panic(err)
		}
		trigs = append(trigs, t)
	}
	sc := bufio.NewScanner(strings.NewReader(git("log", "--first-parent", "--merges",
		"--since="+os.Args[1], "--format=%H|%ad|%s", "--date=iso-strict-local", "HEAD")))
	enc := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "|", 3)
		p := pr{Merge: parts[0], Date: parts[1], Cats: map[string][]string{}, Trigs: map[string][]string{}}
		if f := strings.Fields(parts[2]); len(f) >= 6 && f[0] == "Merge" {
			p.Num, p.Branch = f[3], f[5]
		}
		p.Files = strings.Fields(git("diff", "--name-only", p.Merge+"^1", p.Merge))
		ns := strings.Fields(git("diff", "--numstat", p.Merge+"^1", p.Merge))
		for i := 0; i+2 < len(ns); i += 3 {
			var a, d int
			fmt.Sscan(ns[i], &a)
			fmt.Sscan(ns[i+1], &d)
			p.Lines += a + d
		}
		fmt.Sscan(git("rev-list", "--count", "--no-merges", p.Merge+"^1.."+p.Merge+"^2"), &p.Commits)
		fmt.Sscan(git("rev-list", "--count", "--first-parent", p.Merge+"^1.."+p.Merge+"^2"), &p.Pushes)
		for _, m := range trustboundary.Classify(p.Files) {
			p.Cats[m.Category.ID] = m.Paths
		}
		for _, t := range trigs {
			for _, file := range p.Files {
				hit := false
				for _, pat := range t.Paths {
					hit = hit || pat == "**" || trustboundary.MatchStatePath(pat, file)
				}
				for _, ig := range t.Ignore {
					hit = hit && !trustboundary.MatchStatePath(ig, file)
				}
				if hit {
					p.Trigs[t.Name] = append(p.Trigs[t.Name], file)
				}
			}
		}
		enc.Encode(p)
	}
}
```

</details>
