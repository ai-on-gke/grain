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

import { DOCUMENT } from "@angular/common";
import {
  ChangeDetectionStrategy,
  Component,
  type OnChanges,
  type OnInit,
  type SimpleChanges,
  ViewEncapsulation,
  computed,
  effect,
  inject,
  input,
  signal,
  untracked,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import type { AgentPauseStatus, GrainConfig } from "../api/models";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { ThemeService } from "../services/theme.service";
import {
  ConfirmDialogComponent,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import {
  type ModelBackendLike,
  availabilityNote,
  containsModel,
  effortSupport,
  type EffortSupport,
  modelAvailability,
  modelGroups,
  parseDefaultModels,
} from "../shared/model-catalog";
import { copyToClipboard } from "../shared/clipboard";
import { ModelPickerComponent } from "../shared/model-picker.component";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import {
  type UsagePauseInput,
  parseResetMs,
  summarizeDispatchStatus,
} from "../shared/dispatch-status";
import {
  type SandboxCommitment,
  SandboxCommitmentNoteComponent,
} from "./sandbox-commitment-note.component";
import { formatInTimeZone, type SettingsSectionId } from "./settings-sections";
import { SettingsStateService, errText } from "./settings-state.service";

// SandboxBackendOptionDto is one entry of GET /api/settings
// sandboxBackendOptions (model.SandboxBackendOption).
/** POST /api/sandbox-probe's answer (pkg/ui SandboxProbeResult). */
interface SandboxProbeResult {
  sandbox: string;
  pod: string;
  access: string;
  stdout: string;
  stderr: string;
  exitCode: number;
}

interface SandboxBackendOptionDto {
  backend: string;
  available?: boolean;
  reason?: string;
}

// The same names and descriptions the classic Settings -> Sandboxes uses
// (ui/src/components/settings/SandboxesSection.jsx).
const SANDBOX_BACKEND_LABELS: Readonly<Record<string, string>> = {
  none: "None (control plane only)",
  host: "Host directories (no isolation)",
  container: "Local Docker containers",
  granule: "Granule virtual machines",
  kubernetes: "Kubernetes — container pod per run (no VM)",
  "kubernetes-granule": "Kubernetes — Granule VM per run",
};

const SANDBOX_BACKEND_DESCRIPTIONS: Readonly<Record<string, string>> = {
  none: "This daemon runs grain's control plane only (settings, the inbox, the config repository) and creates no sandboxes, so no task runs here: each dispatch fails at sandbox acquisition and says so. Meant for tests and walkthroughs.",
  host: "Each run executes directly in a directory on the daemon machine with no container or VM isolation.",
  container:
    "Each run executes in an unprivileged local Docker container sharing the host kernel (no VM).",
  granule:
    "Each run boots an isolated Granule microVM with its own guest kernel on this host.",
  kubernetes:
    "Each run executes in a Kubernetes container pod scheduled on the cluster, sharing the node kernel (no VM).",
  "kubernetes-granule":
    "Each run boots a Granule microVM with its own guest kernel over /dev/kvm inside a privileged Kubernetes pod.",
};

const NETWORK_ACCESS_LABELS: Readonly<Record<string, string>> = {
  open: "Open (full network)",
  none: "None (isolated)",
};

interface ModelSettingRow {
  model: string;
  effort?: string;
}

interface BackendEntry extends ModelBackendLike {
  id: string;
  name?: string;
  framework: string;
  models?: string[];
  authMode?: string;
  cliPath?: string;
  concurrency?: number;
  hostd?: string;
  disabled?: boolean;
}

// GET/PUT/DELETE /api/agent-backends/{id}/codex-auth: a Codex
// connection's own ChatGPT-subscription login, reported by presence.
// A connection never falls back to the deployment-wide login
// (configuredCodexAuthFile), so sharedSet is not shown here.
interface BackendCodexAuthStatus {
  enabled?: boolean;
  set?: boolean;
  refreshError?: string;
}

interface TokenBudgetRow {
  connection: string;
  model?: string;
  tokens: number;
  window: string;
}

interface UsageSettingsDto {
  mode?: string;
  minWorkers?: number;
  retentionDays?: number;
  budgets?: TokenBudgetRow[];
}

interface HostdInstallDto {
  release?: string;
  image?: string;
  lines?: string[];
  note?: string;
  command?: string;
}

interface HostdRow {
  name: string;
  owner?: string;
  state?: string;
  revoked_at?: string | null;
  revokedAt?: string | null;
  labels?: Record<string, string> | null;
  capacity?: number;
  shared?: boolean;
  key_fingerprint?: string;
  enrollment_code?: string;
  token_prefix?: string;
  last_seen_at?: string | null;
  // connection is what a host that dials in last said when it did
  // (hostd.RemoteStatus): absent for local and for one that has not
  // connected since the daemon started.
  connection?: {
    connected?: boolean;
    host?: string;
    principal?: string;
    build?: string;
  } | null;
  // update is a newer signed grain-agentd release the host's owner can
  // take by running command on the host (HostUpdate); absent when there
  // is none, and always for a root-installed `grain host`.
  update?: { build: string; available: string; command: string } | null;
}

// DeletedHostdRow is what deleting a host answers with: its name and the
// usage-limit pauses that went with it.
interface DeletedHostdRow {
  name: string;
  clearedPauses?: string[] | null;
}

function deletedHostdLine(d: DeletedHostdRow): string {
  let line = `Deleted host ${d.name}.`;
  if (d.clearedPauses?.length) {
    line += ` Cleared its usage-limit pauses: ${d.clearedPauses.join(", ")}.`;
  }
  return line;
}

// EnrollmentCodeRow is one entry of GET /api/hostds/enrollment-codes
// (HostEnrollmentCodeView): code and install are present only while the
// code is not revoked.
interface EnrollmentCodeRow {
  name: string;
  prefix?: string;
  code?: string;
  install?: HostdInstallDto;
  expires_at?: string | null;
  max_uses?: number;
  uses?: number;
  revoked_at?: string | null;
  default_labels?: Record<string, string> | null;
  default_capacity?: number;
  allowed_principals?: string[] | null;
}

// pickEnrollmentCode is the code Invite hosts shows: the one the operator
// chose while it still exists, else the active "default", else the first
// active code, else the first code at all.
export function pickEnrollmentCode(
  codes: EnrollmentCodeRow[],
  chosen: string,
): EnrollmentCodeRow | null {
  const active = codes.filter((c) => !c.revoked_at && c.code);
  return (
    codes.find((c) => c.name === chosen) ??
    active.find((c) => c.name === "default") ??
    active[0] ??
    codes[0] ??
    null
  );
}

// inviteLines is the enrollment snippet for one code: the daemon's
// rendered install lines (RegisterHostdInstall) when it sent them, or the
// standard /hostd/install --join command.
export function inviteLines(
  origin: string,
  code: EnrollmentCodeRow | null,
): string[] {
  if (!code || code.revoked_at) return [];
  if (Array.isArray(code.install?.lines) && code.install.lines.length > 0) {
    return code.install.lines;
  }
  if (!code.code) return [];
  return [`curl -fsSL ${origin}/hostd/install | sh -s -- --join ${code.code}`];
}

interface PendingHostdRow {
  id: string;
  hostname?: string;
  suggested_name?: string;
  enrolled_by?: string;
  os?: string;
  arch?: string;
  key_fingerprint?: string;
  default_labels?: Record<string, string> | null;
  default_capacity?: number;
  // rekey_hostd names the registered host a rekey request moves onto a
  // new key: accepting it keeps that host's name.
  rekey_hostd?: string;
  enrollment_code?: string;
  created_at?: string;
  expires_at?: string;
}

// PendingDraft is what the owner may change on a join request before
// accepting it: the host's name, labels and capacity.
interface PendingDraft {
  name: string;
  labels: string;
  capacity: string;
}

// HostdEdit is a host row's Edit (owner, labels, capacity) in progress.
interface HostdEdit {
  name: string;
  owner: string;
  labels: string;
  capacity: string;
}

// shortFingerprint keeps a SHA256 key fingerprint compact inside table cells;
// the full fingerprint stays in the element's title attribute.
function shortFingerprint(fp: string): string {
  if (!fp) return "";
  if (fp.length <= 24) return fp;
  return `${fp.slice(0, 20)}…`;
}

// hostdAuthSummary says how a host authenticates so key-bound, legacy token,
// and deprecated shared-token rows read apart at a glance.
export function hostdAuthSummary(
  h: HostdRow,
): { text: string; title?: string } | null {
  if (h.shared) return { text: "Shared token (deprecated)" };
  if (h.key_fingerprint) {
    const short = shortFingerprint(h.key_fingerprint);
    const via = h.enrollment_code ? ` · via ${h.enrollment_code}` : "";
    return { text: `${short}${via}`, title: h.key_fingerprint };
  }
  if (h.token_prefix) return { text: `token ${h.token_prefix}…` };
  return null;
}

// hostdConnectionLine says where a host connected from, as whom and on
// what build, as classic Settings → Hosts does: "" when there is nothing
// more to say than its status pill.
export function hostdConnectionLine(h: HostdRow): string {
  if (h.revoked_at || h.revokedAt || h.state === "revoked") return "";
  if (h.name === "local" && !h.shared) return "Co-located (socket)";
  const c = h.connection;
  if (!c) return "Never connected";
  if (!c.connected) return "";
  const parts: string[] = [];
  if (c.host) parts.push(`from ${c.host}`);
  if (c.principal) parts.push(`as ${c.principal}`);
  if (c.build) parts.push(c.build);
  return parts.join(" · ");
}

// parseHostCapacity reads a capacity field: a whole number of runs, 0
// (unlimited) when blank, so clearing the field clears the capacity.
export function parseHostCapacity(
  raw: string,
  label: string,
): { value: number; error?: string } {
  const s = raw.trim();
  if (!s) return { value: 0 };
  if (!/^\d+$/.test(s) || !Number.isSafeInteger(Number(s))) {
    return {
      value: 0,
      error: `${label} "${s}" must be a non-negative whole number`,
    };
  }
  return { value: Number(s) };
}

export function parseHostdLabels(raw: string): {
  labels: Record<string, string>;
  error?: string;
} {
  const trimmed = raw.trim();
  if (!trimmed) return { labels: {} };
  const out: Record<string, string> = {};
  for (const part of trimmed.split(/[\s,]+/).filter(Boolean)) {
    const eq = part.indexOf("=");
    if (eq <= 0 || eq === part.length - 1) {
      return {
        labels: {},
        error: `Host label "${part}" must be key=value (no spaces or commas).`,
      };
    }
    const k = part.slice(0, eq).trim();
    const v = part.slice(eq + 1).trim();
    if (!k || !v || v.includes("=")) {
      return {
        labels: {},
        error: `Host label "${part}" must be key=value.`,
      };
    }
    out[k] = v;
  }
  return { labels: out };
}

export function formatHostdLabels(
  labels?: Record<string, string> | null,
): string {
  if (!labels || typeof labels !== "object" || Array.isArray(labels)) {
    return "—";
  }
  const entries = Object.entries(labels);
  if (entries.length === 0) return "—";
  return entries.map(([k, v]) => `${k}=${v}`).join(", ");
}

// formatRefreshedAt is "" for a connection whose provider has never
// answered: the server sends Go's zero time ("0001-01-01T00:00:00Z") for
// it, which is no time anything was refreshed.
function formatRefreshedAt(iso?: string): string {
  if (!iso || iso.startsWith("0001-01-01")) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  if (d.getUTCFullYear() <= 1) return "";
  const months = [
    "Jan",
    "Feb",
    "Mar",
    "Apr",
    "May",
    "Jun",
    "Jul",
    "Aug",
    "Sep",
    "Oct",
    "Nov",
    "Dec",
  ];
  const hh = String(d.getUTCHours()).padStart(2, "0");
  const mm = String(d.getUTCMinutes()).padStart(2, "0");
  return `${months[d.getUTCMonth()]} ${d.getUTCDate()}, ${hh}:${mm} UTC`;
}

export interface PausedModelScopeTarget {
  connection: string;
  connectionName?: string;
  model?: string;
  label: string;
}

export function pauseScopeQuery(target: {
  connection: string;
  model?: string;
}): string {
  const params = new URLSearchParams();
  params.set("connection", target.connection.trim());
  const model = target.model?.trim();
  if (model) {
    params.set("model", model);
  }
  return params.toString();
}

export function collectPausedModelTargets(
  cfg: GrainConfig | null | undefined,
  usage: UsagePauseInput | null | undefined,
  nowMs = Date.now(),
): PausedModelScopeTarget[] {
  const unliftableConns = new Set<string>();
  const unliftableModels = new Set<string>();
  const wholeConns = new Map<string, string>();
  const modelEntries = new Map<
    string,
    { connection: string; connectionName?: string; model: string }
  >();

  for (const p of cfg?.agentPause?.pauses ?? []) {
    const conn = p.connection?.trim() ?? "";
    if (!conn) continue;
    const ms = parseResetMs(p.until, p.secondsRemaining, nowMs);
    if (ms !== null && ms <= nowMs) continue;
    const model = p.model?.trim() ?? "";
    const connLabel = p.connectionName?.trim() || conn;
    if (p.canLift === false) {
      if (!model) {
        unliftableConns.add(conn);
      } else {
        unliftableModels.add(`${conn}\0${model.toLowerCase()}`);
      }
      continue;
    }
    if (!model) {
      wholeConns.set(conn, connLabel);
    } else {
      const key = `${conn}\0${model.toLowerCase()}`;
      if (!modelEntries.has(key)) {
        modelEntries.set(key, {
          connection: conn,
          connectionName: p.connectionName?.trim() || undefined,
          model,
        });
      }
    }
  }

  for (const s of usage?.scopes ?? []) {
    const conn = s.connection?.trim() ?? "";
    if (!conn || !s.pausedUntil || unliftableConns.has(conn)) continue;
    const ms = parseResetMs(s.pausedUntil, undefined, nowMs);
    if (ms !== null && ms <= nowMs) continue;
    const model = s.model?.trim() ?? "";
    const connLabel = s.connectionName?.trim() || conn;
    if (!model) {
      if (!wholeConns.has(conn)) {
        wholeConns.set(conn, connLabel);
      }
      continue;
    }
    const key = `${conn}\0${model.toLowerCase()}`;
    if (unliftableModels.has(key) || wholeConns.has(conn)) continue;
    if (!modelEntries.has(key)) {
      modelEntries.set(key, {
        connection: conn,
        connectionName: s.connectionName?.trim() || undefined,
        model,
      });
    }
  }

  const targets: PausedModelScopeTarget[] = [];
  for (const [connection, label] of wholeConns) {
    targets.push({
      connection,
      ...(label !== connection ? { connectionName: label } : {}),
      label,
    });
  }
  for (const entry of modelEntries.values()) {
    if (wholeConns.has(entry.connection)) continue;
    const connLabel = entry.connectionName || entry.connection;
    targets.push({
      connection: entry.connection,
      ...(entry.connectionName ? { connectionName: entry.connectionName } : {}),
      model: entry.model,
      label: `${connLabel} · ${entry.model}`,
    });
  }
  return targets;
}

interface OvercommitBound {
  field: string;
  label: string;
  fallback: string;
  step: string;
  max?: number;
}

interface K8sField {
  key: string;
  label: string;
  placeholder: string;
  hint: string;
}

interface DiskFloorRow {
  path: string;
  totalMB: number;
  floorMB: number;
  reclaimMB: number;
}

interface ContractInfo {
  contract?: number;
  minimumContract?: number;
  contractLabel?: string;
  granuleContract?: number;
  granuleContractLabel?: string;
  guestBaseImage?: string;
  k8sSandboxBaseImage?: string;
}

function mib(mb: number): string {
  return mb >= 1024 ? `${(mb / 1024).toFixed(1)} GiB` : `${mb} MiB`;
}

@Component({
  selector: "g-settings-compute",
  imports: [
    PillSelectComponent,
    ConfirmDialogComponent,
    ModelPickerComponent,
    SandboxCommitmentNoteComponent,
  ],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @switch (section()) {
      @case ("models") {
        @if (modelsDispatchStatus(); as dp) {
          <div
            class="s-banner"
            [class.error]="dp.tone === 'error'"
            [class.warn]="dp.tone === 'warn'"
            role="status"
            data-testid="models-quota-banner"
          >
            <span>
              <strong>{{ dp.headline }}</strong>
              @if (dp.resetText) {
                <span class="s-num"> · {{ dp.resetText }}</span>
              }
              @if (dp.subline) {
                <span> — {{ dp.subline }}</span>
              }
            </span>
            @if (
              access.can("DELETE /api/pause") && pausedModelTargets().length > 0
            ) {
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                data-testid="resume-paused-models-btn"
                [disabled]="resumingModels()"
                (click)="resumePausedModels()"
              >
                Resume
              </button>
            }
          </div>
        }
        <div class="s-card" data-testid="card-model-defaults">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">
                Default model, fallbacks &amp; reasoning effort
              </h3>
              <p class="s-card-desc">
                The model a task runs on when it does not pin one, the fallback
                models tried after it, and the reasoning effort each runs at.
                The choices are the models your connections advertise.
              </p>
            </div>
          </div>
          <div class="s-grid">
            <div class="s-field">
              <span class="s-label">Deployment default model</span>
              <g-model-picker
                testid="picker-default-model"
                ariaLabel="Deployment default model"
                placeholder="Not set: each connection's own default"
                [groups]="modelGroups()"
                [value]="modelChain()[0] || ''"
                [disabled]="!canEditChain()"
                [refreshable]="canRefreshModels() && anyDiscovers()"
                [refreshing]="refreshingModels()"
                [refreshNote]="refreshNote()"
                (refresh)="refreshDiscoveredModels()"
                (valueChange)="setDefaultModel($event)"
              />
            </div>
            @if (modelChain()[0]; as dm) {
              <div class="s-field" data-testid="default-model-effort">
                <span class="s-label">Reasoning effort</span>
                @if (effortFor(dm); as es) {
                  @if (es.separate || modelEffort(dm)) {
                    <g-pill-select
                      testid="effort-default-model"
                      [ariaLabel]="'Reasoning effort for ' + dm"
                      align="left"
                      [options]="effortOptionsFor(dm)"
                      [value]="modelEffort(dm)"
                      [disabled]="!canWriteDeployment()"
                      (valueChange)="setModelEffort(dm, $event)"
                    />
                  } @else {
                    <span class="s-hint" data-testid="effort-default-model-none"
                      >{{ es.reason }}.</span
                    >
                  }
                }
              </div>
            }
          </div>

          <div class="s-field mp-fallbacks" data-testid="fallback-models">
            <span class="s-label">Fallback models</span>
            <span class="s-hint">
              Tried in order after the default model when it is paused after a
              usage limit, its connections are at their concurrency limit, or no
              enabled connection advertises it. A task that pins its own model
              never falls back.
            </span>
            @if (fallbackModels().length > 0) {
              <ol class="mp-chain" aria-label="Fallback models, in order">
                @for (m of fallbackModels(); track m) {
                  <li
                    class="mp-chain-row"
                    [attr.data-testid]="'fallback-row-' + $index"
                  >
                    <span class="mp-chain-pos" aria-hidden="true">{{
                      $index + 1
                    }}</span>
                    <span class="mp-chain-model">{{ m }}</span>
                    @if (modelNote(m)) {
                      <span class="mp-flag">{{ modelNote(m) }}</span>
                    }
                    <span class="mp-chain-effort">
                      @if (effortFor(m); as es) {
                        @if (es.separate || modelEffort(m)) {
                          <g-pill-select
                            [testid]="'effort-fallback-' + $index"
                            [ariaLabel]="'Reasoning effort for ' + m"
                            prefix="Effort"
                            align="left"
                            [options]="effortOptionsFor(m)"
                            [value]="modelEffort(m)"
                            [disabled]="!canWriteDeployment()"
                            (valueChange)="setModelEffort(m, $event)"
                          />
                        } @else {
                          <span
                            class="mp-chain-note"
                            [attr.data-testid]="
                              'effort-fallback-none-' + $index
                            "
                            >{{ es.reason }}</span
                          >
                        }
                      }
                    </span>
                    <span class="mp-chain-actions">
                      <button
                        type="button"
                        class="btn-text s-btn-sm"
                        [attr.aria-label]="'Move ' + m + ' up'"
                        [attr.data-testid]="'fallback-up-' + $index"
                        [disabled]="$first || !canEditChain()"
                        (click)="moveFallback(m, -1)"
                      >
                        Up
                      </button>
                      <button
                        type="button"
                        class="btn-text s-btn-sm"
                        [attr.aria-label]="'Move ' + m + ' down'"
                        [attr.data-testid]="'fallback-down-' + $index"
                        [disabled]="$last || !canEditChain()"
                        (click)="moveFallback(m, 1)"
                      >
                        Down
                      </button>
                      <button
                        type="button"
                        class="btn-text s-btn-sm"
                        [attr.aria-label]="'Remove ' + m"
                        [attr.data-testid]="'fallback-remove-' + $index"
                        [disabled]="!canEditChain()"
                        (click)="removeFallback(m)"
                      >
                        Remove
                      </button>
                    </span>
                  </li>
                }
              </ol>
            }
            <g-model-picker
              testid="picker-add-fallback"
              ariaLabel="Add fallback model"
              [placeholder]="
                modelChain().length
                  ? 'Add fallback model'
                  : 'Choose a default model first'
              "
              [groups]="modelGroups()"
              [exclude]="modelChain()"
              [disabled]="!canEditChain() || modelChain().length === 0"
              [refreshable]="canRefreshModels() && anyDiscovers()"
              [refreshing]="refreshingModels()"
              [refreshNote]="refreshNote()"
              (refresh)="refreshDiscoveredModels()"
              (valueChange)="addFallback($event)"
            />
          </div>

          @if (chainWarning()) {
            <div
              class="s-banner warn"
              role="status"
              data-testid="default-model-warning"
            >
              {{ chainWarning() }}
            </div>
          }

          <div class="s-subsection">
            <div class="s-subhead">
              <h4 class="s-subhead-title">Reasoning effort for other models</h4>
            </div>
            <span class="s-hint">
              For a model a task pins itself, outside the default and fallback
              list above.
            </span>
            @if (otherModelSettingsRows().length > 0) {
              <div class="s-table-wrap">
                <table
                  class="s-table"
                  aria-label="Per-model reasoning settings"
                >
                  <thead>
                    <tr>
                      <th>Model</th>
                      <th>Reasoning effort</th>
                      <th class="s-actions-cell">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    @for (ms of otherModelSettingsRows(); track ms.model) {
                      <tr [attr.data-testid]="'model-setting-row-' + ms.model">
                        <td class="s-mono">{{ ms.model }}</td>
                        <td>
                          {{ ms.effort || "default" }}
                          @if (ms.effort && effortFor(ms.model); as es) {
                            @if (!es.separate) {
                              <span
                                class="s-meta"
                                [attr.data-testid]="
                                  'model-setting-unused-' + ms.model
                                "
                                >(not applied: {{ es.reason }}.)</span
                              >
                            }
                          }
                        </td>
                        <td class="s-actions-cell">
                          <div class="s-row-actions">
                            <button
                              type="button"
                              class="btn-text s-btn-sm"
                              [disabled]="!canWriteDeployment()"
                              [attr.aria-label]="
                                'Remove effort for ' + ms.model
                              "
                              (click)="setModelEffort(ms.model, '')"
                            >
                              Remove
                            </button>
                          </div>
                        </td>
                      </tr>
                    }
                  </tbody>
                </table>
              </div>
            }

            <div class="s-inline">
              <g-model-picker
                class="mp-inline"
                testid="picker-model-setting-model"
                ariaLabel="Model for a reasoning effort setting"
                placeholder="Choose a model"
                [groups]="modelGroups()"
                [exclude]="modelChain()"
                [value]="newModelSettingName()"
                [disabled]="!canWriteDeployment()"
                (valueChange)="pickModelSettingName($event)"
              />
              <g-pill-select
                testid="select-model-setting-effort"
                ariaLabel="Reasoning effort"
                placeholder="Reasoning effort"
                align="left"
                [options]="adderEffortOptions()"
                [value]="newModelSettingEffort()"
                [disabled]="
                  !canWriteDeployment() ||
                  !newModelSettingName().trim() ||
                  !effortFor(newModelSettingName()).separate
                "
                (valueChange)="newModelSettingEffort.set($event)"
              />
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                data-testid="add-model-setting-btn"
                [disabled]="!canAddModelSetting()"
                (click)="addModelSetting()"
              >
                Set model effort
              </button>
            </div>
            @if (newModelSettingName(); as nm) {
              @if (effortFor(nm); as es) {
                @if (!es.separate) {
                  <span class="s-hint" data-testid="model-setting-effort-none"
                    >{{ es.reason }}.</span
                  >
                }
              }
            }
          </div>
        </div>

        @if (!modelsConfigured()) {
          <div class="s-banner readonly">
            <span
              >No connections configured yet. Tasks run only on configured
              connections and wait until at least one is available.</span
            >
          </div>
        }
        <div class="s-card" data-testid="card-model-backends">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">
                Model connections &amp; discovered models
              </h3>
              <p class="s-card-desc">
                Named Vertex AI, Anthropic, or OpenAI-compatible backends with
                dedicated credentials and model discovery.
              </p>
            </div>
            <button
              type="button"
              class="btn-tonal s-btn-sm"
              data-testid="refresh-discovered-models"
              [disabled]="!access.can('GET /api/agent-models')"
              (click)="loadDiscoveredModels()"
            >
              Discover models
            </button>
          </div>

          @if (backends().length === 0) {
            <p class="s-card-desc" data-testid="models-backends-empty">
              No model connections configured yet. Add a connection below to
              advertise models for agent runs.
            </p>
          } @else {
            <div class="s-table-wrap s-table-pinned">
              <table class="s-table" aria-label="Model connections">
                <thead>
                  <tr>
                    <th>Connection</th>
                    <th>Framework</th>
                    <th>Placement &amp; discovery</th>
                    <th class="s-actions-cell">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  @for (b of backends(); track b.id) {
                    <tr [attr.data-testid]="'backend-row-' + b.id">
                      <td>
                        <div class="s-cell-stack">
                          <span class="s-cell-title">{{ b.name || b.id }}</span>
                          @if (b.name) {
                            <span class="s-meta-mono">{{ b.id }}</span>
                          }
                        </div>
                      </td>
                      <td>
                        <span class="status-pill neutral">{{
                          b.framework
                        }}</span>
                      </td>
                      <td>
                        <div>
                          {{ b.authMode || "default"
                          }}{{ b.hostd ? " · host " + b.hostd : ""
                          }}{{ b.concurrency ? " · max " + b.concurrency : "" }}
                        </div>
                        @if (b.models?.length) {
                          <div class="s-meta">
                            Pinned:
                            <span class="s-mono">{{
                              b.models?.join(", ")
                            }}</span>
                          </div>
                        }
                        @if (b.discovered) {
                          <details class="s-disc-details">
                            <summary>
                              {{ b.discovered.models?.length || 0 }} models
                              discovered
                              @if (
                                formatRefreshedAt(b.discovered.refreshedAt);
                                as refLbl
                              ) {
                                <span
                                  class="s-meta"
                                  [attr.title]="b.discovered.refreshedAt"
                                >
                                  · {{ refLbl }}</span
                                >
                              }
                            </summary>
                            <div class="s-disc-models">
                              {{ b.discovered.models?.join(", ") || "None" }}
                            </div>
                          </details>
                          @if (b.discovered.error) {
                            <div
                              class="status-pill error"
                              style="margin-top: 6px"
                            >
                              {{ b.discovered.error }}
                            </div>
                          }
                        }
                      </td>
                      <td class="s-actions-cell">
                        <div class="s-row-actions">
                          <button
                            type="button"
                            class="btn-tonal s-btn-sm"
                            [attr.data-testid]="'test-backend-' + b.id"
                            [attr.aria-label]="'Test saved connection ' + b.id"
                            [disabled]="
                              connectionTests()[b.id]?.pending ||
                              !access.can(
                                'POST /api/agent-keys/{framework}/check'
                              )
                            "
                            (click)="testBackend(b)"
                          >
                            {{
                              connectionTests()[b.id]?.pending
                                ? "Testing…"
                                : "Test connection"
                            }}
                          </button>
                          @if (discovers(b) && canRefreshModels()) {
                            <button
                              type="button"
                              class="btn-tonal s-btn-sm"
                              [attr.data-testid]="'refresh-backend-' + b.id"
                              [attr.aria-label]="'Refresh models of ' + b.id"
                              [disabled]="refreshingModels()"
                              (click)="refreshDiscoveredModels(b.id)"
                            >
                              Refresh
                            </button>
                          }
                          @if (b.authMode !== "ambient") {
                            <button
                              type="button"
                              class="btn-tonal s-btn-sm"
                              [attr.data-testid]="'toggle-cred-' + b.id"
                              [attr.aria-expanded]="
                                openCredBackendId() === b.id
                              "
                              [attr.aria-label]="
                                'Manage credential for ' + b.id
                              "
                              (click)="toggleCredEditor(b.id)"
                            >
                              Credential
                            </button>
                          }
                          <button
                            type="button"
                            class="btn-tonal s-btn-sm"
                            [attr.data-testid]="'edit-backend-' + b.id"
                            [attr.aria-label]="'Edit connection ' + b.id"
                            [disabled]="!canManageBackends()"
                            (click)="editBackend(b)"
                          >
                            Edit
                          </button>
                          <button
                            type="button"
                            class="btn-text s-btn-sm"
                            [attr.data-testid]="'remove-backend-' + b.id"
                            [attr.aria-label]="'Remove connection ' + b.id"
                            [disabled]="
                              !access.can(
                                'DELETE /api/deployment/agent-backends/{backendId}'
                              ) || state.configRepoOnly()
                            "
                            (click)="startDeleteBackend(b.id)"
                          >
                            Remove
                          </button>
                        </div>
                      </td>
                    </tr>
                    @if (connectionTests()[b.id]; as result) {
                      <tr class="s-subrow">
                        <td colspan="4">
                          <div
                            role="status"
                            [attr.data-testid]="'test-result-' + b.id"
                            [class]="
                              's-banner ' +
                              (result.pending ? '' : result.ok ? 'ok' : 'error')
                            "
                          >
                            {{ result.detail }}
                          </div>
                        </td>
                      </tr>
                    }
                    @if (
                      openCredBackendId() === b.id && b.authMode !== "ambient"
                    ) {
                      <tr
                        class="s-subrow"
                        [attr.data-testid]="'backend-cred-row-' + b.id"
                      >
                        <td colspan="4">
                          @if (isCodexSubscription(b)) {
                            <div
                              class="s-field s-subrow-pin"
                              [attr.data-testid]="'codex-auth-' + b.id"
                            >
                              <div class="s-inline">
                                <label
                                  class="s-label"
                                  [attr.for]="'codex-auth-input-' + b.id"
                                  >ChatGPT subscription login (auth.json) for
                                  <span class="s-mono">{{ b.id }}</span></label
                                >
                                <span
                                  [class]="
                                    'status-pill ' +
                                    (codexAuthStatus()[b.id]?.set
                                      ? 'healthy'
                                      : 'neutral')
                                  "
                                  role="status"
                                  [attr.data-testid]="
                                    'codex-auth-status-' + b.id
                                  "
                                >
                                  {{ codexAuthStatusLabel(b.id) }}
                                </span>
                              </div>
                              @if (codexAuthStatus()[b.id]?.refreshError) {
                                <div
                                  class="status-pill error"
                                  role="alert"
                                  [attr.data-testid]="
                                    'codex-auth-refresh-error-' + b.id
                                  "
                                >
                                  {{ codexAuthStatus()[b.id]?.refreshError }}
                                </div>
                              }
                              <p class="s-hint">
                                Paste the auth.json from a Codex ChatGPT login.
                                Only this connection uses it, and grain keeps
                                its tokens refreshed. API keys are ignored in
                                this mode. JSON is checked here; Codex validates
                                authentication when it runs.
                              </p>
                              <textarea
                                class="s-textarea s-mono"
                                rows="3"
                                autocomplete="off"
                                spellcheck="false"
                                data-lpignore="true"
                                data-1p-ignore="true"
                                [attr.id]="'codex-auth-input-' + b.id"
                                [attr.data-testid]="'input-codex-auth-' + b.id"
                                placeholder='{"tokens": {...}}'
                                [value]="codexAuthDrafts()[b.id] || ''"
                                [disabled]="
                                  codexAuthStatus()[b.id]?.enabled === false ||
                                  !access.can(
                                    'PUT /api/agent-backends/{id}/codex-auth'
                                  )
                                "
                                (input)="
                                  setCodexAuthDraft(
                                    b.id,
                                    $any($event.target).value
                                  )
                                "
                              ></textarea>
                              @if (codexAuthErrors()[b.id]) {
                                <p
                                  role="alert"
                                  class="s-banner error"
                                  [attr.data-testid]="
                                    'codex-auth-error-' + b.id
                                  "
                                >
                                  {{ codexAuthErrors()[b.id] }}
                                </p>
                              }
                              <div class="s-inline">
                                <button
                                  type="button"
                                  class="btn-primary s-btn-sm"
                                  [attr.data-testid]="'save-codex-auth-' + b.id"
                                  [disabled]="
                                    codexAuthBusy() ||
                                    !(codexAuthDrafts()[b.id] || '').trim() ||
                                    codexAuthStatus()[b.id]?.enabled ===
                                      false ||
                                    !access.can(
                                      'PUT /api/agent-backends/{id}/codex-auth'
                                    )
                                  "
                                  (click)="saveCodexAuth(b.id)"
                                >
                                  Save login
                                </button>
                                <button
                                  type="button"
                                  class="btn-tonal s-btn-sm"
                                  [attr.data-testid]="
                                    'clear-codex-auth-' + b.id
                                  "
                                  [attr.aria-label]="
                                    'Clear subscription login for ' + b.id
                                  "
                                  [disabled]="
                                    codexAuthBusy() ||
                                    !codexAuthStatus()[b.id]?.set ||
                                    !access.can(
                                      'DELETE /api/agent-backends/{id}/codex-auth'
                                    )
                                  "
                                  (click)="confirmClearCodexAuth(b.id)"
                                >
                                  Clear login
                                </button>
                                <button
                                  type="button"
                                  class="btn-text s-btn-sm"
                                  (click)="openCredBackendId.set(null)"
                                >
                                  Close
                                </button>
                              </div>
                            </div>
                          } @else {
                            <form
                              class="s-cred-bar s-subrow-pin"
                              autocomplete="off"
                              (submit)="
                                $event.preventDefault(); saveBackendCred(b.id)
                              "
                            >
                              <span class="s-label"
                                >Credential for
                                <span class="s-mono">{{ b.id }}</span></span
                              >
                              <input
                                class="s-input s-input-inline"
                                type="password"
                                [attr.name]="'grain-backend-cred-' + b.id"
                                autocomplete="new-password"
                                data-lpignore="true"
                                data-1p-ignore="true"
                                [attr.aria-label]="'Credential for ' + b.id"
                                placeholder="Paste API key or secret…"
                                [value]="backendCredDrafts()[b.id] || ''"
                                [disabled]="
                                  !access.can(
                                    'PUT /api/agent-backends/{id}/credential'
                                  )
                                "
                                (input)="
                                  setBackendCredDraft(
                                    b.id,
                                    $any($event.target).value
                                  )
                                "
                              />
                              <button
                                type="submit"
                                class="btn-primary s-btn-sm"
                                [disabled]="
                                  !(backendCredDrafts()[b.id] || '').trim() ||
                                  !access.can(
                                    'PUT /api/agent-backends/{id}/credential'
                                  )
                                "
                              >
                                Save secret
                              </button>
                              <button
                                type="button"
                                class="btn-tonal s-btn-sm"
                                [attr.data-testid]="'clear-cred-' + b.id"
                                [attr.aria-label]="
                                  'Clear credential for ' + b.id
                                "
                                [disabled]="
                                  !access.can(
                                    'DELETE /api/agent-backends/{id}/credential'
                                  )
                                "
                                (click)="confirmClearBackendCred(b.id)"
                              >
                                Clear stored secret
                              </button>
                              <button
                                type="button"
                                class="btn-text s-btn-sm"
                                (click)="openCredBackendId.set(null)"
                              >
                                Close
                              </button>
                            </form>
                          }
                        </td>
                      </tr>
                    }
                  }
                </tbody>
              </table>
            </div>
          }

          <div class="s-subsection">
            <div class="s-subhead">
              <h4 class="s-subhead-title">
                {{
                  editingBackendId()
                    ? "Edit connection: " + editingBackendId()
                    : "Add a model connection"
                }}
              </h4>
              @if (editingBackendId()) {
                <button
                  type="button"
                  class="btn-text s-btn-sm"
                  (click)="cancelEditBackend()"
                >
                  Cancel edit
                </button>
              }
            </div>
            <p class="s-hint">
              Save the connection and its credential first, then use Test
              connection on its row. Tests use saved settings and never save
              edits. Ambient, Vertex and Codex subscription authentication
              cannot be tested here.
            </p>
            <div class="s-grid">
              <div class="s-field">
                <label class="s-label" for="nb-id">Connection ID</label>
                <input
                  id="nb-id"
                  class="s-input s-mono"
                  type="text"
                  data-testid="input-new-backend-id"
                  placeholder="claude-team-1"
                  [value]="newBackendId()"
                  [disabled]="!canManageBackends()"
                  (input)="newBackendId.set($any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <label class="s-label" for="nb-name">Display name</label>
                <input
                  id="nb-name"
                  class="s-input"
                  type="text"
                  data-testid="input-new-backend-name"
                  placeholder="Team Claude"
                  [value]="newBackendName()"
                  [disabled]="!canManageBackends()"
                  (input)="newBackendName.set($any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <span class="s-label">Framework</span>
                <g-pill-select
                  testid="select-new-backend-fw"
                  ariaLabel="Backend framework"
                  align="left"
                  [options]="frameworkOptions"
                  [value]="newBackendFw()"
                  [disabled]="!canManageBackends()"
                  (valueChange)="setNewBackendFw($event)"
                />
              </div>
              <div class="s-field">
                <span class="s-label">Auth mode</span>
                <g-pill-select
                  testid="select-new-backend-auth"
                  ariaLabel="Backend auth mode"
                  align="left"
                  [options]="authModeOptions()"
                  [value]="newBackendAuthMode()"
                  [disabled]="!canManageBackends()"
                  (valueChange)="newBackendAuthMode.set($event)"
                />
              </div>
              <div class="s-field">
                <label class="s-label" for="nb-models"
                  >Pinned models (comma-separated)</label
                >
                <input
                  id="nb-models"
                  class="s-input s-mono"
                  type="text"
                  data-testid="input-new-backend-models"
                  placeholder="empty = discover all"
                  [value]="newBackendModels()"
                  [disabled]="!canManageBackends()"
                  (input)="newBackendModels.set($any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <label class="s-label" for="nb-hostd"
                  >Host name or label selector</label
                >
                <input
                  id="nb-hostd"
                  class="s-input s-mono"
                  type="text"
                  data-testid="input-new-backend-hostd"
                  placeholder="workstation-01 or env=dev"
                  [value]="newBackendHostd()"
                  [disabled]="!canManageBackends()"
                  (input)="newBackendHostd.set($any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <label class="s-label" for="nb-clipath"
                  >CLI path override</label
                >
                <input
                  id="nb-clipath"
                  class="s-input s-mono"
                  type="text"
                  data-testid="input-new-backend-clipath"
                  placeholder="/usr/local/bin/agy"
                  [value]="newBackendCliPath()"
                  [disabled]="!canManageBackends()"
                  (input)="newBackendCliPath.set($any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <label class="s-label" for="nb-conc"
                  >Concurrency limit (0 = default)</label
                >
                <input
                  id="nb-conc"
                  class="s-input s-num"
                  type="number"
                  min="0"
                  data-testid="input-new-backend-concurrency"
                  [value]="newBackendConcurrency()"
                  [disabled]="!canManageBackends()"
                  (input)="
                    newBackendConcurrency.set(Number($any($event.target).value))
                  "
                />
              </div>
            </div>
            <div class="s-inline">
              <button
                type="button"
                class="btn-tonal"
                data-testid="add-model-backend"
                [disabled]="!newBackendId().trim() || !canManageBackends()"
                (click)="saveBackend()"
              >
                Save model connection
              </button>
            </div>
          </div>

          @if (discoveredModels().length > 0) {
            <div class="s-field">
              <span class="s-label"
                >Discovered models ({{ discoveredModels().length }})</span
              >
              <div class="s-inline" data-testid="discovered-models-list">
                @for (m of discoveredModels(); track m) {
                  <span class="status-pill neutral s-mono">{{ m }}</span>
                }
              </div>
            </div>
          }
        </div>

        <div class="s-card" id="usage" data-testid="card-token-budget">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Token budget &amp; usage guardrails</h3>
              <p class="s-card-desc">
                Governor mode, worker floor, retention days, and per-connection
                token budgets (PUT /api/usage/settings).
              </p>
            </div>
            <button
              type="button"
              class="btn-primary s-btn-sm"
              data-testid="save-token-budget"
              [disabled]="!canManageUsage()"
              (click)="saveUsageSettings()"
            >
              Save usage settings
            </button>
          </div>

          <div class="s-grid">
            <div class="s-field">
              <span class="s-label">Budget enforcement mode</span>
              <g-pill-select
                testid="select-usage-mode"
                ariaLabel="Budget enforcement mode"
                align="left"
                [options]="usageModeOptions"
                [value]="usageMode()"
                [disabled]="!canManageUsage()"
                (valueChange)="usageMode.set($event)"
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="us-min-workers"
                >Minimum workers floor</label
              >
              <input
                id="us-min-workers"
                class="s-input s-num"
                type="number"
                min="1"
                data-testid="input-usage-min-workers"
                [value]="usageMinWorkers()"
                [disabled]="!canManageUsage()"
                (input)="usageMinWorkers.set(Number($any($event.target).value))"
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="us-retention"
                >Usage retention (days)</label
              >
              <input
                id="us-retention"
                class="s-input s-num"
                type="number"
                min="1"
                data-testid="input-usage-retention-days"
                [value]="usageRetentionDays()"
                [disabled]="!canManageUsage()"
                (input)="
                  usageRetentionDays.set(Number($any($event.target).value))
                "
              />
            </div>
          </div>

          <div class="s-subsection">
            <div class="s-subhead">
              <h4 class="s-subhead-title">Per-connection token budgets</h4>
            </div>
            @if (usageBudgets().length > 0) {
              <div class="s-table-wrap">
                <table class="s-table" aria-label="Token budgets">
                  <thead>
                    <tr>
                      <th>Connection</th>
                      <th>Model</th>
                      <th>Tokens</th>
                      <th>Window</th>
                      <th class="s-actions-cell">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    @for (b of usageBudgets(); track $index; let idx = $index) {
                      <tr [attr.data-testid]="'usage-budget-row-' + idx">
                        <td class="s-mono">{{ b.connection }}</td>
                        <td class="s-mono">{{ b.model || "*" }}</td>
                        <td class="s-num">{{ b.tokens }}</td>
                        <td class="s-num">{{ b.window }}</td>
                        <td class="s-actions-cell">
                          <div class="s-row-actions">
                            <button
                              type="button"
                              class="btn-text s-btn-sm"
                              [disabled]="
                                !access.can('PUT /api/usage/settings')
                              "
                              (click)="removeUsageBudget(idx)"
                            >
                              Remove
                            </button>
                          </div>
                        </td>
                      </tr>
                    }
                  </tbody>
                </table>
              </div>
            }

            <div class="s-inline">
              <input
                class="s-input s-input-inline s-mono"
                type="text"
                aria-label="Budget connection ID"
                data-testid="input-budget-connection"
                placeholder="connection-id"
                [value]="newBudgetConn()"
                [disabled]="!canManageUsage()"
                (input)="newBudgetConn.set($any($event.target).value)"
              />
              <g-model-picker
                class="mp-inline"
                testid="picker-budget-model"
                ariaLabel="Budget model (optional)"
                placeholder="model (optional)"
                [groups]="modelGroups()"
                [value]="newBudgetModel()"
                [disabled]="!canManageUsage()"
                (valueChange)="newBudgetModel.set($event)"
              />
              <input
                class="s-input s-input-inline s-num"
                type="number"
                min="1"
                aria-label="Token limit"
                data-testid="input-budget-tokens"
                placeholder="1000000"
                [value]="newBudgetTokens()"
                [disabled]="!canManageUsage()"
                (input)="newBudgetTokens.set($any($event.target).value)"
              />
              <input
                class="s-input s-input-inline s-num"
                type="text"
                aria-label="Budget window duration"
                data-testid="input-budget-window"
                placeholder="24h"
                [value]="newBudgetWindow()"
                [disabled]="!canManageUsage()"
                (input)="newBudgetWindow.set($any($event.target).value)"
              />
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                data-testid="add-usage-budget-btn"
                [disabled]="
                  !newBudgetConn().trim() ||
                  !(Number(newBudgetTokens()) > 0) ||
                  !newBudgetWindow().trim() ||
                  !canManageUsage()
                "
                (click)="addUsageBudget()"
              >
                Add budget
              </button>
              @if (usageSummary()) {
                <span class="s-hint s-num">{{ usageSummary() }}</span>
              }
            </div>
          </div>
        </div>
      }

      @case ("sandboxes") {
        <div class="s-card" data-testid="card-sandbox-sizing">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Sandbox backend &amp; sizing</h3>
              <p class="s-card-desc">
                Controls isolated task execution environments and per-sandbox
                compute limits.
              </p>
            </div>
            @if (canProbeSandbox()) {
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                data-testid="run-sandbox-probe"
                [disabled]="probing()"
                (click)="runSandboxProbe()"
              >
                {{ probing() ? "Probing…" : "Probe sandbox runtime" }}
              </button>
            }
          </div>
          @if (probing()) {
            <div
              class="s-banner readonly"
              role="status"
              data-testid="sandbox-probe-running"
            >
              Building a throwaway sandbox and running
              <code>true</code> in it. This can take a few minutes.
            </div>
          }
          @if (probeResult(); as res) {
            <div
              class="s-banner"
              [class.ok]="res.exitCode === 0"
              [class.warn]="res.exitCode !== 0"
              role="status"
              data-testid="sandbox-probe-result"
            >
              <div class="s-probe-result">
                <span data-testid="sandbox-probe-summary">
                  {{
                    res.exitCode === 0
                      ? "Sandbox ready:"
                      : "The probe command failed:"
                  }}
                  <code>{{ res.sandbox }}</code>
                  {{ res.access ? "over " + res.access + " " : "" }}exited
                  <span class="s-num" data-testid="sandbox-probe-exit">{{
                    res.exitCode
                  }}</span>
                </span>
                <span class="s-label">stdout</span>
                <pre class="s-code" data-testid="sandbox-probe-stdout">{{
                  res.stdout || "(empty)"
                }}</pre>
                @if (res.stderr) {
                  <span class="s-label">stderr</span>
                  <pre class="s-code" data-testid="sandbox-probe-stderr">{{
                    res.stderr
                  }}</pre>
                }
              </div>
              <button
                type="button"
                class="btn-text s-btn-sm"
                data-testid="sandbox-probe-dismiss"
                (click)="probeResult.set(null)"
              >
                Dismiss
              </button>
            </div>
          }
          <div class="s-grid">
            <div class="s-field">
              <span class="s-label">Sandbox backend</span>
              <g-pill-select
                testid="select-sandbox-backend"
                ariaLabel="Sandbox backend"
                align="left"
                [options]="sandboxBackendOptions()"
                [value]="state.val('sandboxBackend', 'container')"
                [disabled]="!canWriteSettings()"
                (valueChange)="state.setField('sandboxBackend', $event)"
              />
              @if (sandboxBackendDescription()) {
                <span class="s-hint" data-testid="sandbox-backend-desc">{{
                  sandboxBackendDescription()
                }}</span>
              }
            </div>
            <div class="s-field">
              <span class="s-label">Network access</span>
              <g-pill-select
                testid="select-sandbox-network"
                ariaLabel="Sandbox network access"
                align="left"
                [options]="networkOptions()"
                [value]="state.val('networkAccess', 'open')"
                [disabled]="!canWriteSettings()"
                (valueChange)="state.setField('networkAccess', $event)"
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="sb-cpus">vCPUs per sandbox</label>
              <input
                id="sb-cpus"
                class="s-input s-num"
                type="number"
                min="0"
                data-testid="input-sandbox-cpus"
                [value]="state.defaultedNumber('sandboxCpus')"
                [placeholder]="state.defaultHint('sandboxCpus')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setNumber('sandboxCpus', $any($event.target).value)
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="sb-mem">Memory (MiB)</label>
              <input
                id="sb-mem"
                class="s-input s-num"
                type="number"
                min="0"
                data-testid="input-sandbox-memory"
                [value]="state.defaultedNumber('sandboxMemoryMb')"
                [placeholder]="state.defaultHint('sandboxMemoryMb')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setNumber('sandboxMemoryMb', $any($event.target).value)
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="sb-disk">Disk (GiB)</label>
              <input
                id="sb-disk"
                class="s-input s-num"
                type="number"
                min="0"
                data-testid="input-sandbox-disk"
                [value]="state.defaultedNumber('sandboxDiskGb')"
                [placeholder]="state.defaultHint('sandboxDiskGb')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setNumber('sandboxDiskGb', $any($event.target).value)
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="sb-ttl"
                >Session sandbox idle TTL</label
              >
              <input
                id="sb-ttl"
                class="s-input s-num"
                type="text"
                [placeholder]="
                  state.defaultHint('sessionSandboxIdleTtl', '30m')
                "
                [value]="state.val('sessionSandboxIdleTtl', '')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField(
                    'sessionSandboxIdleTtl',
                    $any($event.target).value
                  )
                "
              />
            </div>
          </div>
        </div>

        <div
          class="s-card"
          id="sandbox-budget"
          data-testid="card-sandbox-overcommit"
        >
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Sandbox budget</h3>
              <p class="s-card-desc">
                How much of this host agent and CI sandboxes may be promised,
                and the free disk kept back on every volume.
              </p>
            </div>
          </div>
          <div class="s-grid">
            <div class="s-field">
              <span class="s-label">Overcommit governor</span>
              <g-pill-select
                testid="select-overcommit-mode"
                ariaLabel="Overcommit governor"
                align="left"
                [options]="overcommitOptions"
                [value]="overcommitMode()"
                [disabled]="!canWriteSettings()"
                (valueChange)="setOvercommitMode($event)"
              />
              <span class="s-hint"
                >Fixed holds the ratios here; auto moves them between the bounds
                below from host CPU and memory pressure.</span
              >
            </div>
            <div class="s-field">
              <label class="s-label" for="sb-cpu-oc">CPU overcommit (x)</label>
              <input
                id="sb-cpu-oc"
                class="s-input s-num"
                type="number"
                min="0"
                step="0.25"
                [value]="state.val('sandboxCpuOvercommit', '')"
                [placeholder]="state.defaultHint('sandboxCpuOvercommit')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setNumber(
                    'sandboxCpuOvercommit',
                    $any($event.target).value,
                    reportedDefault('sandboxCpuOvercommit', 2),
                    true
                  )
                "
                (change)="
                  state.setNumber(
                    'sandboxCpuOvercommit',
                    $any($event.target).value,
                    reportedDefault('sandboxCpuOvercommit', 2)
                  )
                "
              />
              @if (overcommitUnbounded("sandboxCpuOvercommit")) {
                <span
                  class="s-hint s-hint-warn"
                  data-testid="cpu-overcommit-unbounded"
                  >0 leaves vCPU bounded by the run counts alone: no vCPU budget
                  is enforced.</span
                >
              } @else {
                <span class="s-hint"
                  >How many times the host's vCPU may be promised to sandboxes.
                  0 means unbounded.</span
                >
              }
            </div>
            <div class="s-field">
              <label class="s-label" for="sb-mem-oc"
                >Memory overcommit (x)</label
              >
              <input
                id="sb-mem-oc"
                class="s-input s-num"
                type="number"
                min="0"
                step="0.25"
                [value]="state.val('sandboxMemoryOvercommit', '')"
                [placeholder]="state.defaultHint('sandboxMemoryOvercommit')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setNumber(
                    'sandboxMemoryOvercommit',
                    $any($event.target).value,
                    reportedDefault('sandboxMemoryOvercommit', 1),
                    true
                  )
                "
                (change)="
                  state.setNumber(
                    'sandboxMemoryOvercommit',
                    $any($event.target).value,
                    reportedDefault('sandboxMemoryOvercommit', 1)
                  )
                "
              />
              @if (overcommitUnbounded("sandboxMemoryOvercommit")) {
                <span
                  class="s-hint s-hint-warn"
                  data-testid="mem-overcommit-unbounded"
                  >0 leaves memory bounded by the run counts alone: no memory
                  budget is enforced.</span
                >
              } @else {
                <span class="s-hint"
                  >How many times the host's memory may be promised to
                  sandboxes. 0 means unbounded.</span
                >
              }
            </div>
          </div>

          @if (overcommitMode() === "auto") {
            <div class="s-subsection" data-testid="overcommit-auto-bounds">
              <div class="s-subhead">
                <h4 class="s-subhead-title">Auto overcommit bounds</h4>
              </div>
              <span class="s-hint"
                >Blank (or 0) uses grain's default, shown greyed in each
                box.</span
              >
              <div class="s-grid">
                @for (b of overcommitBounds; track b.field) {
                  <div class="s-field">
                    <label class="s-label" [attr.for]="'oc-' + b.field">{{
                      b.label
                    }}</label>
                    <input
                      class="s-input s-num"
                      type="number"
                      min="0"
                      [id]="'oc-' + b.field"
                      [attr.max]="b.max ?? null"
                      [step]="b.step"
                      [attr.data-testid]="'input-overcommit-' + b.field"
                      [value]="overcommitBound(b.field)"
                      [placeholder]="b.fallback"
                      [disabled]="!canWriteSettings()"
                      (input)="setOvercommitBound(b, $any($event.target).value)"
                    />
                    @if (state.fieldErrors()[boundKey(b.field)]; as err) {
                      <span
                        class="s-field-error"
                        [attr.data-testid]="'error-overcommit-' + b.field"
                        >{{ err }}</span
                      >
                    }
                  </div>
                }
              </div>
            </div>
          }

          <div class="s-subsection" data-testid="sandbox-disk-floor">
            <div class="s-subhead">
              <h4 class="s-subhead-title">Disk floor</h4>
            </div>
            <div class="s-grid">
              <div class="s-field">
                <label class="s-label" for="sb-floor-share"
                  >Disk floor (share of each volume)</label
                >
                <input
                  id="sb-floor-share"
                  class="s-input s-num"
                  type="number"
                  min="0"
                  max="1"
                  step="0.01"
                  data-testid="input-disk-floor-share"
                  [value]="state.val('sandboxDiskFloorShare', '')"
                  [placeholder]="state.defaultHint('sandboxDiskFloorShare')"
                  [disabled]="!canWriteSettings()"
                  (input)="
                    state.setNumber(
                      'sandboxDiskFloorShare',
                      $any($event.target).value,
                      reportedDefault('sandboxDiskFloorShare', 0.1),
                      true
                    )
                  "
                  (change)="
                    state.setNumber(
                      'sandboxDiskFloorShare',
                      $any($event.target).value,
                      reportedDefault('sandboxDiskFloorShare', 0.1)
                    )
                  "
                />
                <span class="s-hint"
                  >Free space kept on each volume: new CI jobs are held and
                  agent dispatch deferred below it. 0.1 is a tenth; 0 with no
                  minimum holds nothing back.</span
                >
              </div>
              <div class="s-field">
                <label class="s-label" for="sb-floor-min"
                  >Disk floor minimum (MiB)</label
                >
                <input
                  id="sb-floor-min"
                  class="s-input s-num"
                  type="number"
                  min="0"
                  step="1024"
                  data-testid="input-disk-floor-min"
                  [value]="state.val('sandboxDiskFloorMinMb', '')"
                  [placeholder]="state.defaultHint('sandboxDiskFloorMinMb')"
                  [disabled]="!canWriteSettings()"
                  (input)="
                    state.setNumber(
                      'sandboxDiskFloorMinMb',
                      $any($event.target).value,
                      diskFloorMinDefault(),
                      true
                    )
                  "
                  (change)="
                    state.setNumber(
                      'sandboxDiskFloorMinMb',
                      $any($event.target).value,
                      diskFloorMinDefault()
                    )
                  "
                />
                <span class="s-hint"
                  >The least the floor is ever worth on a small volume. 0 is no
                  minimum; blank resets to grain's default ({{
                    diskFloorMinDefault()
                  }}
                  MiB).</span
                >
              </div>
            </div>
            @if (diskFloorSummary()) {
              <span class="s-hint s-num" data-testid="disk-floor-volumes">{{
                diskFloorSummary()
              }}</span>
            }
          </div>

          @if (!sandboxCommitment()?.uncapped) {
            <g-sandbox-commitment-note [commitment]="sandboxCommitment()" />
          }
        </div>

        <div class="s-card" id="kubernetes" data-testid="card-kubernetes">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Kubernetes</h3>
              <p class="s-card-desc">
                Where and how the kubernetes and kubernetes-granule backends
                create sandbox pods, and the Deployment self-upgrade repoints.
                Blank boxes use the default shown greyed in them.
              </p>
            </div>
          </div>
          @if (k8sImageWarning()) {
            <div
              class="s-banner warn"
              role="alert"
              data-testid="k8s-image-warning"
            >
              <span>{{ k8sImageWarning() }}</span>
            </div>
          }
          <div class="s-grid">
            @for (f of k8sFields(); track f.key) {
              <div class="s-field">
                <label class="s-label" [attr.for]="'k8s-' + f.key">{{
                  f.label
                }}</label>
                <input
                  class="s-input s-mono"
                  type="text"
                  spellcheck="false"
                  autocomplete="off"
                  [id]="'k8s-' + f.key"
                  [attr.data-testid]="'input-' + f.key"
                  [value]="state.val(f.key, '')"
                  [placeholder]="f.placeholder"
                  [disabled]="!canWriteSettings()"
                  (input)="state.setField(f.key, $any($event.target).value)"
                />
                <span class="s-hint">{{ f.hint }}{{ restartNote(f.key) }}</span>
                @if (f.key === "k8sGranuleImage" && k8sGranuleEffective()) {
                  <span
                    class="s-hint"
                    data-testid="k8s-granule-image-effective"
                    >{{ k8sGranuleEffective() }}</span
                  >
                }
              </div>
            }
            <div class="s-field">
              <label class="s-label" for="k8s-node-selector"
                >Node selector</label
              >
              <input
                id="k8s-node-selector"
                class="s-input s-mono"
                type="text"
                spellcheck="false"
                autocomplete="off"
                data-testid="input-k8sNodeSelector"
                placeholder="pool=sandboxes"
                [value]="nodeSelectorText()"
                [disabled]="!canWriteSettings()"
                (input)="setNodeSelector($any($event.target).value)"
              />
              @if (state.fieldErrors()["k8sNodeSelector"]; as err) {
                <span
                  class="s-field-error"
                  data-testid="error-k8sNodeSelector"
                  >{{ err }}</span
                >
              } @else {
                <span class="s-hint"
                  >Comma-separated key=value nodeSelector entries for sandbox
                  pods.{{ restartNote("k8sNodeSelector") }}</span
                >
              }
            </div>
          </div>

          <div class="s-subsection" data-testid="k8s-priority-classes">
            <div class="s-subhead">
              <h4 class="s-subhead-title">Pod priority classes</h4>
            </div>
            <span class="s-hint"
              >Empty sets no priority class on those pods. A class the cluster
              lacks is created if it is grain's own, or else left off the pods
              with an alert.</span
            >
            <div class="s-grid">
              @for (f of k8sPriorityFields(); track f.key) {
                <div class="s-field">
                  <label class="s-label" [attr.for]="'k8s-' + f.key">{{
                    f.label
                  }}</label>
                  <input
                    class="s-input s-mono"
                    type="text"
                    spellcheck="false"
                    autocomplete="off"
                    placeholder="none"
                    [id]="'k8s-' + f.key"
                    [attr.data-testid]="'input-' + f.key"
                    [value]="state.val(f.key, '')"
                    [disabled]="!canWriteSettings()"
                    (input)="state.setField(f.key, $any($event.target).value)"
                  />
                  <span class="s-hint"
                    >{{ f.hint }}{{ restartNote(f.key) }}</span
                  >
                </div>
              }
            </div>
          </div>

          <div class="s-toggle-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name"
                >Run Kubernetes sandbox containers privileged</span
              >
              <span class="s-hint"
                >Needed by kubernetes-granule pods that boot a VM over
                /dev/kvm.{{ restartNote("k8sPrivileged") }}</span
              >
            </div>
            <button
              type="button"
              class="g-switch"
              role="switch"
              data-testid="switch-k8s-privileged"
              [attr.aria-checked]="Boolean(state.val('k8sPrivileged', false))"
              [disabled]="!canWriteSettings()"
              aria-label="Run Kubernetes sandbox containers privileged"
              (click)="
                state.setField(
                  'k8sPrivileged',
                  !state.val('k8sPrivileged', false)
                )
              "
            ></button>
          </div>
          <div class="s-toggle-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name"
                >Cluster CNI enforces NetworkPolicy</span
              >
              <span class="s-hint"
                >Required for network: none isolation on Kubernetes pods.{{
                  restartNote("k8sNetworkPolicy")
                }}</span
              >
            </div>
            <button
              type="button"
              class="g-switch"
              role="switch"
              data-testid="switch-k8s-netpol"
              [attr.aria-checked]="
                Boolean(state.val('k8sNetworkPolicy', false))
              "
              [disabled]="!canWriteSettings()"
              aria-label="Cluster CNI enforces NetworkPolicy"
              (click)="
                state.setField(
                  'k8sNetworkPolicy',
                  !state.val('k8sNetworkPolicy', false)
                )
              "
            ></button>
          </div>
        </div>

        <div
          class="s-card"
          id="custom-sandbox-image"
          data-testid="card-custom-sandbox-image"
        >
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Custom sandbox image</h3>
              <p class="s-card-desc">
                The OCI image new sandboxes are created from in place of the one
                this build is stamped for. Running sandboxes keep theirs.
              </p>
            </div>
          </div>
          <div class="s-field">
            <label class="s-label" for="sb-image"
              >Custom sandbox image (OCI reference)</label
            >
            <div class="s-inline">
              <input
                id="sb-image"
                class="s-input s-input-inline s-mono"
                type="text"
                spellcheck="false"
                autocomplete="off"
                data-testid="input-sandbox-image"
                [value]="state.val('sandboxImage', '')"
                [placeholder]="state.defaultHint('sandboxImage')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField('sandboxImage', $any($event.target).value)
                "
              />
              @if (String(state.val("sandboxImage", "")).trim()) {
                <button
                  type="button"
                  class="btn-text s-btn-sm"
                  data-testid="sandbox-image-reset"
                  [disabled]="!canWriteSettings()"
                  (click)="state.setField('sandboxImage', '')"
                >
                  Reset to default
                </button>
              }
            </div>
            <span class="s-hint"
              >Empty uses the image this build is stamped for{{
                state.defaultHint("sandboxImage")
                  ? " (" + state.defaultHint("sandboxImage") + ")"
                  : ""
              }}.</span
            >
          </div>
          @if (sandboxImageStored()) {
            @if (state.settings()["sandboxImageWarning"]; as warn) {
              <div
                class="s-banner warn"
                role="alert"
                data-testid="sandbox-image-warning"
              >
                <span>{{ warn }}</span>
              </div>
            } @else if (sandboxImageVerified()) {
              <div
                class="s-banner ok"
                role="status"
                data-testid="sandbox-image-contract-ok"
              >
                <span>{{ sandboxImageVerified() }}</span>
              </div>
            }
          }
          @if (sandboxMirrorHint()) {
            <span class="s-hint" data-testid="sandbox-image-mirror">{{
              sandboxMirrorHint()
            }}</span>
          }
          @if (granulePinSame()) {
            <div
              class="s-banner readonly"
              role="status"
              data-testid="sandbox-image-k8s-granule-pin"
            >
              <span
                >The Kubernetes Granule VM pod image (k8s_granule_image) holds
                the same reference, {{ granulePin() }}. Clearing only the custom
                image leaves that pin in place.</span
              >
              <button
                type="button"
                class="btn-text s-btn-sm"
                data-testid="sandbox-image-k8s-granule-pin-clear"
                [disabled]="!canWriteSettings()"
                (click)="state.setField('k8sGranuleImage', '')"
              >
                Clear it too
              </button>
            </div>
          }

          <div class="s-subsection" data-testid="sandbox-image-contract">
            <div class="s-subhead">
              <h4 class="s-subhead-title">
                Build it FROM grain's minimal base image
              </h4>
            </div>
            <span class="s-hint" data-testid="sandbox-image-inherits">{{
              imageInherits()
            }}</span>
            <pre
              class="s-code"
              tabindex="0"
              role="region"
              aria-label="Example Dockerfile"
              data-testid="sandbox-image-dockerfile"
              >{{ dockerfileExample() }}</pre>
            <div class="s-subhead">
              <h4 class="s-subhead-title">Sandbox contract</h4>
            </div>
            <ul
              class="s-hint s-contract"
              data-testid="sandbox-contract-versions"
            >
              @for (line of contractLines(); track line.code) {
                <li>
                  <code>{{ line.code }}</code> {{ line.note }}
                </li>
              }
            </ul>
            <span class="s-hint"
              >A custom image needs rebuilding only when the contract version
              its base stamped falls below what grain supports — when grain
              changes what it relies on inside a sandbox. This card and the
              daemon log warn you when it does. Check an image before saving it
              with <code>grain sandbox-image check &lt;image&gt;</code>;
              <a [href]="contractDoc" target="_blank" rel="noopener noreferrer"
                >docs/sandbox-image-contract.md</a
              >
              lists everything the base image provides.</span
            >
          </div>
        </div>

        @if (persistentSandboxes().length > 0) {
          <div class="s-card" data-testid="card-persistent-sandboxes">
            <h3 class="s-card-title">Persistent sandboxes</h3>
            <div class="s-table-wrap">
              <table class="s-table" aria-label="Persistent sandboxes">
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>State</th>
                    <th class="s-actions-cell">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  @for (ps of persistentSandboxes(); track ps.name) {
                    <tr>
                      <td class="s-mono">{{ ps.name }}</td>
                      <td>
                        <span class="status-pill neutral">{{
                          ps.state || "ready"
                        }}</span>
                      </td>
                      <td class="s-actions-cell">
                        <div class="s-row-actions">
                          @for (
                            act of ["start", "restart", "rebuild", "stop"];
                            track act
                          ) {
                            <button
                              type="button"
                              class="btn-tonal s-btn-sm"
                              [disabled]="
                                !access.can(
                                  'POST /api/persistent-sandboxes/{name}/{action}'
                                )
                              "
                              (click)="actPersistentSandbox(ps.name, act)"
                            >
                              {{ act }}
                            </button>
                          }
                        </div>
                      </td>
                    </tr>
                  }
                </tbody>
              </table>
            </div>
          </div>
        }
      }

      @case ("hostds") {
        <div class="s-card" data-testid="card-hostds-list">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Hosts</h3>
              <p class="s-card-desc">
                The hosts this deployment runs host work through.
                <span class="s-mono">local</span> is co-located on this machine
                without a handshake; every other host joins with an enrollment
                code below (a 30-day Ed25519 key-bound credential, renewed while
                connected) or dials in with a legacy per-host token. Revoke cuts
                a host off; a revoked host can then be deleted, which frees its
                name.
              </p>
            </div>
            @if (access.can("POST /api/hostds/delete-revoked")) {
              <button
                type="button"
                class="btn-text s-btn-sm"
                data-testid="prune-revoked-hostds"
                (click)="deleteRevokedHostds()"
              >
                Delete revoked
              </button>
            }
          </div>

          @if (hostdNotice(); as n) {
            <div
              class="s-banner"
              [class.ok]="n.tone === 'ok'"
              [class.warn]="n.tone === 'warn'"
              data-testid="hostd-delete-notice"
            >
              <div style="flex: 1">
                @for (l of n.lines; track $index) {
                  <div>{{ l }}</div>
                }
              </div>
              <button
                type="button"
                class="btn-text s-btn-sm"
                (click)="hostdNotice.set(null)"
              >
                Dismiss
              </button>
            </div>
          }

          @if (!access.canGlobal("GET /api/hostds")) {
            <div class="s-banner warn" data-testid="hostds-restricted">
              {{ access.globalDeniedReason("GET /api/hostds") }}
            </div>
          } @else if (hostds().length === 0) {
            <p class="s-card-desc">No hosts registered yet.</p>
          } @else {
            <div class="s-table-wrap s-table-stack-wrap">
              <table
                class="s-table s-table-stack"
                aria-label="Registered hosts"
              >
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>Owner</th>
                    <th>Status</th>
                    <th>Labels &amp; capacity</th>
                    <th class="s-actions-cell">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  @for (h of hostds(); track h.name) {
                    @let isRevoked =
                      Boolean(h.revokedAt || h.revoked_at) ||
                      h.state === "revoked";
                    @let edit =
                      editingHostd()?.name === h.name ? editingHostd() : null;
                    @let renamingThis = renamingHostd()?.from === h.name;
                    @let remote = h.name !== "local" && !isRevoked && !h.shared;
                    @let keyBound = Boolean(h.key_fingerprint);
                    @let authInfo = hostAuthSummary(h);
                    @let connLine = connectionLine(h);
                    <tr [attr.data-testid]="'hostd-row-' + h.name">
                      <td class="s-mono">
                        @if (renamingThis) {
                          <div class="s-inline">
                            <input
                              class="s-input s-input-inline s-mono"
                              type="text"
                              aria-label="New host name"
                              [attr.data-testid]="
                                'rename-hostd-input-' + h.name
                              "
                              [value]="renamingHostd()?.to ?? ''"
                              (input)="
                                setRenameDraft($any($event.target).value)
                              "
                            />
                            <button
                              type="button"
                              class="btn-primary s-btn-sm"
                              [attr.data-testid]="'save-rename-hostd-' + h.name"
                              [disabled]="!(renamingHostd()?.to ?? '').trim()"
                              (click)="saveRenameHostd()"
                            >
                              Save
                            </button>
                            <button
                              type="button"
                              class="btn-text s-btn-sm"
                              (click)="cancelHostdEdit()"
                            >
                              Cancel
                            </button>
                          </div>
                        } @else {
                          <div class="s-cell-stack">
                            <span>{{ h.name }}</span>
                            @if (authInfo) {
                              <div
                                class="s-meta s-meta-mono"
                                [attr.data-testid]="'hostd-auth-' + h.name"
                                [attr.title]="authInfo.title || null"
                              >
                                {{ authInfo.text }}
                              </div>
                            }
                          </div>
                        }
                      </td>
                      <td data-label="Owner">
                        @if (edit) {
                          <input
                            class="s-input s-input-inline"
                            type="text"
                            aria-label="Owner"
                            [attr.data-testid]="'edit-hostd-owner-' + h.name"
                            [value]="edit.owner"
                            (input)="
                              patchHostdEdit({
                                owner: $any($event.target).value,
                              })
                            "
                          />
                        } @else if (h.owner) {
                          {{ h.owner }}
                        } @else if (remote || h.shared) {
                          <span
                            class="s-meta"
                            [attr.data-testid]="'hostd-owner-missing-' + h.name"
                            >None: needed to dial in through IAP</span
                          >
                        } @else {
                          —
                        }
                      </td>
                      <td class="hd-status-cell" data-label="Status">
                        <div class="s-cell-stack">
                          <span
                            [class]="
                              'status-pill ' +
                              (isRevoked
                                ? 'error'
                                : h.state === 'connected'
                                  ? 'healthy'
                                  : 'neutral')
                            "
                          >
                            {{ isRevoked ? "revoked" : h.state || "offline" }}
                          </span>
                          @if (connLine) {
                            <div
                              class="s-meta"
                              [attr.data-testid]="'hostd-connection-' + h.name"
                            >
                              {{ connLine }}
                            </div>
                          }
                          @if (h.update && !isRevoked) {
                            <div
                              class="s-meta hd-update"
                              [attr.data-testid]="'hostd-update-' + h.name"
                            >
                              <span>
                                Update available: {{ h.update.build }} →
                                {{ h.update.available }}. On the host, run
                              </span>
                              <div class="hd-update-cmd">
                                <code class="s-meta-mono hd-update-code">{{
                                  h.update.command
                                }}</code>
                                <button
                                  type="button"
                                  class="btn-text hd-update-copy"
                                  [attr.data-testid]="
                                    'copy-hostd-update-' + h.name
                                  "
                                  [attr.aria-label]="
                                    'Copy update command for ' + h.name
                                  "
                                  (click)="
                                    copyInvite(
                                      h.update.command,
                                      'Copied update command'
                                    )
                                  "
                                >
                                  Copy
                                </button>
                              </div>
                            </div>
                          }
                        </div>
                      </td>
                      <td class="s-mono" data-label="Labels & capacity">
                        @if (edit) {
                          <div class="s-inline">
                            <input
                              class="s-input s-input-inline s-mono"
                              type="text"
                              aria-label="Labels"
                              placeholder="os=linux,gpu=true"
                              [attr.data-testid]="'edit-hostd-labels-' + h.name"
                              [value]="edit.labels"
                              (input)="
                                patchHostdEdit({
                                  labels: $any($event.target).value,
                                })
                              "
                            />
                            <input
                              class="s-input s-input-inline s-num s-input-narrow"
                              type="text"
                              inputmode="numeric"
                              aria-label="Capacity"
                              placeholder="Capacity"
                              [attr.data-testid]="
                                'edit-hostd-capacity-' + h.name
                              "
                              [value]="edit.capacity"
                              (input)="
                                patchHostdEdit({
                                  capacity: $any($event.target).value,
                                })
                              "
                            />
                          </div>
                        } @else {
                          <div class="s-cell-stack">
                            <span>{{ formatLabels(h.labels) }}</span>
                            @if (h.capacity && h.capacity > 0) {
                              <div
                                class="s-meta"
                                [attr.data-testid]="'hostd-capacity-' + h.name"
                              >
                                capacity {{ h.capacity }}
                              </div>
                            }
                          </div>
                        }
                      </td>
                      <td class="s-actions-cell" data-label="Actions">
                        <!-- local is the co-located host: it holds no
                             token to rotate and can be neither renamed,
                             revoked nor deleted, so it offers none of
                             them (the server refuses each with a 400). -->
                        @if (edit) {
                          <div class="s-row-actions">
                            <button
                              type="button"
                              class="btn-primary s-btn-sm"
                              [attr.data-testid]="'save-edit-hostd-' + h.name"
                              (click)="saveHostdEdit()"
                            >
                              Save
                            </button>
                            <button
                              type="button"
                              class="btn-text s-btn-sm"
                              (click)="cancelHostdEdit()"
                            >
                              Cancel
                            </button>
                          </div>
                        } @else if (h.name !== "local" && !h.shared) {
                          <div class="s-row-actions">
                            @if (remote) {
                              <button
                                type="button"
                                class="btn-tonal s-btn-sm"
                                [attr.data-testid]="'edit-hostd-' + h.name"
                                [disabled]="
                                  !access.can('PATCH /api/hostds/{name}')
                                "
                                (click)="startHostdEdit(h)"
                              >
                                Edit
                              </button>
                              <button
                                type="button"
                                class="btn-tonal s-btn-sm"
                                [attr.data-testid]="'rename-hostd-' + h.name"
                                [disabled]="
                                  !access.can('POST /api/hostds/{name}/rename')
                                "
                                (click)="startRenameHostd(h.name)"
                              >
                                Rename
                              </button>
                            }
                            @if (!keyBound) {
                              <button
                                type="button"
                                class="btn-tonal s-btn-sm"
                                [attr.data-testid]="'rotate-hostd-' + h.name"
                                [disabled]="
                                  !access.can(
                                    'POST /api/hostds/{name}/rotate'
                                  ) || isRevoked
                                "
                                (click)="rotateHostd(h.name)"
                              >
                                Rotate
                              </button>
                            }
                            <button
                              type="button"
                              class="btn-tonal s-btn-sm"
                              [attr.data-testid]="'revoke-hostd-' + h.name"
                              [disabled]="
                                !access.can('POST /api/hostds/{name}/revoke') ||
                                isRevoked
                              "
                              (click)="revokeHostd(h.name)"
                            >
                              Revoke
                            </button>
                            <button
                              type="button"
                              class="btn-text s-btn-sm"
                              [attr.data-testid]="'delete-hostd-' + h.name"
                              [disabled]="
                                !access.can('DELETE /api/hostds/{name}') ||
                                !isRevoked
                              "
                              [attr.title]="
                                isRevoked
                                  ? null
                                  : 'Revoke a host before deleting it'
                              "
                              (click)="deleteHostd(h.name)"
                            >
                              Delete
                            </button>
                          </div>
                        }
                      </td>
                    </tr>
                  }
                </tbody>
              </table>
            </div>
          }

          <div class="s-subsection" data-testid="hostd-legacy-register">
            <div class="s-subhead">
              <h4 class="s-subhead-title">Add a host (legacy token)</h4>
            </div>
            <p class="s-hint" data-testid="hostd-legacy-copy">
              Prefer an enrollment code below for Ed25519 key-bound enrollment.
              Use a per-host token only for non-interactive setups that cannot
              wait for the owner's acceptance. The token is shown once, with the
              command that installs the host on its machine.
            </p>
            <div class="s-grid">
              <div class="s-field">
                <label class="s-label" for="hd-name">Name</label>
                <input
                  id="hd-name"
                  class="s-input s-mono"
                  type="text"
                  data-testid="input-new-hostd-name"
                  placeholder="workstation-01"
                  [value]="newHostdName()"
                  [disabled]="!access.can('POST /api/hostds')"
                  (input)="newHostdName.set($any($event.target).value)"
                />
                <span class="s-hint">Lowercase letters, digits and -</span>
              </div>
              <div class="s-field">
                <label class="s-label" for="hd-owner">Owner</label>
                <input
                  id="hd-owner"
                  class="s-input"
                  type="text"
                  data-testid="input-new-hostd-owner"
                  placeholder="alice@example.com"
                  [value]="newHostdOwner()"
                  [disabled]="!access.can('POST /api/hostds')"
                  (input)="newHostdOwner.set($any($event.target).value)"
                />
                <span class="s-hint"
                  >The email or service account it connects as</span
                >
              </div>
              <div class="s-field">
                <label class="s-label" for="hd-labels">Labels</label>
                <input
                  id="hd-labels"
                  class="s-input s-mono"
                  type="text"
                  data-testid="input-new-hostd-labels"
                  placeholder="os=linux,gpu=true"
                  [value]="newHostdLabels()"
                  [disabled]="!access.can('POST /api/hostds')"
                  (input)="newHostdLabels.set($any($event.target).value)"
                />
                <span class="s-hint">key=value,key2=value2</span>
              </div>
            </div>
            <div class="s-inline">
              <button
                type="button"
                class="btn-primary"
                data-testid="create-hostd-btn"
                [disabled]="
                  !newHostdName().trim() || !access.can('POST /api/hostds')
                "
                (click)="createHostd()"
              >
                Add host
              </button>
            </div>
          </div>
          @if (mintedHostdToken(); as tok) {
            <div class="s-banner ok" data-testid="minted-hostd-token">
              <pre class="s-code" style="flex: 1">{{ tok }}</pre>
              <button
                type="button"
                class="btn-text s-btn-sm"
                (click)="mintedHostdToken.set(null)"
              >
                Dismiss
              </button>
            </div>
          }
        </div>

        <div class="s-card" data-testid="card-hostds-enrollment">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Enrollment codes &amp; pending hosts</h3>
              <p class="s-card-desc">
                Share one enrollment command across machines. The code only
                files a pending join request; each host makes its own Ed25519
                keypair and waits for the deployment owner to accept it (the
                request names the verified person who enrolled it) before it
                gets a 30-day key-bound credential.
              </p>
            </div>
          </div>

          @if (!access.can("GET /api/hostds/enrollment-codes")) {
            <div
              class="s-banner readonly"
              data-testid="hostds-enrollment-restricted"
            >
              <span>{{
                access.deniedReason("GET /api/hostds/enrollment-codes")
              }}</span>
            </div>
          } @else {
            @if (selectedEnrollmentCode(); as sc) {
              <section
                class="hd-invite"
                aria-labelledby="hd-invite-title"
                data-testid="invite-hosts"
              >
                <div class="s-subhead">
                  <h4 class="s-subhead-title" id="hd-invite-title">
                    Invite hosts
                  </h4>
                  @if (enrollmentCodes().length > 1) {
                    <g-pill-select
                      testid="select-invite-code"
                      ariaLabel="Enrollment code to invite with"
                      prefix="Code"
                      [options]="enrollmentCodeOptions()"
                      [value]="sc.name"
                      (valueChange)="selectEnrollmentCode($event)"
                    />
                  }
                </div>
                <p class="s-hint">
                  Run this on each machine to join. The code only files a
                  pending join request: each host makes its own key and waits
                  for you to accept it below.
                </p>
                @if (sc.revoked_at) {
                  <div class="s-banner warn" role="status">
                    <span
                      >Enrollment code <strong>{{ sc.name }}</strong> is revoked
                      and can no longer file join requests. Hosts already
                      accepted are unaffected.</span
                    >
                  </div>
                } @else if (inviteSnippet()) {
                  <pre
                    class="s-code"
                    data-testid="invite-hosts-snippet"
                    aria-label="Invite command"
                    >{{ inviteSnippet() }}</pre>
                  @if (sc.install?.note) {
                    <p class="s-hint" data-testid="invite-hosts-note">
                      {{ sc.install?.note }}
                    </p>
                  }
                  <div class="s-inline">
                    <button
                      type="button"
                      class="btn-primary s-btn-sm"
                      data-testid="copy-invite-command"
                      (click)="
                        copyInvite(inviteSnippet(), 'Copied invite command')
                      "
                    >
                      Copy invite command
                    </button>
                    @if (sc.code) {
                      <button
                        type="button"
                        class="btn-text s-btn-sm"
                        data-testid="toggle-enrollment-secret"
                        [attr.aria-expanded]="enrollmentSecretShown()"
                        (click)="
                          enrollmentSecretShown.set(!enrollmentSecretShown())
                        "
                      >
                        {{
                          enrollmentSecretShown() ? "Hide code" : "Show code"
                        }}
                      </button>
                    }
                  </div>
                  @if (enrollmentSecretShown() && sc.code) {
                    <div class="s-inline">
                      <code
                        class="s-meta-mono hd-invite-secret"
                        data-testid="enrollment-secret"
                        >{{ sc.code }}</code
                      >
                      <button
                        type="button"
                        class="btn-text s-btn-sm"
                        data-testid="copy-enrollment-secret"
                        (click)="copyInvite(sc.code, 'Copied enrollment code')"
                      >
                        Copy code
                      </button>
                    </div>
                  }
                }
                <div class="s-inline" data-testid="invite-hosts-meta">
                  <span class="status-pill neutral"
                    >Uses: {{ sc.uses ?? 0
                    }}{{ sc.max_uses ? " / " + sc.max_uses : "" }}</span
                  >
                  @if (sc.default_capacity) {
                    <span class="status-pill neutral"
                      >Default capacity: {{ sc.default_capacity }}</span
                    >
                  }
                  @if (formatLabelsInput(sc.default_labels); as lbl) {
                    <span class="status-pill neutral">Labels: {{ lbl }}</span>
                  }
                  @for (p of sc.allowed_principals ?? []; track p) {
                    <span class="status-pill neutral">Allow: {{ p }}</span>
                  }
                  @if (sc.expires_at) {
                    <span class="status-pill neutral"
                      >Expires: {{ sc.expires_at.slice(0, 10) }}</span
                    >
                  }
                </div>
              </section>
            }
            @if (enrollmentCodes().length > 0) {
              <div class="s-table-wrap">
                <table class="s-table" aria-label="Enrollment codes">
                  <thead>
                    <tr>
                      <th>Code name</th>
                      <th>Uses</th>
                      <th>Expires</th>
                      <th>Defaults</th>
                      <th class="s-actions-cell">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    @for (c of enrollmentCodes(); track c.name) {
                      @let codeRevoked = Boolean(c.revoked_at);
                      @let defaults = codeDefaults(c);
                      <tr [attr.data-testid]="'enrollment-code-row-' + c.name">
                        <td class="s-mono">{{ c.name }}</td>
                        <td class="s-num">
                          {{ c.uses ?? 0 }} / {{ c.max_uses || "∞" }}
                        </td>
                        <td class="s-num">
                          {{
                            codeRevoked
                              ? "revoked"
                              : c.expires_at
                                ? c.expires_at.slice(0, 10)
                                : "never"
                          }}
                        </td>
                        <td
                          class="s-mono"
                          [attr.data-testid]="
                            'enrollment-code-defaults-' + c.name
                          "
                        >
                          @for (d of defaults; track d) {
                            <div>{{ d }}</div>
                          } @empty {
                            —
                          }
                        </td>
                        <td class="s-actions-cell">
                          @if (codeRevoked) {
                            <span
                              class="s-hint"
                              data-testid="enrollment-code-revoked"
                              >No actions</span
                            >
                          } @else {
                            <div class="s-row-actions">
                              <button
                                type="button"
                                class="btn-tonal s-btn-sm"
                                [disabled]="
                                  !access.can(
                                    'POST /api/hostds/enrollment-codes/{name}/rotate'
                                  )
                                "
                                (click)="rotateEnrollmentCode(c.name)"
                              >
                                Rotate
                              </button>
                              <button
                                type="button"
                                class="btn-text s-btn-sm"
                                [disabled]="
                                  !access.can(
                                    'POST /api/hostds/enrollment-codes/{name}/revoke'
                                  )
                                "
                                (click)="revokeEnrollmentCode(c.name)"
                              >
                                Revoke
                              </button>
                            </div>
                          }
                        </td>
                      </tr>
                    }
                  </tbody>
                </table>
              </div>
            }
            <form
              class="s-subsection"
              autocomplete="off"
              data-testid="new-enrollment-code-form"
              (submit)="$event.preventDefault(); createEnrollmentCode()"
            >
              <div class="s-subhead">
                <h4 class="s-subhead-title">New enrollment code</h4>
              </div>
              <div class="s-grid">
                <div class="s-field">
                  <label class="s-label" for="ec-name">Code name</label>
                  <input
                    id="ec-name"
                    class="s-input s-mono"
                    type="text"
                    data-testid="input-enrollment-code-name"
                    placeholder="team-linux"
                    [value]="newCodeName()"
                    [disabled]="
                      !access.can('POST /api/hostds/enrollment-codes')
                    "
                    (input)="newCodeName.set($any($event.target).value)"
                  />
                </div>
                <div class="s-field">
                  <label class="s-label" for="ec-expires">Expires in</label>
                  <input
                    id="ec-expires"
                    class="s-input s-mono"
                    type="text"
                    data-testid="input-enrollment-code-expires"
                    placeholder="7d or 24h"
                    [value]="newCodeExpiresIn()"
                    (input)="newCodeExpiresIn.set($any($event.target).value)"
                  />
                  <span class="s-hint">Optional; blank never expires</span>
                </div>
                <div class="s-field">
                  <label class="s-label" for="ec-max-uses">Max uses</label>
                  <input
                    id="ec-max-uses"
                    class="s-input s-num"
                    type="text"
                    inputmode="numeric"
                    data-testid="input-enrollment-code-max-uses"
                    placeholder="Unlimited"
                    [value]="newCodeMaxUses()"
                    (input)="newCodeMaxUses.set($any($event.target).value)"
                  />
                </div>
                <div class="s-field">
                  <label class="s-label" for="ec-capacity"
                    >Default capacity</label
                  >
                  <input
                    id="ec-capacity"
                    class="s-input s-num"
                    type="text"
                    inputmode="numeric"
                    data-testid="input-enrollment-code-capacity"
                    placeholder="Unlimited"
                    [value]="newCodeCapacity()"
                    (input)="newCodeCapacity.set($any($event.target).value)"
                  />
                  <span class="s-hint">Optional max runs on each host</span>
                </div>
                <div class="s-field">
                  <label class="s-label" for="ec-labels">Default labels</label>
                  <input
                    id="ec-labels"
                    class="s-input s-mono"
                    type="text"
                    data-testid="input-enrollment-code-labels"
                    placeholder="os=linux,gpu=true"
                    [value]="newCodeLabels()"
                    (input)="newCodeLabels.set($any($event.target).value)"
                  />
                </div>
                <div class="s-field">
                  <label class="s-label" for="ec-principals"
                    >Allowed principals</label
                  >
                  <input
                    id="ec-principals"
                    class="s-input s-mono"
                    type="text"
                    data-testid="input-enrollment-code-principals"
                    placeholder="user:a@example.com, org:acme"
                    [value]="newCodePrincipals()"
                    (input)="newCodePrincipals.set($any($event.target).value)"
                  />
                  <span class="s-hint"
                    >Optional user:, group:, github: or org: rules, comma
                    separated</span
                  >
                </div>
              </div>
              <div class="s-inline">
                <button
                  type="submit"
                  class="btn-tonal"
                  data-testid="create-enrollment-code-btn"
                  [disabled]="
                    !newCodeName().trim() ||
                    !access.can('POST /api/hostds/enrollment-codes')
                  "
                >
                  Create enrollment code
                </button>
              </div>
            </form>
          }

          @if (!access.can("GET /api/hostds/pending")) {
            <div
              class="s-banner readonly"
              data-testid="hostds-pending-restricted"
            >
              <span>{{ access.deniedReason("GET /api/hostds/pending") }}</span>
            </div>
          } @else if (pendingHostds().length > 0) {
            <div class="s-subhead">
              <h4 class="s-subhead-title">
                Pending host requests ({{ pendingHostds().length }})
              </h4>
            </div>
            <div class="s-table-wrap" data-testid="hostds-pending-table">
              <table class="s-table" aria-label="Pending host requests">
                <thead>
                  <tr>
                    <th>Request</th>
                    <th>Key fingerprint</th>
                    <th>Enrolled by</th>
                    <th>Accept as</th>
                    <th class="s-actions-cell">Approve / Reject</th>
                  </tr>
                </thead>
                <tbody>
                  @for (p of pendingHostds(); track p.id) {
                    @let d = pendingDraft(p);
                    @let label = p.hostname || p.id;
                    <tr [attr.data-testid]="'pending-hostd-row-' + p.id">
                      <td class="s-mono">
                        {{ label }}
                        <div class="s-meta">
                          {{ p.os || "?" }}/{{ p.arch || "?" }}
                          @if (p.enrollment_code) {
                            · code {{ p.enrollment_code }}
                          }
                        </div>
                        @if (p.rekey_hostd) {
                          <span
                            class="status-pill reply"
                            [attr.data-testid]="'pending-rekey-' + p.id"
                            >rekey: {{ p.rekey_hostd }}</span
                          >
                        }
                      </td>
                      <!-- There is no pairing code to compare: the owner
                           checks who enrolled the host and the key it
                           will be bound to (docs/host-placement.md). -->
                      <td
                        class="s-fingerprint"
                        [attr.data-testid]="'pending-key-fingerprint-' + p.id"
                      >
                        {{ p.key_fingerprint || "—" }}
                      </td>
                      <td>{{ p.enrolled_by || "—" }}</td>
                      <td>
                        <div class="s-inline">
                          <input
                            class="s-input s-input-inline s-mono"
                            type="text"
                            [attr.aria-label]="'Host name for ' + label"
                            [attr.data-testid]="'pending-name-' + p.id"
                            [attr.title]="
                              p.rekey_hostd
                                ? 'A rekey keeps the host it moves onto a new key'
                                : null
                            "
                            placeholder="Host name"
                            [disabled]="Boolean(p.rekey_hostd)"
                            [value]="d.name"
                            (input)="
                              patchPendingDraft(p, {
                                name: $any($event.target).value,
                              })
                            "
                          />
                          <input
                            class="s-input s-input-inline s-mono"
                            type="text"
                            [attr.aria-label]="'Host labels for ' + label"
                            [attr.data-testid]="'pending-labels-' + p.id"
                            placeholder="k=v (optional)"
                            [value]="d.labels"
                            (input)="
                              patchPendingDraft(p, {
                                labels: $any($event.target).value,
                              })
                            "
                          />
                          <input
                            class="s-input s-input-inline s-num s-input-narrow"
                            type="text"
                            inputmode="numeric"
                            [attr.aria-label]="'Host capacity for ' + label"
                            [attr.data-testid]="'pending-capacity-' + p.id"
                            placeholder="Capacity"
                            [value]="d.capacity"
                            (input)="
                              patchPendingDraft(p, {
                                capacity: $any($event.target).value,
                              })
                            "
                          />
                        </div>
                      </td>
                      <td class="s-actions-cell">
                        <div class="s-row-actions">
                          <button
                            type="button"
                            class="btn-primary s-btn-sm"
                            [attr.data-testid]="'accept-pending-' + p.id"
                            [disabled]="
                              !access.can(
                                'POST /api/hostds/pending/{id}/accept'
                              ) || !d.name.trim()
                            "
                            (click)="acceptPendingHostd(p)"
                          >
                            Accept
                          </button>
                          <button
                            type="button"
                            class="btn-tonal s-btn-sm"
                            [attr.data-testid]="'reject-pending-' + p.id"
                            [disabled]="
                              !access.can(
                                'POST /api/hostds/pending/{id}/reject'
                              )
                            "
                            (click)="rejectPendingHostd(p)"
                          >
                            Reject
                          </button>
                        </div>
                      </td>
                    </tr>
                  }
                </tbody>
              </table>
            </div>
          } @else {
            <p class="s-card-desc" data-testid="hostds-pending-empty">
              No hosts are waiting for approval. Pending requests expire after
              24 hours.
            </p>
          }
        </div>
      }
    }

    @if (confirmRequest(); as req) {
      <g-confirm-dialog [request]="req" (done)="onConfirmDone($event)" />
    }
  `,
})
export class SettingsComputeComponent implements OnInit, OnChanges {
  protected readonly api = inject(GrainApi);
  protected readonly access = inject(AccessService);
  protected readonly state = inject(SettingsStateService);
  protected readonly theme = inject(ThemeService);
  private readonly snackbar = inject(SnackbarService);
  private readonly doc = inject(DOCUMENT);

  readonly section = input.required<SettingsSectionId>();

  protected readonly Number = Number;
  protected readonly Boolean = Boolean;
  protected readonly String = String;

  protected readonly contractDoc =
    "https://github.com/ai-on-gke/grain/blob/main/docs/sandbox-image-contract.md";

  // The auto controller's bounds (model.OvercommitSettings). GET
  // /api/settings reports them resolved, so a stored 0 already reads as
  // grain's default; fallback is that default (model.DefaultOvercommit*)
  // for the placeholder of a box somebody clears.
  protected readonly overcommitBounds: readonly OvercommitBound[] = [
    {
      field: "cpuMin",
      label: "Auto CPU minimum (x)",
      fallback: "1",
      step: "0.25",
    },
    {
      field: "cpuMax",
      label: "Auto CPU maximum (x)",
      fallback: "6",
      step: "0.25",
    },
    {
      field: "memoryMin",
      label: "Auto memory minimum (x)",
      fallback: "1",
      step: "0.05",
    },
    {
      field: "memoryMax",
      label: "Auto memory maximum (x)",
      fallback: "2",
      step: "0.05",
    },
    {
      field: "cpuPressureTarget",
      label: "CPU pressure target (%)",
      fallback: "25",
      step: "1",
      max: 100,
    },
    {
      field: "memoryPressureTarget",
      label: "Memory pressure target (%)",
      fallback: "5",
      step: "1",
      max: 100,
    },
  ];

  protected boundKey(field: string): string {
    return `sandboxOvercommit${field[0].toUpperCase()}${field.slice(1)}`;
  }

  private overcommitObject(): Record<string, unknown> {
    const v = this.state.val<unknown>("sandboxOvercommit", null);
    if (v && typeof v === "object") return v as Record<string, unknown>;
    return { mode: typeof v === "string" && v ? v : "fixed" };
  }

  protected overcommitBound(field: string): string {
    const v = Number(this.overcommitObject()[field] ?? 0);
    return Number.isFinite(v) && v !== 0 ? String(v) : "";
  }

  // setOvercommitBound stages one bound into the sandboxOvercommit object
  // the server saves whole. A refused bound is reported against its own
  // box by name, and holds the Save bar, rather than reaching the server
  // as a bad object or quietly turning into the default.
  protected setOvercommitBound(b: OvercommitBound, raw: string): void {
    const key = this.boundKey(b.field);
    const t = String(raw ?? "").trim();
    const n = t === "" ? 0 : Number(t);
    if (!Number.isFinite(n) || n < 0) {
      this.state.setFieldError(
        key,
        `${b.label} must be 0 (grain's default) or more, not ${t || "that"}.`,
      );
      return;
    }
    if (b.max !== undefined && n > b.max) {
      this.state.setFieldError(
        key,
        `${b.label} is a percentage, at most ${b.max}.`,
      );
      return;
    }
    this.state.setFieldError(key, null);
    this.state.setField("sandboxOvercommit", {
      ...this.overcommitObject(),
      [b.field]: n,
    });
  }

  // reportedDefault is `<key>Default` as a number, for a box whose stored
  // 0 is a value of its own (an unbounded overcommit, a floor share that
  // holds nothing back): a cleared box saves that default, so the greyed
  // placeholder it shows is what Save really stores, and 0 typed on
  // purpose stays 0.
  protected reportedDefault(key: string, fallback: number): number {
    const d = Number(this.state.settings()[`${key}Default`]);
    return Number.isFinite(d) && d > 0 ? d : fallback;
  }

  protected diskFloorMinDefault(): number {
    return (
      Number(this.state.settings()["sandboxDiskFloorMinMbDefault"]) || 5120
    );
  }

  protected readonly diskFloorSummary = computed(() => {
    const floors = this.state.settings()["sandboxDiskFloors"];
    if (!Array.isArray(floors)) return "";
    return (floors as DiskFloorRow[])
      .filter((d) => Number(d.totalMB) > 0)
      .map(
        (d) =>
          `${d.path} (${mib(d.totalMB)}) holds ${mib(d.floorMB)} free, sweeping back to ${mib(d.reclaimMB)}.`,
      )
      .join(" ");
  });

  protected readonly sandboxCommitment = computed(
    () =>
      (this.state.settings()["sandboxCommitment"] as
        SandboxCommitment | undefined) ?? null,
  );

  // restartNote is said after a field's hint when the daemon reads it
  // only at startup (settings.restartRequired), and louder once a saved
  // value is waiting on that restart (settings.pendingRestart).
  protected restartNote(key: string): string {
    const s = this.state.settings();
    const pending = s["pendingRestart"];
    if (Array.isArray(pending) && pending.includes(key)) {
      return " Saved; takes effect after a daemon restart.";
    }
    const req = s["restartRequired"];
    if (Array.isArray(req) && req.includes(key)) {
      return " Read at daemon startup.";
    }
    return "";
  }

  protected readonly k8sImageWarning = computed(() => {
    const s = this.state.settings();
    return [s["k8sSandboxImageWarning"], s["k8sGranuleImageWarning"]]
      .filter((w) => typeof w === "string" && w)
      .join(" ");
  });

  protected readonly k8sFields = computed<readonly K8sField[]>(() => {
    const d = (k: string, fb = "") => this.state.defaultHint(k, fb);
    const build = String(this.state.settings()["daemonBuildVersion"] || "dev");
    return [
      {
        key: "k8sNamespace",
        label: "Namespace",
        placeholder: d("k8sNamespace", "grain"),
        hint: "Namespace sandbox pods are created in.",
      },
      {
        key: "k8sSandboxImage",
        label: "Sandbox pod image",
        placeholder: d("k8sSandboxImage"),
        hint: `Container image every plain Kubernetes sandbox pod runs (matches this daemon's build ${build}).`,
      },
      {
        key: "k8sGranuleImage",
        label: "Granule VM pod image",
        placeholder: d("k8sGranuleImage"),
        hint: `Packed guest image every Kubernetes Granule VM pod boots (matches this daemon's build ${build}).`,
      },
      {
        key: "k8sWorkspace",
        label: "Workspace directory",
        placeholder: d("k8sWorkspace"),
        hint: "Working directory inside each sandbox pod or VM; an absolute path.",
      },
      {
        key: "k8sGitProxyHost",
        label: "Git & model proxy host",
        placeholder: "$GRAIN_POD_IP",
        hint: "Host (no port) sandbox pods reach this daemon's git and model proxies at; in-cluster it defaults to the pod IP.",
      },
      {
        key: "k8sSandboxService",
        label: "Sandbox Service",
        placeholder: "none",
        hint: "Optional ClusterIP Service fronting the proxies, so sandboxes survive a daemon rollout.",
      },
      {
        key: "k8sServiceAccount",
        label: "Sandbox ServiceAccount",
        placeholder: "default",
        hint: "Optional ServiceAccount for sandbox pods.",
      },
      {
        key: "k8sDeployment",
        label: "grain Deployment",
        placeholder: d("k8sDeployment", "grain"),
        hint: "The Deployment running grain itself, repointed on self-upgrade.",
      },
      {
        key: "k8sContainer",
        label: "grain container",
        placeholder: d("k8sContainer", "grain"),
        hint: "Container within that Deployment that self-upgrade repoints.",
      },
      {
        key: "k8sKubectl",
        label: "kubectl binary",
        placeholder: d("k8sKubectl", "kubectl"),
        hint: "Path to kubectl; blank finds kubectl on PATH.",
      },
    ];
  });

  protected readonly k8sPriorityFields = computed<readonly K8sField[]>(() => {
    const def = (k: string) =>
      this.state.defaultHint(k)
        ? ` deploy/kubernetes/ defines ${this.state.defaultHint(k)}.`
        : "";
    return [
      {
        key: "k8sPriorityClassInteractive",
        label: "Interactive sessions",
        placeholder: "none",
        hint: `Highest: may preempt CI pods on a full cluster.${def("k8sPriorityClassInteractive")}`,
      },
      {
        key: "k8sPriorityClassAgent",
        label: "Agent runs",
        placeholder: "none",
        hint: `Agent runs, base-branch repairs and persistent sandboxes.${def("k8sPriorityClassAgent")}`,
      },
      {
        key: "k8sPriorityClassCi",
        label: "CI jobs",
        placeholder: "none",
        hint: `Lowest: a preempted CI job is retried, not failed.${def("k8sPriorityClassCi")}`,
      },
    ];
  });

  protected readonly k8sGranuleEffective = computed(() => {
    const s = this.state.settings();
    const effective = String(s["k8sGranuleImageEffective"] || "");
    const stored = String(s["k8sGranuleImage"] || "").trim();
    const hint = String(s["k8sGranuleImageMirror"] || "");
    const mirror = String(s["sandboxImageMirrorEffective"] || "");
    const onGranule =
      (s["sandboxBackend"] || s["sandboxImageBackend"]) ===
      "kubernetes-granule";
    if (!effective || (!onGranule && !hint)) return "";
    if (hint) {
      return `Treated as mirror repository ${hint}, not booted; sandboxes boot ${effective}. Clearing it keeps that mirror as sandbox_image_mirror.`;
    }
    if (effective === stored && !mirror) return "";
    const derived = s["sandboxImageMirrorDerivedFrom"];
    return `Sandboxes boot ${effective}${mirror ? `, mirrored into ${mirror} (${derived ? `derived from ${derived}` : "sandbox_image_mirror"})` : ""}.`;
  });

  // The node selector box is text while somebody types; it stages the
  // list the server stores, and refuses an entry that is not key=value
  // by name (the server's own check, k8sNodeSelector "x" must be
  // key=value) before Save rather than after.
  protected readonly nodeSelectorDraft = signal<string | null>(null);
  // Save and Discard both bump discardEpoch: the box reads the stored
  // list again rather than keep what was typed.
  private readonly nodeSelectorReset = effect(() => {
    this.state.discardEpoch();
    untracked(() => this.nodeSelectorDraft.set(null));
  });

  protected nodeSelectorText(): string {
    const d = this.nodeSelectorDraft();
    if (d !== null) return d;
    const v = this.state.val<unknown>("k8sNodeSelector", []);
    return Array.isArray(v) ? v.join(", ") : "";
  }

  protected setNodeSelector(raw: string): void {
    this.nodeSelectorDraft.set(raw);
    const entries = raw
      .split(",")
      .map((e) => e.trim())
      .filter(Boolean);
    const bad = entries.find((e) => !/^[^=\s]+=[^=\s]*$/.test(e));
    if (bad) {
      this.state.setFieldError(
        "k8sNodeSelector",
        `Node selector entry "${bad}" must be key=value.`,
      );
      return;
    }
    this.state.setFieldError("k8sNodeSelector", null);
    this.state.setField("k8sNodeSelector", entries);
  }

  protected readonly sandboxImageStored = computed(() =>
    String(this.state.settings()["sandboxImage"] || "").trim(),
  );

  protected readonly sandboxImageVerified = computed(() => {
    const s = this.state.settings();
    const check = s["sandboxImageCheck"] as
      | {
          verified?: boolean;
          contract?: { summary?: string; imageContract?: number };
        }
      | null
      | undefined;
    if (!check?.verified) return "";
    const c = (s["sandboxContract"] as ContractInfo | undefined) ?? {};
    return (
      check.contract?.summary ||
      `${c.contractLabel || "dev.grain.sandbox.contract"} = ${check.contract?.imageContract ?? s["sandboxContractVersion"] ?? ""} (current)`
    );
  });

  protected readonly sandboxMirrorHint = computed(() => {
    const s = this.state.settings();
    const mirror = String(s["sandboxImageMirrorEffective"] || "");
    if (!mirror) return "";
    const from = s["sandboxImageMirrorDerivedFrom"];
    return `Granule guest images are pulled through mirror ${mirror}${from ? ` (derived from ${from})` : " (sandbox_image_mirror)"}.`;
  });

  protected readonly granulePin = computed(() =>
    String(this.state.settings()["k8sGranuleImage"] || "").trim(),
  );

  protected readonly granulePinSame = computed(() => {
    const s = this.state.settings();
    const backend = s["sandboxImageBackend"] || s["sandboxBackend"];
    const pin = this.granulePin();
    return (
      backend === "kubernetes-granule" &&
      pin !== "" &&
      pin === this.sandboxImageStored()
    );
  });

  private imageIsVm(): boolean {
    const s = this.state.settings();
    const b = String(s["sandboxImageBackend"] || s["sandboxBackend"] || "");
    return b === "granule" || b === "kubernetes-granule";
  }

  protected readonly imageInherits = computed(() =>
    this.imageIsVm()
      ? "Granule VM backends boot a packed guest. sandbox-base already satisfies the whole sandbox contract: user debian (uid 1000) with the /home/agent symlink, git, curl, ca-certificates, python3-minimal for port forwarding, passwordless sudo, systemd, grain-connect, the sandbox grain CLI at /usr/local/bin/grain and the contract version stamps. Start FROM it and add only what your tasks need."
      : "Container and Kubernetes pod backends run a container image. sandbox-k8s-base already satisfies the whole sandbox contract: user debian (uid 1000) with the /home/agent symlink, git, curl, ca-certificates, python3-minimal for port forwarding, the sandbox grain CLI at /usr/local/bin/grain and the contract version stamps. Start FROM it and add only what your tasks need.",
  );

  protected readonly dockerfileExample = computed(() => {
    const s = this.state.settings();
    const c = (s["sandboxContract"] as ContractInfo | undefined) ?? {};
    const base =
      String(s["sandboxBaseImage"] || "") ||
      (this.imageIsVm()
        ? c.guestBaseImage || "docker.io/grainai/sandbox-base:latest"
        : c.k8sSandboxBaseImage || "docker.io/grainai/sandbox-k8s-base:latest");
    return `FROM ${base}\nRUN apt-get update \\\n && apt-get install -y --no-install-recommends <your packages> \\\n && rm -rf /var/lib/apt/lists/*`;
  });

  protected readonly contractLines = computed(() => {
    const s = this.state.settings();
    const c = (s["sandboxContract"] as ContractInfo | undefined) ?? {};
    const current = Number(s["sandboxContractVersion"] ?? c.contract ?? 1);
    const minimum = Number(
      s["sandboxMinimumContractVersion"] ?? c.minimumContract ?? 1,
    );
    const range =
      minimum === current ? `v${current}` : `v${minimum}–v${current}`;
    const lines = [
      {
        code: `${c.contractLabel || "dev.grain.sandbox.contract"} = ${current}`,
        note: `(this grain drives images at ${range})`,
      },
    ];
    if (this.imageIsVm()) {
      lines.push({
        code: `${c.granuleContractLabel || "dev.granule.guest.contract"} = ${s["sandboxGranuleContractVersion"] ?? c.granuleContract ?? 1}`,
        note: "(Granule's own guest contract, carried by sandbox-base)",
      });
    }
    return lines;
  });
  protected readonly formatLabels = formatHostdLabels;
  protected readonly formatRefreshedAt = (iso?: string): string =>
    iso && !iso.startsWith("0001-01-01")
      ? formatInTimeZone(iso, this.theme.timeZone()) || formatRefreshedAt(iso)
      : "";

  protected readonly frameworkOptions: readonly PillOption[] = [
    { value: "antigravity", label: "Antigravity (Gemini)" },
    { value: "claude", label: "Claude" },
    { value: "codex", label: "Codex" },
  ];
  // "subscription" (model.CodexAuthSubscription) is a Codex-only mode:
  // the server refuses it for any other framework.
  protected readonly authModeOptions = computed<PillOption[]>(() => [
    { value: "", label: "Provider default" },
    { value: "api_key", label: "API key / OAuth secret" },
    ...(this.newBackendFw() === "codex"
      ? [{ value: "subscription", label: "ChatGPT subscription (auth.json)" }]
      : []),
    { value: "ambient", label: "Host ambient login" },
    { value: "vertex", label: "Vertex AI" },
  ]);
  protected readonly usageModeOptions: readonly PillOption[] = [
    { value: "advisory", label: "Advisory" },
    { value: "auto", label: "Auto throttle" },
    { value: "off", label: "Off" },
  ];
  // The backend and network pickers offer the server's own vocabulary
  // (GET /api/settings sandboxBackendOptions / networkAccesses): a value
  // the daemon does not know is refused on save, and a stored value the
  // list lacks would show as somebody else's choice. Backends this daemon
  // cannot run stay listed but disabled, with the server's reason.
  protected readonly sandboxBackendOptions = computed<PillOption[]>(() => {
    const s = this.state.settings();
    const raw = s["sandboxBackendOptions"];
    const list: SandboxBackendOptionDto[] =
      Array.isArray(raw) && raw.length > 0
        ? (raw as SandboxBackendOptionDto[])
        : (Array.isArray(s["sandboxBackends"])
            ? (s["sandboxBackends"] as string[])
            : ["container"]
          ).map((backend) => ({ backend, available: true }));
    return list.map((o) => ({
      value: o.backend,
      label: SANDBOX_BACKEND_LABELS[o.backend] ?? o.backend,
      disabled: o.available === false,
      hint:
        o.available === false
          ? `Unavailable: ${o.reason || "this daemon cannot run it"}`
          : undefined,
    }));
  });
  protected readonly networkOptions = computed<PillOption[]>(() => {
    const raw = this.state.settings()["networkAccesses"];
    const names =
      Array.isArray(raw) && raw.length > 0
        ? (raw as string[])
        : ["open", "none"];
    return names.map((n) => ({
      value: n,
      label: NETWORK_ACCESS_LABELS[n] ?? n,
    }));
  });
  protected sandboxBackendDescription(): string {
    const b = String(this.state.val("sandboxBackend", "container"));
    return SANDBOX_BACKEND_DESCRIPTIONS[b] ?? "";
  }
  protected readonly overcommitOptions: readonly PillOption[] = [
    { value: "fixed", label: "Fixed multiplier" },
    { value: "auto", label: "Pressure-governed auto" },
  ];

  protected readonly newModelSettingName = signal("");
  protected readonly newModelSettingEffort = signal("high");

  protected readonly backends = signal<BackendEntry[]>([]);
  protected readonly backendCredDrafts = signal<Record<string, string>>({});
  protected readonly openCredBackendId = signal<string | null>(null);
  protected readonly codexAuthDrafts = signal<Record<string, string>>({});
  protected readonly codexAuthStatus = signal<
    Partial<Record<string, BackendCodexAuthStatus>>
  >({});
  protected readonly codexAuthBusy = signal(false);
  protected readonly codexAuthErrors = signal<Record<string, string>>({});
  protected readonly connectionTests = signal<
    Partial<Record<string, { pending: boolean; ok?: boolean; detail: string }>>
  >({});

  private clearConnectionTest(id: string): void {
    this.connectionTests.update((m) => {
      const next = { ...m };
      delete next[id];
      return next;
    });
  }

  protected async testBackend(b: BackendEntry): Promise<void> {
    if (this.connectionTests()[b.id]?.pending) return;
    const pending = { pending: true, detail: "Testing saved connection…" };
    this.connectionTests.update((m) => ({ ...m, [b.id]: pending }));
    const finish = (ok: boolean, detail: string) => {
      // A credential/configuration save invalidates an in-flight result too.
      this.connectionTests.update((m) =>
        m[b.id] === pending
          ? { ...m, [b.id]: { pending: false, ok, detail } }
          : m,
      );
    };
    try {
      const result = await this.api.post<{ ok: boolean; detail: string }>(
        "POST /api/agent-keys/{framework}/check",
        { framework: b.framework || "antigravity" },
        undefined,
        `backend=${encodeURIComponent(b.id)}`,
      );
      finish(
        result?.ok === true,
        result?.detail || "Connection test unavailable on this daemon.",
      );
    } catch {
      finish(
        false,
        "Could not test the saved connection. Check your access, daemon connectivity and test support, then retry.",
      );
    }
  }
  protected readonly newBackendId = signal("");
  protected readonly newBackendName = signal("");
  protected readonly newBackendFw = signal("antigravity");
  protected readonly editingBackendId = signal("");
  protected readonly newBackendAuthMode = signal("");

  protected readonly modelsConfigured = computed(
    () => this.backends().length > 0,
  );
  protected readonly newBackendModels = signal("");
  protected readonly newBackendHostd = signal("");
  protected readonly newBackendCliPath = signal("");
  protected readonly newBackendConcurrency = signal(0);
  protected readonly discoveredModels = signal<string[]>([]);

  protected readonly usageMode = signal("advisory");
  protected readonly usageMinWorkers = signal(1);
  protected readonly usageRetentionDays = signal(30);
  protected readonly usageBudgets = signal<TokenBudgetRow[]>([]);
  protected readonly newBudgetConn = signal("");
  protected readonly newBudgetModel = signal("");
  protected readonly newBudgetTokens = signal("");
  protected readonly newBudgetWindow = signal("24h");
  protected readonly usageSummary = signal("");
  protected readonly usageSnapshot = signal<UsagePauseInput | null>(null);
  protected readonly resumingModels = signal(false);
  protected readonly pausedModelTargets = computed(() =>
    collectPausedModelTargets(
      this.access.rawConfig(),
      this.usageSnapshot() ?? this.access.usage(),
      this.access.clockNow(),
    ),
  );
  protected readonly modelsDispatchStatus = computed(() =>
    summarizeDispatchStatus(
      this.access.rawConfig(),
      this.usageSnapshot() ?? this.access.usage(),
      this.access.clockNow(),
      this.theme.timeZone() === "local" ? undefined : this.theme.timeZone(),
      { modelQuotaOnly: true },
    ),
  );

  protected readonly persistentSandboxes = signal<
    { name: string; state?: string }[]
  >([]);
  protected readonly probing = signal(false);
  protected readonly probeResult = signal<SandboxProbeResult | null>(null);

  protected readonly hostds = signal<HostdRow[]>([]);
  protected readonly confirmRequest = signal<ConfirmRequest | null>(null);
  protected readonly newHostdName = signal("");
  protected readonly newHostdLabels = signal("");
  protected readonly newHostdOwner = signal("");
  protected readonly mintedHostdToken = signal<string | null>(null);
  protected readonly enrollmentCodes = signal<EnrollmentCodeRow[]>([]);
  protected readonly newCodeName = signal("");
  protected readonly newCodeExpiresIn = signal("");
  protected readonly newCodeMaxUses = signal("");
  protected readonly newCodeCapacity = signal("");
  protected readonly newCodeLabels = signal("");
  protected readonly newCodePrincipals = signal("");
  protected readonly chosenEnrollmentCode = signal("");
  protected readonly enrollmentSecretShown = signal(false);
  protected readonly selectedEnrollmentCode = computed(() =>
    pickEnrollmentCode(this.enrollmentCodes(), this.chosenEnrollmentCode()),
  );
  protected readonly enrollmentCodeOptions = computed<PillOption[]>(() =>
    this.enrollmentCodes().map((c) => ({
      value: c.name,
      label: c.revoked_at ? `${c.name} (revoked)` : c.name,
    })),
  );
  protected readonly inviteSnippet = computed(() =>
    inviteLines(
      this.doc.defaultView?.location.origin ?? "",
      this.selectedEnrollmentCode(),
    ).join("\n"),
  );
  protected readonly pendingHostds = signal<PendingHostdRow[]>([]);
  protected readonly pendingDrafts = signal<Record<string, PendingDraft>>({});
  protected readonly editingHostd = signal<HostdEdit | null>(null);
  protected readonly renamingHostd = signal<{
    from: string;
    to: string;
  } | null>(null);
  // hostdNotice is what the last Delete or Delete revoked did: each host
  // deleted, with the usage-limit pauses cleared with it, and each one
  // skipped and why. A toast went before anyone could read a list.
  protected readonly hostdNotice = signal<{
    tone: "ok" | "warn";
    lines: string[];
  } | null>(null);
  protected readonly connectionLine = hostdConnectionLine;
  protected readonly hostAuthSummary = hostdAuthSummary;
  // raisedHostError is the error banner the last refused host action put
  // up, so the save that fixes it takes it down -- and leaves a later,
  // unrelated one alone.
  private raisedHostError: string | null = null;

  // The deployment's default model list (model.Config.DefaultModels):
  // the default first, then its fallbacks, tried in that order. Settings
  // and the deployment record are two views of one grain_config column, so
  // a staged edit on either wins over both stored values.
  protected readonly modelChain = computed<string[]>(() => {
    const dep = this.state.deployDraft();
    if ("defaultModels" in dep) return parseDefaultModels(dep["defaultModels"]);
    const draft = this.state.draft();
    if ("defaultModel" in draft)
      return parseDefaultModels(draft["defaultModel"]);
    const stored = this.state.settings()["defaultModel"];
    if (typeof stored === "string") return parseDefaultModels(stored);
    const d = this.state.deployment();
    return parseDefaultModels(d["defaultModels"] ?? d["defaultModel"]);
  });
  protected readonly fallbackModels = computed(() =>
    this.modelChain().slice(1),
  );
  protected readonly modelGroups = computed(() => modelGroups(this.backends()));
  protected readonly refreshingModels = signal(false);
  protected readonly refreshNote = signal("");

  protected readonly chainWarning = computed(() => {
    const chain = this.modelChain();
    const groups = this.modelGroups();
    if (!chain.length || !groups.length) return "";
    if (chain.some((m) => modelAvailability(m, groups) === "enabled")) {
      return "";
    }
    return chain.length === 1
      ? `No enabled connection advertises ${chain[0]}. Tasks on the default model wait in the queue until one does.`
      : `No enabled connection advertises any default or fallback model (${chain.join(", ")}). Tasks on the default model wait in the queue until one does.`;
  });

  ngOnInit(): void {
    this.loadForSection();
  }

  ngOnChanges(changes: SimpleChanges): void {
    if (changes["section"] && !changes["section"].firstChange) {
      this.loadForSection();
    }
  }

  protected canWriteSettings(): boolean {
    return (
      this.access.can("POST /api/settings") &&
      !this.state.configRepoOnly() &&
      !this.state.sectionLocked(this.section())
    );
  }

  protected canWriteDeployment(): boolean {
    return (
      this.access.can("PUT /api/deployment") &&
      !this.state.configRepoOnly() &&
      !this.state.sectionLocked(this.section())
    );
  }

  protected canManageBackends(): boolean {
    return (
      this.access.can("PUT /api/deployment/agent-backends/{backendId}") &&
      !this.state.configRepoOnly()
    );
  }

  protected canManageUsage(): boolean {
    return (
      this.access.can("PUT /api/usage/settings") && !this.state.configRepoOnly()
    );
  }

  protected resumePausedModels(): void {
    const targets = this.pausedModelTargets();
    if (!targets.length || !this.access.can("DELETE /api/pause")) return;
    if (targets.length === 1) {
      void this.liftPausedModelTargets(targets);
      return;
    }
    const names = targets.map((t) => t.label).join(", ");
    this.confirmRequest.set({
      title: `Resume ${targets.length} paused model quotas?`,
      message: `Clears the usage-limit pauses on ${names}. Other connection, team, or framework pauses stay in place.`,
      accept: "Resume",
      destructive: false,
      run: () => this.liftPausedModelTargets(targets),
    });
  }

  private async liftPausedModelTargets(
    targets: readonly PausedModelScopeTarget[],
  ): Promise<void> {
    if (!targets.length || this.resumingModels()) return;
    this.resumingModels.set(true);
    try {
      let latestPause: AgentPauseStatus | undefined;
      for (const target of targets) {
        const res = await this.api.delete<{ pause?: AgentPauseStatus }>(
          "DELETE /api/pause",
          undefined,
          undefined,
          pauseScopeQuery(target),
        );
        if (res?.pause) {
          latestPause = res.pause;
        }
      }
      if (latestPause) {
        this.access.patchConfig({ agentPause: latestPause });
      }
      const clearLifted = (
        u: UsagePauseInput | null,
      ): UsagePauseInput | null => {
        if (!u) return null;
        const matchesTarget = (conn?: string, model?: string) =>
          Boolean(conn) &&
          targets.some(
            (t) =>
              t.connection === conn &&
              (!t.model ||
                (Boolean(model) &&
                  t.model.toLowerCase() === model!.toLowerCase())),
          );
        return {
          ...u,
          scopes: u.scopes?.map((s) =>
            matchesTarget(s.connection, s.model)
              ? { ...s, pausedUntil: undefined }
              : s,
          ),
          warnings: (u.warnings ?? []).filter(
            (w) => !/\bis paused until\b/i.test(w),
          ),
        };
      };
      this.usageSnapshot.update(clearLifted);
      this.access.setUsage(clearLifted(this.access.usage()));
      await Promise.all([
        this.state.loadCore(true),
        this.refreshUsageSnapshot(false),
      ]);
      this.snackbar.show(
        targets.length === 1
          ? `Resumed ${targets[0].label}`
          : `Resumed ${targets.length} model quotas`,
      );
    } catch (err) {
      this.state.recordError(err);
    } finally {
      this.resumingModels.set(false);
    }
  }

  protected canEditChain(): boolean {
    return this.canWriteSettings() || this.canWriteDeployment();
  }

  protected canRefreshModels(): boolean {
    return this.access.can(
      "POST /api/deployment/agent-backends/{backendId}/discover",
    );
  }

  protected modelNote(m: string): string {
    return availabilityNote(modelAvailability(m, this.modelGroups()));
  }

  // setModelChain stages the whole list through whichever write this
  // person has: POST /api/settings takes it comma-joined (defaultModel),
  // PUT /api/deployment as a list (defaultModels). Both normalise the same
  // way (model.NormalizeDefaultModel).
  private setModelChain(list: string[]): void {
    const next = parseDefaultModels(list);
    if (this.canWriteSettings()) {
      this.state.setField("defaultModel", next.join(","));
    } else if (this.canWriteDeployment()) {
      this.state.setDeployField("defaultModels", next);
    }
  }

  protected setDefaultModel(m: string): void {
    const rest = this.fallbackModels().filter(
      (x) => x.toLowerCase() !== m.toLowerCase(),
    );
    this.setModelChain(m ? [m, ...rest] : rest);
  }

  protected addFallback(m: string): void {
    const chain = this.modelChain();
    if (!m || !chain.length || containsModel(chain, m)) return;
    this.setModelChain([...chain, m]);
  }

  protected moveFallback(model: string, delta: -1 | 1): void {
    const fb = [...this.fallbackModels()];
    const idx = fb.findIndex((x) => x.toLowerCase() === model.toLowerCase());
    if (idx < 0) return;
    const to = idx + delta;
    if (to < 0 || to >= fb.length) return;
    [fb[idx], fb[to]] = [fb[to], fb[idx]];
    this.setModelChain([this.modelChain()[0], ...fb]);
  }

  protected removeFallback(model: string): void {
    const fb = this.fallbackModels().filter(
      (x) => x.toLowerCase() !== model.toLowerCase(),
    );
    this.setModelChain([this.modelChain()[0], ...fb]);
  }

  protected anyDiscovers(): boolean {
    return this.backends().some((b) => this.discovers(b));
  }

  protected discovers(b: BackendEntry): boolean {
    return !(b.models ?? []).some((m) => m.trim());
  }

  // refreshDiscoveredModels asks providers which models they serve now
  // (POST .../discover): one connection, or every connection that
  // advertises what its provider reports.
  protected async refreshDiscoveredModels(only?: string): Promise<void> {
    const ids = this.backends()
      .filter((b) => (only ? b.id === only : this.discovers(b)))
      .map((b) => b.id);
    if (!ids.length || !this.canRefreshModels()) return;
    this.refreshingModels.set(true);
    this.refreshNote.set("");
    const problems: string[] = [];
    let asked = 0;
    for (const backendId of ids) {
      try {
        const res = await this.api.post<{
          backend?: BackendEntry;
          asked?: boolean;
          error?: string;
        }>("POST /api/deployment/agent-backends/{backendId}/discover", {
          backendId,
        });
        if (res?.backend) {
          const fresh = res.backend;
          this.backends.update((list) =>
            list.map((b) => (b.id === fresh.id ? { ...b, ...fresh } : b)),
          );
        }
        if (res?.asked) asked++;
        if (res?.error) problems.push(`${backendId}: ${res.error}`);
      } catch (err) {
        problems.push(`${backendId}: ${errText(err)}`);
      }
    }
    this.refreshingModels.set(false);
    const note =
      (asked
        ? `Refreshed ${asked} connection${asked === 1 ? "" : "s"}.`
        : "No provider was asked.") +
      (problems.length ? ` ${problems.join("; ")}` : "");
    this.refreshNote.set(note);
    this.snackbar.show(note);
  }

  protected readonly effortVocab = computed(() => {
    const s = this.state.settings();
    const list = (v: unknown) =>
      Array.isArray(v) ? v.map((x) => String(x)) : [];
    return {
      gemini: list(s["geminiEfforts"]),
      codex: list(s["codexEfforts"]),
    };
  });

  // effortFor says whether m takes a separate reasoning effort, from the
  // framework of the connections that advertise it.
  protected effortFor(m: string): EffortSupport {
    return effortSupport(m, this.modelGroups(), this.effortVocab());
  }

  protected modelEffort(m: string): string {
    const key = m.trim().toLowerCase();
    return (
      this.modelSettingsRows().find((r) => r.model.trim().toLowerCase() === key)
        ?.effort ?? ""
    );
  }

  // effortOptionsFor is "Model default" plus m's framework's vocabulary,
  // and the stored effort flagged when that vocabulary does not offer it.
  protected effortOptionsFor(m: string): PillOption[] {
    const es = this.effortFor(m);
    const choices = es.separate ? es.choices : [];
    const cur = this.modelEffort(m);
    const out: PillOption[] = [{ value: "", label: "Model default" }];
    if (cur && !choices.includes(cur)) {
      out.push({ value: cur, label: `${cur} (not offered)` });
    }
    for (const e of choices) out.push({ value: e, label: e });
    return out;
  }

  // setModelEffort stages m's effort (model.ModelSetting), or drops m's
  // setting for "", keeping every other model's setting as stored.
  protected setModelEffort(m: string, effort: string): void {
    const key = m.trim().toLowerCase();
    const value = effort.trim();
    const rows = this.modelSettingsRows();
    const at = rows.findIndex((r) => r.model.trim().toLowerCase() === key);
    const next = rows.filter((_, i) => i !== at);
    if (value) {
      const row = { ...(at >= 0 ? rows[at] : { model: m }), effort: value };
      next.splice(at >= 0 ? at : next.length, 0, row);
    }
    this.state.setDeployField("modelSettings", next);
  }

  // otherModelSettingsRows are the effort settings of models outside the
  // default-model list, whose rows carry their own effort.
  protected otherModelSettingsRows(): ModelSettingRow[] {
    const chain = this.modelChain();
    return this.modelSettingsRows().filter(
      (r) => !containsModel(chain, r.model),
    );
  }

  protected modelSettingsRows(): ModelSettingRow[] {
    const v = this.state.deployVal<unknown>("modelSettings", []);
    return Array.isArray(v) ? (v as ModelSettingRow[]) : [];
  }

  // adderEffortOptions lists only real effort levels from the chosen
  // model's framework vocabulary -- never an empty "Model default" entry,
  // since removing a per-model effort is the table row's Remove action.
  protected adderEffortOptions(): PillOption[] {
    const name = this.newModelSettingName().trim();
    if (!name) return [];
    const es = this.effortFor(name);
    if (!es.separate) return [];
    return es.choices.map((e) => ({ value: e, label: e }));
  }

  protected pickModelSettingName(m: string): void {
    this.newModelSettingName.set(m);
    const name = m.trim();
    if (!name) return;
    const es = this.effortFor(name);
    const choices = es.separate ? es.choices : [];
    const stored = this.modelEffort(name);
    const cur = this.newModelSettingEffort().trim();
    const next =
      stored && choices.includes(stored)
        ? stored
        : cur && choices.includes(cur)
          ? cur
          : choices.includes("high")
            ? "high"
            : (choices[0] ?? "");
    this.newModelSettingEffort.set(next);
  }

  private readonly adderEffortReconcile = effect(() => {
    const name = this.newModelSettingName().trim();
    if (!name) return;
    const es = this.effortFor(name);
    const choices = es.separate ? es.choices : [];
    const cur = untracked(() => this.newModelSettingEffort().trim());
    if (choices.length === 0) {
      if (cur !== "") this.newModelSettingEffort.set("");
      return;
    }
    if (!cur || !choices.includes(cur)) {
      const stored = untracked(() => this.modelEffort(name));
      const next =
        stored && choices.includes(stored)
          ? stored
          : choices.includes("high")
            ? "high"
            : (choices[0] ?? "");
      this.newModelSettingEffort.set(next);
    }
  });

  protected canAddModelSetting(): boolean {
    if (!this.canWriteDeployment()) return false;
    const model = this.newModelSettingName().trim();
    if (!model) return false;
    const es = this.effortFor(model);
    if (!es.separate) return false;
    const effort = this.newModelSettingEffort().trim();
    return Boolean(effort) && es.choices.includes(effort);
  }

  protected addModelSetting(): void {
    const model = this.newModelSettingName().trim();
    if (!model) return;
    const es = this.effortFor(model);
    if (!es.separate) return;
    const effort = this.newModelSettingEffort().trim();
    if (!effort || !es.choices.includes(effort)) return;
    this.setModelEffort(model, effort);
    this.newModelSettingName.set("");
  }

  private applyUsageSettings(s?: UsageSettingsDto | null): void {
    if (!s) return;
    this.usageMode.set(s.mode || "advisory");
    this.usageMinWorkers.set(
      s.minWorkers && s.minWorkers > 0 ? s.minWorkers : 1,
    );
    this.usageRetentionDays.set(
      s.retentionDays && s.retentionDays > 0 ? s.retentionDays : 30,
    );
    this.usageBudgets.set(Array.isArray(s.budgets) ? [...s.budgets] : []);
  }

  private async refreshUsageSnapshot(applySettings: boolean): Promise<void> {
    // GET /api/usage reads the whole deployment: a repo-limited role is
    // refused it, so ask only with the scope held globally.
    if (!this.access.canGlobal("GET /api/usage")) return;
    try {
      const u = await this.api.get<Record<string, unknown>>("GET /api/usage");
      if (!u) return;
      if ("scopes" in u || "warnings" in u || "mode" in u) {
        const parsed = u as unknown as UsagePauseInput;
        this.usageSnapshot.set(parsed);
        this.access.setUsage(parsed);
      }
      if (applySettings) {
        this.applyUsageSettings(u["settings"] as UsageSettingsDto | undefined);
      }
      const used = u["billable"] ?? u["tokensToday"] ?? u["totalTokens"];
      if (used !== undefined) {
        this.usageSummary.set(`Reported billable: ${used} tokens`);
      }
    } catch {
      // Non-fatal
    }
  }

  private loadForSection(): void {
    const sec = this.section();
    if (sec === "models") {
      this.syncBackendsFromState();
      void this.refreshUsageSnapshot(true);
    } else if (sec === "sandboxes") {
      if (this.access.canGlobal("GET /api/persistent-sandboxes")) {
        void this.api
          .get<
            | { name: string; state?: string }[]
            | { sandboxes?: { name: string; state?: string }[] }
          >("GET /api/persistent-sandboxes")
          .then((r) => {
            const list = Array.isArray(r) ? r : (r?.sandboxes ?? []);
            this.persistentSandboxes.set(list);
          })
          .catch(() => undefined);
      }
    } else if (sec === "hostds") {
      void this.loadHostds();
    }
  }

  private syncBackendsFromState(): void {
    const raw =
      this.state.settings()["agentBackends"] ??
      this.state.deployment()["agentBackends"];
    if (Array.isArray(raw)) {
      this.backends.set(raw as BackendEntry[]);
    }
  }

  protected setBackendCredDraft(id: string, val: string): void {
    this.backendCredDrafts.update((p) => ({ ...p, [id]: val }));
  }

  protected async saveBackend(): Promise<void> {
    const backendId = this.newBackendId().trim();
    if (!backendId) return;
    if (
      this.editingBackendId() !== backendId &&
      this.backends().some((b) => b.id === backendId)
    ) {
      this.state.error.set(
        `Connection ${backendId} already exists. To edit it, click Edit on its row.`,
      );
      return;
    }
    const models = this.newBackendModels()
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
    const name = this.newBackendName().trim();
    const authMode = this.newBackendAuthMode().trim();
    const hostd = this.newBackendHostd().trim();
    const cliPath = this.newBackendCliPath().trim();
    const concurrency = this.newBackendConcurrency() || 0;
    if (concurrency !== 0 && concurrency < 1) {
      this.state.error.set(
        "Concurrency must be 1 or greater, or blank for unlimited.",
      );
      return;
    }
    const payload: BackendEntry = {
      id: backendId,
      framework: this.newBackendFw(),
      ...(name ? { name } : {}),
      ...(models.length > 0 ? { models } : {}),
      ...(authMode ? { authMode } : {}),
      ...(cliPath ? { cliPath } : {}),
      ...(concurrency > 0 ? { concurrency } : {}),
      ...(hostd ? { hostd } : {}),
    };
    try {
      await this.api.put(
        "PUT /api/deployment/agent-backends/{backendId}",
        { backendId },
        payload,
      );
      this.newBackendId.set("");
      this.newBackendName.set("");
      this.newBackendModels.set("");
      this.newBackendHostd.set("");
      this.newBackendCliPath.set("");
      this.newBackendConcurrency.set(0);
      this.newBackendFw.set("antigravity");
      this.newBackendAuthMode.set("");
      this.editingBackendId.set("");
      await this.state.loadCore();
      this.syncBackendsFromState();
      // An open Credential row that now holds a subscription login reads
      // its status, as opening it would have.
      const saved = this.backends().find((b) => b.id === backendId);
      if (saved && this.isCodexSubscription(saved))
        this.openCredBackendId.set(backendId);
      this.clearConnectionTest(backendId);
      if (
        this.openCredBackendId() === backendId &&
        saved &&
        this.isCodexSubscription(saved)
      ) {
        void this.loadCodexAuth(backendId);
      }
      this.snackbar.show(`Saved model connection ${backendId}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async saveBackendCred(id: string): Promise<void> {
    const value = (this.backendCredDrafts()[id] ?? "").trim();
    if (!value) return;
    try {
      await this.api.put(
        "PUT /api/agent-backends/{id}/credential",
        { id },
        { value },
      );
      this.setBackendCredDraft(id, "");
      this.clearConnectionTest(id);
      this.snackbar.show(`Updated credential for ${id}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  // Clearing a connection's secret cannot be undone short of pasting the
  // key again, so it asks first, as removing the connection does.
  protected confirmClearBackendCred(id: string): void {
    this.confirmRequest.set({
      title: `Clear the credential for ${id}?`,
      message: `The secret stored for ${id} is deleted. Runs and model discovery on ${id} no longer use it, and only saving the key again brings it back.`,
      accept: "Clear secret",
      destructive: true,
      run: () => this.clearBackendCred(id),
    });
  }

  protected async clearBackendCred(id: string): Promise<void> {
    try {
      await this.api.delete("DELETE /api/agent-backends/{id}/credential", {
        id,
      });
      this.setBackendCredDraft(id, "");
      this.clearConnectionTest(id);
      this.snackbar.show(`Cleared credential for ${id}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected startDeleteBackend(backendId: string): void {
    this.confirmRequest.set({
      title: `Remove connection ${backendId}?`,
      message: `Tasks routed to ${backendId} will fall back to remaining connections or wait until another connection advertises their model.`,
      accept: "Remove",
      destructive: true,
      run: () => this.deleteBackend(backendId),
    });
  }

  protected toggleCredEditor(id: string): void {
    this.openCredBackendId.update((cur) => (cur === id ? null : id));
    const b = this.backends().find((x) => x.id === id);
    if (this.openCredBackendId() === id && b && this.isCodexSubscription(b)) {
      void this.loadCodexAuth(id);
    }
  }

  protected setNewBackendFw(fw: string): void {
    this.newBackendFw.set(fw);
    if (fw !== "codex" && this.newBackendAuthMode() === "subscription") {
      this.newBackendAuthMode.set("");
    }
  }

  protected isCodexSubscription(b: BackendEntry): boolean {
    return (
      (b.framework || "").trim().toLowerCase() === "codex" &&
      b.authMode === "subscription"
    );
  }

  protected codexAuthStatusLabel(id: string): string {
    const status = this.codexAuthStatus()[id];
    if (!status) return "Checking…";
    if (status.enabled === false) return "Unavailable on this daemon";
    return status.set ? "Configured" : "Not set";
  }

  protected setCodexAuthDraft(id: string, value: string): void {
    this.codexAuthDrafts.update((d) => ({ ...d, [id]: value }));
    this.codexAuthErrors.update((d) => ({ ...d, [id]: "" }));
  }

  private setCodexAuthStatus(id: string, res: unknown): void {
    const r = (res ?? {}) as BackendCodexAuthStatus;
    this.codexAuthStatus.update((m) => ({
      ...m,
      [id]: {
        enabled: r.enabled,
        set: Boolean(r.set),
        refreshError: r.refreshError || "",
      },
    }));
  }

  protected async loadCodexAuth(id: string): Promise<void> {
    try {
      const res = await this.api.get(
        "GET /api/agent-backends/{id}/codex-auth",
        { id },
      );
      this.setCodexAuthStatus(id, res);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async saveCodexAuth(id: string): Promise<void> {
    const value = (this.codexAuthDrafts()[id] ?? "").trim();
    if (!value) return;
    try {
      const parsed: unknown = JSON.parse(value);
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed))
        throw new Error();
    } catch {
      this.codexAuthErrors.update((d) => ({
        ...d,
        [id]: "auth.json must contain a JSON object. Paste the contents of the Codex login file.",
      }));
      return;
    }
    this.codexAuthBusy.set(true);
    try {
      const res = await this.api.put(
        "PUT /api/agent-backends/{id}/codex-auth",
        { id },
        { value },
      );
      this.setCodexAuthStatus(id, res);
      this.setCodexAuthDraft(id, "");
      this.clearConnectionTest(id);
      this.snackbar.show(`Saved subscription login for ${id}`);
    } catch {
      this.codexAuthErrors.update((d) => ({
        ...d,
        [id]: "Could not save auth.json. Check your access and daemon credential storage, then retry.",
      }));
    } finally {
      this.codexAuthBusy.set(false);
    }
  }

  protected confirmClearCodexAuth(id: string): void {
    this.confirmRequest.set({
      title: `Clear the subscription login for ${id}?`,
      message: `The auth.json stored for ${id} is deleted. Runs on ${id} stop until a new login is uploaded: the connection never falls back to the deployment's shared login.`,
      accept: "Clear login",
      destructive: true,
      run: () => this.clearCodexAuth(id),
    });
  }

  protected async clearCodexAuth(id: string): Promise<void> {
    this.codexAuthBusy.set(true);
    try {
      const res = await this.api.delete(
        "DELETE /api/agent-backends/{id}/codex-auth",
        { id },
      );
      this.setCodexAuthStatus(id, res);
      this.setCodexAuthDraft(id, "");
      this.clearConnectionTest(id);
      this.snackbar.show(`Cleared subscription login for ${id}`);
    } catch (err) {
      this.state.recordError(err);
    } finally {
      this.codexAuthBusy.set(false);
    }
  }

  protected cancelEditBackend(): void {
    this.editingBackendId.set("");
    this.newBackendId.set("");
    this.newBackendName.set("");
    this.newBackendFw.set("antigravity");
    this.newBackendAuthMode.set("");
    this.newBackendHostd.set("");
    this.newBackendCliPath.set("");
    this.newBackendModels.set("");
    this.newBackendConcurrency.set(0);
  }

  protected editBackend(b: BackendEntry): void {
    this.editingBackendId.set(b.id);
    this.newBackendId.set(b.id);
    this.newBackendName.set(b.name || "");
    this.newBackendFw.set(b.framework || "antigravity");
    this.newBackendAuthMode.set(b.authMode || "");
    this.newBackendHostd.set(b.hostd || "");
    this.newBackendCliPath.set(b.cliPath || "");
    this.newBackendModels.set(b.models?.join(", ") || "");
    this.newBackendConcurrency.set(b.concurrency || 0);
  }

  protected async deleteBackend(backendId: string): Promise<void> {
    try {
      await this.api.delete(
        "DELETE /api/deployment/agent-backends/{backendId}",
        { backendId },
      );
      this.backends.update((list) => list.filter((b) => b.id !== backendId));
      this.snackbar.show(`Removed connection ${backendId}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async loadDiscoveredModels(): Promise<void> {
    try {
      const res = await this.api.get<
        | string[]
        | {
            models?: (string | { id?: string; name?: string })[];
            available?: boolean;
            error?: string;
          }
      >("GET /api/agent-models");
      const raw = Array.isArray(res) ? res : (res?.models ?? []);
      this.discoveredModels.set(
        raw
          .map((m) => (typeof m === "string" ? m : (m.id ?? m.name ?? "")))
          .filter(Boolean),
      );
      if (!Array.isArray(res) && res?.error) {
        this.state.error.set(res.error);
      }
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected addUsageBudget(): void {
    const connection = this.newBudgetConn().trim();
    const tokens = Math.floor(Number(this.newBudgetTokens()));
    const window = this.newBudgetWindow().trim();
    const model = this.newBudgetModel().trim();
    if (!connection || !(tokens > 0) || !window) return;
    this.usageBudgets.update((b) => [
      ...b,
      { connection, ...(model ? { model } : {}), tokens, window },
    ]);
    this.newBudgetConn.set("");
    this.newBudgetModel.set("");
    this.newBudgetTokens.set("");
  }

  protected removeUsageBudget(idx: number): void {
    this.usageBudgets.update((b) => b.filter((_, i) => i !== idx));
  }

  protected async saveUsageSettings(): Promise<void> {
    const payload: UsageSettingsDto = {
      mode: this.usageMode() || "advisory",
      minWorkers: Math.max(1, Math.floor(Number(this.usageMinWorkers()) || 1)),
      retentionDays: Math.max(
        1,
        Math.floor(Number(this.usageRetentionDays()) || 30),
      ),
      budgets: this.usageBudgets(),
    };
    try {
      const res = await this.api.put<{ settings?: UsageSettingsDto }>(
        "PUT /api/usage/settings",
        undefined,
        payload,
      );
      if (res?.settings) {
        this.applyUsageSettings(res.settings);
      }
      this.snackbar.show("Usage settings saved");
    } catch (err) {
      this.state.recordError(err);
    }
  }

  /** A fixed ratio of 0 is accepted (pkg/ui settings.go: "leave this
   * dimension to the run counts alone"), so the box says what it means
   * rather than storing it silently. */
  protected overcommitUnbounded(key: string): boolean {
    if (this.overcommitMode() !== "fixed") return false;
    const raw = this.state.val<unknown>(key, null);
    return raw !== null && raw !== "" && Number(raw) === 0;
  }

  protected overcommitMode(): string {
    const v = this.state.val<unknown>("sandboxOvercommit", "fixed");
    if (v && typeof v === "object" && "mode" in v) {
      return String((v as { mode?: string }).mode || "fixed");
    }
    return typeof v === "string" && v ? v : "fixed";
  }

  protected setOvercommitMode(mode: string): void {
    // Fixed mode hides the auto bounds, so drop both client-side refusals
    // and any unsaved edits to those hidden bounds: otherwise a bad bound
    // typed in auto mode (such as cpuMin > cpuMax) stays in draft and makes
    // the server refuse every Save after switching back to fixed.
    if (mode !== "auto") {
      for (const b of this.overcommitBounds) {
        this.state.setFieldError(this.boundKey(b.field), null);
      }
      const saved = this.state.settings()["sandboxOvercommit"];
      if (saved && typeof saved === "object") {
        this.state.setField("sandboxOvercommit", {
          ...(saved as Record<string, unknown>),
          mode,
        });
      } else {
        this.state.setField("sandboxOvercommit", mode);
      }
      return;
    }
    const cur = this.state.val<unknown>("sandboxOvercommit", null);
    if (cur && typeof cur === "object") {
      this.state.setField("sandboxOvercommit", {
        ...(cur as Record<string, unknown>),
        mode,
      });
    } else {
      this.state.setField("sandboxOvercommit", mode);
    }
  }

  /** The probe is offered only where the server can build one:
   * GET /api/settings says so in sandboxProbe (pkg/ui/sandbox_probe.go). */
  protected canProbeSandbox(): boolean {
    return (
      this.access.can("POST /api/sandbox-probe") &&
      this.state.val<boolean>("sandboxProbe", false) === true
    );
  }

  /** POST /api/sandbox-probe as `grain sandbox probe -id <id> -- true`
   * does: a fresh id names the throwaway sandbox, and `true` is the
   * least a sandbox that booted can run. */
  protected async runSandboxProbe(): Promise<void> {
    if (this.probing()) return;
    this.probing.set(true);
    this.probeResult.set(null);
    try {
      const id = `ui-${Date.now().toString(36)}-${Math.random()
        .toString(36)
        .slice(2, 6)}`;
      const res = await this.api.post<SandboxProbeResult>(
        "POST /api/sandbox-probe",
        undefined,
        { id, argv: ["true"] },
      );
      this.probeResult.set({
        sandbox: res?.sandbox ?? `probe-${id}`,
        pod: res?.pod ?? "",
        access: res?.access ?? "",
        stdout: res?.stdout ?? "",
        stderr: res?.stderr ?? "",
        exitCode: Number(res?.exitCode ?? 0),
      });
    } catch (err) {
      this.state.recordError(err);
    } finally {
      this.probing.set(false);
    }
  }

  protected async actPersistentSandbox(
    name: string,
    action: string,
  ): Promise<void> {
    try {
      await this.api.post(
        "POST /api/persistent-sandboxes/{name}/{action}",
        { name, action },
        {},
      );
      this.snackbar.show(`Sandbox ${name}: ${action}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async loadHostds(): Promise<void> {
    if (this.access.canGlobal("GET /api/hostds")) {
      try {
        const r = await this.api.get<
          HostdRow[] | { hostds?: HostdRow[]; items?: HostdRow[] }
        >("GET /api/hostds");
        this.hostds.set(Array.isArray(r) ? r : (r?.hostds ?? r?.items ?? []));
      } catch {
        // Non-fatal
      }
    }
    if (this.access.can("GET /api/hostds/enrollment-codes")) {
      try {
        const c = await this.api.get<
          | EnrollmentCodeRow[]
          | { codes?: EnrollmentCodeRow[]; items?: EnrollmentCodeRow[] }
        >("GET /api/hostds/enrollment-codes");
        this.enrollmentCodes.set(
          Array.isArray(c) ? c : (c?.codes ?? c?.items ?? []),
        );
      } catch {
        // Non-fatal
      }
    }
    if (this.access.can("GET /api/hostds/pending")) {
      try {
        const p = await this.api.get<
          | PendingHostdRow[]
          | { pending?: PendingHostdRow[]; items?: PendingHostdRow[] }
        >("GET /api/hostds/pending");
        this.pendingHostds.set(
          Array.isArray(p) ? p : (p?.pending ?? p?.items ?? []),
        );
      } catch {
        // Non-fatal
      }
    }
  }

  private formatIssuedHostdSecret(
    label: string,
    res?: { token?: string; install?: HostdInstallDto | string } | null,
  ): string | null {
    if (!res) return null;
    const parts: string[] = [];
    if (typeof res.token === "string" && res.token) {
      parts.push(`${label} (shown once): ${res.token}`);
    }
    if (typeof res.install === "string" && res.install.trim()) {
      parts.push(res.install.trim());
    } else if (res.install && typeof res.install === "object") {
      if (typeof res.install.command === "string" && res.install.command) {
        parts.push(res.install.command);
      } else if (Array.isArray(res.install.lines) && res.install.lines.length) {
        parts.push(res.install.lines.join("\n"));
      }
      if (typeof res.install.note === "string" && res.install.note) {
        parts.push(`# ${res.install.note}`);
      }
    }
    return parts.length > 0 ? parts.join("\n\n") : null;
  }

  protected async createHostd(): Promise<void> {
    const name = this.newHostdName().trim();
    if (!name) return;
    const parsed = parseHostdLabels(this.newHostdLabels());
    if (parsed.error) {
      this.state.error.set(parsed.error);
      return;
    }
    try {
      const res = await this.api.post<{
        token?: string;
        install?: HostdInstallDto | string;
        hostd?: HostdRow;
      }>("POST /api/hostds", undefined, {
        name,
        ...(this.newHostdOwner().trim()
          ? { owner: this.newHostdOwner().trim() }
          : {}),
        ...(Object.keys(parsed.labels).length > 0
          ? { labels: parsed.labels }
          : {}),
      });
      this.newHostdName.set("");
      this.newHostdLabels.set("");
      this.newHostdOwner.set("");
      const msg = this.formatIssuedHostdSecret(`Host token (${name})`, res);
      if (msg) this.mintedHostdToken.set(msg);
      await this.loadHostds();
      this.snackbar.show(`Registered host ${name}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected rotateHostd(name: string): void {
    this.confirmRequest.set({
      title: `Rotate host ${name}'s token?`,
      message:
        "Its current token stops working at once, so the host is disconnected until the new token is installed on it.",
      accept: "Rotate token",
      destructive: true,
      run: async () => {
        try {
          const res = await this.api.post<{
            token?: string;
            install?: HostdInstallDto | string;
          }>("POST /api/hostds/{name}/rotate", { name }, {});
          const msg = this.formatIssuedHostdSecret(
            `Rotated token (${name})`,
            res,
          );
          if (msg) this.mintedHostdToken.set(msg);
          await this.loadHostds();
          this.snackbar.show(`Rotated ${name}`);
        } catch (err) {
          this.state.recordError(err);
        }
      },
    });
  }

  protected revokeHostd(name: string): void {
    this.confirmRequest.set({
      title: `Revoke host ${name}?`,
      message:
        "It is disconnected at once and cannot be restored: using that machine again means enrolling it anew. A revoked host can then be deleted.",
      accept: "Revoke host",
      destructive: true,
      run: async () => {
        try {
          await this.api.post("POST /api/hostds/{name}/revoke", { name }, {});
          await this.loadHostds();
          this.snackbar.show(`Revoked ${name}`);
        } catch (err) {
          this.state.recordError(err);
        }
      },
    });
  }

  protected deleteHostd(name: string): void {
    this.confirmRequest.set({
      title: `Delete host ${name}?`,
      message:
        "Its tokens go with it and the name can be registered again; past runs keep showing it, marked deleted.",
      accept: "Delete host",
      destructive: true,
      run: async () => {
        try {
          this.hostdNotice.set(null);
          const out = await this.api.delete<DeletedHostdRow>(
            "DELETE /api/hostds/{name}",
            { name },
          );
          await this.loadHostds();
          this.hostdNotice.set({
            tone: "ok",
            lines: [deletedHostdLine(out ?? { name })],
          });
        } catch (err) {
          this.state.recordError(err);
        }
      },
    });
  }

  protected deleteRevokedHostds(): void {
    this.confirmRequest.set({
      title: "Delete every revoked host?",
      message:
        "One a model connection or capability server still names is skipped.",
      accept: "Delete revoked",
      destructive: true,
      run: async () => {
        try {
          const out = await this.api.post<{
            deleted?: DeletedHostdRow[] | null;
            skipped?: { name: string; reason: string }[] | null;
          }>("POST /api/hostds/delete-revoked", undefined, {});
          await this.loadHostds();
          const lines = (out?.deleted ?? []).map(deletedHostdLine);
          const skipped = out?.skipped ?? [];
          for (const sk of skipped) {
            lines.push(`Skipped ${sk.name}: ${sk.reason}`);
          }
          if (lines.length === 0) lines.push("No revoked hosts to delete.");
          this.hostdNotice.set({
            tone: skipped.length > 0 ? "warn" : "ok",
            lines,
          });
        } catch (err) {
          this.state.recordError(err);
        }
      },
    });
  }

  protected selectEnrollmentCode(name: string): void {
    this.chosenEnrollmentCode.set(name);
    this.enrollmentSecretShown.set(false);
  }

  protected async copyInvite(text: string, notice: string): Promise<void> {
    const ok = await copyToClipboard(this.doc, text);
    this.snackbar.show(ok ? notice : "Could not copy: select the text instead");
  }

  protected async createEnrollmentCode(): Promise<void> {
    const name = this.newCodeName().trim();
    if (!name) return;
    const labels = parseHostdLabels(this.newCodeLabels());
    const maxUses = parseHostCapacity(this.newCodeMaxUses(), "Max uses");
    const capacity = parseHostCapacity(
      this.newCodeCapacity(),
      "Default capacity",
    );
    const invalid = labels.error || maxUses.error || capacity.error;
    if (invalid) {
      this.raiseHostMessage(invalid);
      return;
    }
    const principals = this.newCodePrincipals()
      .split(",")
      .map((x) => x.trim())
      .filter(Boolean);
    const expiresIn = this.newCodeExpiresIn().trim();
    try {
      const res = await this.api.post<{
        code?: string;
        token?: string;
        install?: HostdInstallDto | string;
      }>("POST /api/hostds/enrollment-codes", undefined, {
        name,
        ...(expiresIn ? { expires_in: expiresIn } : {}),
        ...(maxUses.value > 0 ? { max_uses: maxUses.value } : {}),
        ...(capacity.value > 0 ? { default_capacity: capacity.value } : {}),
        ...(Object.keys(labels.labels).length > 0
          ? { default_labels: labels.labels }
          : {}),
        ...(principals.length > 0 ? { allowed_principals: principals } : {}),
      });
      this.settleHostError();
      this.newCodeName.set("");
      this.newCodeExpiresIn.set("");
      this.newCodeMaxUses.set("");
      this.newCodeCapacity.set("");
      this.newCodeLabels.set("");
      this.newCodePrincipals.set("");
      this.selectEnrollmentCode(name);
      const secret = res?.code ?? res?.token;
      const msg = this.formatIssuedHostdSecret(`Enrollment code (${name})`, {
        token: secret,
        install: res?.install,
      });
      if (msg) this.mintedHostdToken.set(msg);
      await this.loadHostds();
      this.snackbar.show(`Created enrollment code ${name}`);
    } catch (err) {
      this.raiseHostError(err);
    }
  }

  // codeDefaults is what a code gives each host it enrolls, one line each.
  protected codeDefaults(c: EnrollmentCodeRow): string[] {
    const out: string[] = [];
    if (c.default_capacity && c.default_capacity > 0) {
      out.push(`capacity ${c.default_capacity}`);
    }
    const labels = this.formatLabelsInput(c.default_labels);
    if (labels) out.push(labels);
    for (const p of c.allowed_principals ?? []) out.push(`allow ${p}`);
    return out;
  }

  protected rotateEnrollmentCode(name: string): void {
    this.confirmRequest.set({
      title: `Rotate enrollment code ${name}?`,
      message:
        "Its invite command stops working at once, so hosts not yet joined with it need the new one. Hosts already joined keep their credentials.",
      accept: "Rotate code",
      destructive: true,
      run: async () => {
        try {
          const res = await this.api.post<{
            code?: string;
            token?: string;
            install?: HostdInstallDto | string;
          }>("POST /api/hostds/enrollment-codes/{name}/rotate", { name }, {});
          const secret = res?.code ?? res?.token;
          const msg = this.formatIssuedHostdSecret(
            `Rotated enrollment code (${name})`,
            { token: secret, install: res?.install },
          );
          if (msg) this.mintedHostdToken.set(msg);
          await this.loadHostds();
          this.snackbar.show(`Rotated code ${name}`);
        } catch (err) {
          this.state.recordError(err);
        }
      },
    });
  }

  protected revokeEnrollmentCode(name: string): void {
    this.confirmRequest.set({
      title: `Revoke enrollment code ${name}?`,
      message:
        "Its invite command stops working for good and the code cannot be restored. Hosts already joined keep their credentials.",
      accept: "Revoke code",
      destructive: true,
      run: async () => {
        try {
          await this.api.post(
            "POST /api/hostds/enrollment-codes/{name}/revoke",
            { name },
            {},
          );
          await this.loadHostds();
          this.snackbar.show(`Revoked code ${name}`);
        } catch (err) {
          this.state.recordError(err);
        }
      },
    });
  }

  protected formatLabelsInput(labels?: Record<string, string> | null): string {
    const formatted = formatHostdLabels(labels);
    return formatted === "—" ? "" : formatted;
  }

  protected pendingDraft(p: PendingHostdRow): PendingDraft {
    return (
      this.pendingDrafts()[p.id] ?? {
        name: p.rekey_hostd || p.suggested_name || p.hostname || "",
        labels: this.formatLabelsInput(p.default_labels),
        capacity: p.default_capacity ? String(p.default_capacity) : "",
      }
    );
  }

  protected patchPendingDraft(
    p: PendingHostdRow,
    patch: Partial<PendingDraft>,
  ): void {
    const cur = this.pendingDraft(p);
    this.pendingDrafts.update((all) => ({
      ...all,
      [p.id]: { ...cur, ...patch },
    }));
  }

  protected async acceptPendingHostd(p: PendingHostdRow): Promise<void> {
    const d = this.pendingDraft(p);
    const labels = parseHostdLabels(d.labels);
    const capacity = parseHostCapacity(d.capacity, "Host capacity");
    const invalid = labels.error || capacity.error;
    if (invalid) {
      this.raiseHostMessage(invalid);
      return;
    }
    const hostName = d.name.trim();
    if (!hostName) return;
    try {
      // labels and capacity are always sent: a field the owner cleared
      // overrides the code's defaults rather than inheriting them.
      await this.api.post(
        "POST /api/hostds/pending/{id}/accept",
        { id: p.id },
        { name: hostName, labels: labels.labels, capacity: capacity.value },
      );
      this.settleHostError();
      this.pendingDrafts.update((all) => {
        const next = { ...all };
        delete next[p.id];
        return next;
      });
      await this.loadHostds();
      this.snackbar.show(
        p.rekey_hostd
          ? `Accepted the new key for ${hostName}`
          : `Approved host ${hostName}`,
      );
    } catch (err) {
      this.raiseHostError(err);
    }
  }

  protected startHostdEdit(h: HostdRow): void {
    this.settleHostError();
    this.renamingHostd.set(null);
    this.editingHostd.set({
      name: h.name,
      owner: h.owner ?? "",
      labels: this.formatLabelsInput(h.labels),
      capacity: h.capacity ? String(h.capacity) : "",
    });
  }

  protected patchHostdEdit(patch: Partial<HostdEdit>): void {
    this.editingHostd.update((e) => (e ? { ...e, ...patch } : e));
  }

  protected cancelHostdEdit(): void {
    this.settleHostError();
    this.editingHostd.set(null);
    this.renamingHostd.set(null);
  }

  // saveHostdEdit sends owner, labels and capacity together; a blank
  // capacity is 0, which clears it (unlimited), where leaving it out
  // would keep the old one.
  protected async saveHostdEdit(): Promise<void> {
    const e = this.editingHostd();
    if (!e) return;
    const labels = parseHostdLabels(e.labels);
    const capacity = parseHostCapacity(e.capacity, "Capacity");
    const invalid = labels.error || capacity.error;
    if (invalid) {
      this.raiseHostMessage(invalid);
      return;
    }
    try {
      await this.api.patch(
        "PATCH /api/hostds/{name}",
        { name: e.name },
        {
          owner: e.owner.trim(),
          labels: labels.labels,
          capacity: capacity.value,
        },
      );
      this.settleHostError();
      this.editingHostd.set(null);
      await this.loadHostds();
      this.snackbar.show(`Saved host ${e.name}`);
    } catch (err) {
      this.raiseHostError(err);
    }
  }

  protected startRenameHostd(name: string): void {
    this.settleHostError();
    this.editingHostd.set(null);
    this.renamingHostd.set({ from: name, to: name });
  }

  protected setRenameDraft(to: string): void {
    this.renamingHostd.update((r) => (r ? { ...r, to } : r));
  }

  protected async saveRenameHostd(): Promise<void> {
    const r = this.renamingHostd();
    const to = r?.to.trim() ?? "";
    if (!r || !to) return;
    if (to === r.from) {
      this.renamingHostd.set(null);
      return;
    }
    try {
      await this.api.post(
        "POST /api/hostds/{name}/rename",
        { name: r.from },
        { name: to },
      );
      this.settleHostError();
      this.renamingHostd.set(null);
      await this.loadHostds();
      this.snackbar.show(`Renamed host ${r.from} to ${to}`);
    } catch (err) {
      this.raiseHostError(err);
    }
  }

  private raiseHostError(err: unknown): void {
    this.state.recordError(err);
    this.raisedHostError = this.state.error();
  }

  private raiseHostMessage(message: string): void {
    this.state.error.set(message);
    this.raisedHostError = message;
  }

  private settleHostError(): void {
    if (this.raisedHostError === null) return;
    if (this.state.error() === this.raisedHostError) this.state.clearError();
    this.raisedHostError = null;
  }

  protected rejectPendingHostd(p: PendingHostdRow): void {
    this.confirmRequest.set({
      title: `Reject the join request from ${p.hostname || p.id}?`,
      message:
        "It is discarded, and that host has to run its join command again to ask once more.",
      accept: "Reject request",
      destructive: true,
      run: async () => {
        try {
          await this.api.post(
            "POST /api/hostds/pending/{id}/reject",
            { id: p.id },
            {},
          );
          await this.loadHostds();
          this.snackbar.show("Rejected pending host request");
        } catch (err) {
          this.state.recordError(err);
        }
      },
    });
  }

  protected async onConfirmDone(accepted: boolean): Promise<void> {
    const req = this.confirmRequest();
    this.confirmRequest.set(null);
    if (accepted && req) {
      try {
        await req.run();
      } catch (err) {
        this.state.recordError(err);
      }
    }
  }
}
