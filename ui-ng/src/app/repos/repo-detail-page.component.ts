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
  OnDestroy,
  OnInit,
  ViewEncapsulation,
  computed,
  inject,
  input,
  signal,
} from "@angular/core";
import { ActivatedRoute, Router, RouterLink } from "@angular/router";
import type { Subscription } from "rxjs";
import type { RouteKey } from "../api/generated-routes";
import { GrainApi } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { errText } from "../settings/settings-state.service";
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

interface BranchRow {
  name: string;
  sha?: string;
  head?: string;
  protected?: boolean;
}

interface ReleaseRow {
  name: string;
  branch?: string;
  state?: string;
  latestCandidate?: string;
}

interface CandidateRow {
  id: string;
  version?: string;
  state?: string;
  qualified?: boolean;
}

interface PendingReleaseRow {
  version: string;
  summary?: string;
  soakReady?: boolean;
}

@Component({
  selector: "g-repo-detail-page",
  imports: [RouterLink, PillSelectComponent],
  encapsulation: ViewEncapsulation.None,
  styleUrls: [
    "../shared/work-surface.css",
    "../shared/pill-select.css",
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
                        Default base branch, clone URL, and pinned Git
                        credential.
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
                      <label class="s-label" for="rc-base"
                        >Default base branch</label
                      >
                      <input
                        id="rc-base"
                        class="s-input tabular"
                        type="text"
                        data-testid="input-repo-base-branch"
                        [value]="baseBranch()"
                        [disabled]="
                          configRepoOnly() ||
                          !can(
                            'PUT /api/deployment/repos/{owner}/{name}/config'
                          )
                        "
                        (input)="baseBranch.set($any($event.target).value)"
                      />
                    </div>
                    <div class="s-field">
                      <label class="s-label" for="rc-clone">Clone URL</label>
                      <input
                        id="rc-clone"
                        class="s-input tabular"
                        type="text"
                        data-testid="input-repo-clone-url"
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
                        Pull external tracker issues into Grain tasks or refresh
                        repository lifecycle metadata.
                      </p>
                    </div>
                    <div class="s-inline">
                      <span class="status-pill neutral tabular">{{
                        lifecycleStage()
                      }}</span>
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
                  <div class="s-inline">
                    <input
                      class="s-input tabular"
                      style="width: 180px"
                      type="text"
                      aria-label="Issue label filter"
                      data-testid="input-import-label"
                      placeholder="grain-ready"
                      [value]="importLabel()"
                      (input)="importLabel.set($any($event.target).value)"
                    />
                    <button
                      type="button"
                      class="btn-tonal"
                      data-testid="import-issues-btn"
                      [disabled]="
                        !can('POST /api/repos/{owner}/{name}/import-issues')
                      "
                      (click)="importIssues()"
                    >
                      Import issues now
                    </button>
                  </div>
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
                        GitHub checks, or both.
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
                    <label class="s-label" for="wf-yaml"
                      >Inline workflow (.grain/workflows/ci.yml)</label
                    >
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
                      (input)="workflowYaml.set($any($event.target).value)"
                    ></textarea>
                    <div class="s-inline">
                      <button
                        type="button"
                        class="btn-tonal s-btn-sm"
                        data-testid="save-repo-workflow-btn"
                        [disabled]="
                          configRepoOnly() ||
                          !can('PUT /api/repos/{owner}/{name}/workflows')
                        "
                        (click)="saveWorkflows()"
                      >
                        Save workflow
                      </button>
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
                      data-testid="save-repo-agent-settings-btn"
                      [disabled]="configRepoOnly()"
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
                            <th>State</th>
                            <th>Candidates &amp; promotion</th>
                          </tr>
                        </thead>
                        <tbody>
                          @for (rel of releases(); track rel.name) {
                            <tr [attr.data-testid]="'release-row-' + rel.name">
                              <td class="tabular">{{ rel.name }}</td>
                              <td>
                                <span class="status-pill neutral">{{
                                  rel.state || "open"
                                }}</span>
                              </td>
                              <td>
                                <div class="s-inline">
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
                                    (click)="promoteCandidate(rel.name)"
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
                                    (click)="mergeRelease(rel.name)"
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
                                    (click)="deleteRelease(rel.name)"
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

                  @if (candidates().length > 0) {
                    <div class="s-table-wrap">
                      <table class="s-table" aria-label="Release candidates">
                        <thead>
                          <tr>
                            <th>Candidate</th>
                            <th>Status</th>
                            <th>Qualification</th>
                          </tr>
                        </thead>
                        <tbody>
                          @for (c of candidates(); track c.id) {
                            <tr>
                              <td class="tabular">{{ c.version || c.id }}</td>
                              <td>{{ c.state || "staged" }}</td>
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
                                  (click)="approveQualification(c.id)"
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
                      class="s-input tabular"
                      style="width: 220px"
                      type="text"
                      aria-label="New release name"
                      data-testid="input-new-release-name"
                      placeholder="v1.4.0"
                      [value]="newReleaseName()"
                      [disabled]="
                        !can('POST /api/repos/{owner}/{name}/releases')
                      "
                      (input)="newReleaseName.set($any($event.target).value)"
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
                </div>
              }
            }
          }
        </div>
      </div>
    </section>
  `,
})
export class RepoDetailPageComponent implements OnInit, OnDestroy {
  private readonly api = inject(GrainApi);
  protected readonly access = inject(AccessService);
  private readonly snackbar = inject(SnackbarService);
  private readonly route = inject(ActivatedRoute);
  private readonly router = inject(Router);

  readonly owner = input<string | undefined>(undefined);
  readonly name = input<string | undefined>(undefined);
  readonly tab = input<string | undefined>(undefined);

  protected readonly tabs = REPO_TABS;
  protected readonly ciModeOptions: readonly PillOption[] = [
    { value: "grain", label: "Grain CI (.grain/workflows)" },
    { value: "github", label: "GitHub Actions checks" },
    { value: "both", label: "Both Grain & GitHub CI" },
    { value: "off", label: "Off" },
  ];

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
  protected readonly lifecycleStage = signal("active");
  protected readonly importLabel = signal("");

  protected readonly ciMode = signal("grain");
  protected readonly watchDefaultBranch = signal(false);
  protected readonly workflowYaml = signal("");

  protected readonly mergeOnGreen = signal(false);
  protected readonly requireUpToDate = signal(false);
  protected readonly bypassHumanGates = signal(false);

  protected readonly setupCommand = signal("");
  protected readonly promptExtension = signal("");
  protected readonly repoCaps = signal("");

  protected readonly branches = signal<BranchRow[]>([]);
  protected readonly newBranchName = signal("");
  protected readonly newBranchFrom = signal("main");

  protected readonly releases = signal<ReleaseRow[]>([]);
  protected readonly candidates = signal<CandidateRow[]>([]);
  protected readonly pendingReleases = signal<PendingReleaseRow[]>([]);
  protected readonly newReleaseName = signal("");

  private subs: Subscription[] = [];

  protected readonly resolvedOwner = computed(
    () => this.routeOwner() || this.owner() || "",
  );
  protected readonly resolvedName = computed(
    () => this.routeName() || this.name() || "",
  );
  protected readonly repoSlug = computed(
    () => `${this.resolvedOwner()}/${this.resolvedName()}`,
  );

  protected readonly activeTab = computed<RepoTabId>(() => {
    const raw = (this.routeTab() ?? this.tab() ?? "overview").toLowerCase();
    return REPO_TABS.some((t) => t.id === raw)
      ? (raw as RepoTabId)
      : "overview";
  });

  protected gitConnOptions(): PillOption[] {
    const creds = this.availableGitCreds();
    return [
      { value: "", label: "Deployment default" },
      ...creds.map((c) => ({ value: c, label: c })),
    ];
  }

  ngOnInit(): void {
    this.subs.push(
      this.route.paramMap.subscribe((pm) => {
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

  protected selectTab(tab: RepoTabId): void {
    this.routeTab.set(tab);
    const o = this.resolvedOwner();
    const n = this.resolvedName();
    if (tab === "overview") {
      void this.router.navigate(["/repos", o, n]);
    } else {
      void this.router.navigate(["/repos", o, n, tab]);
    }
    void this.loadTabData(tab);
  }

  private params(): { owner: string; name: string } {
    return { owner: this.resolvedOwner(), name: this.resolvedName() };
  }

  private async initPage(): Promise<void> {
    try {
      const cfg = await this.api.config();
      if (cfg) {
        this.access.setConfig(cfg);
        const rec = cfg as Record<string, unknown>;
        this.configRepoOnly.set(
          Boolean(rec["configRepoOnly"] || rec["externalConfigRepo"]),
        );
      }
      await this.loadCoreRepo();
      await this.loadTabData();
    } catch (err) {
      this.error.set(errText(err));
    } finally {
      this.ready.set(true);
    }
  }

  private async loadCoreRepo(): Promise<void> {
    const p = this.params();
    if (!p.owner || !p.name) return;
    if (this.can("GET /api/deployment/repos/{owner}/{name}/config")) {
      try {
        const cfg = await this.api.get<Record<string, unknown>>(
          "GET /api/deployment/repos/{owner}/{name}/config",
          p,
        );
        if (cfg?.["baseBranch"]) this.baseBranch.set(String(cfg["baseBranch"]));
        if (cfg?.["cloneUrl"]) this.cloneUrl.set(String(cfg["cloneUrl"]));
        if (cfg?.["gitCredential"]) {
          this.gitCredential.set(String(cfg["gitCredential"]));
        }
      } catch {
        // Non-fatal
      }
    }
    if (this.can("GET /api/repos/{owner}/{name}/settings")) {
      try {
        const st = await this.api.get<Record<string, unknown>>(
          "GET /api/repos/{owner}/{name}/settings",
          p,
        );
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
        if (st?.["setupCommand"]) {
          this.setupCommand.set(String(st["setupCommand"]));
        }
        if (st?.["promptExtension"]) {
          this.promptExtension.set(String(st["promptExtension"]));
        }
      } catch {
        // Non-fatal
      }
    }
    if (this.access.can("GET /api/deployment/git-connections")) {
      try {
        const conns = await this.api.get<
          { credential?: string; id?: string }[] | { connections?: unknown[] }
        >("GET /api/deployment/git-connections");
        const arr = (
          Array.isArray(conns) ? conns : (conns?.connections ?? [])
        ) as { credential?: string; id?: string }[];
        this.availableGitCreds.set(
          arr.map((c) => c.credential ?? c.id ?? "").filter(Boolean),
        );
      } catch {
        // Non-fatal
      }
    }
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
            if (l?.["stage"]) this.lifecycleStage.set(String(l["stage"]));
          })
          .catch(() => undefined);
      }
    } else if (t === "ci") {
      if (this.can("GET /api/repos/{owner}/{name}/workflows")) {
        void this.api
          .get<{ workflows?: { path?: string; text?: string }[] }>(
            "GET /api/repos/{owner}/{name}/workflows",
            p,
          )
          .then((w) => {
            const first = w?.workflows?.[0];
            if (first?.text) this.workflowYaml.set(first.text);
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

  private async loadReleasesTab(): Promise<void> {
    const p = this.params();
    if (this.can("GET /api/repos/{owner}/{name}/release")) {
      try {
        const relCfg = await this.api.get<{
          pendingReleases?: PendingReleaseRow[];
        }>("GET /api/repos/{owner}/{name}/release", p);
        this.pendingReleases.set(relCfg?.pendingReleases ?? []);
      } catch {
        // Non-fatal
      }
    }
    if (this.can("GET /api/repos/{owner}/{name}/releases")) {
      try {
        const list = await this.api.get<
          ReleaseRow[] | { releases?: ReleaseRow[] }
        >("GET /api/repos/{owner}/{name}/releases", p);
        const rows = Array.isArray(list) ? list : (list?.releases ?? []);
        this.releases.set(rows);
        if (
          rows[0]?.name &&
          this.can(
            "GET /api/repos/{owner}/{name}/releases/{release}/candidates",
          )
        ) {
          const cands = await this.api.get<
            CandidateRow[] | { candidates?: CandidateRow[] }
          >("GET /api/repos/{owner}/{name}/releases/{release}/candidates", {
            ...p,
            release: rows[0].name,
          });
          this.candidates.set(
            Array.isArray(cands) ? cands : (cands?.candidates ?? []),
          );
        }
      } catch {
        // Non-fatal
      }
    }
  }

  protected async saveRepoConfig(): Promise<void> {
    try {
      await this.api.put(
        "PUT /api/deployment/repos/{owner}/{name}/config",
        this.params(),
        {
          baseBranch: this.baseBranch().trim() || "main",
          cloneUrl: this.cloneUrl().trim() || undefined,
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
      const res = await this.api.post<{ stage?: string }>(
        "POST /api/repos/{owner}/{name}/lifecycle/refresh",
        this.params(),
        {},
      );
      if (res?.stage) this.lifecycleStage.set(res.stage);
      this.snackbar.show("Lifecycle refreshed");
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  protected async importIssues(): Promise<void> {
    try {
      await this.api.post(
        "POST /api/repos/{owner}/{name}/import-issues",
        this.params(),
        { label: this.importLabel().trim() || undefined },
      );
      this.snackbar.show("Issue import triggered");
    } catch (err) {
      this.error.set(errText(err));
    }
  }

  protected async saveCiMode(ciMode: string): Promise<void> {
    this.ciMode.set(ciMode);
    try {
      await this.api.put(
        "PUT /api/repos/{owner}/{name}/ci-mode",
        this.params(),
        { ciMode },
      );
      this.snackbar.show(`CI mode set to ${ciMode}`);
    } catch (err) {
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

  protected async saveWorkflows(): Promise<void> {
    try {
      const text = this.workflowYaml();
      await this.api.put(
        "PUT /api/repos/{owner}/{name}/workflows",
        this.params(),
        {
          workflows: text.trim()
            ? [{ path: ".grain/workflows/ci.yml", text }]
            : [],
        },
      );
      this.snackbar.show("Workflow saved");
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

  protected async toggleBypassHumanGates(): Promise<void> {
    const next = !this.bypassHumanGates();
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
      await this.loadReleasesTab();
      this.snackbar.show(`Created release ${name}`);
    } catch (err) {
      this.error.set(errText(err));
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
