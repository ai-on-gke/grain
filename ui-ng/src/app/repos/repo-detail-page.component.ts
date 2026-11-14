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
} from "@angular/core";
import { ActivatedRoute, Router, RouterLink } from "@angular/router";
import type { Subscription } from "rxjs";
import type { RouteKey } from "../api/generated-routes";
import { GrainApi, splitRepo } from "../api/grain-api.service";
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

export type RepoTabId =
  "overview" | "ci" | "merge-queue" | "agents" | "branches" | "releases";

const REPO_TABS: readonly { id: RepoTabId; label: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "ci", label: "CI & Workflows" },
  { id: "merge-queue", label: "Merge Queue" },
  { id: "agents", label: "Agents & Grants" },
  { id: "branches", label: "Branches" },
  { id: "releases", label: "Releases" },
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

interface PendingReleaseRow {
  version: string;
  summary?: string;
  soakReady?: boolean;
}

@Component({
  selector: "g-repo-detail-page",
  imports: [RouterLink, PillSelectComponent, ConfirmDialogComponent],
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
              routerLink="/settings/repositories"
              data-testid="back-to-repos-link"
              >← Repositories</a
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
              branches, and release qualification.
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
                    <div class="s-field">
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
  protected readonly importing = signal(false);
  protected readonly importOutcome = signal<{
    error: boolean;
    text: string;
  } | null>(null);

  protected readonly ciMode = signal("grain");
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
      ib.approved !== this.importApproved()
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
    return out;
  });

  protected readonly setupCommand = signal("");
  protected readonly promptExtension = signal("");
  protected readonly repoCaps = signal("");

  protected readonly branches = signal<BranchRow[]>([]);
  protected readonly newBranchName = signal("");
  protected readonly newBranchFrom = signal("main");

  protected readonly releaseEnabled = signal(false);
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
        this.configRepoOnly.set(
          Boolean(
            rec["configRepoOnly"] ||
            rec["externalConfigRepo"] ||
            rec["settingsLocked"],
          ),
        );
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
                }
              | undefined;
            if (imp && typeof imp === "object") {
              this.importEnabled.set(Boolean(imp.enabled));
              this.importLabel.set(String(imp.label ?? ""));
              this.importSourceRepo.set(String(imp.repo ?? ""));
              this.importApproved.set(Boolean(imp.approved));
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
    }
  }

  private applyReleaseSettings(relCfg: Record<string, unknown> | null): void {
    if (!relCfg) return;
    if (relCfg["enabled"] !== undefined) {
      this.releaseEnabled.set(Boolean(relCfg["enabled"]));
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
    try {
      await this.api.put(
        "PUT /api/repos/{owner}/{name}/settings",
        this.params(),
        {
          issueImportEnabled: this.importEnabled(),
          issueImportLabel: this.importLabel().trim(),
          issueImportRepo: this.importSourceRepo().trim(),
          issueImportApproved: this.importApproved(),
        },
      );
      this.markImportSaved();
      this.snackbar.show("Issue importer settings saved");
    } catch (err) {
      this.error.set(errText(err));
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
      this.snackbar.show("Release settings saved");
    } catch (err) {
      this.error.set(errText(err));
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
