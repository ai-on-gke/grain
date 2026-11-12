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

// The slices of the server's JSON the Angular UI reads. Field names are
// the server's own (pkg/ui's json tags); everything optional the server
// omits when empty is optional here too.

export interface AccessInfo {
  role: string;
  roles: string[];
  full: boolean;
  scopes: string[];
  missing?: string[];
  repoLimits?: Record<string, string[]>;
}

export interface CustomFieldDeclaration {
  name: string;
  label?: string;
  type: "string" | "enum" | "bool" | "number" | string;
  options?: string[];
  description?: string;
  default?: unknown;
  position?: string;
  multiple?: boolean;
  showOnRow?: boolean;
}

export interface AgentBackendOption {
  id: string;
  name?: string;
  framework?: string;
  models?: string[];
  disabled?: boolean;
  team?: string;
}

export interface GrainConfig {
  access?: AccessInfo | null;
  viewerOwner?: string;
  actor?: string;
  // The build stamp (api/version.ts); a string on older servers.
  version?: string | Record<string, unknown>;
  stateRepoSyncFailure?: unknown;
  capabilities?: CapabilityOption[];
  defaultCapabilities?: string[];
  repoDefaultCapabilities?: Record<string, string[]>;
  agentBackends?: AgentBackendOption[];
  agentFramework?: string;
  models?: string[];
  defaultModel?: string;
  customFields?: CustomFieldDeclaration[];
  [key: string]: unknown;
}

export interface PullRequestRef {
  number?: number;
  url?: string;
  [key: string]: unknown;
}

export interface AttachmentUpload {
  filename: string;
  contentType: string;
  content: string;
}

export interface AttachmentMeta {
  id: string | number;
  filename: string;
  contentType?: string;
  size?: number;
}

export interface HumanReviewView {
  source?: string;
  category?: string;
  reason?: string;
  reviewTask?: string;
  run?: string;
  at?: string;
}

export interface TaskPaused {
  at: string;
  by: string;
  byKind?: string;
  bySource?: string;
  byLabel?: string;
  fromState: string;
  immediate?: boolean;
  graceUntil?: string;
}

export interface GoalSpec {
  repo?: string;
  branch?: string;
  publishing?: boolean;
  task?: string;
  number?: number;
  tasks?: string[];
  until?: string;
  repos?: string[];
  cl?: number;
  workflow?: string;
}

export interface GoalDecl {
  kind: string;
  spec: GoalSpec;
  on_met?: string;
  on_limit?: string;
  max_resumes?: number;
  max_stall?: string;
}

export interface TaskGoalView extends GoalDecl {
  metAt?: string;
  limitNotifiedAt?: string;
  resumesWithoutProgress?: number;
  outcome?: string;
  [key: string]: unknown;
}

export interface Task {
  id: string;
  title: string;
  state: string;
  repo?: string;
  createdAt?: string;
  updatedAt?: string;
  proposedBy?: string;
  questionSummary?: string;
  questionCommentId?: number;
  awaitingAccess?: boolean;
  turnStopped?: boolean;
  reviewQuestions?: string;
  reviewFindings?: unknown[];
  humanApprovedBy?: string;
  humanReviewHold?: HumanReviewView[];
  autoMerge?: boolean;
  pullRequest?: PullRequestRef | string | null;
  prClosedAt?: string;
  prMergedAt?: string;
  merged?: boolean;
  closedReason?: string;
  mergeQueueState?: string;
  mergeQueueNote?: string;
  mergeQueueStalledAt?: string;
  mergeQueueBlockedAt?: string;
  redBaseBranch?: unknown;
  stall?: string;
  deferredUntil?: string;
  reads?: string[];
  writes?: { repo: string; base?: string }[];
  capabilities?: string[];
  customFields?: Record<string, string | string[]>;
  reviewers?: string[];
  reviewTemplateId?: string;
  reviewTemplateName?: string;
  sandboxCpus?: number;
  sandboxMemoryMb?: number;
  sandboxDiskGb?: number;
  agentFramework?: string;
  agentBackendId?: string;
  model?: string;
  networkAccess?: string;
  nothingNeededAction?: string;
  promptExtension?: string;
  noPort?: boolean;
  keepRunning?: boolean;
  hosting?: string;
  paused?: TaskPaused | null;
  runNowAt?: string;
  runNowStale?: boolean;
  goal?: TaskGoalView | null;
  consoleSession?: unknown;
  interactive?: boolean;
  // What the current run is doing, in a sentence, and when it said so;
  // activityBySetup marks grain's own setup steps rather than the agent.
  activity?: string;
  activityAt?: string;
  activityBySetup?: boolean;
  priority?: number;
  // Waiting on an open dependency (GET /api/tasks).
  blocked?: boolean;
  // Dispatch pool: "standard", "bypass" or "durable" (model.Admission).
  admission?: string;
  authorKind?: string;
  authorLabel?: string;
  author?: string;
  authorSource?: string;
  branch?: string;
  base?: string;
  // Tasks this one waits for (task detail).
  dependsOn?: string[];
  description?: string;
  body?: string;
  // Who the task belongs to and who filed it (pkg/ui tasks.go Task).
  owner?: string;
  creator?: string;
  approved?: boolean;
  approvedBy?: string;
  approvedByLabel?: string;
  approvedByKind?: string;
  approvedBySource?: string;
  // The task that filed this one, and the work tree it belongs to.
  generatedFrom?: string;
  origin?: string;
  stacked?: boolean;
  review?: boolean;
  scheduled?: boolean;
  suiteRun?: boolean;
  imported?: boolean;
  awaitingImportApproval?: boolean;
  grainBaseRepair?: boolean;
  fixesBaseBranch?: boolean;
  repairOf?: string;
  reviewTask?: string;
  securityReviewTask?: string;
  autoApproveProposals?: boolean;
  externalRef?: string;
  continues?: string;
  rolledOverFrom?: string;
  rolledOverTo?: string;
  startedAt?: string;
  endedAt?: string;
  closedAt?: string;
  filedVia?: string;
  changelists?: string[];
  changeTargetKind?: string;
  clReview?: {
    canPublish?: boolean;
    canMail?: boolean;
    [key: string]: unknown;
  };
  clSubmit?: { canSync?: boolean; [key: string]: unknown };
  derivedFrom?: string[];
  designs?: string[];
  // Open dependencies and the state of each (GET /api/tasks).
  blockedBy?: string[];
  dependsOnStates?: Record<string, string>;
  // The gates a pull request must pass to merge (model.MergeGate).
  mergeGates?: MergeGate[];
  // Why the task is where it is (orchestrator liveness reason), e.g.
  // {reason: "awaiting_reply/stopped", class: "awaiting a person"}.
  reason?: { reason: string; class?: string; control?: string };
}

export type MergeGateState =
  "passed" | "running" | "failing" | "findings" | "needed" | "overridden";

// MergeGate is one required gate on a change (pkg/model merge_gates.go).
// A gate is satisfied when it has passed or was overridden.
export interface MergeGate {
  id: string;
  label: string;
  state: MergeGateState | string;
  detail?: string;
  reviewTask?: string;
  approvedBy?: string;
  overridable?: boolean;
}

export interface AccessRequestItem {
  kind: string;
  repo?: string;
  base?: string;
  capability?: string;
  with?: Record<string, unknown>;
  access?: string;
  grant?: string;
  dangerous?: boolean;
  requiresPerson?: boolean;
  held?: boolean;
  error?: string;
  substitute?: boolean;
  substituteGrant?: string;
}

export interface AccessRequestView {
  summary: string;
  reason: string;
  items: AccessRequestItem[];
  createdAt: string;
}

export interface PendingConfigChange {
  summary: string;
  description?: string;
  branch: string;
  base?: string;
  baseSha?: string;
  headSha: string;
  prNumber?: number;
  files?: string[];
  diff: string;
  validation?: { ok: boolean; error?: string };
  createdAt?: string;
}

export interface TaskCIStatus {
  runId?: string;
  commit?: string;
  startedAt?: string;
  conclusion?: string;
  finishedAt?: string;
  detail?: string;
}

export interface CIRunStep {
  name: string;
  status?: string;
  conclusion?: string;
  exitCode?: number;
  startedAt?: string;
  finishedAt?: string;
  output?: string;
  outputTruncated?: boolean;
}

export interface CIRunJob {
  id?: string | number;
  name: string;
  status?: string;
  conclusion?: string;
  startedAt?: string;
  finishedAt?: string;
  steps?: CIRunStep[];
}

export interface CIRun {
  id: string;
  provider?: string;
  repo?: string;
  ref?: string;
  branch?: string;
  commit?: string;
  event?: string;
  workflow?: string;
  taskId?: string;
  url?: string;
  status: string;
  conclusion?: string;
  detail?: string;
  startedAt?: string;
  finishedAt?: string;
  jobs?: CIRunJob[];
  fault?: { kind?: string; summary?: string; detail?: string };
}

export interface CIRunsResponse {
  enabled?: boolean;
  runs: CIRun[];
  matched?: number;
  faults?: { kind?: string; summary?: string; detail?: string }[];
}

export interface CompareResponse {
  base: string;
  head: string;
  mergeBase?: string;
  merged?: boolean;
  diff: string;
}

export interface CapabilityMethod {
  ref: string;
  capability: string;
  method: string;
  description?: string;
  args?: Record<string, unknown>;
  requiresArgs?: boolean;
  releasable?: boolean;
  policyRefusal?: string;
  aliasOf?: string[];
}

export interface APIScopeSetRow {
  name: string;
  description?: string;
  scopes?: string[];
  source?: string;
  catalogue?: { disabled?: boolean };
}

export interface APIScopeSetsResponse {
  sets: APIScopeSetRow[];
  error?: string;
  path?: string;
}

export interface GrantCheck {
  grant?: string;
  recorded: string;
  error?: string;
  fields?: { field: string; message: string }[];
  catalogueRefusal?: boolean;
}

export interface CapabilityAuditEntry {
  grant: string;
  attemptId?: string;
  at: string;
  hostd?: string;
  record?: unknown;
  note?: string;
}

export interface TaskContextView {
  brief?: string;
  original_description?: string;
  compacted_through?: number;
  handover?: string;
  handover_stale?: boolean;
  summarised?: number;
  brief_edits?: {
    brief?: string;
    reason?: string;
    by?: string;
    at?: string;
  }[];
}

export interface TemplateField {
  name: string;
  label?: string;
  options?: string[];
  default?: string;
  required?: boolean;
}

export interface Template {
  id: string;
  name: string;
  title: string;
  description: string;
  fields?: TemplateField[];
  repo?: string;
  base?: string;
  priority?: number;
  autoMerge?: boolean;
  networkAccess?: string;
  nothingNeededAction?: string;
  model?: string;
  interactive?: boolean;
  keepRunning?: boolean;
  admission?: string;
  hosting?: string;
  reads?: string[];
  writes?: { repo: string; base?: string }[];
  reviewers?: string[];
  customFields?: Record<string, string | string[]>;
  capabilities?: string[];
  declaredIn?: { repo: string; file?: string; path?: string };
  system?: boolean;
  revision?: string;
}

export interface ComposedField {
  name: string;
  values?: string[];
  source: string;
  sources?: string[];
}

export interface ComposedTask {
  templateId?: string;
  templateName?: string;
  title: string;
  fields: ComposedField[];
}

export interface Composition {
  kind: string;
  repo?: string;
  base?: string;
  tasks: ComposedTask[];
}

export interface Comment {
  id: number;
  author?: string;
  // "human", "agent" or "automation" (pkg/ui tasks.go Comment).
  authorKind?: string;
  authorLabel?: string;
  onBehalfOf?: string;
  onBehalfOfLabel?: string;
  body: string;
  summary?: string;
  handover?: boolean;
  createdAt?: string;
  attachments?: AttachmentMeta[];
}

export interface Attempt {
  number: number;
  startedAt?: string;
  agentStartedAt?: string;
  promptTokens?: number;
  capabilityAuditCount?: number;
  // The grain hostd this attempt's host work went to (model.TaskRunHostd).
  hostd?: {
    hostd: string;
    instance?: string;
    owner?: string;
    deleted?: boolean;
  };
  finishedAt?: string;
  outcome?: string;
  detail?: string;
  setupNote?: string;
  model?: string;
  agentFramework?: string;
}

export interface TaskDetail extends Task {
  comments?: Comment[];
  attempts?: Attempt[];
  olderComments?: number;
  olderAttempts?: number;
  compactedThrough?: number;
  attachments?: AttachmentMeta[];
  localScriptApprovals?: LocalScriptApproval[];
  pendingAccess?: AccessRequestView | null;
  pendingSecret?: {
    name: string;
    secret?: string;
    key?: string;
    set?: boolean;
  } | null;
  pendingConfigChange?: PendingConfigChange | null;
  ci?: TaskCIStatus | null;
  ciFaults?: { kind?: string; summary?: string; detail?: string }[];
}

// One standing conversation (pkg/ui console_sessions.go
// ConsoleSessionSetting). Its conversation is the comments of its task.
export interface ConsoleSession {
  id: string;
  title: string;
  team?: string;
  taskId?: string;
  capabilities?: string[];
  missingShippedDefaults?: string[];
  networkAccess?: string;
  effectiveNetworkAccess?: string;
  tasksApproved?: boolean;
  keepRunning?: boolean;
  hosting?: string;
  reads?: string[];
  readsAll?: boolean;
  writes?: { repo: string; base?: string }[];
  googleChatSpace?: string;
  model?: string;
  crowded?: boolean;
  crowdedNote?: string;
}

// A capability this deployment offers (ui.Capability): what a picker
// lists. `ready: false` means it is not set up here.
export interface CapabilityOption {
  id: string;
  name?: string;
  description?: string;
  ready?: boolean;
  grantable?: boolean;
  default?: boolean;
  needs?: string;
}

export interface ConsoleSessionList {
  sessions: ConsoleSession[];
  created?: string;
  capabilities?: CapabilityOption[];
  networkAccessOptions?: string[];
}

export interface Notification {
  id: number;
  subject: string;
  family?: string;
  severity?: string;
  body?: string;
  raisedAt?: string;
  raisedBy?: string;
  raiseCount?: number;
  ackedAt?: string | null;
  actions?: string[];
  repo?: string;
  breaking?: unknown;
  investigationTaskId?: string;
}

export interface LocalScriptApproval {
  id: string;
  taskId: string;
  call?: string;
  tool?: string;
  script?: string;
  args?: string[];
  stdinBytes?: number;
  stdinPreview?: string;
  stdinTruncated?: boolean;
  stdinSha256?: string;
  status?: string;
  deadline?: string;
  requested?: string;
  answeredBy?: string;
  answeredAt?: string;
}

// DiffPreview is GET /api/tasks/{id}/diff-preview (pkg/ui/diff_preview.go):
// the first hunks of a task's branch against its base, already redacted.
export interface DiffPreviewLine {
  kind: "add" | "del" | "ctx";
  text: string;
}

export interface DiffPreviewHunk {
  header: string;
  lines: DiffPreviewLine[];
  linesOmitted?: number;
}

export interface DiffPreviewFile {
  path: string;
  oldPath?: string;
  status: "added" | "deleted" | "renamed" | "modified";
  binary?: boolean;
  additions: number;
  deletions: number;
  hunks: DiffPreviewHunk[];
  hunksOmitted?: number;
}

export interface DiffPreview {
  repo: string;
  base: string;
  head: string;
  mergeBase?: string;
  merged?: boolean;
  files: DiffPreviewFile[];
  totalFiles: number;
  totalHunks: number;
  truncated?: boolean;
}

export interface TreeNode {
  id: string;
  title: string;
  status: string;
  pullRequest?: string;
  done: boolean;
  root?: boolean;
  children?: TreeNode[];
}

export interface WorkTree {
  origin?: string;
  rootTask?: string;
  done: number;
  total: number;
  nodes: TreeNode[];
}

export interface TaskListPage {
  tasks: Task[];
  nextCursor?: string;
  truncated?: boolean;
  total?: number;
  merged?: number;
  sessions?: number;
}

export interface WirePrincipal {
  kind: string;
  id: string;
  source: string;
  label: string;
}

export interface WhoamiResponse {
  actor: WirePrincipal;
  onBehalfOf?: WirePrincipal | null;
  via?: string;
  source: string;
  verified: boolean;
  allowed: boolean;
}

export interface LogoutResponse {
  redirect?: string;
}

export interface BaseBranchGate {
  repo: string;
  branch?: string;
  judged: boolean;
  reason: string;
  lastRunAt?: string;
  lastRunAgeMS?: number;
  runInFlight?: boolean;
  mergesWaiting?: boolean;
  runID?: string;
  runDetail?: string;
  detail?: string;
}

export interface BaseBranchGatesResponse {
  gates: BaseBranchGate[];
  total?: number;
  truncated?: boolean;
  nextCursor?: string;
}

export interface Board {
  id: string;
  name: string;
  folder?: string;
  kind: string;
  description?: string;
  query?: string;
  orderKey?: number;
}

export interface CreateBoardRequest {
  name: string;
  kind?: string;
  folder?: string;
  description?: string;
  query?: string;
}
