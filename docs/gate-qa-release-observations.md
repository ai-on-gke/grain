# QA and release qualification observations

Task #69 extends the observation-only slice reviewed in Task #64. Verified merged
ancestors before implementation: canonical spec PR #1719 (2e45dca71), framework
PR #1739 (6bd221161), and review PR #1803 (07e43408c, including de688e249).
Task #69 used builtin-gate-observer-v8. The shared implementation pin is now
`builtin-gate-observer-v10`; see [coverage and repairs](gate-coverage-audit.md).

The existing disabled-by-default allowlist still selects at most 32 tasks.
Authorized ordinary task reads add QA observations for eligible QA tasks, and
release observations for tasks actually filed by a repository-declared
qualification suite. No deployment, configuration, grants, cohort expansion or
observation window is changed by this port. No new endpoint or evaluator transport
exists. The in-process typed structs are ordinary domain-reader inputs, not the
abandoned snapshot transport or an MCP evaluator/back-call broker.

Every binding is a nonrequired monitor. Legacy lifecycle, scheduling, waits,
bounded repair, triage, approval, merge and release writers retain authority.
All effect/cutover flags remain false; work/actions are empty. QA/release
predictions contain no executable effects or installed waits. A met result
cannot start treatment, request an agent/check/review, approve, close, cut,
promote, sign or publish anything.

## Minimal normative catalogue addition

qa_stage v1 retains stage=s3|s4|s5, flow_revision and assignment=treatment|shadow.
It additionally permits a task subject **only for a nonrequired monitor with
action=null**, so staged work can be observed before a PR exists. Change guards
retain their existing subject and future admission requirements. No custom
promotion/effect adapter is admitted.

release_qualification v1 is a separate repository monitor, with closed required
configuration: predicate, expected_commit (full lowercase SHA), release_version
(vM.m.p), and policy_revision (nonempty exact reader/policy revision).
predicate is suite, provenance, soak, person, breaking_change, signing_version,
publication_readiness, or promotion_policy. It does not subsume QA stages or repository health.
Its enforcement/effect activation is deferred. The canonical contracts and API
descriptor schemas include this addition.

The pure evaluator accepts only typed domain evidence for those predicates:
authenticated suite status/issuer/attempt/head; RC ancestry; recorded soak and
known effective minimum or a qualification-based exemption; verified identified
person decision for the exact head/version and validity interval (the existing
60-second issued-time tolerance); breaking policy disposition; verified artifact
signature/digest/head/version; and a complete required publication-readiness
target set. Missing facts do not default true. No shell command or credentials
can be configured. There is no public endpoint accepting evidence/verification
booleans from an agent.

## Coverage audit

| Existing source / prerequisite | Observation mapping and evidence |
| --- | --- |
| qa_phases.go S0 intent and S2 plan critic | Prompt/execution phases, not completion prerequisites. Their telemetry does not satisfy S3/S4/S5. No unrelated gate is introduced. |
| qa_gate.go treatment versus control/shadow; resolve package | qa_stage monitors use the actual read-only resolver, sticky assignment/enrollment, experiment, flow, arm and engine pins. Control/shadow results are labeled and cannot satisfy treatment authority. Missing assignment/head/flow/engine stays unknown. Legacy unversioned flows, repo-custom overlays and composed task/repository overlays without a recorded composite policy pin stay unknown. |
| checks.RunS3 integrity, strict decoder, sibling/exported surface, UI/spec budget, hygiene, mutation/boundary and PR-contract rules | qa_stage s3 requires every enabled exact-flow S3/S3.5 rule, including the current runtime's actual rule IDs. Complete empty is not success when rules are required. Warning/shadow dispositions remain advisory. Any earlier authentic same-head fail remains a failure, matching loadRecordedS3Outcome; a later same-head pass cannot erase it. |
| Custom checks and deterministic/isolated/human custom stages | Observe the existing typed stage/check records; grouped checks are not also required unconditionally. Exact-head changed-file/tier metadata can prove conditional exclusion; missing applicability remains unknown. Post-S4 stages belong to s4. Required child check rows prevent an aggregate pass from concealing skipped/erroring checks. Human holds remain unmet. This is evaluation-only parity; custom effects/promotion have no enforcement adapter. |
| Fresh-context S4 and salvage/repair caps | qa_stage s4 uses the authentic verifier result, exact-flow blast-tier applicability, custom post-S4 records and existing hold/attempt facts. No result is synthesized when execution failed before persistence. The current max-S4-round clamp (0..3), attempted rounds and one-shot S3 salvage are described; they are never reset or overridden. |
| S5 mode off/shadow/enforced, require_human and human floors | qa_stage s5 interprets TriageDecision, rather than treating the row's Verdict=pass as approval. Shadow triage remains shadow. Existing human-review, QA/security/review holds and operator auto-merge floors are visible. The recorded verdict does not reconstruct unretained historic CI/file-list/trust inputs or attest current merge readiness. |
| releasestage1.go and candidate-gate.sh Stage 1 job attempts | Owned by nightly/health observation work. This port does not duplicate base, nightly, flake-budget, systemic-failure, required-job or publication-health predicates. Stage 2 skipped under app=none is not manufactured into a new qualification success; its Stage 1 evidence belongs to health. |
| releasequalification.go Stage 2 | release_qualification suite reuses the existing authenticated aggregate verdict, including highest infrastructure attempt and latest check ID within that attempt. Exact head, issuer, check name and external attempt provenance must match. Success is met; confirmed failure unmet; running/canceled/neutral infrastructure outcomes cannot pass. Missing/denied/oversized collections are unknown. |
| Stage 2 footer SHA/tag, failed/canceled task outcomes, PR/proposal/scratch-child/scratch-ref restrictions, bounded product/infra retries and final-item ordering | These remain the existing JudgeQualificationAttempt/DecideReleaseQualification writers' responsibility. The observer consumes their trusted aggregate check; it does not re-run tasks/scripts or independently claim all child/private evidence is readable. Missing aggregate evidence is unknown, not a pass inferred from a closing note. |
| RC tag/head, discarded marker, pending commit and ancestry | provenance uses complete scoped tags, exact commit reads, matching pending-tag head and existing CompareRefs behind/identical ancestry to the pinned main/convergence head. Discarded or mismatched pending heads are negative. Missing ancestry is unknown. Tags do not preserve an immutable original suite-invocation SHA or original suite-policy receipt; no action admission is claimed. |
| release-from-trunk.sh qualification-era versus soak-era pending tag | soak reuses the exact-head Qualification: PASS exemption and reads recorded Soak-Seconds. The tag reader does not expose tagger times, and effective workflow/environment soak/slack overrides are not retained: elapsed-tag fallback or a missing effective minimum remains unknown. A short recorded soak never becomes longer merely because the observation clock advances. Typed evaluation supports a pinned minimum without overriding the human short_soak_ok floor. |
| release_approval.go / verify-release-approval.sh person restriction and signed answers | person requires a verified identified human, positive exact-head/version decision and current validity. release_approval stores dispatch/deduplication, not signed authorization or short_soak_ok/allow_breaking answers: a successful dispatch row never grants approval. Rejected/failed rows remain negative; missing signed proof is unknown. No signing key is loaded or created. |
| release-notes.sh check-breaking, RELEASE_MAJOR and explicit allow_breaking | breaking_change reads exact-candidate release.conf and the complete previous-release commit range using the existing pure parsing/breaking predicates. A no-breaking range can pass only with a complete release-tag collection and below the compare reader's 250-commit truncation boundary. Synced-release runs do not collect that tag history, so their breaking-change prediction remains unknown. Observed breaking changes display the declared-major/person requirement; unretained workflow/environment overrides remain unknown. No exception is invented from prose or missing data. |
| verify-release-approval.sh and release image/asset signing, artifact/version/ref guards | signing_version has a typed exact-head/version/digest/signature predicate. The current approved read sources expose neither verified artifact signatures nor transient signed workflow inputs: this projection honestly stays unknown. Tag prose and a dispatch row are insufficient. Wrong/unsigned artifacts are negative in the typed adapter tests. |
| Required build/image/asset target set and publication readiness | publication_readiness is separately typed. No complete registry/asset-set receipt is available from these scoped qualification-task reads, so it stays unknown. A build or signature never stands for publication. Existing publishing-health work retains publication receipt ownership. |
| Release version ordering/floor/reuse, operator veto, auto-promotion eligibility, daily cap/interval and release workflow concurrency | promotion_policy reads exact current-main RELEASE_NEVER_PROMOTE and detects an already-occupied version from complete scoped tags. Declared vetoes are displayed; missing effective environment/override/scheduling policy stays unknown. Existing release selection/scheduling/action policy remains authoritative. These are not claimed as a complete qualification receipt by this port. Config/environment and executor conditions not retained in the readable verdict are explicit limits; they cannot be bypassed by a QA/suite met result. No cut/promotion/publication effect is activated. |

## Collection, persistence and display

Readers are scoped to one authorized task and its repository. Release collection
first verifies real suite membership and its repository, then reads that suite's
qualification declaration. It does not scan sibling tasks, private conversations,
the deployment suite catalogue or notifications. Provider errors are redacted.

QA reads at most 512 exact-task/head check rows and 512 findings, with 8192-byte
details and 768-character finding summaries. Overflow is incomplete. Complete tags
are capped at 2048 and release checks at 512. The ordinary forge APIs perform their
existing pagination; record admission limits apply after they return, rather than
providing a new bounded paging transport. Acquisition can exceed the admitted
record limits on those legacy interfaces. A failed collection is never interpreted
as empty success.
Network reads have a separate three-second collection context outside the diagnostic transaction, using the configured
ordinary forge read adapter and its transport timeout. Context expiry prevents
acceptance, but these legacy forge interfaces themselves do not accept a context;
a blocked underlying call is bounded by that transport's existing timeout rather
than the 250 ms diagnostic database budget. This is a material latency limitation,
not a claim of a new bounded forge transport.

An optimistic source fence is captured before/after collection and checked again
in the diagnostic writer transaction. It covers task declaration/holds, QA config,
sticky assignment/enrollment/experiment policy, repository flow binding,
QA-record membership and suite candidate/declaration identity. Remote exact-head
samples have a maximum 60-second lease and are refreshed on reads, not atomically
locked with GitHub. No remote operation uses these samples. Unknown, denied, stale,
unsupported and error states remain explicit. An older completed collection or
failure cannot replace a newer accepted sample. Existing retry/backoff and cache
restart semantics are retained. Clock-only heartbeats do not create source events.

The existing age/per-task/global/current-cache history bounds remain. The QA/release contribution to each persisted
view is additionally bounded to 64 KiB, 128 evidence references and
64 blockers per result; exceeding bounds produces an explained resource-limit
unknown instead of truncating to met. Reads return at most 20 retained samples.
Those samples have no independent legacy parity oracle and are labeled accordingly.

The Angular card displays every retained blocker, exact head/evidence versions,
configuration/policy provenance and proposed next steps as shadow text. There are
no action buttons. Existing task/repository authorization runs before collection;
hidden tasks do not expose a sidecar. The classic UI remains compatible.

## Validation and limits

Tests exercise real scoped QA rows/findings, custom conditional/check completeness,
head/flow/arm/engine changes, control/shadow isolation, denied/missing data, signed
versus wrong/unsigned typed artifacts, incomplete suite/stage evidence, soak
thresholds, human validity/identity restrictions and failed/canceled verdicts.
Both real database engines exercise durable replay/restart, source/policy fences,
late success/failure ordering, backoff/recovery and unchanged legacy tables/wakes.
Canonical schema pins, API authorization and Angular inertness remain required.

Evaluation coverage is not a declaration that the inaccessible evidence in this
audit now exists. There is no migration, cutover, operational comparison window,
automatic follow-up filing or authority handoff. Future enforcement still requires
the original exact-version policy/authorization receipts and admitted custom
effect adapters.

Current main's review/approval port is retained, including linked-resource
authorization and the omission of retained history for scoped API callers. The
shared projection keeps its 64-binding limit: QA/release monitors reserve a bounded
portion of that budget, and an overflowing review collection stays explicitly
incomplete. Existing review payload bounds remain separate from the 64 KiB
QA/release contribution limit.

Missing/unreadable annotated-tag bodies do not establish either a qualification
exemption or a commit mismatch. Complete tag scopes must agree, all provider heads
used as immutable proof are full SHAs, and unrelated nightly/publication checks
do not version the configured qualification predicate.

The merged Task #66 CI observers are retained alongside QA/release and review/approval. Shared admission reserves both CI and qualification bindings before collecting review scopes, preserving the canonical 64-binding limit.

The release qualification family also requires the ordinary CI read scope. Denied callers do not trigger release source reads; cached/historical suite views are rechecked before display. Authorized task-local QA observations remain independent of that CI permission.

The merged delivery/integration/children port is retained, including related-task redaction, explicit child selections and byte-bounded history reads. Binding admission reserves actual outcome, CI and qualification contributions before collecting review scopes. The security review fix keeps breaking-change observations unknown without a complete release tag history.

The delivered health-family port is retained with repository-only projections, access-limited transient reads, filtered history/comparison counts and family transitions. Health goals bypass task QA/release collectors; health projections never persist task qualification bindings. Ordinary task projections continue to reserve QA/release, CI and outcome capacity before review fanout. All thirteen catalogue types are registered, with inaccessible evidence still unknown.
