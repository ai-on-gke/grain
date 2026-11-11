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

export interface GrainConfig {
  access?: AccessInfo | null;
  // The build stamp (api/version.ts); a string on older servers.
  version?: string | Record<string, unknown>;
  stateRepoSyncFailure?: unknown;
  [key: string]: unknown;
}

export interface PullRequestRef {
  number?: number;
  url?: string;
  [key: string]: unknown;
}

export interface Task {
  id: string;
  title: string;
  state: string;
  repo?: string;
  repos?: string[];
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
  humanReviewHold?: { category?: string }[];
  autoMerge?: boolean;
  pullRequest?: PullRequestRef | string | null;
  prClosedAt?: string;
  prMergedAt?: string;
  merged?: boolean;
  closedReason?: string;
  mergeQueueState?: string;
  mergeQueueStalledAt?: string;
  mergeQueueBlockedAt?: string;
  redBaseBranch?: unknown;
  stall?: string;
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
  branch?: string;
  // Tasks this one waits for (task detail).
  dependsOn?: string[];
  description?: string;
  body?: string;
  branch?: string;
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
}

export interface AccessRequestView {
  summary: string;
  reason: string;
  items: AccessRequestItem[];
  createdAt: string;
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
  attachments?: {
    id: string;
    filename: string;
    contentType?: string;
    size?: number;
  }[];
}

export interface Attempt {
  number: number;
  startedAt?: string;
  agentStartedAt?: string;
  promptTokens?: number;
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
  model?: string;
}

export interface TaskDetail extends Task {
  comments?: Comment[];
  attempts?: Attempt[];
  pendingAccess?: AccessRequestView | null;
  pendingSecret?: { name: string; secret?: string; key?: string } | null;
}

// One standing conversation (pkg/ui console_sessions.go
// ConsoleSessionSetting). Its conversation is the comments of its task.
export interface ConsoleSession {
  id: string;
  title: string;
  taskId?: string;
  capabilities?: string[];
  missingShippedDefaults?: string[];
  networkAccess?: string;
  effectiveNetworkAccess?: string;
  tasksApproved?: boolean;
  keepRunning?: boolean;
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
  tool?: string;
  script?: string;
  args?: string[];
  stdinBytes?: number;
  stdinPreview?: string;
  stdinTruncated?: boolean;
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
