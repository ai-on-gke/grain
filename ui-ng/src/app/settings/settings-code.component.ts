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
import { RouterLink } from "@angular/router";
import { GrainApi, splitRepo } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import type { SettingsSectionId } from "./settings-sections";
import {
  SettingsStateService,
  errText,
  type ReachableReposState,
} from "./settings-state.service";
import {
  ConfirmDialogComponent,
  bypassConfirm,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import { NoGitConnectionNoticeComponent } from "../shared/no-git-connection-notice.component";

interface RepoFacts {
  ciMode?: string;
  baseBranch?: string;
  // Whether grain reads the repository's own .grain/ declarations:
  // undefined while GET .../declarations is out, null when it failed.
  declarations?: boolean | null;
  bypassGates?: boolean;
}

interface GitConnectionRow {
  id?: string;
  credential?: string;
  host?: string;
  kind?: string;
  scope?: string;
  authType?: string;
  type?: string;
  author?: string;
  // status is what the server says about the connection now
  // (gitproxy.ConnectionStatus): the proxy its host is reached through,
  // and why it cannot be used, when it cannot.
  status?: {
    proxy?: string;
    error?: string;
    ambientPending?: boolean;
    ambientWarning?: string;
    defaultKnownHosts?: string;
  } | null;
}

// generatedCredential is whether cred is a name grain minted for a
// connection saved without one (model.DeploymentOwnsGitCredential:
// "grain-git-c-<hex>", or the older "grain-team-<team>-c-<hex>").
function generatedCredential(cred: string): boolean {
  return /^grain-(git|team)-/.test(cred);
}

// GitCheckResult is POST /api/deployment/git-connections/check's answer
// for one connection (ui.CredentialCheck).
interface GitCheckResult {
  ok?: boolean;
  host?: string;
  login?: string;
  app?: boolean;
  installationRepos?: number;
  scopes?: string[];
  expires?: string;
  error?: string;
  summary?: string;
  repos?: { repo?: string; read?: boolean; push?: boolean; error?: string }[];
  skipped?: number;
}

// gitCheckLines says what testing one connection found, as classic
// Settings → Git does: who the credential acts as, then each repository
// it can or cannot push to.
export function gitCheckLines(r: GitCheckResult): string[] {
  if (!r.ok) return [r.error || r.summary || "The credential was not accepted"];
  const who = r.app
    ? `GitHub App installation${r.installationRepos ? ` (${r.installationRepos} repositories)` : ""}`
    : r.login
      ? `@${r.login}`
      : "";
  const lines = [
    `Accepted${who ? ` as ${who}` : ""}${r.host ? ` by ${r.host}` : ""}`,
  ];
  const details: string[] = [];
  if (r.scopes?.length) details.push(`scopes: ${r.scopes.join(", ")}`);
  if (r.expires) details.push(`expires ${r.expires}`);
  if (details.length) lines.push(details.join(" · "));
  for (const repo of r.repos ?? []) {
    lines.push(
      `${repo.push ? "✓" : "✗"} ${repo.repo}: ${
        repo.read
          ? repo.push
            ? "read, push"
            : "read only -- cannot push or open pull requests"
          : repo.error || "not visible to this credential"
      }`,
    );
  }
  if (r.skipped && r.skipped > 0) {
    lines.push(`${r.skipped} more repositories were not checked.`);
  }
  return lines;
}

interface StateRepoEntryDto {
  path?: string;
  dir?: boolean;
  size?: number;
}

@Component({
  selector: "g-settings-code",
  imports: [
    RouterLink,
    PillSelectComponent,
    ConfirmDialogComponent,
    NoGitConnectionNoticeComponent,
  ],
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
            <div class="s-table-wrap s-table-stack-wrap">
              <table
                class="s-table s-table-stack"
                aria-label="Configured repositories"
              >
                <thead>
                  <tr>
                    <th>Repository</th>
                    <th>Base branch</th>
                    <th>Declarations</th>
                    <th>CI mode</th>
                    <th>Git connection</th>
                    <th>Base repair bypass</th>
                    <th class="s-actions-cell">Manage</th>
                  </tr>
                </thead>
                <tbody>
                  @for (repo of repos(); track repo) {
                    @let fact = repoFacts()[repo];
                    <tr [attr.data-testid]="'repo-row-' + repo">
                      <td class="s-mono">
                        <a
                          [routerLink]="repoRoute(repo)"
                          [attr.data-testid]="'repo-link-' + repo"
                          >{{ repo }}</a
                        >
                      </td>
                      <td class="s-mono" data-label="Base branch">
                        {{ fact?.baseBranch || "main" }}
                      </td>
                      <td
                        data-label="Declarations"
                        [attr.data-testid]="'repo-declarations-' + repo"
                      >
                        <a
                          [routerLink]="['/processes', 'declarations']"
                          [queryParams]="{ repo: repo }"
                          [attr.aria-label]="
                            repo + ' declarations: ' + declarationsText(fact)
                          "
                          >{{ declarationsText(fact) }}</a
                        >
                      </td>
                      <td data-label="CI mode">
                        <span class="status-pill neutral">{{
                          fact?.ciMode || "grain"
                        }}</span>
                      </td>
                      <td
                        data-label="Git connection"
                        [attr.data-testid]="'repo-git-' + repo"
                      >
                        <a
                          [routerLink]="['/settings', 'git']"
                          [attr.aria-label]="
                            repo + ' Git connection: ' + connectionText(repo)
                          "
                          >{{ connectionText(repo) }}</a
                        >
                      </td>
                      <td data-label="Base repair bypass">
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
                      <td class="s-actions-cell" data-label="Manage">
                        <div class="s-row-actions">
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
                            (click)="startRemoveRepo(repo)"
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

          <div class="s-subsection">
            <div class="s-subhead">
              <h4 class="s-subhead-title">Add repository</h4>
            </div>
            <g-no-git-connection-notice [deployment]="deploymentForNotice()" />
            <div class="s-grid">
              @if (reachableRepos().length > 0) {
                <div class="s-field">
                  <span class="s-label">Connected repositories</span>
                  <g-pill-select
                    [options]="reachableRepoOptions()"
                    [value]="
                      hasReachableRepo(newRepoSlug()) ? newRepoSlug() : ''
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
                  class="s-input s-mono"
                  type="text"
                  list="reachable-repos-list"
                  data-testid="input-add-repo"
                  placeholder="acme/platform or Other repository..."
                  [value]="newRepoSlug()"
                  [disabled]="
                    state.sectionLocked('repositories') ||
                    !access.can('POST /api/repos')
                  "
                  (input)="newRepoSlug.set($any($event.target).value)"
                  (focus)="loadReachableRepos()"
                />
                <datalist id="reachable-repos-list">
                  @for (r of reachableRepos(); track r.repo) {
                    <option [value]="r.repo">{{ r.repo }}</option>
                  }
                </datalist>
                @if (loadingReachableRepos()) {
                  <div class="s-caption">Checking reachable repositories…</div>
                }
                @if (reachableErrors().length) {
                  <div class="s-banner error">
                    <span
                      >Errors checking reachable repos:
                      {{ reachableErrors().join("; ") }}</span
                    >
                  </div>
                }
                @if (reachableHostMismatches().length) {
                  <div class="s-banner warn">
                    <span
                      >Host mismatches:
                      {{ reachableHostMismatches().join("; ") }}</span
                    >
                  </div>
                }
                @if (reachableReason()) {
                  <div class="s-caption">{{ reachableReason() }}</div>
                }
              </div>
              <div class="s-field">
                <label class="s-label" for="add-repo-url"
                  >Clone URL (optional)</label
                >
                <input
                  id="add-repo-url"
                  class="s-input s-mono"
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
                  busyRepo() ||
                  !newRepoSlug().trim() ||
                  state.sectionLocked('repositories') ||
                  !access.can('POST /api/repos')
                "
                (click)="addRepo()"
              >
                Add repository
              </button>
              @if (reachableCount() > 0) {
                <span class="s-hint s-num"
                  >{{ reachableCount() }} reachable via Git connections</span
                >
              }
            </div>
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
                class="s-input"
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
                class="s-input s-mono"
                type="text"
                placeholder="github.com"
                [value]="state.val('githubHost', 'github.com')"
                [disabled]="!canWriteSettings()"
                aria-describedby="git-host-hint"
                (input)="
                  state.setField('githubHost', $any($event.target).value)
                "
              />
              <span
                class="s-hint"
                id="git-host-hint"
                data-testid="git-host-credentials-pointer"
              >
                The host grain's GitHub API calls go to. The deployment's shared
                credentials live under
                <a routerLink="/settings/capabilities" fragment="credentials"
                  >Settings → Capabilities</a
                >.
              </span>
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
            <div
              class="s-banner"
              [class.ok]="gitCheckOk()"
              [class.warn]="!gitCheckOk()"
              data-testid="git-check-banner"
            >
              {{ gitCheckMessage() }}
            </div>
          }

          @if (gitRefusal(); as refusal) {
            <div class="s-banner error" data-testid="git-connections-error">
              <span>{{ refusal }}</span>
              <button
                type="button"
                class="btn-text s-btn-sm"
                (click)="settleGitRefusal()"
              >
                Dismiss
              </button>
            </div>
          }

          @if (gitConnections().length > 0) {
            <div class="s-table-wrap">
              <table class="s-table" aria-label="Git connections">
                <thead>
                  <tr>
                    <th>Credential</th>
                    <th>Host</th>
                    <th>Kind</th>
                    <th>Scope</th>
                    <th>Type</th>
                    <th class="s-actions-cell">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  @for (c of gitConnections(); track c.credential || c.id) {
                    @let cred = c.credential || c.id || "default";
                    <tr [attr.data-testid]="'git-conn-row-' + cred">
                      <td class="s-mono">{{ cred }}</td>
                      <td class="s-mono">
                        {{ c.host || "github.com" }}
                        @if (c.status?.proxy) {
                          <div
                            class="s-meta"
                            [attr.data-testid]="'git-conn-proxy-' + cred"
                          >
                            via proxy {{ c.status?.proxy }}
                          </div>
                        }
                        @if (c.status?.error) {
                          <div
                            class="s-meta s-field-error"
                            [attr.data-testid]="'git-conn-status-error-' + cred"
                          >
                            {{ c.status?.error }}
                          </div>
                        }
                      </td>
                      <td>{{ c.kind === "git" ? "git host" : "GitHub" }}</td>
                      <td>
                        @if (c.scope) {
                          <span class="s-mono">{{ c.scope }}</span>
                        } @else {
                          All repos on host
                        }
                      </td>
                      <td>
                        <span class="status-pill neutral">{{
                          c.authType || c.type || "pat"
                        }}</span>
                      </td>
                      <td class="s-actions-cell">
                        <div class="s-row-actions">
                          <button
                            type="button"
                            class="btn-tonal s-btn-sm"
                            [disabled]="
                              !access.can(
                                'POST /api/deployment/git-connections/check'
                              ) || testingGitCred() === cred
                            "
                            [attr.data-testid]="'test-git-conn-' + cred"
                            (click)="testGitConnection(c, cred)"
                          >
                            {{
                              testingGitCred() === cred ? "Testing…" : "Test"
                            }}
                          </button>
                          <button
                            type="button"
                            class="btn-text s-btn-sm"
                            [disabled]="
                              !access.can(
                                'DELETE /api/deployment/git-connections/{cred}'
                              ) || state.sectionLocked('git')
                            "
                            [attr.data-testid]="'remove-git-conn-' + cred"
                            (click)="
                              confirmDeleteGitConnection(
                                cred,
                                c.host || 'github.com'
                              )
                            "
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

          @if (gitTestResult(); as t) {
            <div
              class="s-banner"
              [class.ok]="t.tone === 'ok'"
              [class.warn]="t.tone === 'warn'"
              [class.error]="t.tone === 'error'"
              data-testid="git-conn-test-result"
            >
              <div style="flex: 1; min-width: 0; overflow-wrap: anywhere">
                <strong>Test {{ t.cred }}:</strong>
                @for (l of t.lines; track $index) {
                  <div>{{ l }}</div>
                }
              </div>
              <button
                type="button"
                class="btn-text s-btn-sm"
                (click)="gitTestResult.set(null)"
              >
                Dismiss
              </button>
            </div>
          }

          <p class="s-hint" data-testid="git-secret-note">
            A connection's token or key is never part of the config repo: it is
            stored write-only, under
            <a routerLink="/settings/capabilities" fragment="credentials"
              >Settings → Capabilities</a
            >, or when a task asks for it.
          </p>

          <form
            class="s-subsection"
            autocomplete="off"
            (submit)="$event.preventDefault(); saveGitConnection()"
          >
            <div class="s-subhead">
              <h4 class="s-subhead-title">Add Git connection</h4>
            </div>
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
                  class="s-input s-mono"
                  type="text"
                  data-testid="input-git-conn-host"
                  placeholder="github.com"
                  [value]="newGitHost()"
                  (input)="newGitHost.set($any($event.target).value)"
                />
              </div>
              <div class="s-field">
                <span class="s-label">Host kind</span>
                <g-pill-select
                  testid="select-git-conn-kind"
                  ariaLabel="Host kind"
                  align="left"
                  [options]="gitKindOptions"
                  [value]="newGitKind()"
                  (valueChange)="setGitKind($event)"
                />
              </div>
              <div class="s-field">
                <label class="s-label" for="gc-scope">Scope (optional)</label>
                <input
                  id="gc-scope"
                  class="s-input s-mono"
                  type="text"
                  data-testid="input-git-conn-scope"
                  placeholder="All repos on host, or acme/widgets"
                  [value]="newGitScope()"
                  (input)="newGitScope.set(inputVal($event))"
                />
              </div>
              <div class="s-field">
                <label class="s-label" for="gc-proxy">Proxy (optional)</label>
                <input
                  id="gc-proxy"
                  class="s-input s-mono"
                  type="text"
                  data-testid="input-git-conn-proxy"
                  placeholder="http://proxy.example.com:3128"
                  [value]="newGitProxy()"
                  (input)="newGitProxy.set(inputVal($event))"
                />
                <span class="s-hint"
                  >An http(s)://host:port proxy every connection to this host
                  goes through (no login in it). Other hosts are
                  unaffected.</span
                >
              </div>

              @if (gitConnMode() === "existing") {
                @if (reuseOptions().length > 1) {
                  <div class="s-field">
                    <span class="s-label">Reuse authentication from</span>
                    <g-pill-select
                      testid="select-git-conn-reuse"
                      ariaLabel="Reuse authentication from"
                      align="left"
                      [options]="reuseOptions()"
                      [value]="newGitCred()"
                      (valueChange)="reuseGitCredential($event)"
                    />
                  </div>
                }
                <div class="s-field">
                  <label class="s-label" for="gc-cred"
                    >Existing stored credential name</label
                  >
                  <input
                    id="gc-cred"
                    class="s-input s-mono"
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
                    [options]="gitTypeOptionsForKind()"
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
                      name="grain-git-conn-secret"
                      autocomplete="new-password"
                      data-lpignore="true"
                      data-1p-ignore="true"
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
                      class="s-textarea s-mono"
                      rows="2"
                      data-testid="input-git-conn-ssh-key"
                      placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
                      [value]="newGitSecret()"
                      (input)="newGitSecret.set($any($event.target).value)"
                    ></textarea>
                  </div>
                  <div class="s-field">
                    <label class="s-label" for="gc-known-hosts">{{
                      sshOnGitHub() ? "Known hosts (optional)" : "Known hosts"
                    }}</label>
                    <textarea
                      id="gc-known-hosts"
                      class="s-textarea s-mono"
                      rows="2"
                      data-testid="input-git-conn-known-hosts"
                      aria-describedby="gc-known-hosts-hint"
                      [placeholder]="
                        sshOnGitHub()
                          ? 'github.com ssh-ed25519 AAAA...'
                          : 'git.example.com ssh-ed25519 AAAA...'
                      "
                      [value]="newGitKnownHosts()"
                      (input)="newGitKnownHosts.set($any($event.target).value)"
                    ></textarea>
                    <span
                      class="s-hint"
                      id="gc-known-hosts-hint"
                      data-testid="git-known-hosts-hint"
                    >
                      @if (sshOnGitHub()) {
                        Left blank, github.com is pinned to GitHub's published
                        host keys.
                      } @else {
                        Needed: grain never trusts a host key on first use, so
                        give this host's key lines.
                      }
                    </span>
                    @if (sshOnGitHub() && defaultKnownHosts()) {
                      <div class="s-inline">
                        <button
                          type="button"
                          class="btn-text s-btn-sm"
                          data-testid="fill-git-known-hosts"
                          [disabled]="
                            newGitKnownHosts().trim() === defaultKnownHosts()
                          "
                          (click)="newGitKnownHosts.set(defaultKnownHosts())"
                        >
                          Use GitHub's published keys
                        </button>
                      </div>
                    }
                  </div>
                } @else if (newGitType() === "app") {
                  <div class="s-field">
                    <label class="s-label" for="gc-appid">GitHub App ID</label>
                    <input
                      id="gc-appid"
                      class="s-input s-num"
                      type="number"
                      min="1"
                      data-testid="input-git-app-id"
                      [value]="newGitAppId()"
                      (input)="newGitAppId.set($any($event.target).value)"
                    />
                  </div>
                  <div class="s-field">
                    <label class="s-label" for="gc-instid"
                      >Installation ID</label
                    >
                    <input
                      id="gc-instid"
                      class="s-input s-num"
                      type="number"
                      min="1"
                      data-testid="input-git-app-install-id"
                      [value]="newGitAppInstallId()"
                      (input)="
                        newGitAppInstallId.set($any($event.target).value)
                      "
                    />
                  </div>
                  <div class="s-field">
                    <label class="s-label" for="gc-appkey"
                      >App private key (PEM)</label
                    >
                    <textarea
                      id="gc-appkey"
                      class="s-textarea s-mono"
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
                type="submit"
                class="btn-tonal"
                data-testid="save-git-conn-btn"
                [disabled]="
                  !canSubmitGitConnection() ||
                  !access.can('POST /api/deployment/git-connections')
                "
              >
                Add Git connection
              </button>
            </div>
          </form>

          <form
            class="s-subsection"
            autocomplete="off"
            (submit)="$event.preventDefault(); saveGithubToken()"
          >
            <div class="s-field">
              <span class="s-label"
                >Named GitHub personal/bot tokens ({{
                  githubTokens().join(", ") || "none"
                }})</span
              >
              <span class="s-hint" data-testid="gh-token-default-hint">
                If no unscoped github.com Git connection exists yet, saving a
                named token also registers it as the default github.com
                connection.
              </span>
              <div class="s-inline">
                <input
                  class="s-input s-input-inline s-mono"
                  type="text"
                  autocomplete="off"
                  aria-label="GitHub token name"
                  placeholder="token-name"
                  [value]="ghTokenName()"
                  (input)="ghTokenName.set($any($event.target).value)"
                />
                <input
                  class="s-input s-input-inline"
                  type="password"
                  name="grain-gh-token-secret"
                  autocomplete="new-password"
                  data-lpignore="true"
                  data-1p-ignore="true"
                  aria-label="GitHub token secret"
                  placeholder="ghp_..."
                  [value]="ghTokenValue()"
                  (input)="ghTokenValue.set($any($event.target).value)"
                />
                <button
                  type="submit"
                  class="btn-tonal s-btn-sm"
                  [disabled]="
                    !ghTokenName().trim() ||
                    !ghTokenValue().trim() ||
                    !access.can('PUT /api/github-tokens/{name}')
                  "
                >
                  Save token
                </button>
              </div>
            </div>
          </form>
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
                (click)="confirmFlushCiCache()"
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
                class="s-input s-num"
                type="number"
                min="0"
                data-testid="input-ci-jobs-per-run"
                [value]="state.defaultedNumber('ciJobsPerRun')"
                [placeholder]="state.defaultHint('ciJobsPerRun', 'no limit')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setNumber('ciJobsPerRun', $any($event.target).value)
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="ci-hist">CI runs kept</label>
              <input
                id="ci-hist"
                class="s-input s-num"
                type="number"
                [value]="state.defaultedNumber('ciRunHistory')"
                [placeholder]="state.defaultHint('ciRunHistory')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setNumber('ciRunHistory', $any($event.target).value)
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="ci-cache-mb"
                >CI job cache (MiB)</label
              >
              <input
                id="ci-cache-mb"
                class="s-input s-num"
                type="number"
                min="0"
                [value]="state.defaultedNumber('ciCacheMb')"
                [placeholder]="state.defaultHint('ciCacheMb')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setNumber('ciCacheMb', $any($event.target).value)
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="ci-art-mb">CI artifacts (MiB)</label>
              <input
                id="ci-art-mb"
                class="s-input s-num"
                type="number"
                min="0"
                [value]="state.defaultedNumber('ciArtifactMb')"
                [placeholder]="state.defaultHint('ciArtifactMb')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setNumber('ciArtifactMb', $any($event.target).value)
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="ci-reserve"
                >CI capacity reserved after</label
              >
              <input
                id="ci-reserve"
                class="s-input s-num"
                type="text"
                [placeholder]="state.defaultHint('ciReserveAfter', '1m')"
                [value]="state.val('ciReserveAfter', '')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField('ciReserveAfter', $any($event.target).value)
                "
              />
            </div>
            <div class="s-field">
              <label class="s-label" for="ci-age-boost"
                >CI queue age boost</label
              >
              <input
                id="ci-age-boost"
                class="s-input s-num"
                type="text"
                data-testid="input-ciAgeBoost"
                [placeholder]="state.defaultHint('ciAgeBoost')"
                [value]="state.val('ciAgeBoost', '')"
                [disabled]="!canWriteSettings()"
                (input)="
                  state.setField('ciAgeBoost', $any($event.target).value)
                "
              />
              <span class="s-hint"
                >How long a waiting job queues to gain one step of rank, so
                higher-priority work cannot starve the rest.</span
              >
            </div>
          </div>
          @for (group of ciRetentionGroups; track group.title) {
            <div class="s-subsection">
              <div class="s-subhead">
                <h4 class="s-subhead-title">{{ group.title }}</h4>
              </div>
              <div class="s-grid">
                @for (f of group.fields; track f.key) {
                  <div class="s-field">
                    <label class="s-label" [attr.for]="'ci-' + f.key">{{
                      f.label
                    }}</label>
                    <input
                      class="s-input s-num"
                      type="number"
                      [id]="'ci-' + f.key"
                      [attr.min]="f.min"
                      [attr.data-testid]="'input-' + f.key"
                      [value]="state.defaultedNumber(f.key)"
                      [placeholder]="state.defaultHint(f.key)"
                      [disabled]="!canWriteSettings()"
                      (input)="
                        state.setNumber(f.key, $any($event.target).value)
                      "
                    />
                    <span class="s-hint">{{ f.hint }}</span>
                  </div>
                }
              </div>
            </div>
          }
          @if (ciFaultCount() > 0) {
            <p class="s-hint s-num">
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
                class="status-pill s-mono"
                [class.error]="
                  !!stateRepoError() ||
                  !!stateRepoLoadError() ||
                  stateRepoSchemaMismatch()
                "
                [class.neutral]="
                  !stateRepoError() &&
                  !stateRepoLoadError() &&
                  !stateRepoSchemaMismatch()
                "
                data-testid="state-repo-status"
              >
                {{ stateRepoSummary() }}
              </span>
              @if (stateRepoSchemaMismatch()) {
                <span
                  class="status-pill error s-mono"
                  data-testid="state-repo-schema-mismatch"
                >
                  schema {{ stateRepoSchemaVersion() }} (build
                  {{ stateRepoBuildSchemaVersion() }})
                </span>
              }
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

          @if (stateRepoLoadError()) {
            <div
              class="s-banner error"
              role="alert"
              data-testid="state-repo-load-error"
            >
              <span>
                Could not read config repository status:
                {{ stateRepoLoadError() }}
              </span>
            </div>
          }

          @if (stateRepoError()) {
            <div
              class="s-banner error"
              role="alert"
              data-testid="state-repo-error"
            >
              <span>{{ stateRepoError() }}</span>
            </div>
          } @else if (stateRepoSchemaMismatch()) {
            <div
              class="s-banner error"
              role="alert"
              data-testid="state-repo-schema-error"
            >
              <span>{{ stateRepoSchemaMismatchMessage() }}</span>
            </div>
          }

          @if (state.refusedChanges().length > 0) {
            <div
              class="s-banner warn"
              data-testid="staged-config-changes-banner"
            >
              <span class="s-num">
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
                  class="s-input s-mono"
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
                  class="s-input s-mono"
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
                class="s-input s-mono"
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
              <form
                style="display: contents"
                autocomplete="off"
                (submit)="$event.preventDefault()"
              >
                <input
                  id="sr-imp-key"
                  class="s-input"
                  type="password"
                  name="grain-state-repo-key"
                  autocomplete="new-password"
                  data-lpignore="true"
                  data-1p-ignore="true"
                  placeholder="age-secret-key-..."
                  [value]="importKeyDraft()"
                  [disabled]="!access.can('POST /api/state-repo/import-key')"
                  (input)="importKeyDraft.set($any($event.target).value)"
                />
              </form>
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
          } @else if (configFilesError()) {
            <div
              class="s-banner warn"
              role="status"
              data-testid="config-files-error"
            >
              <span
                >Couldn't list the config repository's files:
                {{ configFilesError() }}</span
              >
            </div>
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
  // The CI history, output, artifact and job-cache limits: each stores 0
  // for grain's default and reports it as <key>Default, which the blank
  // box shows as its placeholder (the Settings rule in the design book).
  protected readonly ciRetentionGroups: readonly {
    title: string;
    fields: readonly {
      key: string;
      label: string;
      hint: string;
      min: number;
    }[];
  }[] = [
    {
      title: "Step output",
      fields: [
        {
          key: "ciStepOutputMb",
          label: "Step output kept (MiB)",
          min: -1,
          hint: "Ceiling on step output across the whole history; the oldest runs' logs go first. -1 keeps every byte.",
        },
        {
          key: "ciStepOutputDays",
          label: "Step output age (days)",
          min: -1,
          hint: "How long a run's step output lives whatever room is left. -1 bounds it by size alone.",
        },
        {
          key: "ciRunOutputKb",
          label: "Output per run (KiB)",
          min: 0,
          hint: "Most step output one run may store, spent on the steps a reader opens first.",
        },
      ],
    },
    {
      title: "Artifacts",
      fields: [
        {
          key: "ciArtifactRunMb",
          label: "Artifacts per run (MiB)",
          min: 0,
          hint: "Ceiling on what one run's artifacts may come to.",
        },
        {
          key: "ciArtifactFileMb",
          label: "Largest artifact (MiB)",
          min: 0,
          hint: "Largest single artifact kept; a job's sandbox refuses one past it before sending.",
        },
        {
          key: "ciArtifactDays",
          label: "Artifacts age (days)",
          min: 0,
          hint: "How long a run's artifacts are kept.",
        },
      ],
    },
    {
      title: "Job cache",
      fields: [
        {
          key: "ciCacheEntries",
          label: "Job cache entries",
          min: 0,
          hint: "Ceiling on how many entries the cache holds, alongside its size.",
        },
        {
          key: "ciCacheDays",
          label: "Job cache age (days)",
          min: 0,
          hint: "How long a result nobody asks for again is kept.",
        },
      ],
    },
  ];

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
  protected readonly reachableRepos = signal<{ repo: string; push: boolean }[]>(
    [],
  );
  protected readonly reachableRepoOptions = computed<PillOption[]>(() => [
    { value: "", label: "Choose connected repository…" },
    ...this.reachableRepos().map((r) => ({ value: r.repo, label: r.repo })),
  ]);
  protected readonly newRepoSlug = signal("");
  protected readonly newRepoUrl = signal("");
  protected readonly busyRepo = signal(false);
  protected readonly reposPlanNotice = signal("");
  protected readonly confirmReq = signal<ConfirmRequest | null>(null);
  protected readonly loadingReachableRepos = signal(false);
  protected readonly reachableErrors = signal<string[]>([]);
  protected readonly reachableHostMismatches = signal<string[]>([]);
  protected readonly reachableReason = signal("");
  // The last reachable-repos answer, for the Git connection column:
  // null while it has not come back, false when it failed or may not
  // be read.
  protected readonly reach = signal<ReachableReposState | null | false>(null);

  protected readonly gitConnections = signal<GitConnectionRow[]>([]);
  protected readonly gitCheckMessage = signal("");
  protected readonly gitCheckOk = signal(true);
  protected readonly gitConnMode = signal("inline");
  protected readonly newGitCred = signal("");
  protected readonly newGitHost = signal("github.com");
  protected readonly newGitScope = signal("");
  protected readonly newGitType = signal("pat");
  protected readonly newGitSecret = signal("");
  protected readonly newGitAppId = signal("");
  protected readonly newGitAppInstallId = signal("");
  protected readonly newGitKnownHosts = signal("");
  protected readonly newGitProxy = signal("");
  protected readonly testingGitCred = signal<string | null>(null);
  protected readonly gitTestResult = signal<{
    cred: string;
    tone: "ok" | "warn" | "error";
    lines: string[];
  } | null>(null);
  // gitRefusal is why the last add or remove was refused, said beside the
  // form until a save or remove goes through (or it is dismissed): the
  // page's banner alone left the next, fixed save under the old error.
  protected readonly gitRefusal = signal<string | null>(null);
  // reuseOptions are the stored credentials an existing connection can
  // name, each told apart by the host, kind and scope of every
  // connection it already serves, so two connections on one host are
  // not both "github.com". A connection saved with no name is stored
  // under a generated grain-git-c-<hex>, which says nothing to a
  // person: its row leads with where it points and keeps the name as
  // the hint.
  protected readonly reuseOptions = computed<PillOption[]>(() => {
    const byCred = new Map<string, { where: string[]; type: string }>();
    for (const c of this.gitConnections()) {
      const cred = (c.credential || c.id || "").trim();
      if (!cred || cred === "anonymous") continue;
      const where = [
        c.host || "github.com",
        c.kind === "git" ? "git host" : "GitHub",
        c.scope || "all repos",
      ].join(" · ");
      const e = byCred.get(cred) ?? {
        where: [],
        type: c.authType || c.type || "pat",
      };
      if (!e.where.includes(where)) e.where.push(where);
      byCred.set(cred, e);
    }
    return [
      { value: "", label: "Choose a stored credential…" },
      ...[...byCred.entries()].map(([cred, e]) => ({
        value: cred,
        label: `${e.where.join("; ")} (${e.type})`,
        hint: generatedCredential(cred) ? `stored as ${cred}` : cred,
      })),
    ];
  });
  // defaultKnownHosts is GitHub's published host keys, which every
  // connection's status carries (ui.GitCredentialStatus): what a blank
  // known_hosts on github.com is pinned to.
  protected readonly defaultKnownHosts = computed(
    () =>
      this.gitConnections()
        .map((c) => c.status?.defaultKnownHosts?.trim() ?? "")
        .find(Boolean) ?? "",
  );
  // sshOnGitHub is whether a blank known_hosts would be filled in with
  // GitHub's keys: only for github.com itself (gitproxy.SetSSHKey).
  protected readonly sshOnGitHub = computed(
    () =>
      (this.newGitHost().trim().toLowerCase() || "github.com") === "github.com",
  );
  // deploymentForNotice is the deployment the no-git-connection notice
  // reads: undefined until it has loaded, so the notice does not flash.
  protected readonly deploymentForNotice = computed(() => {
    const d = this.state.deployment();
    return Object.keys(d).length > 0 ? d : undefined;
  });
  // newGitKind is the connection's host kind (model.GitHostKind): a
  // GitHub host, or a plain git host with no GitHub API. The server
  // reads an empty kind as GitHub, so a plain host has to say so.
  protected readonly newGitKind = signal<"github" | "git">("github");
  protected readonly gitKindOptions: readonly PillOption[] = [
    { value: "github", label: "GitHub (github.com or Enterprise)" },
    { value: "git", label: "Plain git host" },
  ];
  // A GitHub App needs a GitHub host: a plain git host has no App API,
  // and the server refuses one there only on save.
  protected gitTypeOptionsForKind(): PillOption[] {
    return this.gitTypeOptions.map((o) =>
      o.value === "app" && this.newGitKind() === "git"
        ? {
            ...o,
            disabled: true,
            hint: "Needs a GitHub host: a plain git host has no App API",
          }
        : o,
    );
  }
  protected setGitKind(kind: string): void {
    const k = kind === "git" ? "git" : "github";
    this.newGitKind.set(k);
    if (k === "git" && this.newGitType() === "app") this.newGitType.set("pat");
  }
  protected readonly githubTokens = signal<string[]>([]);
  protected readonly ghTokenName = signal("");
  protected readonly ghTokenValue = signal("");

  protected readonly ciFaultCount = signal(0);

  protected readonly stateRepoSummary = signal("Not initialized");
  protected readonly stateRepoError = signal("");
  protected readonly stateRepoLoadError = signal("");
  protected readonly stateRepoSchemaVersion = signal(0);
  protected readonly stateRepoBuildSchemaVersion = signal(0);
  protected readonly stateRepoSchemaMismatch = computed(() => {
    const repo = this.stateRepoSchemaVersion();
    const build = this.stateRepoBuildSchemaVersion();
    return repo > 0 && build > 0 && repo !== build;
  });
  protected readonly stateRepoSchemaMismatchMessage = computed(() => {
    const repo = this.stateRepoSchemaVersion();
    const build = this.stateRepoBuildSchemaVersion();
    if (repo <= 0 || build <= 0 || repo === build) return "";
    if (repo > build) {
      return `The repository holds schema ${repo} and this build knows ${build}. Upgrade grain before applying changes from this repository.`;
    }
    return `The repository holds schema ${repo} and this build knows ${build}. grain does not migrate a dump between schemas; the repository has to be re-seeded.`;
  });
  protected readonly syncingStateRepo = signal(false);
  protected readonly prTitle = signal("");
  protected readonly remoteUrl = signal("");
  protected readonly remoteBranch = signal("main");
  protected readonly kmsKeyUri = signal("");
  protected readonly importKeyDraft = signal("");
  protected readonly configFiles = signal<string[]>([]);
  // Why GET /api/state-repo/files failed -- no config repository here, or
  // one this controller cannot read -- shown in the file viewer in place
  // of "Select a config file", which offered nothing to select.
  protected readonly configFilesError = signal("");
  protected readonly selectedConfigFile = signal("");
  protected readonly configFileContent = signal("");

  protected inputVal(ev: Event): string {
    const target = ev.target;
    return target instanceof HTMLInputElement ||
      target instanceof HTMLTextAreaElement
      ? target.value
      : "";
  }

  protected repoWarning(repo: string): string | null {
    const policy = (
      this.state.config() as
        Record<string, Record<string, string[]>> | undefined
    )?.["localPolicy"]?.["repos"];
    if (policy && policy.length > 0 && !policy.includes(repo))
      return "Blocked by local policy";
    if (
      this.reachableRepos().length > 0 &&
      !this.reachableRepos().some((r) => r.repo === repo)
    )
      return "Unreachable";
    return null;
  }

  protected hasReachableRepo(slug: string): boolean {
    return this.reachableRepos().some((r) => r.repo === slug);
  }

  protected async loadReachableRepos(force = false): Promise<void> {
    if (
      (!force && this.reachableRepos().length > 0) ||
      this.loadingReachableRepos()
    )
      return;
    this.loadingReachableRepos.set(true);
    try {
      const data = await this.state.fetchReachableRepos(force);
      this.reachableRepos.set(data.repos);
      this.reachableErrors.set(data.errors);
      this.reachableHostMismatches.set(data.hostMismatches);
      this.reachableReason.set(data.reason);
      this.reachableCount.set(data.repos.length);
      this.reach.set(data);
    } catch (err) {
      this.reach.set(false);
      this.state.error.set(errText(err));
    } finally {
      this.loadingReachableRepos.set(false);
    }
  }

  protected configFileOptions(): PillOption[] {
    return this.configFiles().map((f) => ({ value: f, label: f }));
  }

  // The Declarations and Git connection columns say what classic
  // Settings → Repositories says: "…" while the read is out, "Unknown"
  // when it failed, and otherwise the fact.
  protected declarationsText(fact: RepoFacts | undefined): string {
    const d = fact?.declarations;
    if (d === undefined) return "…";
    if (d === null) return "Unknown";
    return d ? "Read" : "Not read";
  }

  // connectionText is which of the deployment's Git connections reaches
  // repo, by what the connections list: the connections on its host, and
  // whether the listing gave push. The listing names repositories, not
  // the connection each came from, so two connections on one host are
  // both named.
  protected connectionText(repo: string): string {
    const reach = this.reach();
    if (reach === null) return "…";
    if (!reach) return "Unknown";
    if (!reach.available) return "Not listed";
    const host = repoHostOf(repo) || "github.com";
    const listed = reach.repos.find(
      (r) => r.repo.toLowerCase() === repo.toLowerCase(),
    );
    if (!listed) return "Not reached";
    const names = reach.connections
      .filter((c) => c.ok && (c.host || "github.com").toLowerCase() === host)
      .map((c) => c.credential)
      .filter(Boolean);
    const via = names.length > 0 ? names.join(", ") : host;
    return listed.push ? via : `${via} (read only)`;
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
    if (t === "pat") {
      return Boolean(this.newGitSecret().trim());
    }
    if (t === "ssh") {
      // Off github.com a blank known_hosts pins nothing, and the server
      // refuses it (gitproxy.ParseKnownHostsFor).
      return Boolean(
        this.newGitSecret().trim() &&
        (this.sshOnGitHub() || this.newGitKnownHosts().trim()),
      );
    }
    if (t === "app") {
      return Boolean(
        Number(this.newGitAppId()) > 0 && this.newGitSecret().trim(),
      );
    }
    return t === "ambient" || t === "none";
  }

  private lastReachableEpoch = this.state.reachableEpoch();

  constructor() {
    effect(() => {
      void this.state.deployment();
      void this.state.settings();
      void this.state.config();
      const epoch = this.state.reachableEpoch();
      const sec = this.section();
      untracked(() => {
        if (sec !== "repositories") {
          this.lastReachableEpoch = epoch;
          return;
        }
        this.syncReposList();
        if (
          epoch !== this.lastReachableEpoch &&
          this.access.can("GET /api/deployment/reachable-repos")
        ) {
          this.lastReachableEpoch = epoch;
          void this.loadReachableRepos(true);
        }
      });
    });
  }

  ngOnInit(): void {
    this.loadForSection();
  }

  ngOnChanges(changes: SimpleChanges): void {
    if (changes["section"] && !changes["section"].firstChange) {
      this.loadForSection();
    } else if (
      changes["initialFilePath"] &&
      !changes["initialFilePath"].firstChange &&
      this.section() === "config-repo" &&
      this.initialFilePath()
    ) {
      void this.openConfigFile(this.initialFilePath());
    }
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
        void this.loadReachableRepos();
      } else {
        this.reach.set(false);
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
                declarations: Boolean(d?.["declarations"]),
                bypassGates: Boolean(d?.["baseRepairBypassesHumanGates"]),
              },
            }));
          })
          .catch(() =>
            this.repoFacts.update((all) => ({
              ...all,
              [repo]: { ...all[repo], declarations: null },
            })),
          );
      } else {
        this.repoFacts.update((all) => ({
          ...all,
          [repo]: { ...all[repo], declarations: null },
        }));
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

    const warn = this.repoWarning(repo);
    if (warn) {
      this.confirmReq.set({
        title: `Add repository ${repo} anyway?`,
        message: `${warn}. Tasks targeting ${repo} will wait until a Git connection and local policy allow it.`,
        accept: "Add anyway",
        destructive: false,
        run: () => this.performAddRepo(repo),
      });
      return;
    }

    await this.performAddRepo(repo);
  }

  private async performAddRepo(repo: string): Promise<void> {
    if (this.busyRepo()) return;
    this.busyRepo.set(true);
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
    } finally {
      this.busyRepo.set(false);
    }
  }

  protected async checkReposPlan(): Promise<void> {
    try {
      const res = await this.api.post<{
        targetReposStranded?: Array<{
          tasks?: unknown[];
          schedules?: unknown[];
          qualificationPlan?: boolean;
        }>;
      }>("POST /api/repos/plan", undefined, { targetRepos: this.repos() });
      // A stranded entry lists only what it strands (the API omits empty
      // lists), so a schedules-only repo strands no task at all.
      const entries = res?.targetReposStranded ?? [];
      const count = (n: number, one: string, many: string) =>
        `${n} ${n === 1 ? one : many}`;
      const tasks = entries.reduce((s, e) => s + (e?.tasks?.length ?? 0), 0);
      const schedules = entries.reduce(
        (s, e) => s + (e?.schedules?.length ?? 0),
        0,
      );
      const plans = entries.filter((e) => e?.qualificationPlan).length;
      const parts = [
        tasks ? count(tasks, "open task", "open tasks") : "",
        schedules ? count(schedules, "schedule", "schedules") : "",
        plans ? count(plans, "qualification plan", "qualification plans") : "",
      ].filter(Boolean);
      const list =
        parts.length > 1
          ? `${parts.slice(0, -1).join(", ")} and ${parts[parts.length - 1]}`
          : parts[0];
      this.reposPlanNotice.set(
        list
          ? `${list} would be stranded by current repository list.`
          : "All open tasks match active target repositories (0 stranded).",
      );
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  protected async startRemoveRepo(repo: string): Promise<void> {
    this.reposPlanNotice.set("Checking stranded tasks...");
    this.confirmReq.set({
      title: `Remove repository ${repo}?`,
      message: "Checking stranded tasks...",
      accept: "Remove",
      destructive: true,
      run: async () => {
        const { owner, name } = splitRepo(repo);
        await this.api.delete("DELETE /api/repos/{owner}/{name}", {
          owner,
          name,
        });
        this.repos.update((list) => list.filter((r) => r !== repo));
        this.snackbar.show(`Removed ${repo}`);
      },
    });

    try {
      const plan = await this.api.post<{
        tasks?: string[];
        schedules?: string[];
        targetReposStranded?: Array<{
          tasks?: string[];
          schedules?: string[];
        }>;
      }>("POST /api/repos/plan", undefined, {
        targetRepos: this.repos().filter((r) => r !== repo),
      });
      const fromStranded = (plan?.targetReposStranded ?? []).reduce(
        (sum, entry) =>
          sum + (entry?.tasks?.length ?? 0) + (entry?.schedules?.length ?? 0),
        0,
      );
      const stranded =
        fromStranded ||
        (plan?.tasks?.length || 0) + (plan?.schedules?.length || 0);
      let msg = "";
      if (stranded > 0)
        msg += `Warning: This will strand ${stranded} ${stranded === 1 ? "task or schedule" : "tasks or schedules"}.\n`;
      msg += `Warning: Per-repo settings (setup command, prompt, grants) will be deleted.`;
      this.reposPlanNotice.set(msg);
      // Update the request with the new message
      const req = this.confirmReq();
      if (req) {
        this.confirmReq.set({ ...req, message: msg });
      }
    } catch (err) {
      this.reposPlanNotice.set(errText(err));
      const req = this.confirmReq();
      if (req) {
        this.confirmReq.set({ ...req, message: errText(err) });
      }
    }
  }

  protected async onConfirmDone(accepted: boolean): Promise<void> {
    const req = this.confirmReq();
    this.confirmReq.set(null);
    if (!accepted || !req) return;
    try {
      await req.run();
    } catch (err) {
      this.state.error.set(errText(err));
    }
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
    const scope = this.newGitScope().trim();
    let payload: Record<string, unknown>;
    if (this.gitConnMode() === "existing") {
      payload = {
        credential: this.newGitCred().trim(),
        host,
        // The server writes the credential's host record with this
        // kind, and an empty one reads as github.
        kind: this.newGitKind(),
        ...(scope ? { scope } : {}),
        ...(this.newGitProxy().trim()
          ? { proxy: this.newGitProxy().trim() }
          : {}),
      };
    } else {
      const type = this.newGitType();
      payload = {
        credential: "",
        host,
        kind: this.newGitKind(),
        type,
        ...(scope ? { scope } : {}),
      };
      const proxy = this.newGitProxy().trim();
      if (proxy) payload["proxy"] = proxy;
      if (type === "pat") {
        payload["value"] = this.newGitSecret().trim();
      } else if (type === "ssh") {
        const knownHosts = this.newGitKnownHosts().trim();
        payload["ssh"] = {
          privateKey: this.newGitSecret(),
          ...(knownHosts ? { knownHosts } : {}),
        };
      } else if (type === "app") {
        // AppCredentialInput takes both IDs as strings: a JSON number
        // is refused outright ("cannot unmarshal number ... of type
        // string"), which made every App save from here fail.
        const appId = this.newGitAppId().trim();
        const installationId = this.newGitAppInstallId().trim();
        payload["app"] = {
          appId,
          ...(installationId ? { installationId } : {}),
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
      this.newGitScope.set("");
      this.newGitSecret.set("");
      this.newGitAppId.set("");
      this.newGitAppInstallId.set("");
      this.newGitKnownHosts.set("");
      this.newGitProxy.set("");
      this.settleGitRefusal();
      await this.loadGitConnections();
      void this.refreshDeployment();
      this.state.invalidateReachableRepos();
      this.snackbar.show(`Saved Git connection on ${host}`);
    } catch (err) {
      this.raiseGitRefusal(err);
    }
  }

  private raiseGitRefusal(err: unknown): void {
    this.state.recordError(err);
    this.gitRefusal.set(this.state.error() || errText(err));
  }

  // settleGitRefusal takes down the refusal, and the page banner with it
  // when that still says the same thing.
  protected settleGitRefusal(): void {
    const r = this.gitRefusal();
    if (r === null) return;
    if (this.state.error() === r) this.state.clearError();
    this.gitRefusal.set(null);
  }

  // The no-git-connection notice reads the deployment record, which a
  // connection added or removed here changes.
  private async refreshDeployment(): Promise<void> {
    if (!this.access.can("GET /api/deployment")) return;
    try {
      const d = await this.api.get<Record<string, unknown>>(
        "GET /api/deployment",
      );
      if (d) this.state.deployment.set(d);
    } catch {
      // Non-fatal: the notice catches up on the next load.
    }
  }

  protected reuseGitCredential(cred: string): void {
    this.newGitCred.set(cred);
    const c = this.gitConnections().find(
      (x) => (x.credential || x.id) === cred,
    );
    if (c) this.setGitKind(c.kind === "git" ? "git" : "github");
  }

  // testGitConnection checks one connection, as classic's per-row Test
  // does: its host, credential and scope.
  protected async testGitConnection(
    c: GitConnectionRow,
    cred: string,
  ): Promise<void> {
    this.testingGitCred.set(cred);
    this.gitTestResult.set(null);
    try {
      const res = await this.api.post<GitCheckResult>(
        "POST /api/deployment/git-connections/check",
        undefined,
        {
          host: c.host || "github.com",
          credential: cred,
          ...(c.scope ? { scope: c.scope } : {}),
        },
      );
      const r = res ?? {};
      const repos = r.repos ?? [];
      this.gitTestResult.set({
        cred,
        tone: !r.ok
          ? "error"
          : repos.length === 0 || repos.every((x) => x.push)
            ? "ok"
            : "warn",
        lines: gitCheckLines(r),
      });
    } catch (err) {
      this.gitTestResult.set({ cred, tone: "error", lines: [errText(err)] });
    } finally {
      this.testingGitCred.set(null);
    }
  }

  protected async checkGitConnections(): Promise<void> {
    try {
      const res = await this.api.post<{
        ok?: boolean;
        summary?: string;
        error?: string;
        host?: string;
        repos?: {
          repo?: string;
          read?: boolean;
          push?: boolean;
          error?: string;
        }[];
      }>("POST /api/deployment/git-connections/check", undefined, {});

      const unreachable = res?.repos?.filter((r) => !r.read || r.error) || [];
      this.gitCheckOk.set(res?.ok !== false && unreachable.length === 0);
      if (res?.ok === false) {
        // The check says what is wrong in `error` (a refused token, an
        // unreachable host); "Check reported issues" alone left nobody
        // able to act on it.
        this.gitCheckMessage.set(
          res.summary ||
            `${res.host ? res.host + ": " : ""}${res.error || "the check reported a problem"}`,
        );
      } else if (unreachable.length > 0) {
        this.gitCheckMessage.set(
          `OK, but unreachable repos: ${unreachable.map((r) => r.repo + (r.error ? ` (${r.error})` : "")).join("; ")}`,
        );
      } else {
        this.gitCheckMessage.set(
          res?.summary ||
            `OK${res?.host ? " — " + res.host + " answered" : ""}`,
        );
      }
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  // Removing a connection stops every clone and push through it, so it
  // asks first, as the classic page does.
  protected confirmDeleteGitConnection(cred: string, host: string): void {
    this.confirmReq.set({
      title: "Remove Git connection?",
      message: `Remove ${cred} on ${host}? Repositories using it can no longer clone or push until another connection covers ${host}.`,
      accept: "Remove",
      destructive: true,
      run: () => this.deleteGitConnection(cred),
    });
  }

  protected async deleteGitConnection(cred: string): Promise<void> {
    try {
      await this.api.delete("DELETE /api/deployment/git-connections/{cred}", {
        cred,
      });
      this.settleGitRefusal();
      await this.loadGitConnections();
      void this.refreshDeployment();
      this.state.invalidateReachableRepos();
      this.snackbar.show(`Removed ${cred}`);
    } catch (err) {
      this.raiseGitRefusal(err);
    }
  }

  protected async saveGithubToken(): Promise<void> {
    const name = this.ghTokenName().trim();
    const value = this.ghTokenValue().trim();
    if (!name || !value) return;
    const hadUnscopedDefault = this.gitConnections().some(
      (c) => (!c.host || c.host === "github.com") && !c.scope,
    );
    try {
      await this.api.put("PUT /api/github-tokens/{name}", { name }, { value });
      this.ghTokenName.set("");
      this.ghTokenValue.set("");
      await this.loadGitConnections();
      this.state.invalidateReachableRepos();
      const nowHasDefault = this.gitConnections().some(
        (c) => c.credential === name && !c.scope,
      );
      this.snackbar.show(
        !hadUnscopedDefault && nowHasDefault
          ? `Saved GitHub token ${name} and registered it as the default github.com connection`
          : `Saved GitHub token ${name}`,
      );
    } catch (err) {
      this.state.error.set(errText(err));
    }
  }

  // Flushing drops every repository's cached CI job state, so it asks
  // first, as Caches' two flushes do.
  protected confirmFlushCiCache(): void {
    this.confirmReq.set({
      title: "Flush CI job cache?",
      message:
        "Deletes the cached CI job state of every repository on this controller. The next CI run of each starts cold.",
      accept: "Flush cache",
      destructive: true,
      run: () => this.flushCiCache(),
    });
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
        const error =
          typeof sr?.["error"] === "string" ? sr["error"].trim() : "";
        const schemaVersion =
          typeof sr?.["schemaVersion"] === "number" ? sr["schemaVersion"] : 0;
        const buildSchemaVersion =
          typeof sr?.["buildSchemaVersion"] === "number"
            ? sr["buildSchemaVersion"]
            : 0;
        this.remoteUrl.set(remote);
        if (branch) this.remoteBranch.set(branch);
        this.stateRepoSummary.set(
          commit ? `${branch} @ ${commit}` : remote ? branch : "Local state",
        );
        this.stateRepoError.set(error);
        this.stateRepoSchemaVersion.set(schemaVersion);
        this.stateRepoBuildSchemaVersion.set(buildSchemaVersion);
        this.stateRepoLoadError.set("");
      } catch (err) {
        this.stateRepoLoadError.set(errText(err));
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
        this.configFilesError.set("");
        const init = this.initialFilePath() || files[0] || "";
        if (init) void this.openConfigFile(init);
      } catch (err) {
        // Not fatal to the rest of the section, but the viewer says why
        // it has nothing to show.
        this.configFiles.set([]);
        this.configFilesError.set(errText(err));
      }
    }
  }

  protected async syncStateRepo(): Promise<void> {
    this.syncingStateRepo.set(true);
    try {
      await this.api.post("POST /api/state-repo/sync", undefined, {});
      this.state.error.set(null);
      await this.loadStateRepo();
      this.snackbar.show("StateRepo synced");
    } catch (err) {
      const msg = errText(err);
      await this.loadStateRepo();
      const shown = this.stateRepoError();
      if (
        this.stateRepoLoadError() ||
        !shown ||
        (msg && !shown.includes(msg) && !msg.includes(shown))
      ) {
        this.state.recordError(err);
      } else {
        this.state.error.set(null);
      }
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

// repoHostOf is the host a repository spec names -- the authority of a
// URL, the host of an scp-style address, or a dotted first segment of
// host/owner/name -- or "" for a bare owner/name (classic RepoList.jsx's
// helper of the same name).
export function repoHostOf(text: string): string {
  let spec = (text || "").trim();
  if (spec.includes("://")) {
    spec = spec.slice(spec.indexOf("://") + 3);
    const authority = spec.split("/")[0];
    return authority.slice(authority.lastIndexOf("@") + 1).toLowerCase();
  }
  const at = spec.indexOf("@");
  if (at >= 0 && spec.indexOf(":", at) >= 0) {
    return spec.slice(at + 1, spec.indexOf(":", at)).toLowerCase();
  }
  const [first, ...rest] = spec.split("/");
  if (rest.length > 0 && /[.:]/.test(first)) return first.toLowerCase();
  return "";
}
