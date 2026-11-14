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
  ElementRef,
  HostListener,
  OnDestroy,
  OnInit,
  ViewEncapsulation,
  afterRenderEffect,
  computed,
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

export const DISCARD_PROMPT =
  "You have unsaved changes here. Leave and discard them?";

// Which unsaved group a clean save of each action settles.
const SAVE_CLEARS: Record<string, string> = {
  saveWorkflows: "workflows",
  saveIssueImportSettings: "issueImport",
  saveAgentTabSettings: "agents",
};

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

// importOutcomeText says what one "Import issues now" pass did, in the
// classic Repo page's words (ui/src/components/RepoPage.jsx). An error
// with nothing open is the listing itself failing -- a per-issue
// failure needs an issue to fail on -- so it says nothing was read
// rather than "Read 0 open issues", which reads as a source with none.
export function importOutcomeText(out: {
  source?: string;
  open?: number;
  imported?: unknown[] | null;
  skipped?: number;
  error?: string;
}): { error: boolean; text: string } {
  const open = out.open ?? 0;
  const source = out.source || "the source repository";
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

// One branch grain was asked to create (GET .../branches answers
// {repo, name, from, status, error?, createdAt}). The table used to
// show a "HEAD" column these rows never carry, so every row read "—".
interface BranchRow {
  name: string;
  from?: string;
  status?: string;
  error?: string;
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
                (click)="dismissError()"
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
                      (click)="
                        importEnabled.set(!importEnabled());
                        markDirty('issueImport')
                      "
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
                        (input)="
                          importLabel.set($any($event.target).value);
                          markDirty('issueImport')
                        "
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
                          importSourceRepo.set($any($event.target).value);
                          markDirty('issueImport')
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
                      {{ importing() ? "Importing…" : "Import issues now" }}
                    </button>
                  </div>
                  @if (importOutcome(); as o) {
                    <p
                      [class]="o.error ? 's-field-error' : 's-hint'"
                      role="status"
                      data-testid="import-issues-outcome"
                    >
                      {{ o.text }}
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
                      (input)="
                        setupCommand.set($any($event.target).value);
                        markDirty('agents')
                      "
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
                      (input)="
                        promptExtension.set($any($event.target).value);
                        markDirty('agents')
                      "
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
                      (input)="
                        repoCaps.set($any($event.target).value);
                        markDirty('agents')
                      "
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
                            <th>From</th>
                            <th>Status</th>
                          </tr>
                        </thead>
                        <tbody>
                          @for (b of branches(); track b.name) {
                            <tr [attr.data-testid]="'branch-row-' + b.name">
                              <td class="tabular">{{ b.name }}</td>
                              <td class="tabular">{{ b.from || "—" }}</td>
                              <td>
                                <span class="status-pill neutral">{{
                                  b.status || "—"
                                }}</span>
                                @if (b.error) {
                                  <span class="s-field-error">{{
                                    b.error
                                  }}</span>
                                }
                              </td>
                            </tr>
                          }
                        </tbody>
                      </table>
                    </div>
                  } @else {
                    <p class="s-hint" data-testid="repo-branches-empty">
                      No branches have been created here through grain yet.
                    </p>
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
                                    [disabled]="
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
                                    [disabled]="
                                      !can(
                                        'POST /api/repos/{owner}/{name}/releases/{release}/candidates/promote'
                                      )
                                    "
                                    (click)="askPromote(rel)"
                                  >
                                    Promote RC
                                  </button>
                                  <button
                                    type="button"
                                    class="btn-tonal s-btn-sm"
                                    [attr.data-testid]="
                                      'merge-release-' + rel.name
                                    "
                                    [disabled]="
                                      !can(
                                        'POST /api/repos/{owner}/{name}/releases/{release}/merge'
                                      )
                                    "
                                    (click)="askMerge(rel)"
                                  >
                                    Merge back
                                  </button>
                                  <button
                                    type="button"
                                    class="btn-text s-btn-sm"
                                    [attr.data-testid]="
                                      'delete-release-' + rel.name
                                    "
                                    [disabled]="
                                      !can(
                                        'DELETE /api/repos/{owner}/{name}/releases/{release}'
                                      )
                                    "
                                    (click)="askDelete(rel)"
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
                      [attr.aria-describedby]="
                        releaseNameError() ? 'new-release-name-error' : null
                      "
                      [attr.aria-invalid]="releaseNameError() ? 'true' : null"
                      [value]="newReleaseName()"
                      [disabled]="
                        !can('POST /api/repos/{owner}/{name}/releases')
                      "
                      (input)="
                        newReleaseName.set($any($event.target).value);
                        releaseNameError.set(null)
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
                  @if (releaseNameError(); as e) {
                    <p
                      class="s-field-error"
                      id="new-release-name-error"
                      role="alert"
                      data-testid="new-release-name-error"
                    >
                      {{ e }}
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
  private readonly hostEl = inject<ElementRef<HTMLElement>>(ElementRef);

  constructor() {
    // Keep the selected tab inside the tab strip, which scrolls sideways
    // on a phone: a deep link or reload on Releases left it at x 589 of a
    // 390 px screen, out of sight (2026-11-13 repository walkthrough,
    // task 5386).
    afterRenderEffect(() => {
      const id = this.activeTab();
      const nav =
        this.hostEl.nativeElement.querySelector<HTMLElement>(".repo-tabs");
      const btn = nav?.querySelector<HTMLElement>(
        `[data-testid="repo-tab-${id}"]`,
      );
      if (!nav || !btn) return;
      const n = nav.getBoundingClientRect();
      const b = btn.getBoundingClientRect();
      if (b.left < n.left) nav.scrollLeft -= n.left - b.left + 8;
      else if (b.right > n.right) nav.scrollLeft += b.right - n.right + 8;
    });
  }

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
  // errorFrom names the action whose refusal the banner is showing, so
  // that action saving cleanly clears it (refuse/saved below). Without
  // it a refused setting's banner stayed up after the same setting was
  // fixed and saved (2026-11-13 repository walkthrough, task 5386).
  private readonly errorFrom = signal<string | null>(null);
  // Groups of fields edited since they were last saved; see confirmLeave.
  private readonly dirty = signal<ReadonlySet<string>>(new Set());
  private leaveResolve: ((leave: boolean) => void) | null = null;
  // What the last "Import issues now" pass read and imported, or why it
  // read nothing (POST .../import-issues answers {source, open,
  // imported, skipped, error}); the page used to say only "triggered".
  protected readonly importOutcome = signal<{
    error: boolean;
    text: string;
  } | null>(null);
  protected readonly importing = signal(false);
  // A refused release name is reported on its own box, not the banner,
  // and clears on the next edit or create.
  protected readonly releaseNameError = signal<string | null>(null);

  protected readonly baseBranch = signal("main");
  protected readonly cloneUrl = signal("");
  protected readonly gitCredential = signal("");
  protected readonly availableGitCreds = signal<string[]>([]);
  protected readonly lifecycleState = signal("active");
  protected readonly importEnabled = signal(false);
  protected readonly importLabel = signal("");
  protected readonly importSourceRepo = signal("");
  protected readonly importApproved = signal(false);

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
  protected readonly pendingReleases = signal<PendingReleaseRow[]>([]);
  protected readonly newReleaseName = signal("");

  private subs: Subscription[] = [];

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

  // refuse shows err in the page's banner on behalf of action; saved
  // clears that banner once the same action goes through, the way the
  // classic Repo page's refused-setting banner does.
  private refuse(action: string, err: unknown): void {
    this.error.set(errText(err));
    this.errorFrom.set(action);
  }

  private saved(action: string): void {
    const group = SAVE_CLEARS[action];
    if (group) {
      this.dirty.update((d) => {
        const next = new Set(d);
        next.delete(group);
        return next;
      });
    }
    if (this.errorFrom() === action) {
      this.error.set(null);
      this.errorFrom.set(null);
    }
  }

  protected markDirty(group: string): void {
    if (this.dirty().has(group)) return;
    this.dirty.update((d) => new Set(d).add(group));
  }

  // confirmLeave is what the route's canDeactivate guard asks
  // (confirmLeaveGuard in app.routes.ts): true at once with nothing unsaved, else
  // the classic Repo page's question in the shared dialog. Leaving with
  // an unsaved workflow, issue-import, setup, prompt or capabilities
  // edit used to drop it silently (2026-11-13 repository walkthrough,
  // task 5386).
  confirmLeave(): boolean | Promise<boolean> {
    if (this.dirty().size === 0) return true;
    return new Promise<boolean>((resolve) => {
      this.leaveResolve?.(false);
      this.leaveResolve = resolve;
      this.confirmReq.set({
        title: "Discard unsaved changes?",
        message: DISCARD_PROMPT,
        accept: "Leave",
        destructive: true,
        run: async () => undefined,
      });
    });
  }

  @HostListener("window:beforeunload", ["$event"])
  protected onBeforeUnload(e: BeforeUnloadEvent): void {
    if (this.dirty().size > 0) e.preventDefault();
  }

  protected dismissError(): void {
    this.error.set(null);
    this.errorFrom.set(null);
  }

  // selectTab navigates first and switches the pane only once the router
  // has gone there: the repo routes' leave guard may cancel the move
  // (unsaved edits), and switching up front left the Agents pane under
  // a /ci URL, after which a click back on CI -- a navigation to the URL
  // already shown, which the router skips -- reloaded the workflows over
  // the unsaved edit (review of task 5386). A navigation that lands
  // reuses this page and its paramMap subscription loads the tab; one
  // that changes route config builds a fresh page that loads itself.
  protected async selectTab(tab: RepoTabId): Promise<void> {
    if (tab === this.activeTab()) return;
    const h = this.resolvedHost();
    const o = this.resolvedOwner();
    const n = this.resolvedName();
    const base = h ? ["/repos", h, o, n] : ["/repos", o, n];
    await this.router.navigate(tab === "overview" ? base : [...base, tab]);
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
            // Never reload over an edit the reader has not saved.
            if (this.dirty().has("workflows")) return;
            const list = (Array.isArray(w) ? w : (w?.workflows ?? [])).map(
              (item) => ({
                path: item.path,
                text: item.text ?? "",
                ...(item.name ? { name: item.name } : {}),
              }),
            );
            this.workflows.set(list);
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
            if (
              Array.isArray(c?.defaultCapabilities) &&
              !this.dirty().has("agents")
            ) {
              this.repoCaps.set(c.defaultCapabilities.join(", "));
            }
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
      this.saved("saveRepoConfig");
    } catch (err) {
      this.refuse("saveRepoConfig", err);
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
      this.saved("pinGitConnection");
    } catch (err) {
      this.refuse("pinGitConnection", err);
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
      this.saved("refreshLifecycle");
    } catch (err) {
      this.refuse("refreshLifecycle", err);
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
      this.snackbar.show("Issue importer settings saved");
      this.saved("saveIssueImportSettings");
    } catch (err) {
      this.refuse("saveIssueImportSettings", err);
    }
  }

  protected async importIssues(): Promise<void> {
    this.importing.set(true);
    this.importOutcome.set(null);
    try {
      const out = await this.api.post<{
        source?: string;
        open?: number;
        imported?: unknown[] | null;
        skipped?: number;
        error?: string;
      }>("POST /api/repos/{owner}/{name}/import-issues", this.params(), {});
      this.importOutcome.set(importOutcomeText(out ?? {}));
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
      this.saved("saveCiMode");
    } catch (err) {
      // A refused mode is not the repo's mode: put the pill back on
      // what the server still has rather than leave it naming one it
      // refused.
      this.ciMode.set(previous);
      this.refuse("saveCiMode", err);
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
      this.saved("toggleWatchDefaultBranch");
    } catch (err) {
      this.refuse("toggleWatchDefaultBranch", err);
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
      this.saved("triggerBaseCiRun");
    } catch (err) {
      this.refuse("triggerBaseCiRun", err);
    }
  }

  protected selectWorkflow(path: string): void {
    this.selectedWorkflowPath.set(path);
    const found = this.workflows().find((w) => w.path === path);
    this.workflowYaml.set(found?.text ?? "");
  }

  protected onEditWorkflowText(text: string): void {
    this.markDirty("workflows");
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
    this.markDirty("workflows");
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
      const next = remaining[0] ?? null;
      this.selectedWorkflowPath.set(next?.path ?? ".grain/workflows/ci.yml");
      this.workflowYaml.set(next?.text ?? "");
      this.snackbar.show(`Removed workflow ${target}`);
      this.saved("deleteSelectedWorkflow");
    } catch (err) {
      this.refuse("deleteSelectedWorkflow", err);
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
      this.snackbar.show("Workflows saved");
      this.saved("saveWorkflows");
    } catch (err) {
      this.refuse("saveWorkflows", err);
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
      this.saved("saveReleaseSettings");
    } catch (err) {
      this.refuse("saveReleaseSettings", err);
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
      this.saved("toggleMergeOnGreen");
    } catch (err) {
      this.refuse("toggleMergeOnGreen", err);
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
      this.saved("toggleRequireUpToDate");
    } catch (err) {
      this.refuse("toggleRequireUpToDate", err);
    }
  }

  protected async onConfirmDone(accepted: boolean): Promise<void> {
    const req = this.confirmReq();
    this.confirmReq.set(null);
    const leave = this.leaveResolve;
    if (leave) {
      this.leaveResolve = null;
      if (accepted) this.dirty.set(new Set());
      leave(accepted);
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
      this.saved("saveBypassHumanGates");
    } catch (err) {
      this.refuse("saveBypassHumanGates", err);
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
      this.snackbar.show("Saved repository agent settings");
      this.saved("saveAgentTabSettings");
    } catch (err) {
      this.refuse("saveAgentTabSettings", err);
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
      this.saved("createBranch");
    } catch (err) {
      this.refuse("createBranch", err);
    }
  }

  protected async createRelease(): Promise<void> {
    const name = this.newReleaseName().trim();
    if (!name) return;
    this.releaseNameError.set(null);
    try {
      await this.api.post(
        "POST /api/repos/{owner}/{name}/releases",
        this.params(),
        { name },
      );
      this.newReleaseName.set("");
      await this.loadReleasesTab();
      this.snackbar.show(`Created release ${name}`);
    } catch (err) {
      this.releaseNameError.set(errText(err));
    }
  }

  // Promote, merge back and delete each act on branches or drop grain's
  // record of the release, so each asks first and names what it is
  // about to touch, as the classic Releases pane does
  // (ui/src/components/RepoReleases.jsx). The /next/ buttons did it on
  // one click (2026-11-13 repository walkthrough, task 5386).
  protected askPromote(rel: ReleaseRow): void {
    const prod = rel.prodBranch || rel.name;
    const rc =
      this.selectedRelease() === rel.name
        ? this.candidates()[0]?.branch
        : undefined;
    const what = rc ?? `${rel.name}'s current release candidate`;
    this.confirmReq.set({
      title: `Promote ${rc ?? "the current RC"}?`,
      message: `Promote ${what} to ${prod}? This moves ${this.repoSlug()}'s ${prod} branch to ${rc ?? "it"}.`,
      accept: "Promote",
      destructive: false,
      run: () => this.promoteCandidate(rel.name),
    });
  }

  protected askMerge(rel: ReleaseRow): void {
    const prod = rel.prodBranch || rel.name;
    this.confirmReq.set({
      title: `Merge ${prod} back?`,
      message: `Merge ${prod} into ${this.repoSlug()}'s default branch? This opens a pull request on GitHub from ${prod}.`,
      accept: "Open pull request",
      destructive: false,
      run: () => this.mergeRelease(rel.name),
    });
  }

  protected askDelete(rel: ReleaseRow): void {
    this.confirmReq.set({
      title: `Remove release ${rel.name}?`,
      message: `Remove release ${rel.name}? This drops the release and its candidates from grain.`,
      accept: "Remove release",
      destructive: true,
      run: () => this.deleteRelease(rel.name),
    });
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
      this.saved("cutCandidate");
    } catch (err) {
      this.refuse("cutCandidate", err);
    }
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
      this.saved("promoteCandidate");
    } catch (err) {
      this.refuse("promoteCandidate", err);
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
      this.saved("mergeRelease");
    } catch (err) {
      this.refuse("mergeRelease", err);
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
      this.saved("deleteRelease");
    } catch (err) {
      this.refuse("deleteRelease", err);
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
      this.saved("approveQualification");
    } catch (err) {
      this.refuse("approveQualification", err);
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
      this.saved("triggerReleaseRun");
    } catch (err) {
      this.refuse("triggerReleaseRun", err);
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
      this.saved("approvePendingRelease");
    } catch (err) {
      this.refuse("approvePendingRelease", err);
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
      this.saved("rejectPendingRelease");
    } catch (err) {
      this.refuse("rejectPendingRelease", err);
    }
  }
}
