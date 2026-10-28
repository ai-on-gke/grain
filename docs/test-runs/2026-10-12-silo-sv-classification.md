# Silo SV-1..SV-123 classified against design history in grain and silo

Task 3153 (Origin: task 1913, filed by the Silo v1 spec verification, task
2062). Task 2062's sandbox was scoped to bwsalmon/silo, so it could not read
the `Design-Item:` trailers of the 115 ids whose implementing commits are in
grain's history. That sandbox's step 1 is finished here.

## Inputs

- **Doc**: `docs/silo/*.md` at bwsalmon/silo `39850aadb7afadf338eca24328edc23e7eabf635`
  (the revision task 2062 checked). Also run at silo `main` as of this
  report (`8594834`) for comparison.
- **History** (newest first, as `designdoc.History` expects):
  - silo `git log <rev> --grep='Design-Item: docs/silo/'`: 4 commits,
    f6a1150 (SV-96), 59531d8 (SV-97, SV-121), d0a6475 (SV-108, SV-110,
    SV-113, SV-114), 85207e3 (SV-122).
  - grain `git log c2044e70f --grep='Design-Item: docs/silo/'`: 33
    implementing commits. 604dd85aa matches the grep only because its
    body quotes the trailer format; it carries no trailers. grain has no
    `docs/silo` Design-Item commits after c2044e70f.
- **Classification**: `designdoc.History` for each page, then
  `designdoc.Classify(current, history, nil, at)`. No open tasks were
  passed. `at` reads the page from silo and falls back to grain.

## Design-Rev predates the SV ids

Every implementing commit, in both repos, says
`Design-Rev: 33a3b9ef35042788edacccde067fa790fb9d07e8`. That is grain's
merge of #2926, one commit *before* 604dd85aa, which added the `{#SV-n}`
markers. At 33a3b9e no heading has an SV id. `Section` therefore finds
nothing, and a plain `Classify` puts all 123 ids in **changed, done**.

604dd85aa changes heading lines only. Its own message says so, and
`git diff 33a3b9ef3 604dd85aa -- docs/silo` with the `{#SV-n}` markers
stripped from both sides has no remaining difference. Its text is
therefore 33a3b9e's text, and the classification below reads 604dd85aa
wherever a commit names 33a3b9e. A later verification has to make the
same substitution, or it will see 123 false "changed" results.

## Result at silo 39850aa

| Class | Count |
| --- | --- |
| new (no Design-Item commit) | **0** |
| done | 120 |
| changed, done | 3 |

Every SV id has at least one `Design-Item:` commit. The three changed
sections:

| ID | Page | Title | What changed since Design-Rev | Code matches? |
| --- | --- | --- | --- | --- |
| SV-55 | state.md | Testing the store | The move commit (silo 97e94df) changed "a job modelled on grain's own `postgres` job" and `GRAIN_REQUIRE_POSTGRES` to "this repository's `postgres` job" and `SILO_REQUIRE_POSTGRES`. | Yes. silo's `.grain/workflows/tests.yml` has the `postgres` job with `SILO_REQUIRE_POSTGRES: "1"`, and `pkg/silo/internal/pgtest` reads it. |
| SV-119 | testing.md | Fake GCE backend | ba3e33e names the package: `pkg/silo/backend/gcp/gce/fake`. | Yes. The package exists at that path. |
| SV-120 | testing.md | Store conformance | Same edit as SV-55, made by 97e94df. | Yes, as SV-55. |

All three are rewordings that follow the move. None asks for new work.
Task 2062's brief says that only SV-119 changed since the move. It
compared against 97e94df, which already carried the SV-55 and SV-120
rewording. Compared against the Design-Rev, those two have changed as
well.

## Silo main moved after 39850aa

Three silo commits after 39850aa edit `docs/silo` together with code.
None of them carries `Design-Item:` trailers. At `main`, 21 ids classify as
changed, done: the 3 above plus these 18.

| Commit | Ids whose section it changed |
| --- | --- |
| 22f863e Refuse to remove a region while a subnet is still in it (task 2061) | SV-37 |
| 17a84d1 Run subscription sessions on their own login, behind an egress proxy (task 2057) | SV-108, SV-118 |
| 7027ed7 Make tenants use real `<id>-` prefixed names in the compute/v1 facade (task 3024) | SV-4, SV-24, SV-26, SV-56, SV-65, SV-66, SV-70, SV-71, SV-72, SV-75, SV-76, SV-101, SV-104, SV-105, SV-122 |

These edits came with the code that implements them, but the commits name
no `Design-Item`/`Design-Rev`. A planner reading silo `main` will
therefore see the 18 as changed since 33a3b9e. A verification after
39850aa should compare these sections with the code instead of treating
them as unbuilt.

## Every id

Columns: implementing commits are newest first. "@39850aa" is the class
against the doc task 2062 checked; "@main" is against silo `main` (8594834).

| ID | Page | Title | Implementing commits | @39850aa | @main |
| --- | --- | --- | --- | --- | --- |
| SV-1 | fence-api.md | Transport and conventions | grain 6855f299f | done | done |
| SV-2 | fence-api.md | Errors | grain 6855f299f | done | done |
| SV-3 | fence-api.md | Who may do what | grain 6855f299f | done | done |
| SV-4 | fence-api.md | Silo | grain 6855f299f | done | changed, done |
| SV-5 | fence-api.md | Backing | grain 6855f299f | done | done |
| SV-6 | fence-api.md | describe | grain 6855f299f | done | done |
| SV-7 | fence-api.md | create | grain 6855f299f | done | done |
| SV-8 | fence-api.md | delete | grain 6855f299f | done | done |
| SV-9 | fence-api.md | Changing allocations | grain 6855f299f | done | done |
| SV-10 | fence-api.md | Quota preview | grain 5d2857bea | done | done |
| SV-11 | fence-api.md | Room for custom allocators | grain 6855f299f | done | done |
| SV-12 | fence-api.md | Control policy | grain 8ba71d87f | done | done |
| SV-13 | fence-api.md | Grants: exports and links | grain 29f4dd4be | done | done |
| SV-14 | fence-api.md | Exports (proxied) | grain c4c03e8e4 | done | done |
| SV-15 | fence-api.md | Links (L3) | grain 29f4dd4be | done | done |
| SV-16 | fence-api.md | Grant calls on a descendant's behalf | grain 29f4dd4be | done | done |
| SV-17 | fence-api.md | Manage grants | grain b0193f58d | done | done |
| SV-18 | fence-api.md | What a holder may do | grain b0193f58d | done | done |
| SV-19 | fence-api.md | Scope | grain b0193f58d | done | done |
| SV-20 | fence-api.md | Lease | grain b0193f58d | done | done |
| SV-21 | fence-api.md | Operations | grain b0193f58d | done | done |
| SV-22 | fence-api.md | Audit | grain b0193f58d | done | done |
| SV-23 | fence-api.md | Agent policy | grain 804443db7 | done | done |
| SV-24 | fence-api.md | Operations | grain 804443db7 | done | changed, done |
| SV-25 | fence-api.md | Members | grain 6855f299f | done | done |
| SV-26 | fence-api.md | Audit | grain 6855f299f | done | changed, done |
| SV-27 | allocators.md | Classes | grain a4102cb14 | done | done |
| SV-28 | allocators.md | GPU classes | grain 6d2f16017 | done | done |
| SV-29 | allocators.md | Zones | grain a4102cb14 | done | done |
| SV-30 | allocators.md | Global counts | grain a4102cb14 | done | done |
| SV-31 | allocators.md | Hard partition | grain a4102cb14 | done | done |
| SV-32 | allocators.md | Room for custom allocators | grain a4102cb14 | done | done |
| SV-33 | allocators.md | Root capacity | grain 8f5465fd5 | done | done |
| SV-34 | allocators.md | Charging and refunds | grain 5d2857bea | done | done |
| SV-35 | allocators.md | Preview | grain 5d2857bea | done | done |
| SV-36 | allocators.md | Model budget | grain a4102cb14 | done | done |
| SV-37 | allocators.md | Allowed regions | grain 9b2ef5eae | done | changed, done |
| SV-38 | allocators.md | Blocks and subnets | grain eb02115b6 | done | done |
| SV-39 | allocators.md | Every silo's space map | grain eb02115b6 | done | done |
| SV-40 | allocators.md | Not fungible | grain eb02115b6 | done | done |
| SV-41 | allocators.md | Growth | grain eb02115b6 | done | done |
| SV-42 | allocators.md | Freed blocks | grain eb02115b6 | done | done |
| SV-43 | allocators.md | Fence rules and links are written per block | grain e24a683f7 | done | done |
| SV-44 | allocators.md | Checks the facade makes | grain d72620cf6 | done | done |
| SV-45 | state.md | Two backends, one interface | grain d26ea8905 | done | done |
| SV-46 | state.md | What silod relies on the store for | grain f7db20650 grain d26ea8905 | done | done |
| SV-47 | state.md | The Cloud SQL instance (call 33) | grain 2435a5358 | done | done |
| SV-48 | state.md | The control subnet | grain 2435a5358 | done | done |
| SV-49 | state.md | Recommended: Private Service Connect | grain 2435a5358 | done | done |
| SV-50 | state.md | What the fence does | grain e24a683f7 | done | done |
| SV-51 | state.md | Authentication: IAM, no password | grain 2435a5358 | done | done |
| SV-52 | state.md | More than one silod, and the leader (call 34) | grain d173f118a | done | done |
| SV-53 | state.md | Schema migrations and upgrades (call 35) | grain 2435a5358 | done | done |
| SV-54 | state.md | Backups and recovery | grain 2435a5358 | done | done |
| SV-55 | state.md | Testing the store | grain f7db20650 grain d26ea8905 | changed, done | changed, done |
| SV-56 | gcp-backend.md | Layout | grain 28b81f3d7 | done | changed, done |
| SV-57 | gcp-backend.md | Shape | grain e24a683f7 | done | done |
| SV-58 | gcp-backend.md | Ordering (call 9) | grain e24a683f7 | done | done |
| SV-59 | gcp-backend.md | Sealed silos | grain e24a683f7 | done | done |
| SV-60 | gcp-backend.md | Rule budget | grain e24a683f7 | done | done |
| SV-61 | gcp-backend.md | Address and shape | grain 74c711219 | done | done |
| SV-62 | gcp-backend.md | Projects and the caller | grain 74c711219 | done | done |
| SV-63 | gcp-backend.md | Default-deny | grain 74c711219 | done | done |
| SV-64 | gcp-backend.md | Covered | grain 5d2857bea | done | done |
| SV-65 | gcp-backend.md | Refused | grain 5d2857bea | done | changed, done |
| SV-66 | gcp-backend.md | Instances | grain 5d2857bea | done | changed, done |
| SV-67 | gcp-backend.md | GPUs | grain 6d2f16017 | done | done |
| SV-68 | gcp-backend.md | Local SSD | grain 6d2f16017 | done | done |
| SV-69 | gcp-backend.md | Disks | grain 6d2f16017 | done | done |
| SV-70 | gcp-backend.md | Instance templates | grain 874205fdd | done | changed, done |
| SV-71 | gcp-backend.md | Subnetworks | grain d72620cf6 | done | changed, done |
| SV-72 | gcp-backend.md | Firewall rules | grain d72620cf6 | done | changed, done |
| SV-73 | gcp-backend.md | Operations | grain 74c711219 | done | done |
| SV-74 | gcp-backend.md | Lists and filters | grain a5cdf7964 grain 532311acd | done | done |
| SV-75 | gcp-backend.md | Errors | grain 74c711219 | done | changed, done |
| SV-76 | gcp-backend.md | Naming | grain 74c711219 | done | changed, done |
| SV-77 | gcp-backend.md | Reconcile | grain d173f118a | done | done |
| SV-78 | gateway.md | Address | grain 7c219161a | done | done |
| SV-79 | gateway.md | Origin identity | grain 7c219161a | done | done |
| SV-80 | gateway.md | Layer 1: the gateway and silod refuse | grain 7c219161a | done | done |
| SV-81 | gateway.md | Layer 2: the fence drops the API port | grain e24a683f7 | done | done |
| SV-82 | gateway.md | What a sealed silo keeps | grain 9b2ef5eae | done | done |
| SV-83 | gateway.md | Proxied exports | grain c4c03e8e4 | done | done |
| SV-84 | gateway.md | Agent tool channels | grain 9834063ca | done | done |
| SV-85 | gateway.md | Egress proxy | grain 3b0e1872a | done | done |
| SV-86 | gateway.md | Policy | grain 3b0e1872a | done | done |
| SV-87 | gateway.md | Enforcement | grain 3b0e1872a | done | done |
| SV-88 | gateway.md | v1: IAP TCP forwarding plus an ID token (call 15) | grain 8934fcad2 | done | done |
| SV-89 | gateway.md | Placement | grain b9d14e21b | done | done |
| SV-90 | install.md | Input | grain d93868252 | done | done |
| SV-91 | install.md | Approach: Go, not terraform (call 18) | grain d93868252 | done | done |
| SV-92 | install.md | What install sets up | grain 28b81f3d7 | done | done |
| SV-93 | install.md | silod's service account | grain 7b7e57870 | done | done |
| SV-94 | install.md | Prerequisite checks | grain d93868252 | done | done |
| SV-95 | install.md | Plan output | grain d93868252 | done | done |
| SV-96 | install.md | `silo uninstall` | silo f6a115043 | done | done |
| SV-97 | install.md | Testing install | silo 59531d899 | done | done |
| SV-98 | agents.md | 1. Agents are infrastructure | grain 804443db7 | done | done |
| SV-99 | agents.md | The authority of an agent | grain 804443db7 | done | done |
| SV-100 | agents.md | Agents in a sealed silo | grain 053036088 | done | done |
| SV-101 | agents.md | 2. The agent object | grain 804443db7 | done | changed, done |
| SV-102 | agents.md | 3. v1 placement: harnesses on the control VM, tools remote | grain aaefc07fc | done | done |
| SV-103 | agents.md | Adapters | grain aaefc07fc | done | done |
| SV-104 | agents.md | Creation | grain 804443db7 | done | changed, done |
| SV-105 | agents.md | Tools | grain 9834063ca | done | changed, done |
| SV-106 | agents.md | The tool channel | grain 9834063ca | done | done |
| SV-107 | agents.md | 5. The session socket: how a harness reaches silod | grain 053036088 | done | done |
| SV-108 | agents.md | 6. Credentials | silo d0a6475e1 | done | changed, done |
| SV-109 | agents.md | The allocator | grain a4102cb14 | done | done |
| SV-110 | agents.md | Metering | silo d0a6475e1 | done | done |
| SV-111 | agents.md | Prices | grain 804443db7 | done | done |
| SV-112 | agents.md | 8. The agent control surface | grain 053036088 | done | done |
| SV-113 | agents.md | The lease runs out | silo d0a6475e1 | done | done |
| SV-114 | agents.md | Budget runs out | silo d0a6475e1 | done | done |
| SV-115 | agents.md | silod restarts | grain aaefc07fc | done | done |
| SV-116 | agents.md | Placement on the control VMs | grain aaefc07fc | done | done |
| SV-117 | agents.md | Sizing | grain aaefc07fc | done | done |
| SV-118 | agents.md | Isolation on the control VM | grain aaefc07fc | done | changed, done |
| SV-119 | testing.md | Fake GCE backend | grain fbecf179b | changed, done | changed, done |
| SV-120 | testing.md | Store conformance | grain f7db20650 grain d26ea8905 | changed, done | changed, done |
| SV-121 | testing.md | Where and how it runs | silo 59531d899 | done | done |
| SV-122 | testing.md | Cases | silo 85207e358 | done | changed, done |
| SV-123 | custom-allocators.md | What v1 must already allow | grain 6855f299f | done | done |

## Reproducing

Use a sandbox that can read both repos, with silo cloned at `~/silo` and
grain at `~/work`. Run a throwaway `main` package inside grain's module
that does the following:

1. Concatenate `git log <silo-rev> --format='%H%x00%B%x01'
   --grep='Design-Item: docs/silo/'` from silo with the same command run
   on grain at `c2044e70f`. Put silo's commits first.
2. For each page, call `designdoc.History("docs/silo/<page>", commits)`.
3. Call `designdoc.Classify(<page at silo-rev>, history, nil, at)`. Here,
   `at(rev)` swaps `33a3b9ef3` for `604dd85aa` and then runs
   `git show rev:docs/silo/<page>` against silo, falling back to grain.
