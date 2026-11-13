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
  OnChanges,
  OnInit,
  ViewEncapsulation,
  computed,
  inject,
  input,
  signal,
} from "@angular/core";
import { RouterLink } from "@angular/router";
import { GrainApi, splitRepo } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import type { SettingsSectionId } from "./settings-sections";
import { SettingsStateService, errText } from "./settings-state.service";
import {
  ConfirmDialogComponent,
  bypassConfirm,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";

interface RepoFacts {
  ciMode?: string;
  baseBranch?: string;
  declarationsCount?: number;
  bypassGates?: boolean;
}

interface GitConnectionRow {
  id?: string;
  credential?: string;
  host?: string;
  scope?: string;
  authType?: string;
  type?: string;
  author?: string;
  status?: string;
}

interface StateRepoEntryDto {
  path?: string;
  dir?: boolean;
  size?: number;
}

@Component({
  selector: "g-settings-code",
  imports: [RouterLink, PillSelectComponent, ConfirmDialogComponent],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @switch (section()) {
      @case ("repositories") {
        <div class="s-card" data-testid="card-repositories">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Target repositories</h3>
              <p class="s-card-desc">
                Repositories managed by this Grain controller. Click any
                repository to configure its CI workflows, merge queue, branches,
                and releases.
              </p>
            </div>
            @if (access.can("POST /api/repos/plan")) {
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                data-testid="check-repos-plan-btn"
                (click)="checkReposPlan()"
              >
                Check stranded tasks
              </button>
            }
          </div>

          @if (reposPlanNotice()) {
            <div class="s-banner warn" data-testid="repos-plan-notice">
              {{ reposPlanNotice() }}
            </div>
          }

          @if (repos().length === 0) {
            <p class="s-card-desc">No repositories configured yet.</p>
          } @else {
            <div class="s-table-wrap">
              <table class="s-table" aria-label="Configured repositories">
                <thead>
                  <tr>
                    <th>Repository</th>
                    <th>Base branch</th>
                    <th>CI mode</th>
                    <th>Base repair bypass</th>
                    <th>Manage</th>
                  </tr>
                </thead>
                <tbody>
                  @for (repo of repos(); track repo) {
                    @let fact = repoFacts()[repo];
                    <tr [attr.data-testid]="'repo-row-' + repo">
                      <td class="tabular">
                        <a
                          [routerLink]="repoRoute(repo)"
                          [attr.data-testid]="'repo-link-' + repo"
                          >{{ repo }}</a
                        >
                      </td>
                      <td class="tabular">{{ fact?.baseBranch || "main" }}</td>
                      <td>
                        <span class="status-pill neutral">{{
                          fact?.ciMode || "grain"
                        }}</span>
                      </td>
                      <td>
                        <button
                          type="button"
                          class="g-switch"
                          role="switch"
                          [attr.aria-checked]="Boolean(fact?.bypassGates)"
                          [attr.aria-label]="
                            'Base repair bypasses human gates on ' + repo
                          "
                          [disabled]="
                            state.configRepoOnly() ||
                            !access.can(
                              'PUT /api/repos/{owner}/{name}/base-repair-bypasses-human-gates',
                              repo
                            )
                          "
                          (click)="
                            toggleRepoBypass(repo, !Boolean(fact?.bypassGates))
                          "
                        ></button>
                      </td>
                      <td>
                        <div class="s-inline">
                          <a
                            class="btn-tonal s-btn-sm"
                            style="text-decoration: none"
                            [routerLink]="repoRoute(repo)"
                            >Settings</a
                          >
                          <a
                            class="btn-tonal s-btn-sm"
                            style="text-decoration: none"
                            [routerLink]="repoRoute(repo, 'releases')"
                            >Releases</a
                          >
                          <button
                            type="button"
                            class="btn-text s-btn-sm"
                            [attr.data-testid]="'remove-repo-' + repo"
                            [disabled]="
                              state.sectionLocked('repositories') ||
                              !access.can('DELETE /api/repos/{owner}/{name}')
                            "
                            (click)="removeRepo(repo)"
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

          <div class="s-grid">
            @if (reachableRepos().length > 0) {
              <div class="s-field">
                <span class="s-label">Connected repositories</span>
                <g-pill-select
                  [options]="reachableRepoOptions()"
                  [value]="
                    reachableRepos().includes(newRepoSlug())
                      ? newRepoSlug()
                      : ''
                  "
                  ariaLabel="Connected repository"
                  align="left"
                  testid="select-reachable-repo"
                  [disabled]="
                    state.sectionLocked('repositories') ||
                    !access.can('POST /api/repos')
                  "
                  (valueChange)="$event && newRepoSlug.set($event)"
                />
              </div>
            }
            <div class="s-field">
              <label class="s-label" for="add-repo-slug"
                >Repository (owner/name)</label
              >
              <input
                id="add-repo-slug"
                class="s-input tabular"
                type="text"
                data-testid="input-add-repo"
                placeholder="acme/platform"
                [value]="newRepoSlug()"
                [disabled]="
                  state.sectionLocked('repositories') ||
                  !access.can('POST /api/repos')
                "
                (input)="newRepoSlug.set($any($event.target).value)"
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="add-repo-url"
                >Clone URL (optional)</label
              >
              <input
                id="add-repo-url"
                class="s-input tabular"
                type="text"
                placeholder="https://github.com/acme/platform.git"
                [value]="newRepoUrl()"
                [disabled]="
                  state.sectionLocked('repositories') ||
                  !access.can('POST /api/repos')
                "
                (input)="newRepoUrl.set($any($event.target).value)"
              />
            </div>
          </div>
          <div class="s-inline">
            <button
              type="button"
              class="btn-primary"
              data-testid="add-repo-btn"
              [disabled]="
                !newRepoSlug().trim() ||
                state.sectionLocked('repositories') ||
                !access.can('POST /api/repos')
              "
              (click)="addRepo()"
            >
              Add repository
            </button>
            @if (reachableCount() > 0) {
              <span class="s-hint tabular"
                >{{ reachableCount() }} reachable via Git connections</span
              >
            }
          </div>
        </div>
      }

      @case ("git") {
        <div class="s-card" data-testid="card-git-identity">
          <h3 class="s-card-title">Agent Git identity &amp; host</h3>
          <div class="s-grid">
            <div class="s-field">
              <label class="s-label" for="git-author-name"
                >Commit author name</label
              >
              <input
                id="git-author-name"
                class="s-input"
                type="text"
                data-testid="input-git-author-name"
                placeholder="Grain Bot"
                [value]="state.deployVal('agentGitName', '')"
                [disabled]="!canWriteDeployment()"
                (input)="
                  state.setDeployField(
                    'agentGitName',
                    $any($event.target).value
                  )
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="git-author-email"
                >Commit author email</label
              >
              <input
                id="git-author-email"
                class="s-input tabular"
                type="email"
                data-testid="input-git-author-email"
                placeholder="grain@example.com"
                [value]="state.deployVal('agentGitEmail', '')"
                [disabled]="!canWriteDeployment()"
                (input)="
                  state.setDeployField(
                    'agentGitEmail',
                    $any($event.target).value
                  )
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="git-host">Default GitHub host</label>
              <input
                id="git-host"
                class="s-input tabular"
                type="text"
                placeholder="github.com"
                [value]="state.val('githubHost', 'github.com')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField('githubHost', $any($event.target).value)
                "
              />
            </div>
          </div>
          <div class="s-toggle-row">
            <div class="s-toggle-copy">
              <span class="s-toggle-name">Speak plain HTTP to GitHub host</span>
              <span class="s-hint"
                >Only for local test Git servers on trusted loopback
                networks.</span
              >
            </div>
            <button
              type="button"
              class="g-switch"
              role="switch"
              [attr.aria-checked]="
                Boolean(state.val('githubInsecureHttp', false))
              "
              [disabled]="!canWriteSettings()"
              aria-label="Speak plain HTTP to GitHub host"
              (click)="
                state.setField(
                  'githubInsecureHttp',
                  !state.val('githubInsecureHttp', false)
                )
              "
            ></button>
          </div>
        </div>

        <div class="s-card" data-testid="card-git-connections">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">Git connections &amp; tokens</h3>
              <p class="s-card-desc">
                Credentials used to clone repositories, push branches, and open
                pull requests.
              </p>
            </div>
            <button
              type="button"
              class="btn-tonal s-btn-sm"
              data-testid="check-git-connections-btn"
              [disabled]="
                !access.can('POST /api/deployment/git-connections/check')
              "
              (click)="checkGitConnections()"
            >
              Verify connections
            </button>
          </div>

          @if (gitCheckMessage()) {
            <div class="s-banner ok" data-testid="git-check-banner">
              {{ gitCheckMessage() }}
            </div>
          }

          @if (gitConnections().length > 0) {
            <div class="s-table-wrap">
              <table class="s-table" aria-label="Git connections">
                <thead>
                  <tr>
                    <th>Credential</th>
                    <th>Host</th>
                    <th>Type</th>
                    <th>Actions</th>
                  </tr>
                </thead>
                <tbody>
                  @for (c of gitConnections(); track c.credential || c.id) {
                    @let cred = c.credential || c.id || "default";
                    <tr [attr.data-testid]="'git-conn-row-' + cred">
                      <td class="tabular">{{ cred }}</td>
                      <td class="tabular">{{ c.host || "github.com" }}</td>
                      <td>{{ c.authType || c.type || "pat" }}</td>
                      <td>
                        <button
                          type="button"
                          class="btn-text s-btn-sm"
                          [disabled]="
                            !access.can(
                              'DELETE /api/deployment/git-connections/{cred}'
                            ) || state.sectionLocked('git')
                          "
                          (click)="deleteGitConnection(cred)"
                        >
                          Remove
                        </button>
                      </td>
                    </tr>
                  }
                </tbody>
              </table>
            </div>
          }

          <div class="s-grid">
            <div class="s-field">
              <span class="s-label">Credential source</span>
              <g-pill-select
                testid="select-git-conn-mode"
                ariaLabel="Credential source"
                align="left"
                [options]="gitModeOptions"
                [value]="gitConnMode()"
                (valueChange)="gitConnMode.set($event)"
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="gc-host">Host</label>
              <input
                id="gc-host"
                class="s-input tabular"
                type="text"
                data-testid="input-git-conn-host"
                placeholder="github.com"
                [value]="newGitHost()"
                (input)="newGitHost.set($any($event.target).value)"
              />
            </div>

            @if (gitConnMode() === "existing") {
              <div class="s-field">
                <label class="s-label" for="gc-cred"
                  >Existing stored credential name</label
                >
                <input
                  id="gc-cred"
                  class="s-input tabular"
                  type="text"
                  data-testid="input-git-conn-name"
                  placeholder="github-main"
                  [value]="newGitCred()"
                  (input)="newGitCred.set($any($event.target).value)"
                />
              </div>
            } @else {
              <div class="s-field">
                <span class="s-label">Auth type</span>
                <g-pill-select
                  testid="select-git-conn-type"
                  ariaLabel="Git auth type"
                  align="left"
                  [options]="gitTypeOptions"
                  [value]="newGitType()"
                  (valueChange)="newGitType.set($event)"
                />
              </div>

              @if (newGitType() === "pat") {
                <div class="s-field">
                  <label class="s-label" for="gc-val"
                    >Personal access token</label
                  >
                  <input
                    id="gc-val"
                    class="s-input"
                    type="password"
                    autocomplete="off"
                    data-testid="input-git-conn-secret"
                    placeholder="ghp_..."
                    [value]="newGitSecret()"
                    (input)="newGitSecret.set($any($event.target).value)"
                  />
                </div>
              } @else if (newGitType() === "ssh") {
                <div class="s-field">
                  <label class="s-label" for="gc-ssh">SSH private key</label>
                  <textarea
                    id="gc-ssh"
                    class="s-textarea tabular"
                    rows="2"
                    data-testid="input-git-conn-ssh-key"
                    placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
                    [value]="newGitSecret()"
                    (input)="newGitSecret.set($any($event.target).value)"
                  ></textarea>
                </div>
              } @else if (newGitType() === "app") {
                <div class="s-field">
                  <label class="s-label" for="gc-appid">GitHub App ID</label>
                  <input
                    id="gc-appid"
                    class="s-input tabular"
                    type="number"
                    min="1"
                    data-testid="input-git-app-id"
                    [value]="newGitAppId()"
                    (input)="newGitAppId.set($any($event.target).value)"
                  />
                </div>
                <div class="s-field">
                  <label class="s-label" for="gc-instid">Installation ID</label>
                  <input
                    id="gc-instid"
                    class="s-input tabular"
                    type="number"
                    min="1"
                    data-testid="input-git-app-install-id"
                    [value]="newGitAppInstallId()"
                    (input)="newGitAppInstallId.set($any($event.target).value)"
                  />
                </div>
                <div class="s-field">
                  <label class="s-label" for="gc-appkey"
                    >App private key (PEM)</label
                  >
                  <textarea
                    id="gc-appkey"
                    class="s-textarea tabular"
                    rows="2"
                    data-testid="input-git-app-private-key"
                    placeholder="-----BEGIN RSA PRIVATE KEY-----"
                    [value]="newGitSecret()"
                    (input)="newGitSecret.set($any($event.target).value)"
                  ></textarea>
                </div>
              }
            }
          </div>
          <div class="s-inline">
            <button
              type="button"
              class="btn-tonal"
              data-testid="save-git-conn-btn"
              [disabled]="
                !canSubmitGitConnection() ||
                !access.can('POST /api/deployment/git-connections')
              "
              (click)="saveGitConnection()"
            >
              Add Git connection
            </button>
          </div>

          <div class="s-field">
            <span class="s-label"
              >Named GitHub personal/bot tokens ({{
                githubTokens().join(", ") || "none"
              }})</span
            >
            <div class="s-inline">
              <input
                class="s-input s-input-inline tabular"
                type="text"
                aria-label="GitHub token name"
                placeholder="token-name"
                [value]="ghTokenName()"
                (input)="ghTokenName.set($any($event.target).value)"
              />
              <input
                class="s-input s-input-inline"
                type="password"
                autocomplete="off"
                aria-label="GitHub token secret"
                placeholder="ghp_..."
                [value]="ghTokenValue()"
                (input)="ghTokenValue.set($any($event.target).value)"
              />
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                [disabled]="
                  !ghTokenName().trim() ||
                  !ghTokenValue().trim() ||
                  !access.can('PUT /api/github-tokens/{name}')
                "
                (click)="saveGithubToken()"
              >
                Save token
              </button>
            </div>
          </div>
        </div>
      }

      @case ("ci") {
        <div class="s-card" data-testid="card-ci-settings">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">CI runner &amp; retention defaults</h3>
              <p class="s-card-desc">
                Concurrency, queue boost, step output retention, and shared CI
                job caching across repositories.
              </p>
            </div>
            @if (access.can("DELETE /api/ci/cache")) {
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                data-testid="flush-ci-cache-btn"
                (click)="flushCiCache()"
              >
                Flush CI job cache
              </button>
            }
          </div>
          <div class="s-grid">
            <div class="s-field">
              <label class="s-label" for="ci-per-run">CI jobs per run</label>
              <input
                id="ci-per-run"
                class="s-input tabular"
                type="number"
                min="0"
                data-testid="input-ci-jobs-per-run"
                [value]="state.val('ciJobsPerRun', 4)"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField(
                    'ciJobsPerRun',
                    Number($any($event.target).value)
                  )
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="ci-hist">CI runs kept</label>
              <input
                id="ci-hist"
                class="s-input tabular"
                type="number"
                [value]="state.val('ciRunHistory', 200)"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField(
                    'ciRunHistory',
                    Number($any($event.target).value)
                  )
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="ci-cache-mb"
                >CI job cache (MiB)</label
              >
              <input
                id="ci-cache-mb"
                class="s-input tabular"
                type="number"
                min="0"
                [value]="state.val('ciCacheMb', 2048)"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField('ciCacheMb', Number($any($event.target).value))
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="ci-art-mb">CI artifacts (MiB)</label>
              <input
                id="ci-art-mb"
                class="s-input tabular"
                type="number"
                min="0"
                [value]="state.val('ciArtifactMb', 1024)"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField(
                    'ciArtifactMb',
                    Number($any($event.target).value)
                  )
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="ci-reserve"
                >CI capacity reserved after</label
              >
              <input
                id="ci-reserve"
                class="s-input tabular"
                type="text"
                placeholder="5m"
                [value]="state.val('ciReserveAfter', '')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField('ciReserveAfter', $any($event.target).value)
                "
              />
            </div>
          </div>
          @if (ciFaultCount() > 0) {
            <p class="s-hint tabular">
              {{ ciFaultCount() }} shared CI fault classification rule(s)
              active.
            </p>
          }
        </div>
      }

      @case ("config-repo") {
        <div class="s-card" data-testid="card-state-repo">
          <div class="s-card-head">
            <div>
              <h3 class="s-card-title">StateRepo synchronization</h3>
              <p class="s-card-desc">
                Declarative Git-backed state repository for deployment settings,
                RBAC tables, and encrypted secrets.
              </p>
            </div>
            <div class="s-inline">
              <span
                class="status-pill neutral tabular"
                data-testid="state-repo-status"
              >
                {{ stateRepoSummary() }}
              </span>
              <button
                type="button"
                class="btn-primary s-btn-sm"
                data-testid="sync-state-repo-btn"
                [disabled]="
                  !access.can('POST /api/state-repo/sync') || syncingStateRepo()
                "
                (click)="syncStateRepo()"
              >
                {{ syncingStateRepo() ? "Syncing…" : "Sync now" }}
              </button>
            </div>
          </div>

          @if (state.refusedChanges().length > 0) {
            <div
              class="s-banner warn"
              data-testid="staged-config-changes-banner"
            >
              <span class="tabular">
                {{ state.refusedChanges().length }} rehearsed config file
                change(s) ready:
                {{ refusedPathsSummary() }}
              </span>
              <button
                type="button"
                class="btn-text s-btn-sm"
                (click)="state.refusedChanges.set([])"
              >
                Clear
              </button>
            </div>
          } @else {
            <span class="s-hint" data-testid="config-pr-empty-hint">
              Config pull requests require rehearsed file patches from a
              config-managed setting change.
            </span>
          }

          <div class="s-inline">
            <input
              class="s-input s-input-inline"
              type="text"
              aria-label="Config PR title"
              data-testid="input-state-repo-pr-title"
              placeholder="Propose config PR title…"
              [value]="prTitle()"
              [disabled]="
                state.refusedChanges().length === 0 ||
                !access.can('POST /api/state-repo/pull-requests')
              "
              (input)="prTitle.set($any($event.target).value)"
            />
            <button
              type="button"
              class="btn-tonal s-btn-sm"
              data-testid="create-state-repo-pr-btn"
              [disabled]="
                state.refusedChanges().length === 0 ||
                !access.can('POST /api/state-repo/pull-requests')
              "
              (click)="openConfigPr()"
            >
              Open config pull request
            </button>
          </div>
        </div>

        <div class="s-card" data-testid="card-state-repo-remote">
          <h3 class="s-card-title">Remote repository &amp; KMS secret key</h3>
          @if (!access.can("POST /api/state-repo/remote")) {
            <div
              class="s-banner readonly"
              data-testid="state-repo-remote-restricted"
            >
              {{ access.deniedReason("POST /api/state-repo/remote") }}
            </div>
          } @else {
            <div class="s-grid">
              <div class="s-field">
                <label class="s-label" for="sr-remote">Remote Git URL</label>
                <input
                  id="sr-remote"
                  class="s-input tabular"
                  type="text"
                  data-testid="input-state-repo-remote"
                  placeholder="https://github.com/org/grain-config.git"
                  [value]="remoteUrl()"
                  (input)="remoteUrl.set($any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <label class="s-label" for="sr-branch">Branch</label>
                <input
                  id="sr-branch"
                  class="s-input tabular"
                  type="text"
                  placeholder="main"
                  [value]="remoteBranch()"
                  (input)="remoteBranch.set($any($event.target).value)"
                />
              </div>
            </div>
            <div class="s-inline">
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                data-testid="save-state-repo-remote-btn"
                [disabled]="!remoteUrl().trim()"
                (click)="saveStateRepoRemote()"
              >
                Save remote
              </button>
              <button
                type="button"
                class="btn-text s-btn-sm"
                data-testid="reset-state-repo-remote-btn"
                (click)="useInternalConfigRepo()"
              >
                Use internal config repo
              </button>
            </div>
          }

          <div class="s-grid">
            <div class="s-field">
              <label class="s-label" for="sr-kms">Cloud KMS key resource</label>
              <input
                id="sr-kms"
                class="s-input tabular"
                type="text"
                placeholder="projects/.../cryptoKeys/..."
                [value]="kmsKeyUri()"
                [disabled]="
                  !access.can('POST /api/state-repo/secrets-key/wrap')
                "
                (input)="kmsKeyUri.set($any($event.target).value)"
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="sr-imp-key"
                >Import raw secrets key</label
              >
              <input
                id="sr-imp-key"
                class="s-input"
                type="password"
                autocomplete="off"
                placeholder="age-secret-key-..."
                [value]="importKeyDraft()"
                [disabled]="!access.can('POST /api/state-repo/import-key')"
                (input)="importKeyDraft.set($any($event.target).value)"
              />
            </div>
          </div>
          <div class="s-inline">
            <button
              type="button"
              class="btn-tonal s-btn-sm"
              [disabled]="
                !kmsKeyUri().trim() ||
                !access.can('POST /api/state-repo/secrets-key/wrap')
              "
              (click)="wrapSecretsKey()"
            >
              Wrap with KMS
            </button>
            <button
              type="button"
              class="btn-tonal s-btn-sm"
              [disabled]="
                !access.can('POST /api/state-repo/secrets-key/unwrap')
              "
              (click)="unwrapSecretsKey()"
            >
              Unwrap KMS key
            </button>
            <button
              type="button"
              class="btn-tonal s-btn-sm"
              [disabled]="
                !importKeyDraft().trim() ||
                !access.can('POST /api/state-repo/import-key')
              "
              (click)="importSecretsKey()"
            >
              Import key
            </button>
          </div>
        </div>

        <div class="s-card" data-testid="card-config-files">
          <div class="s-card-head">
            <h3 class="s-card-title">Config repository file viewer</h3>
            @if (configFileOptions().length > 0) {
              <g-pill-select
                testid="select-config-file"
                ariaLabel="Config file"
                align="left"
                [options]="configFileOptions()"
                [value]="selectedConfigFile()"
                (valueChange)="openConfigFile($event)"
              />
            }
          </div>
          @if (configFileContent()) {
            <pre class="s-code" data-testid="config-file-content">{{
              configFileContent()
            }}</pre>
          } @else {
            <p class="s-card-desc">
              Select a config file to inspect its committed YAML/JSON state.
            </p>
          }
        </div>
      }
    }
    @if (confirmReq(); as req) {
      <g-confirm-dialog [request]="req" (done)="onConfirmDone($event)" />
    }
  `,
})
export class SettingsCodeComponent implements OnInit, OnChanges {
  protected readonly api = inject(GrainApi);
  protected readonly access = inject(AccessService);
  protected readonly state = inject(SettingsStateService);
  private readonly snackbar = inject(SnackbarService);

  readonly section = input.required<SettingsSectionId>();
  readonly initialFilePath = input<string>("");

  protected readonly Number = Number;
  protected readonly Boolean = Boolean;

  protected readonly gitModeOptions: readonly PillOption[] = [
    { value: "inline", label: "Create inline credential" },
    { value: "existing", label: "Link stored credential" },
  ];

  protected readonly gitTypeOptions: readonly PillOption[] = [
    { value: "pat", label: "Personal access token (pat)" },
    { value: "ssh", label: "SSH private key (ssh)" },
    { value: "app", label: "GitHub App (app)" },
    { value: "ambient", label: "Host ambient login (ambient)" },
    { value: "none", label: "None / anonymous (none)" },
  ];

  protected readonly repos = signal<string[]>([]);
  protected readonly repoFacts = signal<Record<string, RepoFacts>>({});
  protected readonly reachableCount = signal(0);
  protected readonly reachableRepos = signal<string[]>([]);
  protected readonly reachableRepoOptions = computed<PillOption[]>(() => [
    { value: "", label: "Choose connected repository…" },
    ...this.reachableRepos().map((r) => ({ value: r, label: r })),
  ]);
  protected readonly newRepoSlug = signal("");
  protected readonly newRepoUrl = signal("");
  protected readonly reposPlanNotice = signal("");

  protected readonly gitConnections = signal<GitConnectionRow[]>([]);
  protected readonly gitCheckMessage = signal("");
  protected readonly gitConnMode = signal("inline");
  protected readonly newGitCred = signal("");
  protected readonly newGitHost = signal("github.com");
  protected readonly newGitType = signal("pat");
  protected readonly newGitSecret = signal("");
  protected readonly newGitAppId = signal("");
  protected readonly newGitAppInstallId = signal("");
  protected readonly githubTokens = signal<string[]>([]);
  protected readonly ghTokenName = signal("");
  protected readonly ghTokenValue = signal("");

  protected readonly ciFaultCount = signal(0);

  protected readonly stateRepoSummary = signal("Not initialized");
  protected readonly syncingStateRepo = signal(false);
  protected readonly prTitle = signal("");
  protected readonly remoteUrl = signal("");
  protected readonly remoteBranch = signal("main");
  protected readonly kmsKeyUri = signal("");
  protected readonly importKeyDraft = signal("");
  protected readonly configFiles = signal<string[]>([]);
  protected readonly selectedConfigFile = signal("");
  protected readonly configFileContent = signal("");

  protected configFileOptions(): PillOption[] {
    return this.configFiles().map((f) => ({ value: f, label: f }));
  }

  protected repoRoute(repo: string, tab?: string): string[] {
    const parts = repo.trim().split("/").filter(Boolean);
    return tab ? ["/repos", ...parts, tab] : ["/repos", ...parts];
  }

  protected refusedPathsSummary(): string {
    return this.state
      .refusedChanges()
      .map((c) => c.path)
      .join(", ");
  }

  protected canSubmitGitConnection(): boolean {
    if (this.gitConnMode() === "existing") {
      return Boolean(this.newGitCred().trim());
    }
    const t = this.newGitType();
    if (t === "pat" || t === "ssh") {
      return Boolean(this.newGitSecret().trim());
    }
    if (t === "app") {
      return Boolean(
        Number(this.newGitAppId()) > 0 && this.newGitSecret().trim(),
      );
    }
    return t === "ambient" || t === "none";
  }

  ngOnInit(): void {
    this.loadForSection();
  }

  ngOnChanges(): void {
    this.loadForSection();
  }

  protected canWriteSettings(): boolean {
    return (
      this.access.can("POST /api/settings") &&
      !this.state.sectionLocked(this.section())
    );
  }

  protected canWriteDeployment(): boolean {
    return (
      this.access.can("PUT /api/deployment") &&
      !this.state.sectionLocked(this.section())
    );
  }

  private loadForSection(): void {
    const sec = this.section();
    if (sec === "repositories") {
      this.syncReposList();
      if (this.access.can("GET /api/deployment/reachable-repos")) {
        void this.api
          .get<unknown[] | { repos?: unknown[] }>(
            "GET /api/deployment/reachable-repos",
          )
          .then((r) => {
            const list = Array.isArray(r) ? r : (r?.repos ?? []);
            const names = Array.from(
              new Set(
                list
                  .map((item) => {
                    if (typeof item === "string") return item.trim();
                    if (item && typeof item === "object" && "repo" in item) {
                      const slug = (item as { repo?: unknown }).repo;
                      return typeof slug === "string" ? slug.trim() : "";
                    }
                    return "";
                  })
                  .filter(Boolean),
              ),
            ).sort((a, b) => a.localeCompare(b));
            this.reachableCount.set(list.length);
            this.reachableRepos.set(names);
          })
          .catch(() => undefined);
      }
    } else if (sec === "git") {
      void this.loadGitConnections();
    } else if (sec === "ci") {
      if (this.access.can("GET /api/ci/faults")) {
        void this.api
          .get<unknown[] | { rules?: unknown[] }>("GET /api/ci/faults")
          .then((r) => {
            const list = Array.isArray(r) ? r : (r?.rules ?? []);
            this.ciFaultCount.set(list.length);
          })
          .catch(() => undefined);
      }
    } else if (sec === "config-repo") {
      void this.loadStateRepo();
    }
  }

  private syncReposList(): void {
    const fromDep = this.state.deployment()["targetRepos"];
    const fromSet = this.state.settings()["targetRepos"];
    const cfg = this.state.config() as Record<string, unknown> | null;
    const fromCfg = cfg?.["targetRepos"] ?? cfg?.["repos"];
    const raw = Array.isArray(fromDep)
      ? fromDep
      : Array.isArray(fromSet)
        ? fromSet
        : Array.isArray(fromCfg)
          ? fromCfg
          : [];
    const names = Array.from(
      new Set(
        raw
          .map((r) =>
            typeof r === "string"
              ? r
              : String((r as { name?: string })?.name ?? ""),
          )
          .filter(Boolean),
      ),
    );
    this.repos.set(names);
    for (const repo of names) {
      const { owner, name } = splitRepo(repo);
      if (!owner || !name) continue;
      if (this.access.can("GET /api/repos/{owner}/{name}/declarations", repo)) {
        void this.api
          .get<Record<string, unknown>>(
            "GET /api/repos/{owner}/{name}/declarations",
            { owner, name },
          )
          .then((d) => {
            this.repoFacts.update((all) => ({
              ...all,
              [repo]: {
                ...all[repo],
                ciMode: String(d?.["ciMode"] ?? "grain"),
                bypassGates: Boolean(d?.["baseRepairBypassesHumanGates"]),
              },
            }));
          })
          .catch(() => undefined);
      }
      if (this.access.can("GET /api/repos/{owner}/{name}/release", repo)) {
        void this.api
          .get<Record<string, unknown>>(
            "GET /api/repos/{owner}/{name}/release",
            { owner, name },
          )
          .then((rel) => {
            const branches = rel?.["baseBranches"] as string[] | undefined;
            this.repoFacts.update((all) => ({
              ...all,
              [repo]: {
                ...all[repo],
                baseBranch:
                  branches?.[0] ?? String(rel?.["baseBranch"] ?? "main"),
              },
            }));
          })
          .catch(() => undefined);
      }
    }
  }

  protected async addRepo(): Promise<void> {
    const repo = this.newRepoSlug().trim();
    if (!repo) return;
    try {
      const cloneUrl = this.newRepoUrl().trim();
      await this.api.post("POST /api/repos", undefined, {
        repo,
        ...(cloneUrl ? { cloneUrl } : {}),
      });
      this.newRepoSlug.set("");
      this.newRepoUrl.set("");
      await this.state.loadCore();
      this.syncReposList();
      this.snackbar.show(`Added ${repo}`);
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async checkReposPlan(): Promise<void> {
    try {
      const res = await this.api.post<{
        targetReposStranded?: unknown[];
      }>("POST /api/repos/plan", undefined, { targetRepos: this.repos() });
      const n = res?.targetReposStranded?.length ?? 0;
      this.reposPlanNotice.set(
        n > 0
          ? `${n} open task(s) would be stranded by current repository list.`
          : "All open tasks match active target repositories (0 stranded).",
      );
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async removeRepo(repo: string): Promise<void> {
    const { owner, name } = splitRepo(repo);
    try {
      await this.api.delete("DELETE /api/repos/{owner}/{name}", {
        owner,
        name,
      });
      this.repos.update((list) => list.filter((r) => r !== repo));
      this.snackbar.show(`Removed ${repo}`);
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected readonly confirmReq = signal<ConfirmRequest | null>(null);

  protected async onConfirmDone(accepted: boolean): Promise<void> {
    const req = this.confirmReq();
    this.confirmReq.set(null);
    if (!accepted || !req) return;
    await req.run();
  }

  protected toggleRepoBypass(repo: string, on: boolean): void {
    if (on) {
      this.confirmReq.set(
        bypassConfirm(repo, () => this.saveRepoBypass(repo, true)),
      );
      return;
    }
    void this.saveRepoBypass(repo, false);
  }

  private async saveRepoBypass(repo: string, on: boolean): Promise<void> {
    const { owner, name } = splitRepo(repo);
    try {
      await this.api.put(
        "PUT /api/repos/{owner}/{name}/base-repair-bypasses-human-gates",
        { owner, name },
        { baseRepairBypassesHumanGates: on },
      );
      this.repoFacts.update((all) => ({
        ...all,
        [repo]: { ...all[repo], bypassGates: on },
      }));
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  private async loadGitConnections(): Promise<void> {
    if (this.access.can("GET /api/deployment/git-connections")) {
      try {
        const r = await this.api.get<
          GitConnectionRow[] | { connections?: GitConnectionRow[] }
        >("GET /api/deployment/git-connections");
        this.gitConnections.set(Array.isArray(r) ? r : (r?.connections ?? []));
      } catch {
        // Non-fatal
      }
    }
    if (this.access.can("GET /api/github-tokens")) {
      try {
        const t = await this.api.get<
          (string | { name?: string })[] | { tokens?: string[] }
        >("GET /api/github-tokens");
        const arr = Array.isArray(t) ? t : (t?.tokens ?? []);
        this.githubTokens.set(
          arr
            .map((x) => (typeof x === "string" ? x : (x.name ?? "")))
            .filter(Boolean),
        );
      } catch {
        // Non-fatal
      }
    }
  }

  protected async saveGitConnection(): Promise<void> {
    if (!this.canSubmitGitConnection()) return;
    const host = this.newGitHost().trim() || "github.com";
    let payload: Record<string, unknown>;
    if (this.gitConnMode() === "existing") {
      payload = {
        credential: this.newGitCred().trim(),
        host,
      };
    } else {
      const type = this.newGitType();
      payload = {
        credential: "",
        host,
        type,
      };
      if (type === "pat") {
        payload["value"] = this.newGitSecret().trim();
      } else if (type === "ssh") {
        payload["ssh"] = { privateKey: this.newGitSecret() };
      } else if (type === "app") {
        const appId = Math.floor(Number(this.newGitAppId()) || 0);
        const installationId = Math.floor(
          Number(this.newGitAppInstallId()) || 0,
        );
        payload["app"] = {
          appId,
          ...(installationId > 0 ? { installationId } : {}),
          privateKey: this.newGitSecret(),
        };
      }
    }
    try {
      await this.api.post(
        "POST /api/deployment/git-connections",
        undefined,
        payload,
      );
      this.newGitCred.set("");
      this.newGitSecret.set("");
      this.newGitAppId.set("");
      this.newGitAppInstallId.set("");
      await this.loadGitConnections();
      this.snackbar.show(`Saved Git connection on ${host}`);
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async checkGitConnections(): Promise<void> {
    try {
      const res = await this.api.post<{ ok?: boolean; summary?: string }>(
        "POST /api/deployment/git-connections/check",
        undefined,
        {},
      );
      this.gitCheckMessage.set(
        res?.summary || (res?.ok === false ? "Check reported issues" : "OK"),
      );
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async deleteGitConnection(cred: string): Promise<void> {
    try {
      await this.api.delete("DELETE /api/deployment/git-connections/{cred}", {
        cred,
      });
      await this.loadGitConnections();
      this.snackbar.show(`Removed ${cred}`);
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async saveGithubToken(): Promise<void> {
    const name = this.ghTokenName().trim();
    const value = this.ghTokenValue().trim();
    if (!name || !value) return;
    try {
      await this.api.put("PUT /api/github-tokens/{name}", { name }, { value });
      this.ghTokenName.set("");
      this.ghTokenValue.set("");
      await this.loadGitConnections();
      this.snackbar.show(`Saved GitHub token ${name}`);
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async flushCiCache(): Promise<void> {
    try {
      await this.api.delete("DELETE /api/ci/cache");
      this.snackbar.show("CI job cache flushed");
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  private async loadStateRepo(): Promise<void> {
    if (this.access.can("GET /api/state-repo")) {
      try {
        const sr = await this.api.get<Record<string, unknown>>(
          "GET /api/state-repo",
        );
        const branch = String(sr?.["branch"] ?? "main");
        const commit = String(sr?.["head"] ?? sr?.["commit"] ?? "").slice(0, 8);
        const remote = String(sr?.["remote"] ?? "");
        this.remoteUrl.set(remote);
        if (branch) this.remoteBranch.set(branch);
        this.stateRepoSummary.set(
          commit ? `${branch} @ ${commit}` : remote ? branch : "Local state",
        );
      } catch {
        // Non-fatal
      }
    }
    if (this.access.can("GET /api/state-repo/files")) {
      try {
        const res = await this.api.get<
          | (string | StateRepoEntryDto)[]
          | { entries?: StateRepoEntryDto[]; files?: string[] }
        >("GET /api/state-repo/files");
        let files: string[] = [];
        if (Array.isArray(res)) {
          files = res
            .filter((e) => typeof e === "string" || !e.dir)
            .map((e) => (typeof e === "string" ? e : (e.path ?? "")))
            .filter(Boolean);
        } else if (res && typeof res === "object") {
          if (Array.isArray(res.entries)) {
            files = res.entries
              .filter((e) => !e.dir)
              .map((e) => e.path ?? "")
              .filter(Boolean);
          } else if (Array.isArray(res.files)) {
            files = res.files.filter(Boolean);
          }
        }
        this.configFiles.set(files);
        const init = this.initialFilePath() || files[0] || "";
        if (init) void this.openConfigFile(init);
      } catch {
        // Non-fatal
      }
    }
  }

  protected async syncStateRepo(): Promise<void> {
    this.syncingStateRepo.set(true);
    try {
      await this.api.post("POST /api/state-repo/sync", undefined, {});
      await this.loadStateRepo();
      this.snackbar.show("StateRepo synced");
    } catch (err) {
      this.state.recordError(err);
    } finally {
      this.syncingStateRepo.set(false);
    }
  }

  protected async openConfigPr(): Promise<void> {
    const changes = this.state.refusedChanges();
    if (changes.length === 0) return;
    const title = this.prTitle().trim();
    try {
      await this.api.post("POST /api/state-repo/pull-requests", undefined, {
        ...(title ? { title } : {}),
        changes,
      });
      this.prTitle.set("");
      this.state.refusedChanges.set([]);
      this.snackbar.show("Config pull request opened");
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async saveStateRepoRemote(): Promise<void> {
    try {
      await this.api.post("POST /api/state-repo/remote", undefined, {
        remote: this.remoteUrl().trim(),
        branch: this.remoteBranch().trim() || "main",
      });
      await this.loadStateRepo();
      this.snackbar.show("StateRepo remote updated");
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async useInternalConfigRepo(): Promise<void> {
    try {
      await this.api.post("POST /api/state-repo/remote", undefined, {
        none: true,
      });
      this.remoteUrl.set("");
      await this.loadStateRepo();
      this.snackbar.show("Switched to internal config repository");
    } catch (err) {
      this.state.recordError(err);
    }
  }

  protected async wrapSecretsKey(): Promise<void> {
    try {
      await this.api.post("POST /api/state-repo/secrets-key/wrap", undefined, {
        kmsKey: this.kmsKeyUri().trim(),
      });
      this.snackbar.show("Secrets key wrapped with KMS");
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async unwrapSecretsKey(): Promise<void> {
    try {
      await this.api.post(
        "POST /api/state-repo/secrets-key/unwrap",
        undefined,
        {},
      );
      this.snackbar.show("Secrets key unwrapped");
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async importSecretsKey(): Promise<void> {
    try {
      await this.api.post("POST /api/state-repo/import-key", undefined, {
        key: this.importKeyDraft().trim(),
      });
      this.importKeyDraft.set("");
      this.snackbar.show("Imported secrets key");
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async openConfigFile(path: string): Promise<void> {
    this.selectedConfigFile.set(path);
    if (!path || !this.access.can("GET /api/fs/config/{path...}")) return;
    try {
      const txt = await this.api.getText("GET /api/fs/config/{path...}", {
        path,
      });
      this.configFileContent.set(txt);
    } catch {
      try {
        const res = await this.api.get<{ content?: string }>(
          "GET /api/state-repo/file",
          undefined,
          `path=${encodeURIComponent(path)}`,
        );
        this.configFileContent.set(res?.content ?? "");
      } catch {
        this.configFileContent.set("");
      }
    }
  }
}
