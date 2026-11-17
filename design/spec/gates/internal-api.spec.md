# Built-in registry and evaluator API

**Status:** normative proposed interface, documentation only. Implements the boundary
described by [BG-1–4](gates.spec.md), not a deployed Go package. BCP 14 applies.
[contracts.schema.json](contracts.schema.json) defines the wire representation and
all scalar bounds/enums; [api.schema.json](api.schema.json) defines controller
acceptance, discovery and operation projections. These are complementary contracts.

## Interface and ownership

The following signatures are exact behavioral interfaces. Package layout, database,
locking primitive, caches and concrete Go implementation names are internal freedom.
Implementations MAY use equivalent private representations; they MUST preserve these
types' information, validation and observable behavior. No private evaluator state,
mutation client, remote evaluator, continuation or snapshot transport is required.

```go
type GateID struct { Type GateType; Version uint32 } // exact registered pair
type GateType string // the twelve catalogue names, version is exactly 1 in v1
type Digest string // sha256:<64 lowercase hex>; RFC 8785 hashed JSON
type ID string // nonempty UTF-8, <=256 characters
type Revision uint64 // JSON integer 0..9007199254740991
type Principal struct {
    ID ID
    Kind string // human, agent, automation, service
    DelegatedBy *ID // controller-verified, never caller-supplied impersonation
}
type Subject struct {
    Kind string // task, change, repository; exactly the matching fields set
    TaskID *ID
    Authority *string
    Repository *string
    ChangeKind *string // pr, cl
    ChangeID *ID
    Ref *string
}
type Origin struct { Kind, ID, Revision string; Imported bool }
type Binding struct {
    ID ID // local attachment ID in the canonical attachment JSON
    QualifiedID ID // derived source_kind/source_id/attachment_id; not a serialized property
    Gate GateID
    Purpose string // completion, guard, monitor
    Action *string // change.merge/change.submit for guards, null otherwise
    Required bool
    Subject Subject
    Origin Origin
    FreshnessSeconds uint32
    Config Config
}
type Config struct {
    Delivery *DeliveryConfig
    Acknowledgment *AcknowledgmentConfig
    Checks *ChecksConfig
    Review *ReviewConfig
    Approval *ApprovalConfig
    Integration *IntegrationConfig
    Children *ChildrenConfig
    BaseHealth *BaseHealthConfig
    NightlyHealth *NightlyHealthConfig
    PublishingHealth *PublishingHealthConfig
    PipelineHealth *PipelineHealthConfig
    QAStage *QAStageConfig
} // exactly one member; MUST match Binding.Gate
type DeliveryConfig struct { Product, Semantics string }
type AcknowledgmentConfig struct { Mode string }
type ChecksConfig struct { Mode string; ExtraJobs []string; Empty, Semantics string }
type ReviewConfig struct { Category, RequirementID, Semantics string }
type ApprovalConfig struct { RequirementIDs []string; Semantics string }
type IntegrationConfig struct { Kind, Semantics string }
type ChildrenConfig struct { Tasks []ID; Predicate string }
```

Remaining config structs' exact fields are the schema property names converted to
Go exported names (shown below); enums and requiredness are identical to the
catalogue. There are no opaque configuration maps. Optional JSON values are Go
pointers; repeated fields are slices, never truncated.

```go
type BaseHealthConfig struct {
    Semantics string
    AllowNonreproduction bool
}
type NightlyHealthConfig struct {
    Workflow string
    Since string
    WindowSeconds uint64
    Semantics string
}
type PublishingHealthConfig struct {
    Semantics string
    ExpectedCommit string
}
type PipelineHealthConfig struct {
    Repos []string
    Semantics string
}
type QAStageConfig struct {
    Stage string
    FlowRevision string
    Assignment string
}
```

```go
type EvaluationInput struct {
    Context EvaluationContext
    Binding Binding
    Actor Principal
    ReadScope ReadScope
}
type EvaluationContext struct {
    EvaluationID, BindingID ID
    TaskID, AttemptID *ID
    Subject Subject
    Head *string
    ResourceVersion string
    ContractRevision, AuthorityEpoch, AuthorizationRevision Revision
    PolicyDigest Digest
    Implementation string
    Now, Deadline time.Time
}
type ReadScope struct {
    TaskIDs []ID
    Subjects []Subject
    RepositoryRefs []Subject
    AuthorizationRevision Revision
} // resolved intersection of evaluator needs and controller-approved observation access
type Evaluator interface {
    Evaluate(context.Context, EvaluationInput, ReadView) (Result, error)
}
type Descriptor struct {
    ID GateID
    SchemaDigest Digest
    ConfigSchema []byte // closed local JSON Schema, <=64 KiB/depth 16
    Implementation string
    Subjects, Purposes []string
    Reads []ReadKind
    Actions []ActionName
}
type Registry interface {
    Discover() []Descriptor // sorted type/version; complete <=12 entries in v1
    Lookup(GateID) (Descriptor, bool)
    Validate(context.Context, AdmissionInput) (Binding, error)
    Evaluate(context.Context, EvaluationInput, ReadView) (Result, error)
}
type AdmissionInput struct {
    AttachmentID ID // local source-owned ID; registry derives QualifiedID
    Action *string // change.merge/change.submit for guards, null otherwise
    Gate GateID
    ConfigJSON []byte // <=64 KiB, depth <=16, no duplicate keys
    Subject Subject
    Purpose string
    Required bool
    Origin Origin // supplied by controller from source, not trusted agent provenance
    FreshnessSeconds uint32
    Authority Principal
}
func Register(registry RegistryBuilder, descriptor Descriptor, evaluator Evaluator) error
type RegistryBuilder interface {
    Add(Descriptor, Evaluator) error
    Freeze() (Registry, error)
}
```

Registration is startup-only. Duplicate pair, missing/remote config reference,
schema/build mismatch, unknown read/action or a declaration inconsistent with the
catalogue MUST fail startup, not overwrite. Freeze prohibits subsequent Add.
Discover returns immutable copies and includes schemas; no evaluator code/credential
is discoverable. Every type has version 1; semantics changes require a new pair,
except a compatible recorded build fix under BG-1. Lookup unknown returns false.
Validate materializes the catalogue defaults, validates shape then the registered
subject/purpose/action combination and controller-verified legacy profile provenance.
It returns canonical typed config and derived qualified ID or a typed error, never
a partially validated config. This local registry check is necessary but not sufficient
for admission: the controller MUST additionally authorize source/read scopes and
wildcards, resolve inheritance/ceilings, detect transactional cycles and resolve
self/template fields before storing a contract or evaluating it. Registry validation
alone is not a task-admission or permission grant.

## Result, state and read contracts

```go
type Status string // met, unmet, pending, unknown, error
type Evidence struct {
    Kind string
    ID ID
    Version string
    Subject Subject
    Head *string
    Digest Digest
}
type Dependency struct {
    Key string
    Subject Subject
    Head *string
    Version, CollectionGeneration string
    ScopeDigest Digest
    Complete bool
    ObservedAt, ValidUntil time.Time
    AuthorizationRevision, DirtyGeneration, CoveredGeneration Revision
}
type Blocker struct { Code, Summary string; Target Subject }
type Work struct {
    Intent string
    Target Subject
    Head *string
    ContextDigest Digest
    Reason string
}
type Wait struct {
    Event, Key, Version string
    Deadline time.Time
    Owner string
    OnTimeout string // escalate
}
type ActionName string // exact ui_action oneOf names in contracts.schema.json
type UIAction struct {
    Reevaluate *ReevaluateAction
    RetryChecks *RetryChecksAction
    RequestReview *RequestReviewAction
    Answer *AnswerAction
    RequestAccess *RequestAccessAction
    Acknowledge *AcknowledgeAction
    Submit *SubmitAction
    AcceptFindings *AcceptFindingsAction
    WaiveCompletion *WaiveCompletionAction
    OverrideAction *OverrideActionAction
    ReconcileOperation *ReconcileOperationAction
} // exactly one variant, selected by name; no untyped action arguments

type EvaluationError struct {
    Code string // timeout, unavailable, invalid_result, resource_limit, unsupported
    Message string
    Retryable bool
}
type Result struct {
    EvaluationID, BindingID ID
    Gate GateID
    ContextDigest Digest
    Status Status
    Summary string
    HumanText, AgentText *string
    Evidence []Evidence
    Dependencies []Dependency
    Blockers []Blocker
    Work []Work
    Waits []Wait
    Actions []UIAction
    ObservedAt, ValidUntil time.Time
    Error *EvaluationError
}
type EvaluationState struct {
    EvaluationID ID
    Revision, DirtyGeneration, CoveredGeneration Revision
    Disposition string // candidate, accepted, invalidated, rejected
    Result Result
    AcceptedAt, InvalidatedAt, RetryAt *time.Time
    InvalidationReason *string
} // controller-owned immutable versions; evaluator never receives mutable state
type SourceRevision struct { Source, Revision string }
type ContextSet struct {
    APIVersion string // grain.gates/v1
    Contexts []EvaluationContext // sorted by qualified binding ID
    Bindings []Binding // same ordering/coverage as Contexts
    Actor Principal
    ReadScope ReadScope
    SourceRevisions []SourceRevision // sorted by source
    RegistryDigest Digest
}
type AcceptanceInput struct {
    Candidates []Result // <=64 required results; optional monitors excluded
    ExpectedContextSet ContextSet // exact api.schema.json context_set shape
    ExpectedEvidenceDigest Digest // digest of sorted union of dependencies
}
type Acceptance struct {
    Accepted bool
    ContextDigest, EvidenceDigest Digest
    States []EvaluationState
    Blockers []Blocker
}
type Acceptor interface {
    Accept(context.Context, AcceptanceInput) (Acceptance, error)
}
```

The exact closed action variants follow; their enums/bounds match ui_action.

```go
type ReevaluateAction struct {
    Name string
    Label string
    Target Subject
    Head *string
    ContextDigest Digest
    Args ReevaluateArgs
}
type ReevaluateArgs struct {
}
type RetryChecksAction struct {
    Name string
    Label string
    Target Subject
    Head *string
    ContextDigest Digest
    Args RetryChecksArgs
}
type RetryChecksArgs struct {
    Jobs []string
}
type RequestReviewAction struct {
    Name string
    Label string
    Target Subject
    Head *string
    ContextDigest Digest
    Args RequestReviewArgs
}
type RequestReviewArgs struct {
    RequirementID string
}
type AnswerAction struct {
    Name string
    Label string
    Target Subject
    Head *string
    ContextDigest Digest
    Args AnswerArgs
}
type AnswerArgs struct {
    QuestionID string
}
type RequestAccessAction struct {
    Name string
    Label string
    Target Subject
    Head *string
    ContextDigest Digest
    Args RequestAccessArgs
}
type RequestAccessArgs struct {
    BlockerID string
}
type AcknowledgeAction struct {
    Name string
    Label string
    Target Subject
    Head *string
    ContextDigest Digest
    Args AcknowledgeArgs
}
type AcknowledgeArgs struct {
    ReportID string
    ReportVersion string
}
type SubmitAction struct {
    Name string
    Label string
    Target Subject
    Head *string
    ContextDigest Digest
    Args SubmitArgs
}
type SubmitArgs struct {
    Action string
}
type AcceptFindingsAction struct {
    Name string
    Label string
    Target Subject
    Head *string
    ContextDigest Digest
    Args AcceptFindingsArgs
}
type AcceptFindingsArgs struct {
    ReviewID string
    ReviewVersion string
}
type WaiveCompletionAction struct {
    Name string
    Label string
    Target Subject
    Head *string
    ContextDigest Digest
    Args WaiveCompletionArgs
}
type WaiveCompletionArgs struct {
    BindingIDs []string
}
type OverrideActionAction struct {
    Name string
    Label string
    Target Subject
    Head *string
    ContextDigest Digest
    Args OverrideActionArgs
}
type OverrideActionArgs struct {
    Action string
    BindingIDs []string
}
type ReconcileOperationAction struct {
    Name string
    Label string
    Target Subject
    Head *string
    ContextDigest Digest
    Args ReconcileOperationArgs
}
type ReconcileOperationArgs struct {
    OperationID string
}
```

```go
type ReadKind string // task, messages, children, change, policy, health
type TaskFacts struct {
    TaskID ID
    AuthorityEpoch Revision
    Product *EvidenceValue[ReportFact] // authenticated product provenance
    Acknowledgment *EvidenceValue[AcknowledgmentFact]
    LiveWriterIDs []ID
    UnsettledOperationIDs []ID
}
type MessageFacts struct {
    IncomingWatermark, ProcessedWatermark Revision
    PendingQuestions []QuestionFacts
    PendingPermissions []PermissionRequestFacts
}
type QuestionFacts struct {
    ID ID
    Version string
    Recipient Principal
    IncomingWatermark Revision
    AnswerID *ID
}
type PermissionRequestFacts struct {
    ID ID
    Version string
    Requester Principal
    Status string // requested, granted, denied
    AuthorizationRevision Revision
}
type ChildFacts struct {
    TaskID ID
    Found bool
    Outcome *string // succeeded, failed, canceled; null means nonterminal
    Integrated bool
    OutcomeEvidence *EvidenceValue[ChildOutcomeFact]
}
type ChangeFacts struct {
    Subject Subject
    Head, Base, ResourceVersion string
    Checks []EvidenceValue[CheckFact]
    Reviews []EvidenceValue[ReviewFact]
    Approvals []EvidenceValue[ApprovalFact]
    Integrated *EvidenceValue[IntegrationFact]
    Conflicted, Behind bool
    RequiredCheckIDs, RequiredReviewIDs, RequiredApprovalIDs []ID
}
type PolicyFacts struct {
    Sources []Origin
    Bindings []Binding
    Digest Digest
    ExplicitUnguarded bool
    CoverageComplete bool
}
type HealthFacts struct {
    Subject Subject
    ExpectedTargets []ID
    Observations []EvidenceRecord
    ActiveProblems []EvidenceRecord
}
type Read[T any] struct {
    Value T
    Found bool
    Dependencies []Dependency
} // Found=false is authoritative absence ONLY if dependencies are complete
type ReadView interface {
    Task(context.Context, ID) (Read[TaskFacts], error)
    Messages(context.Context, ID) (Read[MessageFacts], error)
    Children(context.Context, ID, []ID) (Read[[]ChildFacts], error)
    Change(context.Context, Subject) (Read[ChangeFacts], error)
    TaskChanges(context.Context, ID) (Read[[]Subject], error)
    Policy(context.Context, Subject, string) (Read[PolicyFacts], error)
    Health(context.Context, Subject, GateID) (Read[HealthFacts], error)
}
```

These are bounded domain projections, not user-selectable queries. The concrete
EvidenceValue fact types below carry predicate fields (check conclusions/attempts,
review/findings/disposition, approver eligibility, product digest and health/policy
coverage) with the immutable reference and captured dependencies in the same read.
An adapter MUST NOT resolve a reference through an uncaptured later lookup. Helpers own authorization, collection and captured
dependencies, including all policy applicability/negative/inline facts. Controller
unions the captured ledger with citations; omitted citations cannot remove a fence.
ReadScope is neither the agent's entire grants nor a new broker credential.

Each helper MUST:
1. Reject subjects outside ReadScope before looking them up; permission error does
   not reveal existence. Return a safe permission blocker and unknown predicate.
2. Read value, absence and collection tokens in one bounded DB transaction.
   External collection happens outside transactions, then commits an authenticated
   observation with source timestamp, local sequence and dirty/coverage fences.
3. Latch collection generation and query scope. ABA, membership, relevant record,
   policy, auth and message-watermark changes advance local tokens. A stable empty
   collection is valid negative evidence; a missing provider or omitted/truncated
   page is not. There is no external paging request API.
4. Enforce aggregate 8192 records/8 MiB per evaluation. On incomplete collection,
   return incomplete dependencies; evaluator MUST use unknown, never met. Transport
   failures are typed errors. Preserve actual collection time on cache hits.
5. Honor the earlier parent deadline and input deadline, returning promptly on
   cancellation; no background read or unbounded retry after Evaluate returns.

Adapters may use existing clients and richer private types; fields affecting a
predicate MUST be represented in captured evidence versions. There is no promise
that these minimal projections expose every existing domain field.

## Errors, timing and acceptance

The controller creates evaluation IDs, context, actor, bindings and fixed Now.
Deadline = min(parent deadline, Now+10s); aggregation is at most 30s, including
sequential reads. No additional deadline can extend the parent. Canceling a read or
evaluation discards its candidate; it cannot cancel an already committed operation.
Go context.Canceled/context.DeadlineExceeded MUST remain detectable with errors.Is.
A non-nil Go error discards the returned Result (including a seemingly met result).
Registry/controller catches panics, records safe error/invalid_result and retries
under persisted backoff, without crashing acceptance or leaking private panic text.

Admission errors implement:
```go
type APIError struct {
    Code string // invalid, unsupported, forbidden, conflict, unavailable, resource_limit
    Field *string // JSON pointer when safe
    Retryable bool
    Message string
}
func (e *APIError) Error() string
```
Unknown type/provider/config/version is unsupported on admission; a persisted
unsupported gate projects unknown/unsupported, not omitted. Read denial is
unknown/permission; missing provider/evidence is unknown/unavailable; stale source
is unknown/stale; a known failed check/child is unmet/unsatisfied. A pending process
needs an identified owner/wait/deadline, never an indefinite generic retry. Technical
failure projects error with required error record. Retryable infrastructure errors
use 1, 2, 4, ... 60 second persisted delays with 0..20% jitter; event invalidation
can schedule earlier, while deduplication and one dirty marker prevent storms.
Exhausting retries holds and notifies, never converts unknown to met.

Accept is normative and separate from predicate evaluation. In one short write
transaction it validates the union of required candidates, core context, build and
all captured reads: identity/head/base, contracts/epoch, policy coverage and
authorization revisions, record/set generations, dirty<=covered and freshness.
For positive freshness budgets, result validity is min(all dependencies' expiry,
source time+attachment age budget). Zero requires collection in this persisted
decision cycle with expiry <=source validity and a <=10s cycle deadline, as the
HTTP contract specifies; it never sets expiry to source-time plus zero. Errors, mismatched IDs,
duplicate dependencies or incomplete met candidates are rejected and audited.
An accepted unmet/pending/unknown observation is displayable but cannot authorize
completion or action. Error states are retained as disposition=rejected diagnostics, never accepted predicates. Acceptance does
not mean the condition is met, nor that a protected operation executed.

Revalidation uses the same union immediately at closure and action dispatch, not
merely at button render. Incoming/processed message fences, live/uncertain effects
and required-child/link outcomes join the closure transaction. Expected-head remote
effects and exclusion/forge policy must cover external races; a local successful
transaction alone cannot prove external review/base immutability. Failure to prove
that boundary is boundary_unavailable. History retains acceptance and invalidation
versions; a new fact invalidates current evidence even if visible status stays met.

Useful state (accepted results, waits, dirty/retry generations and operations) is
controller-owned. No private gate state or evaluator-owned wake subscription exists.
Waits are bounded to registered owners, 64/evaluation, deadlines no later than the
current policy escalation horizon (v1 maximum 24h); expiry schedules recollection
or person escalation, not success. Work and actions are descriptors subject to the
[HTTP operation contracts](http-api.spec.md). Scheduler admission, capacity, pause,
budget, standing intent and concrete goal APIs retain their existing ownership.


## Concrete evidence and protected-action records

Canonical JSON uses the schema's field names, flattened gate type/version and
selected config/action variant; it is not default Go struct encoding. Derived
QualifiedID is omitted from attachment JSON, while context binding IDs are qualified.

These Go values correspond exactly to the typed evidence_record facts and the
canonical action_request/action_receipt/exception records. Imports for all snippets
are context and time from the Go standard library. Tagged unions use exactly one
non-nil variant; validation enforces the schema enum, subject and field bounds.

```go
type ReportFact struct {
    Product string
    Settled bool
    Producer Principal
    ContentDigest string
    DeliveryRef string
    DeliveredAt time.Time
}

type AcknowledgmentFact struct {
    ReportID string
    ReportVersion string
    Recipient Principal
    ReceivedAt time.Time
    RevokedAt *time.Time
}

type CheckFact struct {
    JobID string
    SuiteID string
    AppID *string
    Provider string
    RunID string
    Attempt Revision
    Required bool
    Applicable bool
    Status string
    Conclusion *string
    Issuer Principal
    CompletedAt *time.Time
}

type ReviewFact struct {
    ReviewTask string
    RequirementID string
    Category string
    Reviewer Principal
    AssignmentVersion string
    Disposition string
    Findings []Finding
    CompleteDiff bool
    CarryForward *struct { FromHead string; ToHead string; RuleID string; PolicyRevision string }
}

type ApprovalFact struct {
    RequirementID string
    Approver Principal
    Eligible bool
    RenderedHead string
    PolicyDigest string
    ApprovedAt time.Time
    RevokedAt *time.Time
}

type IntegrationFact struct {
    Source string
    OperationID *string
    IntegratedVersion string
    CanonicalTarget Subject
    IntegratedAt time.Time
}

type ChildOutcomeFact struct {
    TaskID string
    Outcome *string
    OutcomeVersion string
    Integrated bool
    AuthorityEpoch Revision
}

type BaseFact struct {
    Ref string
    Commit string
    RunID *string
    Conclusion *string
    NonreproductionDisposition *string
}

type NightlyFact struct {
    Workflow string
    RunID string
    Event string
    VerifiedDefaultPush bool
    Eligible bool
    Jobs []CheckFact
}

type PublicationFact struct {
    Commit string
    ArtifactDigest string
    ReleaseVersion string
    Target string
    ReceiptID string
    PublishedAt time.Time
}

type PipelineFact struct {
    Repositories []string
    ExpectedBases []Subject
    BaseEvidence []Evidence
    Notifications []string
    StalledTasks []string
    BlockedQueueEntries []string
    Complete bool
}

type QAFact struct {
    Stage string
    FlowRevision string
    Assignment string
    Engine string
    Passed bool
    TriageDisposition *string
}

type OperationFact struct {
    OperationID string
    State string
    Receipt *ActionReceipt
}

type PolicyFact struct {
    SourceRevisions []struct { Source string; Revision string }
    RequiredBindingIDs []string
    CoverageComplete bool
    ExplicitUnguarded bool
}

type Finding struct {
    ID string
    Version string
    State string
    DispositionID *string
}

type EvidenceValue[T any] struct {
    Reference Evidence
    Dependencies []Dependency
    CollectedAt, ValidUntil time.Time
    Facts T
}

type EvidenceRecord struct {
    Report *EvidenceValue[ReportFact]
    Acknowledgment *EvidenceValue[AcknowledgmentFact]
    Check *EvidenceValue[CheckFact]
    Review *EvidenceValue[ReviewFact]
    Approval *EvidenceValue[ApprovalFact]
    Integration *EvidenceValue[IntegrationFact]
    ChildOutcome *EvidenceValue[ChildOutcomeFact]
    Base *EvidenceValue[BaseFact]
    Nightly *EvidenceValue[NightlyFact]
    Publication *EvidenceValue[PublicationFact]
    Pipeline *EvidenceValue[PipelineFact]
    QA *EvidenceValue[QAFact]
    Operation *EvidenceValue[OperationFact]
    Policy *EvidenceValue[PolicyFact]
} // exactly one member, selected by Reference.Kind

type ActionRequest struct {
    RequestID string
    IDempotencyKey string
    Action string
    Target Subject
    ExpectedHead string
    ExpectedResourceVersion string
    ExpectedContractRevision Revision
    ExpectedPolicyDigest string
    ExpectedContextDigest string
    Reason *string
    ExpectedAuthorityEpoch Revision
    ExpectedEvidenceDigest string
}

type ActionReceipt struct {
    Intent string // approve_and_queue or execute
    OperationID string
    Request ActionRequest
    Actor string
    Disposition string
    ResultVersion *string
    RecordedAt time.Time
    Evidence []Evidence
    ExceptionIDs []string
}

type Exception struct {
    ID string
    Kind string
    BindingIDs []string
    Target Subject
    Head *string
    ConfigDigest string
    ContractRevision Revision
    PolicyDigest string
    EvidenceDigest string
    OperationID *string
    Actor string
    Reason string
    IssuedAt time.Time
    ExpiresAt time.Time
    RevokedAt *time.Time
    Revision Revision
}

```

Children returns exactly one ChildFacts per configured ID, with Found=false plus
an absence dependency for a known missing child. An unreadable child returns a
safe permission error before lookup and does not assert absence.
Missing is unknown; failed/canceled is unmet for success-required children. A
complete empty task-change set cannot vacuously satisfy integration: it is unmet
unless an authorized explicit no-work contract disposition removes/waives that
obligation. Read projections cannot omit an inconvenient child or linked change.


## Observation-only controller use

The same read-only evaluator boundary may initially be used only to compare two
selected gates with legacy outcomes. Controller observation_view is a separate
diagnostic projection, with legacy authority and all lifecycle/action/dispatch/
automatic-cutover flags fixed false. Its bindings are monitors, its results carry
no work/actions, and no decision or protected-action admission consumes those
cached IDs. Wait descriptors can explain existing pending processes, but the
observation controller must not register them as holds, escalation or task wakes. Observation cache/audit writes are diagnostic state, not evaluator
private state or a lifecycle writer. Missing complete generations/freshness must be
reported honestly as unknown/incomplete; they do not force implementation of the
entire enforcement store before a read-only display can ship.

Registry/config, read and result interfaces retain their definitions; the controller
chooses observational use, not an evaluator-supplied authority claim. Existing
legacy services continue to own state and actions. A later cutover requires a
separately authorized handoff/admission, never a timeout or passing observation.
See [the exact wire projection](http-api.spec.md#initial-observation-only-apidisplay-subset)
and api.schema.json $defs.observation_view.

```go
type ObservationView struct {
    APIVersion string // grain.gates/v1
    Mode string // observation_only
    Authority string // legacy
    LifecycleEffects, ActionEffects, DispatchEffects, AutomaticCutover bool // all false
    SourceRevision string
    ComparedAt time.Time
    LegacyState string
    Bindings []Binding // purpose=monitor, required=false, action=null
    Results []EvaluationState // diagnostics only, not decision inputs
    Dirty bool
}
```
