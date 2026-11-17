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

import {
  ChangeDetectionStrategy,
  Component,
  HostListener,
  OnDestroy,
  OnInit,
  ViewEncapsulation,
  afterRenderEffect,
  computed,
  ElementRef,
  inject,
  input,
  signal,
  viewChild,
} from "@angular/core";
import { ActivatedRoute, Router, RouterLink } from "@angular/router";
import type { Subscription } from "rxjs";
import type { RouteKey } from "../api/generated-routes";
import { GrainApi, splitRepo } from "../api/grain-api.service";
import type { BaseBranchGate, BaseBranchGatesResponse } from "../api/models";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { errText } from "../settings/settings-state.service";
import {
  ConfirmDialogComponent,
  bypassConfirm,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import type { CapabilityOption } from "../api/models";
import { RepoCICapabilitiesComponent } from "./repo-ci-capabilities.component";
import { RepoQualificationPlanComponent } from "./repo-qualification-plan.component";
import { RepoWorkOwnershipComponent } from "./repo-work-ownership.component";
import { StatusIconComponent } from "../shared/status-icon.component";

// CI_MODE_ID is the CI mode control's anchor on the CI & Workflows tab,
// which an unjudged gate's warning links to: on a repository grain CI is
// not enabled for, that control is what enrols its watched branch.
export const CI_MODE_ID = "repo-ci-mode";

// unjudgedGatesFor is the base-branch gates nothing is judging for repo,
// as the classic Repo page's header and Releases pane pick them out of
// GET /api/base-branch-gates. A gate that is only late is left to the
// release settings it sits beside, as on classic.
export function unjudgedGatesFor(
  gates: readonly BaseBranchGate[],
  repo: string,
): BaseBranchGate[] {
  const want = repo.toLowerCase();
  return gates.filter((g) => !g.judged && g.repo.toLowerCase() === want);
}

export type RepoTabId =
  | "overview"
  | "ci"
  | "merge-queue"
  | "agents"
  | "branches"
  | "releases"
  | "advanced";

const REPO_TABS: readonly { id: RepoTabId; label: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "ci", label: "CI & Workflows" },
  { id: "merge-queue", label: "Merge Queue" },
  { id: "agents", label: "Agents & Grants" },
  { id: "branches", label: "Branches" },
  { id: "releases", label: "Releases" },
  { id: "advanced", label: "Advanced" },
];

function isRepoTabId(val: string | null | undefined): val is RepoTabId {
  const clean = (val ?? "").toLowerCase();
  return REPO_TABS.some((t) => t.id === clean);
}

function normalizeWorkflowPath(raw: string): string {
  const clean = raw.trim().replace(/^\/+/, "");
  if (!clean) return "";
  if (clean.includes("/")) return clean;
  const withExt =
    clean.endsWith(".yml") || clean.endsWith(".yaml") ? clean : `${clean}.yml`;
  return `.grain/workflows/${withExt}`;
}

// importOutcomeText words one import pass the way the classic Repo page
// does (ui/src/components/RepoPage.jsx importIssuesNow): an error with
// nothing open is the listing itself failing, so nothing was read.
export function importOutcomeText(
  out: {
    source?: string;
    open?: number;
    imported?: string[] | null;
    skipped?: number;
    error?: string;
  },
  repo: string,
): { error: boolean; text: string } {
  const source = out.source || repo;
  const open = out.open ?? 0;
  if (out.error && !open) {
    return {
      error: true,
      text: `Could not read the open issues on ${source}: ${out.error}`,
    };
  }
  const n = (out.imported ?? []).length;
  let text = `Read ${open} open issue${open === 1 ? "" : "s"} on ${source}: imported ${n}, ${out.skipped ?? 0} already imported.`;
  if (out.error) text += ` Some could not be imported: ${out.error}`;
  return { error: Boolean(out.error), text };
}

// releaseActionReasons says why Cut RC and Promote RC are off for a
// release, one sentence each, or null for a button that is on: the
// classic pane's rule (ui/src/components/RepoReleases.jsx). candidate is
// the release's newest candidate, undefined when it is not loaded.
export function releaseActionReasons(
  release: { name: string; status?: string; latestBranch?: string },
  candidate: { branch?: string; status?: string } | null | undefined,
  loaded: boolean,
): { cut: string | null; promote: string | null } {
  const status = release.status || "active";
  if (status !== "active") {
    const why =
      status === "provisioning"
        ? `${release.name} is still provisioning: grain cuts and promotes its candidates once its ${release.latestBranch || release.name + ".latest"} branch exists.`
        : `${release.name} is ${status}: only an active release cuts or promotes candidates.`;
    return { cut: why, promote: why };
  }
  if (!loaded) return { cut: null, promote: null };
  let cut: string | null = null;
  if (candidate && candidate.status !== "promoted") {
    cut =
      candidate.status === "cutting"
        ? `${candidate.branch} is still being cut.`
        : `${candidate.branch} is ${candidate.status || "active"}: a new RC is cut once it is promoted.`;
  }
  let promote: string | null = null;
  if (!candidate) {
    promote = "No candidate to promote yet: cut one first.";
  } else if (candidate.status === "promoted") {
    promote = `${candidate.branch} is already promoted: a candidate is promoted once.`;
  } else if (candidate.status && candidate.status !== "active") {
    promote = `${candidate.branch} is ${candidate.status}: only an active candidate can be promoted.`;
  }
  return { cut, promote };
}

function workflowKey(list: readonly { path: string; text: string }[]): string {
  return JSON.stringify(list.map((w) => [w.path, w.text ?? ""]));
}

interface ImportFields {
  enabled: boolean;
  label: string;
  repo: string;
  approved: boolean;
  // interval is the "Every (minutes)" box's text, empty for the default.
  interval: string;
}

// importIntervalText is a stored issue-import interval in the box's own
// shape: whole minutes, empty for the default (0).
export function importIntervalText(seconds: number | undefined): string {
  return seconds ? String(Math.round(seconds / 60)) : "";
}

// importIntervalSeconds reads the "Every (minutes)" box: 0 for empty (the
// default), the seconds otherwise, or an error for anything the server
// would refuse (PUT .../settings: 0 or at least a minute).
export function importIntervalSeconds(
  text: string,
): { seconds: number; error: null } | { seconds: null; error: string } {
  const t = text.trim();
  if (t === "") return { seconds: 0, error: null };
  const n = Number(t);
  if (!Number.isFinite(n) || n < 1) {
    return {
      seconds: null,
      error: "Every (minutes) must be at least 1, or empty for the default.",
    };
  }
  return { seconds: Math.round(n * 60), error: null };
}

interface AgentFields {
  setup: string;
  prompt: string;
  caps: string;
}

interface BranchRow {
  name: string;
  sha?: string;
  head?: string;
  protected?: boolean;
}

interface RepoWorkflowRow {
  path: string;
  text: string;
  name?: string;
}

interface ReleaseRow {
  name: string;
  latestBranch?: string;
  prodBranch?: string;
  status?: string;
  pullRequestUrl?: string;
  error?: string;
}

interface CandidateRow {
  id: number | string;
  release?: string;
  number?: number;
  branch?: string;
  status?: string;
  version?: string;
  error?: string;
}

interface RepoReleaseSecretRow {
  name: string;
  present?: boolean;
}

// What GET /api/repos/{owner}/{name}/declarations answers with
// (ui.RepoDefaults), the part the Advanced tab reads.
interface DeclaredPushRow {
  path: string;
  refs: { ref: string; force?: boolean; delete?: boolean }[];
}

interface DeclarationsStatus {
  commit?: string;
  error?: string;
  templates?: string[];
  suites?: string[];
  schedules?: string[];
  triggers?: string[];
  pushes?: DeclaredPushRow[];
  warnings?: string[];
}

interface PortDirection {
  from: string;
  to: string;
  paired?: boolean;
  operatorTypes?: string[];
  declaredTypes?: string[];
  types?: string[];
}

interface RepoDeclarations {
  declarations?: boolean;
  declarationsStatus?: DeclarationsStatus | null;
  portBranches?: string[];
  declaredPortBranches?: string[];
  pairedBranches?: string[];
  portDirections?: PortDirection[];
  portWarnings?: string[];
}

// declaredSummary counts what a declarations status says the repository
// declares, the classic page's words: "1 template(s), 1 suite(s), 0
// schedule(s), 0 trigger(s)".
export function declaredSummary(st: DeclarationsStatus): string {
  return (
    `${(st.templates ?? []).length} template(s), ` +
    `${(st.suites ?? []).length} suite(s), ` +
    `${(st.schedules ?? []).length} schedule(s), ` +
    `${(st.triggers ?? []).length} trigger(s)`
  );
}

// declaredPushRef is one push entry as it reads: "refs/tags/latest
// (force)".
export function declaredPushRef(r: {
  ref: string;
  force?: boolean;
  delete?: boolean;
}): string {
  const flags = [r.force && "force", r.delete && "delete"].filter(Boolean);
  return flags.length ? `${r.ref} (${flags.join(", ")})` : r.ref;
}

// removeRepoMessage is what the Remove confirm says: what dropping repo
// from allowed does to new work, then what POST /api/repos/plan says
// the stored work loses (stranded, one summary each). Taking the only
// entry off the allowlist does not narrow it to nothing: an empty
// allowlist means unrestricted (model.RepoAllowed), and the confirm
// says so rather than promise a narrowing that does not happen.
export function removeRepoMessage(
  repo: string,
  allowed: readonly string[],
  stranded: readonly string[],
): string {
  const rest = allowed.filter((r) => r.toLowerCase() !== repo.toLowerCase());
  const unrestricts = allowed.length > 0 && rest.length === 0;
  const consequence = unrestricts
    ? `${repo} is the only repo this deployment allows. An empty allowlist is what means unrestricted, so removing it allows any repo again: new tasks may target any repo, ${repo} included. To stay restricted, add the repo you want first.`
    : `New tasks won't be able to target ${repo} until it's added back, and it drops off the repos list.`;
  const cost =
    stranded.length === 0
      ? "Tasks already filed there are not affected."
      : stranded.join(" ");
  return `${consequence} ${cost}`;
}

interface PendingReleaseRow {
  version: string;
  summary?: string;
  soakReady?: boolean;
}

@Component({
  selector: "g-repo-detail-page",
  imports: [
    RouterLink,
    PillSelectComponent,
    ConfirmDialogComponent,
    RepoCICapabilitiesComponent,
    RepoQualificationPlanComponent,
    RepoWorkOwnershipComponent,
    StatusIconComponent,
  ],
  encapsulation: ViewEncapsulation.None,
  styleUrls: [
    "../shared/work-surface.css",
    "../shared/surface-controls.css",
    "../settings/settings-forms.css",
    "./repo-detail.css",
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <section class="view-panel is-active" aria-labelledby="repo-page-heading">
      <div class="repo-page-scroll">
        <div class="repo-page-inner">
          <div class="repo-top-nav">
            <a
              class="btn-text"
              routerLink="/repos"
              data-testid="back-to-repos-link"
              >← Repos</a
            >
            <a
              class="btn-tonal s-btn-sm"
              style="text-decoration: none"
              [routerLink]="['/tasks']"
              [queryParams]="{ repo: repoSlug() }"
              >View tasks in {{ repoSlug() }}</a
            >
          </div>

          <div>
            <h1
              class="view-headline tabular"
              id="repo-page-heading"
              data-testid="repo-detail-heading"
            >
              {{ repoSlug() }}
            </h1>
            <p class="view-subtitle">
              Per-repository Git connection, CI workflows, merge gates,
              branches, release qualification, and what the repository declares
              for itself.
            </p>
          </div>

          <nav class="repo-tabs" aria-label="Repository sections">
            @for (t of tabs; track t.id) {
              <button
                type="button"
                class="repo-tab-btn"
                [class.is-active]="activeTab() === t.id"
                [attr.aria-current]="activeTab() === t.id ? 'page' : null"
                [attr.data-testid]="'repo-tab-' + t.id"
                (click)="selectTab(t.id)"
              >
                {{ t.label }}
              </button>
            }
          </nav>

          @if (configRepoOnly()) {
            <div class="s-banner readonly" data-testid="repo-readonly-banner">
              Repository configuration is managed in the config repository;
              operational actions (branches, issue import, and releases) remain
              available according to your role.
            </div>
          }

          @if (error(); as err) {
            <div
              class="s-banner error"
              role="alert"
              data-testid="repo-error-banner"
            >
              <span>{{ err }}</span>
              <button
                type="button"
                class="btn-text s-btn-sm"
                (click)="error.set(null)"
              >
                Dismiss
              </button>
            </div>
          }

          @if (ready()) {
            @switch (activeTab()) {
              @case ("overview") {
                <!-- A watched base branch nothing judges (GET
                     /api/base-branch-gates): with CI mode on GitHub or No
                     CI, grain never enrols the branch. Classic says it on
                     the Repo page's header and on Releases, in the
                     server's own sentence; it blocks nothing, so it is a
                     warning (design book, "A deployment finding that
                     blocks nothing"). -->
                @for (
                  gate of unjudgedGates();
                  track gate.repo + " " + (gate.branch ?? "")
                ) {
                  <div
                    class="repo-gate-banner"
                    role="status"
                    data-testid="repo-base-gate-warning"
                  >
                    <g-status-icon tone="blast" kind="warning" />
                    <span class="repo-gate-text">
                      {{ gate.detail || gate.reason }}
                      @if (gate.runID) {
                        <a
                          [routerLink]="['/ci', 'runs', gate.runID]"
                          data-testid="repo-base-gate-run-link"
                          >View CI run</a
                        >
                      } @else if (gate.reason === "grain-ci-off") {
                        <a
                          [routerLink]="ciTabLink()"
                          [fragment]="ciModeId"
                          data-testid="repo-base-gate-ci-mode-link"
                          (click)="goToCiMode($event)"
                          >Go to CI Mode</a
                        >
                      }
                    </span>
                  </div>
                }
                <div class="s-card" data-testid="card-repo-config">
                  <div class="s-card-head">
                    <div>
                      <h2 class="s-card-title">
                        Repository config &amp; Git connection
                      </h2>
                      <p class="s-card-desc">
                        Clone URL override and pinned Git credential.
                      </p>
                    </div>
                    <button
                      type="button"
                      class="btn-primary s-btn-sm"
                      data-testid="save-repo-config-btn"
                      [disabled]="
                        configRepoOnly() ||
                        !can('PUT /api/deployment/repos/{owner}/{name}/config')
                      "
                      (click)="saveRepoConfig()"
                    >
                      Save config
                    </button>
                  </div>

                  <div class="s-grid">
                    <div class="s-field">
                      <label class="s-label" for="rc-clone">Clone URL</label>
                      <input
                        id="rc-clone"
                        class="s-input tabular"
                        type="text"
                        data-testid="input-repo-clone-url"
                        placeholder="https://github.com/owner/repo.git"
                        [value]="cloneUrl()"
                        [disabled]="
                          configRepoOnly() ||
                          !can(
                            'PUT /api/deployment/repos/{owner}/{name}/config'
                          )
                        "
                        (input)="cloneUrl.set($any($event.target).value)"
                      />
                    </div>
                    <div class="s-field">
                      <span class="s-label">Git connection</span>
                      <div class="s-inline">
                        <g-pill-select
                          testid="select-repo-git-conn"
                          ariaLabel="Git connection"
                          align="left"
                          [options]="gitConnOptions()"
                          [value]="gitCredential()"
                          [disabled]="
                            configRepoOnly() ||
                            !can(
                              'PUT /api/deployment/repos/{owner}/{name}/git-connection'
                            )
                          "
                          (valueChange)="pinGitConnection($event)"
                        />
                      </div>
                    </div>
                  </div>
                </div>

                <div class="s-card" data-testid="card-repo-operations">
                  <div class="s-card-head">
                    <div>
                      <h2 class="s-card-title">
                        Issue importer &amp; lifecycle refresh
                      </h2>
                      <p class="s-card-desc">
                        Configure GitHub issue import settings, trigger an
                        immediate import pass, or refresh repository lifecycle
                        state.
                      </p>
                    </div>
                    <div class="s-inline">
                      <span
                        class="status-pill neutral tabular"
                        data-testid="repo-lifecycle-state"
                        >{{ lifecycleState() }}</span
                      >
                      <button
                        type="button"
                        class="btn-tonal s-btn-sm"
                        data-testid="refresh-lifecycle-btn"
                        [disabled]="
                          !can(
                            'POST /api/repos/{owner}/{name}/lifecycle/refresh'
                          )
                        "
                        (click)="refreshLifecycle()"
                      >
                        Refresh lifecycle
                      </button>
                    </div>
                  </div>

                  <div class="s-toggle-row">
                    <div class="s-toggle-copy">
                      <span class="s-toggle-name">Enable issue importer</span>
                      <span class="s-hint"
                        >Periodically import open GitHub issues matching the
                        configured label.</span
                      >
                    </div>
                    <button
                      type="button"
                      class="g-switch"
                      role="switch"
                      data-testid="switch-issue-import-enabled"
                      [attr.aria-checked]="importEnabled()"
                      [disabled]="!canWriteSettings()"
                      aria-label="Enable issue importer"
                      (click)="importEnabled.set(!importEnabled())"
                    ></button>
                  </div>

                  <div class="s-toggle-row">
                    <div class="s-toggle-copy">
                      <span class="s-toggle-name"
                        >Queue imported tasks at once</span
                      >
                      <span class="s-hint" data-testid="issue-import-trust-hint"
                        >With this off, each imported task waits as Imported
                        until somebody approves it. Turn it on only if you trust
                        everybody who can open an issue on
                        {{ importSourceRepo().trim() || repoSlug() }}: they
                        write what the agent is told to do.</span
                      >
                    </div>
                    <button
                      type="button"
                      class="g-switch"
                      role="switch"
                      data-testid="switch-issue-import-approved"
                      [attr.aria-checked]="importApproved()"
                      [disabled]="!canWriteSettings()"
                      [attr.aria-label]="
                        'Queue imported issues in ' +
                        repoSlug() +
                        ' without approval'
                      "
                      (click)="importApproved.set(!importApproved())"
                    ></button>
                  </div>

                  <div class="s-grid">
                    <div class="s-field">
                      <label class="s-label" for="imp-label"
                        >Issue label filter</label
                      >
                      <input
                        id="imp-label"
                        class="s-input tabular"
                        type="text"
                        data-testid="input-import-label"
                        placeholder="grain-ready"
                        [value]="importLabel()"
                        [disabled]="!canWriteSettings()"
                        (input)="importLabel.set($any($event.target).value)"
                      />
                    </div>
                    <div class="s-field">
                      <label class="s-label" for="imp-repo"
                        >Source repository (empty = this repo)</label
                      >
                      <input
                        id="imp-repo"
                        class="s-input tabular"
                        type="text"
                        data-testid="input-import-repo"
                        placeholder="upstream/repo (optional)"
                        [value]="importSourceRepo()"
                        [disabled]="!canWriteSettings()"
                        (input)="
                          importSourceRepo.set($any($event.target).value)
                        "
                      />
                    </div>
                    <div class="s-field">
                      <label class="s-label" for="imp-interval"
                        >Every (minutes)</label
                      >
                      <input
                        id="imp-interval"
                        class="s-input s-num"
                        type="number"
                        min="1"
                        data-testid="input-import-interval"
                        [placeholder]="importEffectiveMinutes()"
                        [value]="importInterval()"
                        [disabled]="!canWriteSettings()"
                        [attr.aria-invalid]="
                          importIntervalError() ? 'true' : null
                        "
                        [attr.aria-describedby]="
                          importIntervalError()
                            ? 'imp-interval-err'
                            : 'imp-interval-hint'
                        "
                        (input)="
                          importInterval.set($any($event.target).value);
                          importIntervalError.set(null)
                        "
                      />
                      @if (importIntervalError(); as ivErr) {
                        <span
                          id="imp-interval-err"
                          class="s-field-error"
                          data-testid="import-interval-error"
                          >{{ ivErr }}</span
                        >
                      } @else {
                        <span id="imp-interval-hint" class="s-caption">{{
                          importEffectiveMinutes()
                            ? "Empty for the default (" +
                              importEffectiveMinutes() +
                              " minutes now)."
                            : "Empty for the default."
                        }}</span>
                      }
                    </div>
                  </div>

                  <div class="s-inline">
                    <button
                      type="button"
                      class="btn-primary s-btn-sm"
                      data-testid="save-issue-import-btn"
                      [disabled]="!canWriteSettings()"
                      (click)="saveIssueImportSettings()"
                    >
                      Save issue import
                    </button>
                    <button
                      type="button"
                      class="btn-tonal s-btn-sm"
                      data-testid="import-issues-btn"
                      [disabled]="
                        importing() ||
                        !can('POST /api/repos/{owner}/{name}/import-issues')
                      "
                      (click)="importIssues()"
                    >
                      Import issues now
                    </button>
                  </div>
                  @if (importOutcome(); as out) {
                    <p
                      class="s-hint"
                      role="status"
                      data-testid="import-issues-outcome"
                      [class.is-error]="out.error"
                      [style.color]="
                        out.error ? 'var(--md-sys-color-error)' : null
                      "
                    >
                      {{ out.text }}
                    </p>
                  }
                </div>
              }

              @case ("ci") {
                <div class="s-card" data-testid="card-repo-ci">
                  <div class="s-card-head">
                    <div>
                      <h2 class="s-card-title">
                        CI mode &amp; base-branch runs
                      </h2>
                      <p class="s-card-desc">
                        Choose whether Grain runs .grain/workflows, waits for
                        GitHub checks, or merges without waiting for CI.
                      </p>
                    </div>
                    <button
                      type="button"
                      class="btn-tonal s-btn-sm"
                      data-testid="trigger-base-ci-btn"
                      [disabled]="
                        !can('POST /api/repos/{owner}/{name}/base-branch-runs')
                      "
                      (click)="triggerBaseCiRun()"
                    >
                      Run base-branch CI
                    </button>
                  </div>

                  <div class="s-grid">
                    <div class="s-field" [id]="ciModeId">
                      <span class="s-label">CI mode</span>
                      <g-pill-select
                        testid="select-repo-ci-mode"
                        ariaLabel="Repository CI mode"
                        align="left"
                        [options]="ciModeOptions"
                        [value]="ciMode()"
                        [disabled]="
                          configRepoOnly() ||
                          !can('PUT /api/repos/{owner}/{name}/ci-mode')
                        "
                        (valueChange)="saveCiMode($event)"
                      />
                    </div>
                  </div>

                  <div class="s-toggle-row">
                    <div class="s-toggle-copy">
                      <span class="s-toggle-name">Watch default branch</span>
                      <span class="s-hint"
                        >Automatically file repair tasks when trunk CI turns
                        red.</span
                      >
                    </div>
                    <button
                      type="button"
                      class="g-switch"
                      role="switch"
                      [attr.aria-checked]="watchDefaultBranch()"
                      [disabled]="
                        configRepoOnly() ||
                        !can(
                          'PUT /api/repos/{owner}/{name}/watch-default-branch'
                        )
                      "
                      aria-label="Watch default branch"
                      (click)="toggleWatchDefaultBranch()"
                    ></button>
                  </div>

                  <div class="s-field">
                    <div class="s-row">
                      <label class="s-label" for="wf-yaml">
                        Declared workflow ({{ selectedWorkflowPath() }})
                      </label>
                      <div class="s-inline">
                        @if (workflowOptions().length > 0) {
                          <g-pill-select
                            testid="select-repo-workflow"
                            ariaLabel="Select workflow file"
                            align="left"
                            [options]="workflowOptions()"
                            [value]="selectedWorkflowPath()"
                            (valueChange)="selectWorkflow($event)"
                          />
                        }
                        <input
                          class="s-input s-input-inline tabular"
                          type="text"
                          aria-label="New workflow file path"
                          data-testid="input-new-workflow-path"
                          placeholder=".grain/workflows/release.yml"
                          [value]="newWorkflowPath()"
                          [disabled]="
                            configRepoOnly() ||
                            !can('PUT /api/repos/{owner}/{name}/workflows')
                          "
                          (input)="
                            newWorkflowPath.set($any($event.target).value)
                          "
                        />
                        <button
                          type="button"
                          class="btn-tonal s-btn-sm"
                          data-testid="add-repo-workflow-btn"
                          [disabled]="
                            !newWorkflowPath().trim() ||
                            configRepoOnly() ||
                            !can('PUT /api/repos/{owner}/{name}/workflows')
                          "
                          (click)="addWorkflowFile()"
                        >
                          Add file
                        </button>
                      </div>
                    </div>
                    <textarea
                      id="wf-yaml"
                      class="s-textarea tabular"
                      rows="5"
                      data-testid="input-repo-workflow"
                      [value]="workflowYaml()"
                      [disabled]="
                        configRepoOnly() ||
                        !can('PUT /api/repos/{owner}/{name}/workflows')
                      "
                      (input)="onEditWorkflowText($any($event.target).value)"
                    ></textarea>
                    <div class="s-inline">
                      <button
                        type="button"
                        class="btn-primary s-btn-sm"
                        data-testid="save-repo-workflow-btn"
                        [disabled]="
                          configRepoOnly() ||
                          !can('PUT /api/repos/{owner}/{name}/workflows')
                        "
                        (click)="saveWorkflows()"
                      >
                        Save workflows
                      </button>
                      @if (workflows().length > 0) {
                        <button
                          type="button"
                          class="btn-text s-btn-sm"
                          data-testid="delete-repo-workflow-btn"
                          [disabled]="
                            configRepoOnly() ||
                            !can('PUT /api/repos/{owner}/{name}/workflows')
                          "
                          (click)="deleteSelectedWorkflow()"
                        >
                          Remove selected file
                        </button>
                      }
                    </div>
                  </div>
                </div>

                <g-repo-ci-capabilities
                  [repo]="repoSlug()"
                  [configRepoOnly]="configRepoOnly()"
                  [offered]="ciCapabilityOptions()"
                />
              }

              @case ("merge-queue") {
                <div class="s-card" data-testid="card-repo-merge-queue">
                  <h2 class="s-card-title">Merge queue &amp; gate policy</h2>
                  <div class="s-toggle-row">
                    <div class="s-toggle-copy">
                      <span class="s-toggle-name">Merge on green</span>
                      <span class="s-hint"
                        >Land approved pull requests automatically once checks
                        succeed.</span
                      >
                    </div>
                    <button
                      type="button"
                      class="g-switch"
                      role="switch"
                      data-testid="switch-merge-on-green"
                      [attr.aria-checked]="mergeOnGreen()"
                      [disabled]="
                        configRepoOnly() ||
                        !can('PUT /api/repos/{owner}/{name}/merge-on-green')
                      "
                      aria-label="Merge on green"
                      (click)="toggleMergeOnGreen()"
                    ></button>
                  </div>

                  <div class="s-toggle-row">
                    <div class="s-toggle-copy">
                      <span class="s-toggle-name"
                        >Require branch up to date before merge</span
                      >
                    </div>
                    <button
                      type="button"
                      class="g-switch"
                      role="switch"
                      [attr.aria-checked]="requireUpToDate()"
                      [disabled]="
                        configRepoOnly() ||
                        !can(
                          'PUT /api/repos/{owner}/{name}/require-branch-up-to-date'
                        )
                      "
                      aria-label="Require branch up to date"
                      (click)="toggleRequireUpToDate()"
                    ></button>
                  </div>

                  <div class="s-toggle-row">
                    <div class="s-toggle-copy">
                      <span class="s-toggle-name"
                        >Base-branch repairs bypass human review gates</span
                      >
                    </div>
                    <button
                      type="button"
                      class="g-switch"
                      role="switch"
                      [attr.aria-checked]="bypassHumanGates()"
                      [disabled]="
                        configRepoOnly() ||
                        !can(
                          'PUT /api/repos/{owner}/{name}/base-repair-bypasses-human-gates'
                        )
                      "
                      aria-label="Base-branch repairs bypass human review gates"
                      (click)="toggleBypassHumanGates()"
                    ></button>
                  </div>
                </div>
              }

              @case ("agents") {
                <div class="s-card" data-testid="card-repo-agents">
                  <h2 class="s-card-title">
                    Repository instructions, setup &amp; capability grants
                  </h2>
                  <div class="s-field">
                    <label class="s-label" for="rp-setup"
                      >Sandbox setup command</label
                    >
                    <input
                      id="rp-setup"
                      class="s-input tabular"
                      type="text"
                      data-testid="input-repo-setup-cmd"
                      placeholder="npm ci"
                      [value]="setupCommand()"
                      [disabled]="
                        configRepoOnly() ||
                        !can('PUT /api/repos/{owner}/{name}/setup-command')
                      "
                      (input)="setupCommand.set($any($event.target).value)"
                    />
                  </div>
                  <div class="s-field">
                    <label class="s-label" for="rp-prompt"
                      >Standing prompt extension</label
                    >
                    <textarea
                      id="rp-prompt"
                      class="s-textarea"
                      rows="4"
                      data-testid="input-repo-prompt-ext"
                      [value]="promptExtension()"
                      [disabled]="
                        configRepoOnly() ||
                        !can('PUT /api/repos/{owner}/{name}/prompt-extension')
                      "
                      (input)="promptExtension.set($any($event.target).value)"
                    ></textarea>
                  </div>
                  <div class="s-field">
                    <label class="s-label" for="rp-caps"
                      >Default capabilities (comma-separated)</label
                    >
                    <input
                      id="rp-caps"
                      class="s-input tabular"
                      type="text"
                      [value]="repoCaps()"
                      [disabled]="
                        configRepoOnly() ||
                        !can('PUT /api/repos/{owner}/{name}/capabilities')
                      "
                      (input)="repoCaps.set($any($event.target).value)"
                    />
                  </div>
                  <div class="s-inline">
                    <button
                      type="button"
                      class="btn-primary s-btn-sm"
                      data-testid="save-repo-agents-btn"
                      [disabled]="!canSaveRepoAgents()"
                      (click)="saveAgentTabSettings()"
                    >
                      Save agent settings
                    </button>
                  </div>
                </div>
              }

              @case ("branches") {
                <div class="s-card" data-testid="card-repo-branches">
                  <h2 class="s-card-title">Repository branches</h2>
                  @if (branches().length > 0) {
                    <div class="s-table-wrap">
                      <table class="s-table" aria-label="Repository branches">
                        <thead>
                          <tr>
                            <th>Branch</th>
                            <th>HEAD</th>
                          </tr>
                        </thead>
                        <tbody>
                          @for (b of branches(); track b.name) {
                            <tr [attr.data-testid]="'branch-row-' + b.name">
                              <td class="tabular">{{ b.name }}</td>
                              <td class="tabular">
                                {{ (b.sha || b.head || "—").slice(0, 10) }}
                              </td>
                            </tr>
                          }
                        </tbody>
                      </table>
                    </div>
                  }
                  <div class="s-grid">
                    <div class="s-field">
                      <label class="s-label" for="br-name"
                        >New branch name</label
                      >
                      <input
                        id="br-name"
                        class="s-input tabular"
                        type="text"
                        data-testid="input-new-branch-name"
                        placeholder="release/v1.2"
                        [value]="newBranchName()"
                        [disabled]="
                          !can('POST /api/repos/{owner}/{name}/branches')
                        "
                        (input)="newBranchName.set($any($event.target).value)"
                      />
                    </div>
                    <div class="s-field">
                      <label class="s-label" for="br-from">From ref</label>
                      <input
                        id="br-from"
                        class="s-input tabular"
                        type="text"
                        data-testid="input-new-branch-from"
                        placeholder="main"
                        [value]="newBranchFrom()"
                        [disabled]="
                          !can('POST /api/repos/{owner}/{name}/branches')
                        "
                        (input)="newBranchFrom.set($any($event.target).value)"
                      />
                    </div>
                  </div>
                  <div class="s-inline">
                    <button
                      type="button"
                      class="btn-primary"
                      data-testid="create-branch-btn"
                      [disabled]="
                        !newBranchName().trim() ||
                        !can('POST /api/repos/{owner}/{name}/branches')
                      "
                      (click)="createBranch()"
                    >
                      Create branch
                    </button>
                  </div>
                </div>
              }

              @case ("releases") {
                <!-- A watched base branch nothing judges (GET
                     /api/base-branch-gates): with CI mode on GitHub or No
                     CI, grain never enrols the branch. Classic says it on
                     the Repo page's header and on Releases, in the
                     server's own sentence; it blocks nothing, so it is a
                     warning (design book, "A deployment finding that
                     blocks nothing"). -->
                @for (
                  gate of unjudgedGates();
                  track gate.repo + " " + (gate.branch ?? "")
                ) {
                  <div
                    class="repo-gate-banner"
                    role="status"
                    data-testid="repo-base-gate-warning"
                  >
                    <g-status-icon tone="blast" kind="warning" />
                    <span class="repo-gate-text">
                      {{ gate.detail || gate.reason }}
                      @if (gate.runID) {
                        <a
                          [routerLink]="['/ci', 'runs', gate.runID]"
                          data-testid="repo-base-gate-run-link"
                          >View CI run</a
                        >
                      } @else if (gate.reason === "grain-ci-off") {
                        <a
                          [routerLink]="ciTabLink()"
                          [fragment]="ciModeId"
                          data-testid="repo-base-gate-ci-mode-link"
                          (click)="goToCiMode($event)"
                          >Go to CI Mode</a
                        >
                      }
                    </span>
                  </div>
                }

                <!-- Who runs this repository's standing work, directly
                     above the card that writes one item of it: the release
                     switch below is the release item's own override, and
                     this is the default it is an exception to. -->
                <g-repo-work-ownership
                  [repo]="repoSlug()"
                  [refreshKey]="releaseSaves()"
                  (saved)="onWorkOwnershipSaved()"
                />

                <div class="s-card" data-testid="card-repo-release-settings">
                  <div class="s-card-head">
                    <div>
                      <h2 class="s-card-title">
                        Release workflow &amp; base-branch CI jobs
                      </h2>
                      <p class="s-card-desc">
                        Controls release publishing, base-branch judging jobs,
                        post-merge dispatch jobs, and release secrets.
                      </p>
                    </div>
                    <button
                      type="button"
                      class="btn-primary s-btn-sm"
                      data-testid="save-repo-release-btn"
                      [disabled]="
                        configRepoOnly() ||
                        !can('PUT /api/repos/{owner}/{name}/release')
                      "
                      (click)="saveReleaseSettings()"
                    >
                      Save release settings
                    </button>
                  </div>

                  <div class="s-toggle-row">
                    <div class="s-toggle-copy">
                      <span class="s-toggle-name">Enable release workflow</span>
                      <span class="s-hint"
                        >Run declared release workflows when base branches
                        advance.</span
                      >
                    </div>
                    <button
                      type="button"
                      class="g-switch"
                      role="switch"
                      data-testid="switch-repo-release-enabled"
                      [attr.aria-checked]="releaseEnabled()"
                      [disabled]="
                        configRepoOnly() ||
                        !can('PUT /api/repos/{owner}/{name}/release')
                      "
                      aria-label="Enable release workflow"
                      (click)="releaseEnabled.set(!releaseEnabled())"
                    ></button>
                  </div>

                  <div class="s-grid">
                    <div class="s-field">
                      <label class="s-label" for="rel-base-jobs"
                        >Base-branch CI jobs (comma-separated)</label
                      >
                      <input
                        id="rel-base-jobs"
                        class="s-input tabular"
                        type="text"
                        data-testid="input-release-base-jobs"
                        placeholder="empty = all push jobs"
                        [value]="baseBranchJobsText()"
                        [disabled]="
                          configRepoOnly() ||
                          !can('PUT /api/repos/{owner}/{name}/release')
                        "
                        (input)="
                          baseBranchJobsText.set($any($event.target).value)
                        "
                      />
                    </div>
                    <div class="s-field">
                      <label class="s-label" for="rel-post-jobs"
                        >Post-merge dispatch jobs (comma-separated)</label
                      >
                      <input
                        id="rel-post-jobs"
                        class="s-input tabular"
                        type="text"
                        data-testid="input-release-post-merge-jobs"
                        placeholder="guest-cache (optional)"
                        [value]="postMergeJobsText()"
                        [disabled]="
                          configRepoOnly() ||
                          !can('PUT /api/repos/{owner}/{name}/release')
                        "
                        (input)="
                          postMergeJobsText.set($any($event.target).value)
                        "
                      />
                    </div>
                  </div>

                  @if (releaseSecrets().length > 0) {
                    <div class="s-inline" data-testid="repo-release-secrets">
                      <span class="s-label">Release secrets:</span>
                      @for (sec of releaseSecrets(); track sec.name) {
                        <span
                          [class]="
                            'status-pill tabular ' +
                            (sec.present !== false ? 'healthy' : 'warn')
                          "
                        >
                          {{ sec.name }}:
                          {{ sec.present !== false ? "present" : "missing" }}
                        </span>
                      }
                    </div>
                  }
                </div>

                @if (pendingReleases().length > 0) {
                  <div class="s-card" data-testid="card-pending-releases">
                    <h2 class="s-card-title">
                      Pending trunk releases awaiting approval
                    </h2>
                    <div class="s-table-wrap">
                      <table class="s-table" aria-label="Pending releases">
                        <thead>
                          <tr>
                            <th>Version</th>
                            <th>Summary</th>
                            <th>Decision</th>
                          </tr>
                        </thead>
                        <tbody>
                          @for (pr of pendingReleases(); track pr.version) {
                            <tr
                              [attr.data-testid]="
                                'pending-release-row-' + pr.version
                              "
                            >
                              <td class="tabular">{{ pr.version }}</td>
                              <td>{{ pr.summary || "Staged for release" }}</td>
                              <td>
                                <div class="s-inline">
                                  <button
                                    type="button"
                                    class="btn-primary s-btn-sm"
                                    [attr.data-testid]="
                                      'approve-pending-release-' + pr.version
                                    "
                                    [disabled]="
                                      !can(
                                        'POST /api/repos/{owner}/{name}/pending-releases/{version}/approve'
                                      )
                                    "
                                    (click)="approvePendingRelease(pr.version)"
                                  >
                                    Approve
                                  </button>
                                  <button
                                    type="button"
                                    class="btn-tonal s-btn-sm"
                                    [attr.data-testid]="
                                      'reject-pending-release-' + pr.version
                                    "
                                    [disabled]="
                                      !can(
                                        'POST /api/repos/{owner}/{name}/pending-releases/{version}/reject'
                                      )
                                    "
                                    (click)="rejectPendingRelease(pr.version)"
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
                  </div>
                }

                <div class="s-card" data-testid="card-releases-list">
                  <div class="s-card-head">
                    <div>
                      <h2 class="s-card-title">
                        Release trains &amp; candidate qualification
                      </h2>
                      <p class="s-card-desc">
                        Cut release trains, qualify release candidates, and
                        promote verified builds.
                      </p>
                    </div>
                    <button
                      type="button"
                      class="btn-tonal s-btn-sm"
                      data-testid="trigger-release-run-btn"
                      [disabled]="
                        !can('POST /api/repos/{owner}/{name}/release-runs')
                      "
                      (click)="triggerReleaseRun()"
                    >
                      Run release workflow
                    </button>
                  </div>

                  @if (releases().length > 0) {
                    <div class="s-table-wrap">
                      <table class="s-table" aria-label="Releases">
                        <thead>
                          <tr>
                            <th>Release</th>
                            <th>Status</th>
                            <th>Candidates &amp; promotion</th>
                          </tr>
                        </thead>
                        <tbody>
                          @for (rel of releases(); track rel.name) {
                            <tr [attr.data-testid]="'release-row-' + rel.name">
                              <td class="tabular">{{ rel.name }}</td>
                              <td>
                                <span
                                  class="status-pill neutral"
                                  [attr.data-testid]="
                                    'release-status-' + rel.name
                                  "
                                  >{{ rel.status || "open" }}</span
                                >
                              </td>
                              <td>
                                <div class="s-inline">
                                  <button
                                    type="button"
                                    class="btn-tonal s-btn-sm"
                                    [attr.data-testid]="
                                      'inspect-rc-' + rel.name
                                    "
                                    [attr.aria-pressed]="
                                      selectedRelease() === rel.name
                                    "
                                    (click)="
                                      selectReleaseForCandidates(rel.name)
                                    "
                                  >
                                    Inspect RCs
                                  </button>
                                  <button
                                    type="button"
                                    class="btn-tonal s-btn-sm"
                                    [attr.data-testid]="'cut-rc-' + rel.name"
                                    [attr.title]="reasonsFor(rel).cut"
                                    [disabled]="
                                      !!reasonsFor(rel).cut ||
                                      !can(
                                        'POST /api/repos/{owner}/{name}/releases/{release}/candidates'
                                      )
                                    "
                                    (click)="cutCandidate(rel.name)"
                                  >
                                    Cut RC
                                  </button>
                                  <button
                                    type="button"
                                    class="btn-primary s-btn-sm"
                                    [attr.data-testid]="
                                      'promote-rc-' + rel.name
                                    "
                                    [attr.title]="reasonsFor(rel).promote"
                                    [disabled]="
                                      !!reasonsFor(rel).promote ||
                                      !can(
                                        'POST /api/repos/{owner}/{name}/releases/{release}/candidates/promote'
                                      )
                                    "
                                    (click)="askPromoteCandidate(rel)"
                                  >
                                    Promote RC
                                  </button>
                                  <button
                                    type="button"
                                    class="btn-tonal s-btn-sm"
                                    [disabled]="
                                      !can(
                                        'POST /api/repos/{owner}/{name}/releases/{release}/merge'
                                      )
                                    "
                                    (click)="askMergeRelease(rel)"
                                  >
                                    Merge back
                                  </button>
                                  <button
                                    type="button"
                                    class="btn-text s-btn-sm"
                                    [disabled]="
                                      !can(
                                        'DELETE /api/repos/{owner}/{name}/releases/{release}'
                                      )
                                    "
                                    [attr.data-testid]="
                                      'delete-release-' + rel.name
                                    "
                                    (click)="askDeleteRelease(rel.name)"
                                  >
                                    Delete
                                  </button>
                                </div>
                              </td>
                            </tr>
                          }
                        </tbody>
                      </table>
                    </div>
                  }

                  @for (rel of releases(); track rel.name) {
                    @if (rel.name === selectedRelease()) {
                      @let why = reasonsFor(rel);
                      @if (why.cut || why.promote) {
                        <p class="s-hint" data-testid="release-action-reasons">
                          @if (why.cut) {
                            <span>Cut RC: {{ why.cut }}</span>
                          }
                          @if (why.cut && why.promote) {
                            <br />
                          }
                          @if (why.promote) {
                            <span>Promote RC: {{ why.promote }}</span>
                          }
                        </p>
                      }
                    }
                  }

                  @if (releases().length > 1) {
                    <div class="s-inline">
                      <span class="s-label">Inspecting candidates for:</span>
                      <g-pill-select
                        testid="select-inspect-release"
                        ariaLabel="Select release to inspect candidates"
                        align="left"
                        [options]="releaseOptions()"
                        [value]="selectedRelease()"
                        (valueChange)="selectReleaseForCandidates($event)"
                      />
                    </div>
                  }

                  @if (candidates().length > 0) {
                    <div class="s-table-wrap">
                      <table class="s-table" aria-label="Release candidates">
                        <thead>
                          <tr>
                            <th>Candidate</th>
                            <th>Branch</th>
                            <th>Status</th>
                            <th>Qualification</th>
                          </tr>
                        </thead>
                        <tbody>
                          @for (c of candidates(); track c.id) {
                            <tr [attr.data-testid]="'candidate-row-' + c.id">
                              <td class="tabular">
                                {{
                                  c.number !== undefined
                                    ? "rc#" + c.number
                                    : c.version || c.id
                                }}
                              </td>
                              <td class="tabular">
                                {{ c.branch || c.version || "—" }}
                              </td>
                              <td>
                                <span class="status-pill neutral">{{
                                  c.status || "active"
                                }}</span>
                              </td>
                              <td>
                                <button
                                  type="button"
                                  class="btn-tonal s-btn-sm"
                                  [attr.data-testid]="
                                    'approve-qualification-' + c.id
                                  "
                                  [disabled]="
                                    !can(
                                      'POST /api/repos/{owner}/{name}/candidates/{id}/qualification/approve'
                                    )
                                  "
                                  (click)="approveQualification('' + c.id)"
                                >
                                  Approve qualification
                                </button>
                              </td>
                            </tr>
                          }
                        </tbody>
                      </table>
                    </div>
                  }

                  <div class="s-inline">
                    <input
                      class="s-input s-input-inline tabular"
                      type="text"
                      aria-label="New release name"
                      data-testid="input-new-release-name"
                      placeholder="v1.4.0"
                      aria-describedby="new-release-error"
                      [attr.aria-invalid]="newReleaseError() ? 'true' : null"
                      [value]="newReleaseName()"
                      [disabled]="
                        !can('POST /api/repos/{owner}/{name}/releases')
                      "
                      (input)="
                        newReleaseName.set($any($event.target).value);
                        newReleaseError.set(null)
                      "
                    />
                    <button
                      type="button"
                      class="btn-primary"
                      data-testid="create-release-btn"
                      [disabled]="
                        !newReleaseName().trim() ||
                        !can('POST /api/repos/{owner}/{name}/releases')
                      "
                      (click)="createRelease()"
                    >
                      Create release
                    </button>
                  </div>
                  @if (newReleaseError(); as relErr) {
                    <p
                      id="new-release-error"
                      class="s-hint"
                      role="alert"
                      data-testid="new-release-error"
                      style="color: var(--md-sys-color-error)"
                    >
                      {{ relErr }}
                    </p>
                  }
                </div>

                <g-repo-qualification-plan
                  [repo]="repoSlug()"
                  [configRepoOnly]="configRepoOnly()"
                />
              }
              @case ("advanced") {
                <div class="s-card" data-testid="card-repo-declarations">
                  <div class="s-card-head">
                    <div>
                      <h2 class="s-card-title">.grain/ declarations</h2>
                      <p class="s-card-desc">
                        The templates, suites, schedules and path triggers
                        {{ repoSlug() }} declares in its own .grain/templates,
                        .grain/suites, .grain/schedules and .grain/triggers.
                        Grain re-reads them within a couple of minutes of every
                        change; they are edited in those files, not here. A
                        declaration files work in {{ repoSlug() }} on a cadence
                        and can never widen what that work reads, writes or
                        holds.
                      </p>
                    </div>
                  </div>
                  @if (declarations(); as d) {
                    <div class="s-toggle-row">
                      <div class="s-toggle-copy">
                        <span class="s-toggle-name"
                          >Read this repository's declarations</span
                        >
                        @if (declarationsLockedReason(); as why) {
                          <span
                            class="s-hint"
                            id="declarations-locked-reason"
                            data-testid="declarations-locked-reason"
                            >{{ why }}</span
                          >
                        } @else {
                          <span class="s-hint"
                            >Off unless somebody here turns it on: a declared
                            schedule is a standing claim on this deployment's
                            agents.</span
                          >
                        }
                      </div>
                      <button
                        type="button"
                        class="g-switch"
                        role="switch"
                        data-testid="switch-repo-declarations"
                        [attr.aria-label]="declarationsSwitchLabel()"
                        [attr.aria-checked]="!!d.declarations"
                        [attr.aria-describedby]="
                          declarationsLockedReason()
                            ? 'declarations-locked-reason'
                            : null
                        "
                        [disabled]="
                          !!declarationsLockedReason() || savingDecl()
                        "
                        (click)="saveDeclarations(!d.declarations)"
                      ></button>
                    </div>
                    @if (d.declarationsStatus; as st) {
                      <p
                        class="s-hint repo-adv-status"
                        data-testid="declarations-status"
                      >
                        @if (st.error) {
                          Last read failed: {{ st.error }}
                          @if (st.commit) {
                            — still running what
                            {{ shortSha(st.commit) }} declared.
                          }
                        } @else {
                          Read at {{ shortSha(st.commit) || "no commit" }}:
                          {{ declaredSummary(st) }}
                        }
                      </p>
                      @if (st.error && st.commit) {
                        <p
                          class="s-hint"
                          data-testid="declarations-still-running"
                        >
                          Still running from {{ shortSha(st.commit) }}:
                          {{ declaredSummary(st) }}
                        </p>
                      }
                      @for (w of st.warnings ?? []; track w) {
                        <p class="s-hint repo-adv-warning">Warning: {{ w }}</p>
                      }
                    } @else if (d.declarations) {
                      <p class="s-hint" data-testid="declarations-status">
                        Not read yet — grain picks this up on its next cycle.
                      </p>
                    } @else {
                      <p class="s-hint" data-testid="declarations-status">
                        Grain has never read {{ repoSlug() }}'s declarations.
                      </p>
                    }
                  } @else if (declarationsError()) {
                    <p class="s-hint repo-adv-warning" role="alert">
                      Couldn't read the declarations: {{ declarationsError() }}
                    </p>
                  } @else {
                    <p class="s-hint">Loading declarations…</p>
                  }
                </div>

                <div class="s-card" data-testid="card-repo-triggers">
                  <div class="s-card-head">
                    <div>
                      <h2 class="s-card-title">Trigger files</h2>
                      <p class="s-card-desc">
                        The path triggers {{ repoSlug() }} declares in
                        .grain/triggers: a Processes trigger row names only its
                        file, so the file is listed here.
                      </p>
                    </div>
                  </div>
                  @if (triggerFiles().length > 0) {
                    <ul class="repo-adv-list" aria-label="Trigger files">
                      @for (path of triggerFiles(); track path) {
                        <li>
                          <code class="s-mono">{{ path }}</code>
                        </li>
                      }
                    </ul>
                  } @else {
                    <p class="s-hint" data-testid="triggers-empty">
                      {{ emptyDeclared("path triggers") }}
                    </p>
                  }
                </div>

                <div class="s-card" data-testid="card-repo-push-refs">
                  <div class="s-card-head">
                    <div>
                      <h2 class="s-card-title">Extra push refs</h2>
                      <p class="s-card-desc">
                        What the runs of tasks filed from a declared template
                        may push in {{ repoSlug() }} beyond their own branch, as
                        its default branch declares it in the template's push:
                        list.
                      </p>
                    </div>
                  </div>
                  @if (pushes().length > 0) {
                    <div
                      class="s-table-wrap"
                      role="region"
                      tabindex="0"
                      aria-label="Extra push refs table"
                    >
                      <table
                        class="s-table repo-adv-table"
                        aria-label="Extra push refs"
                      >
                        <thead>
                          <tr>
                            <th>Template file</th>
                            <th>May also push</th>
                          </tr>
                        </thead>
                        <tbody>
                          @for (p of pushes(); track p.path) {
                            <tr>
                              <td>
                                <code class="s-mono">{{ p.path }}</code>
                              </td>
                              <td class="s-mono">{{ pushRefsText(p) }}</td>
                            </tr>
                          }
                        </tbody>
                      </table>
                    </div>
                  } @else {
                    <p class="s-hint" data-testid="push-refs-empty">
                      {{
                        emptyDeclared(
                          "templates that push beyond their own branch"
                        )
                      }}
                    </p>
                  }
                </div>

                <div class="s-card" data-testid="card-repo-ports">
                  <div class="s-card-head">
                    <div>
                      <h2 class="s-card-title">Ports</h2>
                      <p class="s-card-desc">
                        Which branches grain ports each merge between, and which
                        merges each direction carries. Set by the operator in
                        grain/config and by {{ repoSlug() }}'s own
                        .grain/settings.yml port-types; not editable here.
                      </p>
                    </div>
                  </div>
                  @if (declarations(); as d) {
                    <p class="s-hint" data-testid="ports-summary">
                      @if ((d.pairedBranches ?? []).length > 0) {
                        Grain ports each merge between
                        {{ (d.pairedBranches ?? []).join(", ") }}
                      } @else {
                        No branches are paired
                      }
                      — operator:
                      {{ (d.portBranches ?? []).join(", ") || "none" }};
                      declared in .grain/settings.yml:
                      {{ (d.declaredPortBranches ?? []).join(", ") || "none" }}.
                    </p>
                    @if ((d.portDirections ?? []).length > 0) {
                      <div
                        class="s-table-wrap"
                        role="region"
                        tabindex="0"
                        aria-label="Port directions table"
                      >
                        <table
                          class="s-table repo-adv-table"
                          aria-label="Port directions"
                        >
                          <thead>
                            <tr>
                              <th>Direction</th>
                              <th>Operator</th>
                              <th>Declared</th>
                              <th>Ports</th>
                            </tr>
                          </thead>
                          <tbody>
                            @for (
                              dir of d.portDirections ?? [];
                              track dir.from + "->" + dir.to
                            ) {
                              <tr>
                                <td class="s-mono">
                                  {{ dir.from }}→{{ dir.to }}
                                </td>
                                <td>
                                  {{
                                    (dir.operatorTypes ?? []).join(", ") || "—"
                                  }}
                                </td>
                                <td>
                                  {{
                                    (dir.declaredTypes ?? []).join(", ") || "—"
                                  }}
                                </td>
                                <td>{{ portsText(dir) }}</td>
                              </tr>
                            }
                          </tbody>
                        </table>
                      </div>
                    }
                    @for (w of portWarnings(); track w) {
                      <p class="s-hint repo-adv-warning">Warning: {{ w }}</p>
                    }
                  } @else {
                    <p class="s-hint">Loading ports…</p>
                  }
                </div>

                <div
                  class="s-card repo-adv-danger"
                  data-testid="card-repo-remove"
                >
                  <div class="s-card-head">
                    <div>
                      <h2 class="s-card-title">Remove repository</h2>
                      <p class="s-card-desc" data-testid="remove-repo-desc">
                        @switch (removeState()) {
                          @case ("config") {
                            {{ repoSlug() }} is this deployment's own
                            configuration repository and cannot be removed here.
                          }
                          @case ("untargeted") {
                            {{ repoSlug() }} is not one of this deployment's
                            target repos, so there is nothing to remove.
                          }
                          @default {
                            Takes {{ repoSlug() }} off this deployment's target
                            repos. Its tasks and history stay.
                          }
                        }
                      </p>
                      @if (removeState() === "target") {
                        @if (removeLockedReason(); as why) {
                          <p
                            class="s-hint"
                            id="remove-locked-reason"
                            data-testid="remove-locked-reason"
                          >
                            {{ why }}
                          </p>
                        }
                      }
                    </div>
                    @if (removeState() === "target") {
                      <button
                        type="button"
                        class="btn-tonal danger s-btn-sm"
                        data-testid="remove-repo-btn"
                        [attr.aria-describedby]="
                          removeLockedReason() ? 'remove-locked-reason' : null
                        "
                        [disabled]="!!removeLockedReason() || removing()"
                        (click)="askRemoveRepo()"
                      >
                        Remove
                      </button>
                    }
                  </div>
                </div>
              }
            }
          }
        </div>
      </div>
      @if (confirmReq(); as req) {
        <g-confirm-dialog [request]="req" (done)="onConfirmDone($event)" />
      }
    </section>
  `,
})
export class RepoDetailPageComponent implements OnInit, OnDestroy {
  private readonly api = inject(GrainApi);
  protected readonly access = inject(AccessService);
  private readonly snackbar = inject(SnackbarService);
  private readonly route = inject(ActivatedRoute);
  private readonly router = inject(Router);

  readonly host = input<string | undefined>(undefined);
  readonly owner = input<string | undefined>(undefined);
  readonly name = input<string | undefined>(undefined);
  readonly tab = input<string | undefined>(undefined);

  protected readonly tabs = REPO_TABS;
  // The three modes PUT .../ci-mode accepts (model.CIMode: grain,
  // github, none). The page used to offer "both" and "off", which the
  // server refuses with a 400, and had no option for "none", so a repo
  // on no CI read as the first option, Grain CI (2026-11-13 repository
  // walkthrough).
  protected readonly ciModeOptions: readonly PillOption[] = [
    { value: "grain", label: "Grain CI (.grain/workflows)" },
    { value: "github", label: "GitHub Actions checks" },
    { value: "none", label: "No CI (merge without waiting)" },
  ];

  private readonly routeHost = signal("");
  private readonly routeOwner = signal("");
  private readonly routeName = signal("");
  private readonly routeTab = signal<string | null>(null);

  protected readonly ready = signal(false);
  protected readonly configRepoOnly = signal(false);
  protected readonly error = signal<string | null>(null);

  protected readonly baseBranch = signal("main");
  protected readonly cloneUrl = signal("");
  protected readonly gitCredential = signal("");
  protected readonly availableGitCreds = signal<string[]>([]);
  protected readonly lifecycleState = signal("active");
  protected readonly importEnabled = signal(false);
  protected readonly importLabel = signal("");
  protected readonly importSourceRepo = signal("");
  protected readonly importApproved = signal(false);
  protected readonly importInterval = signal("");
  // The interval the importer runs at now, in minutes: the stored one or
  // the deployment default, shown as the empty box's placeholder.
  protected readonly importEffectiveMinutes = signal("");
  protected readonly importIntervalError = signal<string | null>(null);
  protected readonly importing = signal(false);
  protected readonly importOutcome = signal<{
    error: boolean;
    text: string;
  } | null>(null);

  protected readonly ciMode = signal("grain");
  protected readonly ciModeId = CI_MODE_ID;
  protected readonly baseBranchGates = signal<BaseBranchGate[]>([]);
  protected readonly unjudgedGates = computed(() =>
    unjudgedGatesFor(this.baseBranchGates(), this.repoSlug()),
  );
  protected readonly watchDefaultBranch = signal(false);
  protected readonly workflows = signal<RepoWorkflowRow[]>([]);
  protected readonly selectedWorkflowPath = signal(".grain/workflows/ci.yml");
  protected readonly workflowYaml = signal("");
  protected readonly newWorkflowPath = signal("");

  protected readonly mergeOnGreen = signal(false);
  protected readonly requireUpToDate = signal(false);
  protected readonly bypassHumanGates = signal(false);
  protected readonly confirmReq = signal<ConfirmRequest | null>(null);
  private discardResolve: ((ok: boolean) => void) | null = null;

  // What the server last said for the fields that wait for a Save
  // button. Leaving the page (or switching tab, which reloads a tab's
  // data) with one of them changed used to drop the edit without a word,
  // where the classic page asks (2026-11-14 repository walkthrough).
  private readonly importBaseline = signal<ImportFields>({
    enabled: false,
    label: "",
    repo: "",
    approved: false,
    interval: "",
  });
  private readonly agentBaseline = signal<AgentFields>({
    setup: "",
    prompt: "",
    caps: "",
  });
  private readonly workflowBaseline = signal<string>("[]");

  protected readonly unsavedSections = computed(() => {
    const out: string[] = [];
    const ib = this.importBaseline();
    if (
      ib.enabled !== this.importEnabled() ||
      ib.label !== this.importLabel() ||
      ib.repo !== this.importSourceRepo() ||
      ib.approved !== this.importApproved() ||
      ib.interval !== this.importInterval()
    ) {
      out.push("issue import");
    }
    const ab = this.agentBaseline();
    if (
      ab.setup !== this.setupCommand() ||
      ab.prompt !== this.promptExtension() ||
      ab.caps !== this.repoCaps()
    ) {
      out.push("agent settings");
    }
    if (this.workflowBaseline() !== workflowKey(this.workflows())) {
      out.push("CI workflows");
    }
    if (this.planEditor()?.dirty()) {
      out.push("qualification plan");
    }
    return out;
  });

  protected readonly setupCommand = signal("");
  protected readonly promptExtension = signal("");
  protected readonly repoCaps = signal("");

  protected readonly branches = signal<BranchRow[]>([]);
  protected readonly newBranchName = signal("");
  protected readonly newBranchFrom = signal("main");

  protected readonly releaseEnabled = signal(false);
  // releaseSaves counts saves of the release switch, which is the release
  // item's own work-ownership override, so the ownership card re-reads.
  protected readonly releaseSaves = signal(0);
  // releaseEnabledStored is the release switch as the server last said,
  // so a re-read after a work-ownership save can tell an unsaved flip of
  // the switch from a stale value.
  private releaseEnabledStored = false;
  private readonly planEditor = viewChild(RepoQualificationPlanComponent);
  protected readonly ciCapabilityOptions = signal<readonly CapabilityOption[]>(
    [],
  );
  protected readonly baseBranchJobsText = signal("");
  protected readonly postMergeJobsText = signal("");
  protected readonly releaseSecrets = signal<RepoReleaseSecretRow[]>([]);
  protected readonly releases = signal<ReleaseRow[]>([]);
  protected readonly selectedRelease = signal("");
  protected readonly candidates = signal<CandidateRow[]>([]);
  private readonly candidatesFor = signal("");
  protected readonly pendingReleases = signal<PendingReleaseRow[]>([]);
  protected readonly newReleaseName = signal("");
  // A refused release name is said beside its box, the way the classic
  // pane does, not in the page-wide banner, and clears on the next edit
  // or a clean create.
  protected readonly newReleaseError = signal<string | null>(null);

  // The Advanced tab: what GET .../declarations last said, and the
  // allowlist and config repo /api/config last said, which decide
  // whether this repository can be removed at all.
  protected readonly declarations = signal<RepoDeclarations | null>(null);
  protected readonly declarationsError = signal<string | null>(null);
  protected readonly savingDecl = signal(false);
  protected readonly removing = signal(false);
  private readonly targetRepos = signal<string[]>([]);
  private readonly configRepo = signal("");

  protected readonly triggerFiles = computed(
    () => this.declarations()?.declarationsStatus?.triggers ?? [],
  );
  protected readonly pushes = computed(
    () => this.declarations()?.declarationsStatus?.pushes ?? [],
  );
  // Only what the declarations card has not already said: the
  // repository's own port-types findings are among its warnings too
  // (ui.Client.RepoDefaults appends them), and drawing them twice read
  // as two problems (2026-11-11 repository walkthrough).
  protected readonly portWarnings = computed(() => {
    const d = this.declarations();
    const shown = new Set(d?.declarationsStatus?.warnings ?? []);
    return (d?.portWarnings ?? []).filter((w) => !shown.has(w));
  });

  // removeState is whether Remove applies: the config repo is refused
  // (ui.Client.RemoveTargetRepoForTeam says so with a 400) and a repo
  // off the allowlist has nothing to take off it.
  protected readonly removeState = computed<"config" | "untargeted" | "target">(
    () => {
      const key = this.repoSlug().toLowerCase();
      if (this.configRepo().toLowerCase() === key) return "config";
      return this.targetRepos().some((r) => r.toLowerCase() === key)
        ? "target"
        : "untargeted";
    },
  );

  private subs: Subscription[] = [];
  private readonly hostEl = inject(ElementRef<HTMLElement>);

  // On a phone the tab bar scrolls sideways, and a deep link or reload
  // on a later tab (/releases) left the selected tab off screen
  // (2026-11-14 repository walkthrough). Bring it into view whenever the
  // tab changes.
  private readonly keepActiveTabInView = afterRenderEffect(() => {
    this.activeTab();
    const btn = (this.hostEl.nativeElement as HTMLElement).querySelector(
      ".repo-tab-btn.is-active",
    ) as HTMLElement | null;
    btn?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
  });

  private readonly normalizedRouteParts = computed(() => {
    const rawHost = this.routeHost() || this.host() || "";
    const rawOwner = this.routeOwner() || this.owner() || "";
    const rawName = this.routeName() || this.name() || "";
    const rawTab = this.routeTab() ?? this.tab() ?? null;

    // Handle plain-path collisions when a 3-segment URL matches either
    // repos/:owner/:name/:tab or repos/:host/:owner/:name:
    if (!rawHost && /[.:]/.test(rawOwner) && rawTab && !isRepoTabId(rawTab)) {
      return {
        host: rawOwner,
        owner: rawName,
        name: rawTab,
        tab: null as string | null,
      };
    }
    if (rawHost && !/[.:]/.test(rawHost) && !rawTab && isRepoTabId(rawName)) {
      return {
        host: "",
        owner: rawHost,
        name: rawOwner,
        tab: rawName,
      };
    }
    return {
      host: rawHost,
      owner: rawOwner,
      name: rawName,
      tab: rawTab,
    };
  });

  protected readonly resolvedHost = computed(
    () => this.normalizedRouteParts().host,
  );
  protected readonly resolvedOwner = computed(
    () => this.normalizedRouteParts().owner,
  );
  protected readonly resolvedName = computed(
    () => this.normalizedRouteParts().name,
  );
  protected readonly repoSlug = computed(() => {
    const h = this.resolvedHost();
    const o = this.resolvedOwner();
    const n = this.resolvedName();
    return h ? `${h}/${o}/${n}` : `${o}/${n}`;
  });

  protected readonly activeTab = computed<RepoTabId>(() => {
    const raw = (this.normalizedRouteParts().tab ?? "overview").toLowerCase();
    return isRepoTabId(raw) ? raw : "overview";
  });

  protected gitConnOptions(): PillOption[] {
    const creds = this.availableGitCreds();
    return [
      { value: "", label: "Deployment default" },
      ...creds.map((c) => ({ value: c, label: c })),
    ];
  }

  protected workflowOptions(): PillOption[] {
    return this.workflows().map((w) => ({
      value: w.path,
      label: w.name ? `${w.path} (${w.name})` : w.path,
    }));
  }

  protected releaseOptions(): PillOption[] {
    return this.releases().map((r) => ({ value: r.name, label: r.name }));
  }

  protected canWriteSettings(): boolean {
    if (this.configRepoOnly()) return false;
    return this.can("PUT /api/repos/{owner}/{name}/settings");
  }

  protected canSaveRepoAgents(): boolean {
    if (this.configRepoOnly()) return false;
    return (
      this.can("PUT /api/repos/{owner}/{name}/setup-command") ||
      this.can("PUT /api/repos/{owner}/{name}/prompt-extension") ||
      this.can("PUT /api/repos/{owner}/{name}/capabilities")
    );
  }

  ngOnInit(): void {
    this.subs.push(
      this.route.paramMap.subscribe((pm) => {
        this.routeHost.set(pm.get("host") ?? "");
        this.routeOwner.set(pm.get("owner") ?? "");
        this.routeName.set(pm.get("name") ?? "");
        this.routeTab.set(pm.get("tab"));
        if (this.ready()) {
          void this.loadTabData();
          // The same page shown for another repository reads that
          // repository's gates; unjudgedGates already drops the last one's.
          void this.loadBaseBranchGates();
        }
      }),
    );
    void this.initPage();
  }

  ngOnDestroy(): void {
    this.subs.forEach((s) => s.unsubscribe());
  }

  protected can(route: RouteKey): boolean {
    return this.access.can(route, this.repoSlug());
  }

  private markImportSaved(): void {
    this.importBaseline.set({
      enabled: this.importEnabled(),
      label: this.importLabel(),
      repo: this.importSourceRepo(),
      approved: this.importApproved(),
      interval: this.importInterval(),
    });
  }

  // canLeave asks before unsaved edits are dropped: by the route's
  // canDeactivate guard (app.routes.ts) for Back, the rail and links,
  // and by selectTab, since switching tab reloads that tab's data over
  // the edit. Discarding puts the fields back on what the server has, so
  // the guard that runs next does not ask a second time.
  async canLeave(): Promise<boolean> {
    const sections = this.unsavedSections();
    if (sections.length === 0) return true;
    if (this.discardResolve) return false;
    const ok = await new Promise<boolean>((resolve) => {
      this.discardResolve = resolve;
      this.confirmReq.set({
        title: "Discard unsaved changes?",
        message: `You have unsaved ${sections.join(", ")} changes on ${this.repoSlug()}. Leave and discard them?`,
        accept: "Discard",
        destructive: true,
        run: async () => undefined,
      });
    });
    if (ok) this.revertUnsaved();
    return ok;
  }

  private revertUnsaved(): void {
    const ib = this.importBaseline();
    this.importEnabled.set(ib.enabled);
    this.importLabel.set(ib.label);
    this.importSourceRepo.set(ib.repo);
    this.importApproved.set(ib.approved);
    this.importInterval.set(ib.interval);
    this.importIntervalError.set(null);
    this.planEditor()?.revert();
    const ab = this.agentBaseline();
    this.setupCommand.set(ab.setup);
    this.promptExtension.set(ab.prompt);
    this.repoCaps.set(ab.caps);
    const wf = JSON.parse(this.workflowBaseline()) as [string, string][];
    this.workflows.set(wf.map(([path, text]) => ({ path, text })));
    const cur = this.workflows().find(
      (w) => w.path === this.selectedWorkflowPath(),
    );
    this.workflowYaml.set(cur?.text ?? "");
  }

  @HostListener("window:beforeunload", ["$event"])
  protected onBeforeUnload(ev: BeforeUnloadEvent): void {
    if (this.unsavedSections().length > 0) ev.preventDefault();
  }

  protected async selectTab(tab: RepoTabId): Promise<void> {
    if (tab === this.activeTab()) return;
    if (!(await this.canLeave())) return;
    this.routeTab.set(tab);
    const h = this.resolvedHost();
    const o = this.resolvedOwner();
    const n = this.resolvedName();
    const base = h ? ["/repos", h, o, n] : ["/repos", o, n];
    if (tab === "overview") {
      void this.router.navigate(base);
    } else {
      void this.router.navigate([...base, tab]);
    }
    void this.loadTabData(tab);
  }

  private params(): { owner: string; name: string } {
    return splitRepo(this.repoSlug());
  }

  private async initPage(): Promise<void> {
    try {
      const cfg = await this.api.config();
      if (cfg) {
        this.access.setConfig(cfg);
        const rec = cfg as Record<string, unknown>;
        this.applyAllowlist(rec);
        this.configRepoOnly.set(
          Boolean(
            rec["configRepoOnly"] ||
            rec["externalConfigRepo"] ||
            rec["settingsLocked"],
          ),
        );
        // The capabilities CI shells can be given; a server that sends
        // no list of its own offers the task ones, as the classic page reads.
        const ciCaps = (rec["ciCapabilities"] ?? rec["capabilities"]) as
          CapabilityOption[] | undefined;
        this.ciCapabilityOptions.set(Array.isArray(ciCaps) ? ciCaps : []);
      }
      await Promise.all([this.loadCoreRepo(), this.loadTabData()]);
      this.ready.set(true);
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  private async loadCoreRepo(): Promise<void> {
    const p = this.params();
    if (!p.owner || !p.name) return;
    const jobs: Promise<void>[] = [];
    if (this.can("GET /api/deployment/repos/{owner}/{name}/config")) {
      jobs.push(
        this.api
          .get<Record<string, unknown>>(
            "GET /api/deployment/repos/{owner}/{name}/config",
            p,
          )
          .then((cfg) => {
            const eff =
              (cfg?.["effective"] as Record<string, unknown> | undefined) ??
              cfg;
            if (eff?.["cloneUrl"] !== undefined) {
              this.cloneUrl.set(String(eff["cloneUrl"] ?? ""));
            }
            if (eff?.["gitCredential"] !== undefined) {
              this.gitCredential.set(String(eff["gitCredential"] ?? ""));
            }
          })
          .catch(() => undefined),
      );
    }
    if (this.can("GET /api/repos/{owner}/{name}/settings")) {
      jobs.push(
        this.api
          .get<Record<string, unknown>>(
            "GET /api/repos/{owner}/{name}/settings",
            p,
          )
          .then((st) => {
            if (st?.["ciMode"]) this.ciMode.set(String(st["ciMode"]));
            if (st?.["mergeOnGreen"] !== undefined) {
              this.mergeOnGreen.set(Boolean(st["mergeOnGreen"]));
            }
            if (st?.["requireBranchUpToDate"] !== undefined) {
              this.requireUpToDate.set(Boolean(st["requireBranchUpToDate"]));
            }
            if (st?.["baseRepairBypassesHumanGates"] !== undefined) {
              this.bypassHumanGates.set(
                Boolean(st["baseRepairBypassesHumanGates"]),
              );
            }
            if (st?.["watchDefaultBranch"] !== undefined) {
              this.watchDefaultBranch.set(Boolean(st["watchDefaultBranch"]));
            }
            if (st?.["setupCommand"] !== undefined) {
              this.setupCommand.set(String(st["setupCommand"] ?? ""));
            }
            if (st?.["promptExtension"] !== undefined) {
              this.promptExtension.set(String(st["promptExtension"] ?? ""));
            }
            const imp = st?.["issueImport"] as
              | {
                  enabled?: boolean;
                  label?: string;
                  repo?: string;
                  approved?: boolean;
                  intervalSeconds?: number;
                  effectiveIntervalSeconds?: number;
                }
              | undefined;
            if (imp && typeof imp === "object") {
              this.importEnabled.set(Boolean(imp.enabled));
              this.importLabel.set(String(imp.label ?? ""));
              this.importSourceRepo.set(String(imp.repo ?? ""));
              this.importApproved.set(Boolean(imp.approved));
              this.importInterval.set(importIntervalText(imp.intervalSeconds));
              this.importEffectiveMinutes.set(
                imp.effectiveIntervalSeconds
                  ? String(Math.round(imp.effectiveIntervalSeconds / 60))
                  : "",
              );
            }
            this.markImportSaved();
            this.agentBaseline.update((b) => ({
              ...b,
              setup: this.setupCommand(),
              prompt: this.promptExtension(),
            }));
          })
          .catch(() => undefined),
      );
    }
    jobs.push(this.loadBaseBranchGates());
    if (this.access.can("GET /api/deployment/git-connections")) {
      jobs.push(
        this.api
          .get<
            { credential?: string; id?: string }[] | { connections?: unknown[] }
          >("GET /api/deployment/git-connections")
          .then((conns) => {
            const arr = (
              Array.isArray(conns) ? conns : (conns?.connections ?? [])
            ) as { credential?: string; id?: string }[];
            this.availableGitCreds.set(
              arr.map((c) => c.credential ?? c.id ?? "").filter(Boolean),
            );
          })
          .catch(() => undefined),
      );
    }
    await Promise.all(jobs);
  }

  private async loadTabData(t: RepoTabId = this.activeTab()): Promise<void> {
    const p = this.params();
    if (!p.owner || !p.name) return;
    if (t === "overview") {
      if (this.can("GET /api/repos/{owner}/{name}/lifecycle")) {
        void this.api
          .get<Record<string, unknown>>(
            "GET /api/repos/{owner}/{name}/lifecycle",
            p,
          )
          .then((l) => {
            const st = l?.["state"] ?? l?.["stage"];
            if (st) this.lifecycleState.set(String(st));
          })
          .catch(() => undefined);
      }
    } else if (t === "ci") {
      if (this.can("GET /api/repos/{owner}/{name}/workflows")) {
        void this.api
          .get<{ workflows?: RepoWorkflowRow[] } | RepoWorkflowRow[]>(
            "GET /api/repos/{owner}/{name}/workflows",
            p,
          )
          .then((w) => {
            const list = (Array.isArray(w) ? w : (w?.workflows ?? [])).map(
              (item) => ({
                path: item.path,
                text: item.text ?? "",
                ...(item.name ? { name: item.name } : {}),
              }),
            );
            this.workflows.set(list);
            this.workflowBaseline.set(workflowKey(list));
            const cur = this.selectedWorkflowPath();
            const active = list.find((x) => x.path === cur) ?? list[0] ?? null;
            if (active) {
              this.selectedWorkflowPath.set(active.path);
              this.workflowYaml.set(active.text);
            } else {
              this.selectedWorkflowPath.set(".grain/workflows/ci.yml");
              this.workflowYaml.set("");
            }
          })
          .catch(() => undefined);
      }
    } else if (t === "agents") {
      if (this.can("GET /api/repos/{owner}/{name}/capabilities")) {
        void this.api
          .get<{ defaultCapabilities?: string[] }>(
            "GET /api/repos/{owner}/{name}/capabilities",
            p,
          )
          .then((c) => {
            if (Array.isArray(c?.defaultCapabilities)) {
              this.repoCaps.set(c.defaultCapabilities.join(", "));
            }
            this.agentBaseline.update((b) => ({ ...b, caps: this.repoCaps() }));
          })
          .catch(() => undefined);
      }
    } else if (t === "branches") {
      if (this.can("GET /api/repos/{owner}/{name}/branches")) {
        void this.api
          .get<BranchRow[] | { branches?: BranchRow[] }>(
            "GET /api/repos/{owner}/{name}/branches",
            p,
          )
          .then((b) =>
            this.branches.set(Array.isArray(b) ? b : (b?.branches ?? [])),
          )
          .catch(() => undefined);
      }
    } else if (t === "releases") {
      await this.loadReleasesTab();
    } else if (t === "advanced") {
      await this.loadDeclarations();
    }
  }

  private applyAllowlist(cfg: Record<string, unknown>): void {
    const t = cfg["targetRepos"];
    this.targetRepos.set(
      Array.isArray(t) ? t.filter((r): r is string => !!r) : [],
    );
    this.configRepo.set(String(cfg["configRepo"] ?? ""));
  }

  private async loadDeclarations(): Promise<void> {
    if (!this.can("GET /api/repos/{owner}/{name}/declarations")) {
      this.declarationsError.set(
        `your role cannot read ${this.repoSlug()}'s settings (it needs repos:read).`,
      );
      return;
    }
    try {
      this.declarations.set(
        await this.api.get<RepoDeclarations>(
          "GET /api/repos/{owner}/{name}/declarations",
          this.params(),
        ),
      );
      this.declarationsError.set(null);
    } catch (err) {
      this.declarationsError.set(errText(err));
    }
  }

  // declarationsLockedReason says why the declarations switch is off
  // for this person, or null when they may flip it.
  protected declarationsLockedReason(): string | null {
    if (this.configRepoOnly()) {
      return "Repository settings are managed in the config repository, so this is switched there.";
    }
    if (!this.can("PUT /api/repos/{owner}/{name}/declarations")) {
      return this.roleCannot("switch declarations on or off");
    }
    return null;
  }

  // removeLockedReason says why Remove is off for this person, or null.
  protected removeLockedReason(): string | null {
    if (this.configRepoOnly()) {
      return "The target repo list is managed in the config repository, so a repository is removed there.";
    }
    if (!this.can("DELETE /api/repos/{owner}/{name}")) {
      return this.roleCannot("remove repositories");
    }
    return null;
  }

  private roleCannot(what: string): string {
    const role = this.access.roleLabel();
    return `${role ? `Your role, ${role},` : "Your role"} cannot ${what} (it needs repos:write on ${this.repoSlug()}).`;
  }

  // saveDeclarations turns reading this repository's own declarations
  // on or off. The request reads and removes nothing itself: the next
  // cycle does both (orchestrator.reconcileDeclarations), which is why
  // the status can still be empty a moment after the switch moves.
  protected async saveDeclarations(on: boolean): Promise<void> {
    this.savingDecl.set(true);
    try {
      this.declarations.set(
        await this.api.put<RepoDeclarations>(
          "PUT /api/repos/{owner}/{name}/declarations",
          this.params(),
          { declarations: on },
        ),
      );
    } catch (err) {
      this.error.set(errText(err));
    } finally {
      this.savingDecl.set(false);
    }
  }

  // askRemoveRepo asks before taking this repository off the allowlist,
  // naming what the removal strands: asked of the daemon as the very
  // save it is (POST /api/repos/plan with this repo dropped), since the
  // work behind a repo is the store's to count. A plan this build cannot
  // get must not stop a removal that never depended on one.
  protected async askRemoveRepo(): Promise<void> {
    const repo = this.repoSlug();
    this.removing.set(true);
    let allowed = this.targetRepos();
    try {
      const cfg = await this.api.config();
      if (cfg) {
        this.applyAllowlist(cfg as Record<string, unknown>);
        allowed = this.targetRepos();
      }
    } catch {
      // Keep the allowlist the page loaded with.
    }
    if (this.removeState() !== "target") {
      this.removing.set(false);
      return;
    }
    let stranded: string[] = [];
    try {
      const plan = await this.api.post<{
        targetReposStranded?: { summary?: string }[];
      }>("POST /api/repos/plan", undefined, {
        targetRepos: allowed.filter(
          (r) => r.toLowerCase() !== repo.toLowerCase(),
        ),
      });
      stranded = (plan?.targetReposStranded ?? [])
        .map((x) => x.summary ?? "")
        .filter(Boolean);
    } catch {
      // Said as it always was, without the stranded work.
    }
    this.removing.set(false);
    this.confirmReq.set({
      title: `Remove ${repo} from target repos?`,
      message: removeRepoMessage(repo, allowed, stranded),
      accept: "Remove",
      destructive: true,
      run: () => this.removeRepo(),
    });
  }

  // Removing the repo leaves this page describing one the deployment
  // no longer allows, so it goes back to the Repos list.
  private async removeRepo(): Promise<void> {
    this.removing.set(true);
    try {
      await this.api.delete("DELETE /api/repos/{owner}/{name}", this.params());
      this.snackbar.show(`Removed ${this.repoSlug()} from target repos.`);
      await this.router.navigate(["/repos"]);
    } catch (err) {
      this.error.set(errText(err));
    } finally {
      this.removing.set(false);
    }
  }

  protected declarationsSwitchLabel(): string {
    return `Read ${this.repoSlug()}'s own declarations`;
  }

  protected shortSha(sha: string | undefined): string {
    return (sha ?? "").slice(0, 12);
  }

  protected declaredSummary(st: DeclarationsStatus): string {
    return declaredSummary(st);
  }

  protected pushRefsText(p: DeclaredPushRow): string {
    return (p.refs ?? []).map(declaredPushRef).join(", ");
  }

  protected portsText(d: PortDirection): string {
    if (!d.paired) return "nothing (not a paired branch)";
    return (d.types ?? []).length > 0
      ? (d.types ?? []).join(", ")
      : "every merge";
  }

  // emptyDeclared words an empty list under the declarations status: a
  // repository grain has not read looks the same as one declaring
  // nothing unless the line says which it is.
  protected emptyDeclared(what: string): string {
    const d = this.declarations();
    if (!d) return "Loading…";
    if (!d.declarationsStatus) {
      return d.declarations
        ? `Not read yet, so no ${what} are known.`
        : `Grain does not read ${this.repoSlug()}'s declarations, so it has no ${what}.`;
    }
    return `${this.repoSlug()} declares no ${what}.`;
  }

  private applyReleaseSettings(relCfg: Record<string, unknown> | null): void {
    if (!relCfg) return;
    if (relCfg["enabled"] !== undefined) {
      this.releaseEnabled.set(Boolean(relCfg["enabled"]));
      this.releaseEnabledStored = Boolean(relCfg["enabled"]);
    }
    if (Array.isArray(relCfg["baseBranchJobs"])) {
      this.baseBranchJobsText.set(
        (relCfg["baseBranchJobs"] as string[]).join(", "),
      );
    }
    if (Array.isArray(relCfg["postMergeDispatchJobs"])) {
      this.postMergeJobsText.set(
        (relCfg["postMergeDispatchJobs"] as string[]).join(", "),
      );
    }
    if (Array.isArray(relCfg["secrets"])) {
      this.releaseSecrets.set(relCfg["secrets"] as RepoReleaseSecretRow[]);
    }
    if (Array.isArray(relCfg["baseBranches"]) && relCfg["baseBranches"][0]) {
      this.baseBranch.set(String(relCfg["baseBranches"][0]));
    }
    if (Array.isArray(relCfg["pendingReleases"])) {
      this.pendingReleases.set(
        relCfg["pendingReleases"] as PendingReleaseRow[],
      );
    }
  }

  private async loadReleasesTab(): Promise<void> {
    const p = this.params();
    if (this.can("GET /api/repos/{owner}/{name}/release")) {
      try {
        const relCfg = await this.api.get<Record<string, unknown>>(
          "GET /api/repos/{owner}/{name}/release",
          p,
        );
        this.applyReleaseSettings(relCfg);
      } catch {
        // Non-fatal
      }
    }
    if (this.can("GET /api/repos/{owner}/{name}/releases")) {
      try {
        const list = await this.api.get<
          ReleaseRow[] | { releases?: ReleaseRow[]; items?: ReleaseRow[] }
        >("GET /api/repos/{owner}/{name}/releases", p);
        const rows = Array.isArray(list)
          ? list
          : (list?.releases ?? list?.items ?? []);
        this.releases.set(rows);
        const current = this.selectedRelease();
        const target =
          rows.find((r) => r.name === current)?.name ?? rows[0]?.name ?? "";
        this.selectedRelease.set(target);
        if (target) {
          await this.loadCandidatesFor(target);
        } else {
          this.candidates.set([]);
        }
      } catch {
        // Non-fatal
      }
    }
  }

  protected async selectReleaseForCandidates(release: string): Promise<void> {
    this.selectedRelease.set(release);
    await this.loadCandidatesFor(release);
  }

  private async loadCandidatesFor(release: string): Promise<void> {
    if (
      !release ||
      !this.can("GET /api/repos/{owner}/{name}/releases/{release}/candidates")
    ) {
      return;
    }
    try {
      const cands = await this.api.get<
        CandidateRow[] | { candidates?: CandidateRow[]; items?: CandidateRow[] }
      >("GET /api/repos/{owner}/{name}/releases/{release}/candidates", {
        ...this.params(),
        release,
      });
      this.candidates.set(
        Array.isArray(cands)
          ? cands
          : (cands?.candidates ?? cands?.items ?? []),
      );
      this.candidatesFor.set(release);
    } catch {
      // Non-fatal
    }
  }

  protected async saveRepoConfig(): Promise<void> {
    try {
      await this.api.put(
        "PUT /api/deployment/repos/{owner}/{name}/config",
        this.params(),
        {
          cloneUrl: this.cloneUrl().trim(),
          ...(this.gitCredential()
            ? { gitCredential: this.gitCredential() }
            : {}),
        },
      );
      this.snackbar.show("Repository configuration saved");
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  protected async pinGitConnection(credential: string): Promise<void> {
    this.gitCredential.set(credential);
    try {
      await this.api.put(
        "PUT /api/deployment/repos/{owner}/{name}/git-connection",
        this.params(),
        { credential },
      );
      this.snackbar.show("Pinned Git connection");
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  protected async refreshLifecycle(): Promise<void> {
    try {
      const res = await this.api.post<{ state?: string; stage?: string }>(
        "POST /api/repos/{owner}/{name}/lifecycle/refresh",
        this.params(),
        {},
      );
      const st = res?.state ?? res?.stage;
      if (st) this.lifecycleState.set(st);
      this.snackbar.show("Lifecycle refreshed");
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  protected async saveIssueImportSettings(): Promise<void> {
    // An untouched box is left out of the save, so a stored interval
    // that is not a whole number of minutes (set through the API, CLI or
    // config repo) is not rounded to what the box shows.
    const intervalEdited =
      this.importInterval() !== this.importBaseline().interval;
    const interval = importIntervalSeconds(this.importInterval());
    if (intervalEdited && interval.error !== null) {
      this.importIntervalError.set(interval.error);
      return;
    }
    try {
      const res = await this.api.put<{
        issueImport?: { effectiveIntervalSeconds?: number };
      }>("PUT /api/repos/{owner}/{name}/settings", this.params(), {
        issueImportEnabled: this.importEnabled(),
        issueImportLabel: this.importLabel().trim(),
        issueImportRepo: this.importSourceRepo().trim(),
        issueImportApproved: this.importApproved(),
        ...(intervalEdited
          ? { issueImportIntervalSeconds: interval.seconds }
          : {}),
      });
      const eff = res?.issueImport?.effectiveIntervalSeconds;
      if (eff) this.importEffectiveMinutes.set(String(Math.round(eff / 60)));
      this.markImportSaved();
      this.snackbar.show("Issue importer settings saved");
    } catch (err) {
      const text = errText(err);
      // The interval is the one field here the server refuses by name;
      // say so beside it rather than in the page-wide banner.
      if (/issueImportIntervalSeconds/.test(text)) {
        this.importIntervalError.set(text);
      } else {
        this.error.set(text);
      }
    }
  }

  // importIssues runs one import pass now and says what it did, as the
  // classic Repo page does. The route answers 200 with an "error" beside
  // the counts when the source cannot be listed (or some issues failed),
  // so a bare "triggered" toast read as success for a source repository
  // that does not exist (2026-11-14 repository walkthrough).
  protected async importIssues(): Promise<void> {
    this.importing.set(true);
    this.importOutcome.set(null);
    try {
      const out = await this.api.post<{
        source?: string;
        open?: number;
        imported?: string[] | null;
        skipped?: number;
        error?: string;
      }>("POST /api/repos/{owner}/{name}/import-issues", this.params(), {});
      this.importOutcome.set(importOutcomeText(out ?? {}, this.repoSlug()));
    } catch (err) {
      this.importOutcome.set({ error: true, text: errText(err) });
    } finally {
      this.importing.set(false);
    }
  }

  // loadBaseBranchGates reads this repository's base-branch gates. It is
  // asked again after every save that changes whether grain judges the
  // branch (CI mode, Watch default branch), so the warning comes and goes
  // with the control rather than on the next reload. A failed reading
  // keeps the last one, as the server's own contract for it does.
  //
  // Readings run one after another: GrainApi.get shares a request still
  // in flight for the same URL, so a second save's reading started
  // beside the first's would be answered with the state before it.
  private gatesReading: Promise<void> = Promise.resolve();

  private loadBaseBranchGates(): Promise<void> {
    this.gatesReading = this.gatesReading.then(() =>
      this.readBaseBranchGates(),
    );
    return this.gatesReading;
  }

  private async readBaseBranchGates(): Promise<void> {
    if (!this.can("GET /api/base-branch-gates")) return;
    try {
      const q = new URLSearchParams({
        repo: this.repoSlug(),
        fields: "full",
      });
      const r = await this.api.get<BaseBranchGatesResponse>(
        "GET /api/base-branch-gates",
        undefined,
        q.toString(),
      );
      this.baseBranchGates.set(Array.isArray(r?.gates) ? r.gates : []);
    } catch {
      // Keep the previous reading.
    }
  }

  protected ciTabLink(): string[] {
    const h = this.resolvedHost();
    const o = this.resolvedOwner();
    const n = this.resolvedName();
    return h ? ["/repos", h, o, n, "ci"] : ["/repos", o, n, "ci"];
  }

  // goToCiMode opens the CI & Workflows tab and brings its CI mode
  // control into view, focused, as classic's "Go to CI Mode" does.
  protected async goToCiMode(ev: Event): Promise<void> {
    ev.preventDefault();
    if (this.activeTab() !== "ci") {
      if (!(await this.canLeave())) return;
      this.routeTab.set("ci");
      void this.router.navigate(this.ciTabLink(), { fragment: CI_MODE_ID });
      void this.loadTabData("ci");
    }
    setTimeout(() => {
      const root = this.hostEl.nativeElement as HTMLElement;
      const el = root.querySelector<HTMLElement>(`#${CI_MODE_ID}`);
      el?.scrollIntoView?.({ block: "center" });
      el?.querySelector<HTMLElement>("button")?.focus();
    });
  }

  protected async saveCiMode(ciMode: string): Promise<void> {
    const previous = this.ciMode();
    this.ciMode.set(ciMode);
    try {
      await this.api.put(
        "PUT /api/repos/{owner}/{name}/ci-mode",
        this.params(),
        { ciMode },
      );
      this.snackbar.show(`CI mode set to ${ciMode}`);
      void this.loadBaseBranchGates();
    } catch (err) {
      // A refused mode is not the repo's mode: put the pill back on
      // what the server still has rather than leave it naming one it
      // refused.
      this.ciMode.set(previous);
      this.error.set(errText(err));
    }
  }

  protected async toggleWatchDefaultBranch(): Promise<void> {
    const next = !this.watchDefaultBranch();
    try {
      await this.api.put(
        "PUT /api/repos/{owner}/{name}/watch-default-branch",
        this.params(),
        { watchDefaultBranch: next },
      );
      this.watchDefaultBranch.set(next);
      void this.loadBaseBranchGates();
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  protected async triggerBaseCiRun(): Promise<void> {
    try {
      await this.api.post(
        "POST /api/repos/{owner}/{name}/base-branch-runs",
        this.params(),
        { branch: this.baseBranch() || "main" },
      );
      this.snackbar.show("Triggered base-branch CI run");
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  protected selectWorkflow(path: string): void {
    this.selectedWorkflowPath.set(path);
    const found = this.workflows().find((w) => w.path === path);
    this.workflowYaml.set(found?.text ?? "");
  }

  protected onEditWorkflowText(text: string): void {
    this.workflowYaml.set(text);
    const path = this.selectedWorkflowPath();
    this.workflows.update((list) => {
      const idx = list.findIndex((w) => w.path === path);
      if (idx < 0) return list;
      const copy = [...list];
      copy[idx] = { ...copy[idx], text };
      return copy;
    });
  }

  protected addWorkflowFile(): void {
    const path = normalizeWorkflowPath(this.newWorkflowPath());
    if (!path) return;
    this.newWorkflowPath.set("");
    const existing = this.workflows().find((w) => w.path === path);
    if (existing) {
      this.selectedWorkflowPath.set(existing.path);
      this.workflowYaml.set(existing.text);
      return;
    }
    const stub = `name: ${
      path
        .split("/")
        .pop()
        ?.replace(/\.ya?ml$/, "") || "ci"
    }\n`;
    this.workflows.update((list) => [...list, { path, text: stub }]);
    this.selectedWorkflowPath.set(path);
    this.workflowYaml.set(stub);
  }

  protected async deleteSelectedWorkflow(): Promise<void> {
    const target = this.selectedWorkflowPath();
    const remaining = this.workflows().filter((w) => w.path !== target);
    try {
      await this.api.put(
        "PUT /api/repos/{owner}/{name}/workflows",
        this.params(),
        {
          workflows: remaining.map((w) => ({ path: w.path, text: w.text })),
        },
      );
      this.workflows.set(remaining);
      this.workflowBaseline.set(workflowKey(remaining));
      const next = remaining[0] ?? null;
      this.selectedWorkflowPath.set(next?.path ?? ".grain/workflows/ci.yml");
      this.workflowYaml.set(next?.text ?? "");
      this.snackbar.show(`Removed workflow ${target}`);
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  protected async saveWorkflows(): Promise<void> {
    const path =
      normalizeWorkflowPath(this.selectedWorkflowPath()) ||
      ".grain/workflows/ci.yml";
    const text = this.workflowYaml();
    const current = this.workflows();
    const idx = current.findIndex((w) => w.path === path);
    let next: RepoWorkflowRow[];
    if (text.trim()) {
      if (idx >= 0) {
        next = current.map((w, i) => (i === idx ? { ...w, path, text } : w));
      } else {
        next = [...current, { path, text }];
      }
    } else {
      next = current.filter((w) => w.path !== path);
    }
    try {
      const res = await this.api.put<{ workflows?: RepoWorkflowRow[] }>(
        "PUT /api/repos/{owner}/{name}/workflows",
        this.params(),
        {
          workflows: next.map((w) => ({ path: w.path, text: w.text })),
        },
      );
      if (Array.isArray(res?.workflows)) {
        this.workflows.set(res.workflows);
      } else {
        this.workflows.set(next);
      }
      this.workflowBaseline.set(workflowKey(this.workflows()));
      this.snackbar.show("Workflows saved");
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  protected async saveReleaseSettings(): Promise<void> {
    const parseJobs = (raw: string): string[] =>
      raw
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean);
    try {
      const res = await this.api.put<Record<string, unknown>>(
        "PUT /api/repos/{owner}/{name}/release",
        this.params(),
        {
          enabled: this.releaseEnabled(),
          baseBranchJobs: parseJobs(this.baseBranchJobsText()),
          postMergeDispatchJobs: parseJobs(this.postMergeJobsText()),
        },
      );
      if (res && typeof res === "object") {
        this.applyReleaseSettings(res);
      }
      this.releaseSaves.update((n) => n + 1);
      this.snackbar.show("Release settings saved");
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  // onWorkOwnershipSaved re-reads the release switch, which shows the
  // release item's override. The rest of the release card is a draft
  // that waits for its own Save, so only the switch is taken from the
  // re-read, and only while nobody has flipped it unsaved.
  protected async onWorkOwnershipSaved(): Promise<void> {
    // Who runs this repository's standing work decides whether its base
    // branch is judged here at all (pkg/ui/basebranchgate.go,
    // baseCheckSwitchedOff), so the unjudged-gate warning is re-read too,
    // as the classic Repo page does.
    void this.loadBaseBranchGates();
    if (!this.can("GET /api/repos/{owner}/{name}/release")) return;
    try {
      const rel = await this.api.get<Record<string, unknown>>(
        "GET /api/repos/{owner}/{name}/release",
        this.params(),
      );
      if (rel?.["enabled"] !== undefined) {
        const stored = Boolean(rel["enabled"]);
        if (this.releaseEnabled() === this.releaseEnabledStored) {
          this.releaseEnabled.set(stored);
        }
        this.releaseEnabledStored = stored;
      }
    } catch {
      // Non-fatal: the card itself already says what was stored.
    }
  }

  protected async toggleMergeOnGreen(): Promise<void> {
    const next = !this.mergeOnGreen();
    try {
      await this.api.put(
        "PUT /api/repos/{owner}/{name}/merge-on-green",
        this.params(),
        { mergeOnGreen: next },
      );
      this.mergeOnGreen.set(next);
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  protected async toggleRequireUpToDate(): Promise<void> {
    const next = !this.requireUpToDate();
    try {
      await this.api.put(
        "PUT /api/repos/{owner}/{name}/require-branch-up-to-date",
        this.params(),
        { requireBranchUpToDate: next },
      );
      this.requireUpToDate.set(next);
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  protected async onConfirmDone(accepted: boolean): Promise<void> {
    const req = this.confirmReq();
    this.confirmReq.set(null);
    const resolve = this.discardResolve;
    if (resolve) {
      this.discardResolve = null;
      resolve(accepted);
      return;
    }
    if (!accepted || !req) return;
    await req.run();
  }

  protected toggleBypassHumanGates(): void {
    const next = !this.bypassHumanGates();
    if (next) {
      this.confirmReq.set(
        bypassConfirm(this.repoSlug(), () => this.saveBypassHumanGates(true)),
      );
      return;
    }
    void this.saveBypassHumanGates(false);
  }

  private async saveBypassHumanGates(next: boolean): Promise<void> {
    try {
      await this.api.put(
        "PUT /api/repos/{owner}/{name}/base-repair-bypasses-human-gates",
        this.params(),
        { baseRepairBypassesHumanGates: next },
      );
      this.bypassHumanGates.set(next);
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  protected async saveAgentTabSettings(): Promise<void> {
    const p = this.params();
    try {
      if (this.can("PUT /api/repos/{owner}/{name}/setup-command")) {
        await this.api.put("PUT /api/repos/{owner}/{name}/setup-command", p, {
          setupCommand: this.setupCommand(),
        });
      }
      if (this.can("PUT /api/repos/{owner}/{name}/prompt-extension")) {
        await this.api.put(
          "PUT /api/repos/{owner}/{name}/prompt-extension",
          p,
          { promptExtension: this.promptExtension() },
        );
      }
      if (this.can("PUT /api/repos/{owner}/{name}/capabilities")) {
        const defaultCapabilities = this.repoCaps()
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean);
        await this.api.put("PUT /api/repos/{owner}/{name}/capabilities", p, {
          defaultCapabilities,
        });
      }
      this.agentBaseline.set({
        setup: this.setupCommand(),
        prompt: this.promptExtension(),
        caps: this.repoCaps(),
      });
      this.snackbar.show("Saved repository agent settings");
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  protected async createBranch(): Promise<void> {
    const name = this.newBranchName().trim();
    if (!name) return;
    try {
      await this.api.post(
        "POST /api/repos/{owner}/{name}/branches",
        this.params(),
        { name, from: this.newBranchFrom().trim() || "main" },
      );
      this.newBranchName.set("");
      await this.loadTabData();
      this.snackbar.show(`Created branch ${name}`);
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  protected async createRelease(): Promise<void> {
    const name = this.newReleaseName().trim();
    if (!name) return;
    try {
      await this.api.post(
        "POST /api/repos/{owner}/{name}/releases",
        this.params(),
        { name },
      );
      this.newReleaseName.set("");
      this.newReleaseError.set(null);
      await this.loadReleasesTab();
      this.snackbar.show(`Created release ${name}`);
    } catch (err) {
      this.newReleaseError.set(errText(err));
    }
  }

  protected async cutCandidate(release: string): Promise<void> {
    try {
      await this.api.post(
        "POST /api/repos/{owner}/{name}/releases/{release}/candidates",
        { ...this.params(), release },
        {},
      );
      await this.loadReleasesTab();
      this.snackbar.show(`Cut candidate for ${release}`);
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  protected reasonsFor(rel: ReleaseRow): {
    cut: string | null;
    promote: string | null;
  } {
    const selected = rel.name === this.selectedRelease();
    return releaseActionReasons(
      rel,
      selected ? this.candidates()[0] : undefined,
      selected && this.candidatesFor() === rel.name,
    );
  }

  // Promote, merge back and delete act on branches or drop the release,
  // so each asks first with the classic pane's words; the /next/ page
  // used to run them on the first click (2026-11-14 repository
  // walkthrough).
  protected askPromoteCandidate(rel: ReleaseRow): void {
    const rc =
      (rel.name === this.selectedRelease() && this.candidates()[0]?.branch) ||
      `${rel.name}'s current candidate`;
    const prod = rel.prodBranch || rel.name;
    this.confirmReq.set({
      title: `Promote ${rc}?`,
      message: `Promote ${rc} to ${prod}? This moves ${this.repoSlug()}'s ${prod} branch to ${rc}.`,
      accept: "Promote",
      destructive: false,
      run: () => this.promoteCandidate(rel.name),
    });
  }

  protected askMergeRelease(rel: ReleaseRow): void {
    const prod = rel.prodBranch || rel.name;
    this.confirmReq.set({
      title: `Merge ${prod} back?`,
      message: `Merge ${prod} into ${this.repoSlug()}'s default branch? This opens a pull request on GitHub from ${prod}.`,
      accept: "Open pull request",
      destructive: false,
      run: () => this.mergeRelease(rel.name),
    });
  }

  protected askDeleteRelease(release: string): void {
    this.confirmReq.set({
      title: `Remove release ${release}?`,
      message: `Remove release ${release}? This drops the release and its candidates from grain.`,
      accept: "Remove release",
      destructive: true,
      run: () => this.deleteRelease(release),
    });
  }

  protected async promoteCandidate(release: string): Promise<void> {
    try {
      await this.api.post(
        "POST /api/repos/{owner}/{name}/releases/{release}/candidates/promote",
        { ...this.params(), release },
        {},
      );
      await this.loadReleasesTab();
      this.snackbar.show(`Promoted candidate on ${release}`);
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  protected async mergeRelease(release: string): Promise<void> {
    try {
      await this.api.post(
        "POST /api/repos/{owner}/{name}/releases/{release}/merge",
        { ...this.params(), release },
        {},
      );
      await this.loadReleasesTab();
      this.snackbar.show(`Merged ${release}`);
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  protected async deleteRelease(release: string): Promise<void> {
    try {
      await this.api.delete(
        "DELETE /api/repos/{owner}/{name}/releases/{release}",
        { ...this.params(), release },
      );
      await this.loadReleasesTab();
      this.snackbar.show(`Deleted release ${release}`);
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  protected async approveQualification(id: string): Promise<void> {
    try {
      await this.api.post(
        "POST /api/repos/{owner}/{name}/candidates/{id}/qualification/approve",
        { ...this.params(), id },
        {},
      );
      await this.loadReleasesTab();
      this.snackbar.show("Approved candidate qualification");
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  protected async triggerReleaseRun(): Promise<void> {
    try {
      await this.api.post(
        "POST /api/repos/{owner}/{name}/release-runs",
        this.params(),
        {},
      );
      this.snackbar.show("Started release workflow run");
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  protected async approvePendingRelease(version: string): Promise<void> {
    try {
      await this.api.post(
        "POST /api/repos/{owner}/{name}/pending-releases/{version}/approve",
        { ...this.params(), version },
        {},
      );
      await this.loadReleasesTab();
      this.snackbar.show(`Approved release ${version}`);
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  protected async rejectPendingRelease(version: string): Promise<void> {
    try {
      await this.api.post(
        "POST /api/repos/{owner}/{name}/pending-releases/{version}/reject",
        { ...this.params(), version },
        {},
      );
      await this.loadReleasesTab();
      this.snackbar.show(`Rejected release ${version}`);
    } catch (err) {
      this.error.set(errText(err));
    }
  }
}
