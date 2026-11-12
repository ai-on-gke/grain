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
  ElementRef,
  type OnDestroy,
  type OnInit,
  ViewEncapsulation,
  computed,
  effect,
  inject,
  input,
  signal,
  untracked,
  viewChild,
} from "@angular/core";
import { Router, RouterLink } from "@angular/router";
import type { AttachmentUpload, Comment } from "../api/models";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { ActorChipComponent } from "../shared/actor-chip.component";
import {
  AttachmentListComponent,
  AttachmentPickerComponent,
  filesFromTransfer,
  readFilesAsUploads,
} from "../shared/attachments";
import { MarkdownDirective } from "../shared/code-highlight";
import { GrantsPickerComponent } from "../shared/grants-picker.component";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import { StatusIconComponent } from "../shared/status-icon.component";
import { TipDirective } from "../shared/tip.directive";
import {
  DIRECTOR_ID,
  apiGrantCount,
  isHuman,
  otherGrants,
  sessionStatus,
  toggleRows,
} from "./session-view";
import { SessionsStore } from "./sessions.store";

// clock is a message's time: the time of day today, the date otherwise.
export function clock(iso: string | undefined, now = new Date()): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const sameDay = d.toDateString() === now.toDateString();
  return sameDay
    ? d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })
    : d.toLocaleDateString([], { month: "short", day: "numeric" });
}

// SessionsPageComponent is Sessions (spec D-8) with zero Classic fallbacks:
// standing conversations on the left, live "Apply now" banner on run-fixed
// edits, per-method/API grants & team/model settings, and file attachments.
@Component({
  selector: "g-sessions-page",
  imports: [
    RouterLink,
    TipDirective,
    StatusIconComponent,
    ActorChipComponent,
    MarkdownDirective,
    AttachmentListComponent,
    AttachmentPickerComponent,
    GrantsPickerComponent,
    PillSelectComponent,
  ],
  encapsulation: ViewEncapsulation.None,
  styleUrls: [
    "./sessions-page.component.css",
    "../shared/conversation.css",
    "../shared/code-highlight.css",
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    "(document:keydown.escape)": "onEscape($event)",
    "(document:keydown.tab)": "onTab($event)",
    "(document:keydown.shift.tab)": "onTab($event)",
  },
  template: `
    <section
      class="view-panel is-active sessions-panel"
      [class.is-chat-open]="!!id()"
      aria-labelledby="sessions-headline"
      [attr.inert]="newOpen() || confirmDelete() ? '' : null"
    >
      <div class="stage-header">
        <div class="view-headline-row">
          <div class="headline-cluster">
            <h1 class="view-headline" id="sessions-headline">Sessions</h1>
          </div>
          @if (canCreate()) {
            <button
              #newBtn
              type="button"
              class="btn-outlined"
              data-testid="new-session"
              gTip="Start a standing conversation with its own interactive agent"
              (click)="openNew()"
            >
              New session
            </button>
          }
        </div>
        <p class="view-subtitle">
          Standing conversations with the Director and other interactive agents.
          Each one is a task that stays open; ask it to file work, look into a
          problem, or change a setting.
        </p>
      </div>

      @if (store.loadError(); as err) {
        <p class="banner banner-error" role="alert" data-testid="load-error">
          {{ err }}
        </p>
      }

      <div class="sessions-split" [class.is-chat-open]="!!id()">
        <nav class="session-list" aria-label="Sessions">
          <ul>
            @for (r of store.rows(); track r.session.id) {
              @let st = statusOf(r.task);
              <li>
                <a
                  class="session-item"
                  [routerLink]="['/sessions', r.session.id]"
                  [attr.aria-current]="id() === r.session.id ? 'page' : null"
                  [attr.data-testid]="'session-' + r.session.id"
                >
                  <span class="session-item-top">
                    <span class="s-name">{{ r.session.title }}</span>
                    <span [class]="'status-pill ' + st.tone"
                      ><g-status-icon [tone]="st.tone" [kind]="st.label" />{{
                        st.label
                      }}</span
                    >
                  </span>
                  @if (r.session.team) {
                    <span class="muted" style="font-size: 11px"
                      >Team: {{ r.session.team }}</span
                    >
                  }
                  @if (r.line) {
                    <span class="session-line">{{ r.line }}</span>
                  }
                </a>
              </li>
            }
          </ul>
          @if (store.loaded() && store.rows().length === 0) {
            <p class="empty-state">No sessions yet.</p>
          }
        </nav>

        <section
          class="chat-pane"
          aria-labelledby="chat-title"
          data-testid="chat-pane"
        >
          @if (store.selected(); as row) {
            @let st = store.status();
            <div class="chat-head">
              <a
                class="icon-btn chat-back"
                routerLink="/sessions"
                aria-label="Back to sessions"
                >←</a
              >
              <h2 class="chat-title" id="chat-title" tabindex="-1" #chatTitle>
                {{ row.session.title }}
              </h2>
              @if (row.session.team) {
                <span
                  class="status-pill neutral"
                  data-testid="session-team-pill"
                  >{{ row.session.team }}</span
                >
              }
              <span
                [class]="'status-pill ' + st.tone"
                data-testid="chat-status"
                role="status"
                [gTip]="st.tip"
                ><g-status-icon [tone]="st.tone" [kind]="st.label" />{{
                  st.label
                }}<span class="sr-only">: {{ st.tip }}</span></span
              >
              @if (st.live && canStop()) {
                <button
                  type="button"
                  class="btn-text"
                  data-testid="stop"
                  gTip="Stop the run working on your last message; the conversation stays"
                  [disabled]="store.isBusy('stop')"
                  (click)="store.stopTurn()"
                >
                  Stop
                </button>
              }
              @if (canEditCaps()) {
                <button
                  type="button"
                  class="btn-text"
                  data-testid="session-settings-toggle"
                  [attr.aria-expanded]="settingsOpen()"
                  (click)="toggleSettingsPanel()"
                >
                  {{
                    settingsOpen()
                      ? "Close settings"
                      : apiGrants() > 0
                        ? "Grants & settings (" + apiGrants() + " API)"
                        : "Grants & settings"
                  }}
                </button>
              }
              @if (row.session.id !== director && canDelete()) {
                <button
                  #deleteBtn
                  type="button"
                  class="btn-text danger"
                  data-testid="delete-session"
                  (click)="confirmDelete.set(true)"
                >
                  Delete
                </button>
              }
            </div>

            @if (activity(); as a) {
              <p class="chat-activity" data-testid="chat-activity">
                {{ a }}
              </p>
            }

            <!-- Live Run Stale Grants/Model Banner ("Apply now") -->
            @if (store.needsApplyNow()) {
              <div
                class="apply-now-banner"
                role="status"
                data-testid="apply-now-banner"
              >
                <span
                  >Saved. This turn is already running with the previous
                  grants/model — restart the turn to apply them right now.</span
                >
                @if (canStop()) {
                  <button
                    type="button"
                    class="btn-primary"
                    data-testid="apply-now-btn"
                    [disabled]="store.isBusy('stop')"
                    (click)="applyNow()"
                  >
                    Apply now (restart turn)
                  </button>
                }
              </div>
            }

            <div
              class="cap-toggles"
              role="group"
              aria-labelledby="caps-label"
              data-testid="cap-toggles"
            >
              <span id="caps-label" class="cap-label">Capabilities</span>
              @for (c of capRows(); track c.id) {
                @let held = holds(c.id);
                <button
                  type="button"
                  class="cap-toggle"
                  [attr.aria-pressed]="held"
                  [attr.data-testid]="'cap-' + c.id"
                  [disabled]="!canEditCaps() || store.isBusy(c.id)"
                  [gTip]="
                    (held
                      ? 'Granted. Select to remove'
                      : 'Not granted. Select to grant') +
                    '; applies from the next run.' +
                    (c.description ? ' ' + c.description : '')
                  "
                  (click)="store.toggleCapability(c.id)"
                >
                  {{ c.name || c.id }}
                </button>
              }
              @for (g of grants(); track g) {
                <span class="cap-held" data-testid="cap-held">{{ g }}</span>
              }
              @if (apiGrants() > 0 && !settingsOpen()) {
                <button
                  type="button"
                  class="btn-text"
                  data-testid="cap-api-manage"
                  (click)="toggleSettingsPanel()"
                >
                  {{ apiGrants() }} API grant{{ apiGrants() === 1 ? "" : "s" }}
                </button>
              }
              @if (
                row.session.missingShippedDefaults?.length && canEditCaps()
              ) {
                <button
                  type="button"
                  class="btn-tonal"
                  data-testid="adopt-shipped-defaults"
                  [disabled]="store.isBusy('settings')"
                  (click)="adoptDefaults()"
                >
                  Adopt shipped defaults
                </button>
              }
              <span class="muted cap-note"
                >Changes apply from the next run.</span
              >
            </div>

            <!-- Inline Session Settings & Per-Method / API-Scope Grants Drawer -->
            @if (settingsOpen() && canEditCaps()) {
              <div
                class="session-settings-drawer"
                data-testid="session-settings-panel"
              >
                <div class="session-settings-grid">
                  <div class="session-setting-field">
                    <span>Model</span>
                    <g-pill-select
                      [options]="modelOptions()"
                      [value]="row.session.model || ''"
                      ariaLabel="Session model"
                      align="left"
                      testid="session-model"
                      [disabled]="store.isBusy('settings')"
                      (valueChange)="saveModel($event)"
                    />
                  </div>
                  <div class="session-setting-field">
                    <span>Network access</span>
                    <g-pill-select
                      [options]="networkOptions()"
                      [value]="row.session.networkAccess || ''"
                      ariaLabel="Session network access"
                      align="left"
                      testid="session-network"
                      [disabled]="store.isBusy('settings')"
                      (valueChange)="saveNetwork($event)"
                    />
                  </div>
                  <label class="check-row session-setting-field">
                    <input
                      type="checkbox"
                      data-testid="session-keep-running"
                      [checked]="row.session.keepRunning"
                      [disabled]="
                        store.isBusy('settings') || !access.isFullAccess()
                      "
                      (change)="
                        store.updateSettings({
                          keepRunning: $any($event.target).checked,
                        })
                      "
                    />
                    <span>Keep running (prevent sleep)</span>
                  </label>
                  <label class="check-row session-setting-field">
                    <input
                      type="checkbox"
                      data-testid="session-tasks-approved"
                      [checked]="row.session.tasksApproved"
                      [disabled]="store.isBusy('settings')"
                      (change)="
                        store.updateSettings({
                          tasksApproved: $any($event.target).checked,
                        })
                      "
                    />
                    <span>Tasks approved (auto-approve all tasks)</span>
                  </label>
                  <label class="check-row session-setting-field">
                    <input
                      type="checkbox"
                      data-testid="session-reads-all"
                      [checked]="row.session.readsAll"
                      [disabled]="store.isBusy('settings')"
                      (change)="
                        store.updateSettings({
                          readsAll: $any($event.target).checked,
                        })
                      "
                    />
                    <span>Reads all (read access to all repos)</span>
                  </label>
                  <label class="session-setting-field">
                    <span>Visible to team / members</span>
                    <div style="display: flex; gap: 6px">
                      <input
                        type="text"
                        data-testid="session-team-input"
                        placeholder="default (everyone allowed)"
                        [value]="teamDraft()"
                        (input)="teamDraft.set(inputValue($event))"
                      />
                      <button
                        type="button"
                        class="btn-tonal"
                        data-testid="session-team-save"
                        [disabled]="store.isBusy('settings')"
                        (click)="saveTeam()"
                      >
                        Save
                      </button>
                    </div>
                  </label>
                </div>

                <div>
                  <span
                    class="cap-label"
                    style="display: block; margin-bottom: 6px"
                    >Method &amp; API scope grants</span
                  >
                  <g-grants-picker
                    testid="session-grants"
                    [offered]="store.offered()"
                    [held]="row.session.capabilities ?? []"
                    [disabled]="store.isBusy('grants')"
                    (heldChange)="onSessionGrantsChange($event)"
                  />
                </div>
              </div>
            }

            @if (showFresh()) {
              <div class="crowded-note" data-testid="fresh-chat">
                @if (row.session.crowded) {
                  <span>{{
                    row.session.crowdedNote ||
                      "This conversation is getting long."
                  }}</span>
                }
                @if (!freshOpen()) {
                  <button
                    type="button"
                    class="btn-text"
                    data-testid="fresh-chat-open"
                    gTip="Close this conversation and start a new one on the same session"
                    (click)="freshOpen.set(true)"
                  >
                    Start a fresh chat
                  </button>
                } @else {
                  <span
                    >Closes this conversation and starts a new one with the same
                    grants. The messages stay on the closed task.</span
                  >
                  <button
                    type="button"
                    class="btn-tonal"
                    data-testid="fresh-chat-handover"
                    [disabled]="store.isBusy('restart')"
                    (click)="restart(true)"
                  >
                    Carry the handover note
                  </button>
                  <button
                    type="button"
                    class="btn-outlined"
                    data-testid="fresh-chat-clean"
                    [disabled]="store.isBusy('restart')"
                    (click)="restart(false)"
                  >
                    Start clean
                  </button>
                  <button
                    type="button"
                    class="btn-text"
                    (click)="freshOpen.set(false)"
                  >
                    Cancel
                  </button>
                }
              </div>
            }

            @if (!newOpen() && store.actionError(); as err) {
              <p
                class="banner banner-error"
                role="alert"
                data-testid="action-error"
              >
                {{ err }}
                <button
                  type="button"
                  class="btn-text"
                  (click)="store.actionError.set(null)"
                >
                  Dismiss
                </button>
              </p>
            }

            <div
              #log
              class="chat-log"
              role="log"
              aria-label="Conversation"
              tabindex="0"
              data-testid="chat-log"
            >
              @for (c of comments(); track c.id) {
                <article
                  class="msg"
                  [class.is-human]="human(c)"
                  [class.is-handover]="c.handover"
                  [attr.data-testid]="'msg-' + c.id"
                >
                  <span class="msg-meta">
                    <span class="msg-author"
                      ><g-actor-chip
                        [label]="actorLabel(c)"
                        [kind]="c.authorKind"
                        [behalf]="c.onBehalfOfLabel || c.onBehalfOf"
                    /></span>
                    @if (c.handover) {
                      <span class="status-pill neutral">Handover note</span>
                    }
                    <time class="mono" [attr.datetime]="c.createdAt">{{
                      time(c.createdAt)
                    }}</time>
                  </span>
                  <div class="msg-body" [gMarkdown]="c.body"></div>
                  @if (c.attachments?.length && row.session.taskId) {
                    <g-attachment-list
                      [taskId]="row.session.taskId"
                      [attachments]="c.attachments"
                    />
                  }
                </article>
              } @empty {
                @if (store.detailError(); as err) {
                  <p class="banner banner-error" role="alert">{{ err }}</p>
                } @else if (store.selectedDetail()) {
                  <p class="empty-state">No messages yet. Say hello.</p>
                } @else {
                  <p class="empty-state">Loading the conversation…</p>
                }
              }
            </div>

            @if (canWrite() && row.session.taskId) {
              <form
                class="chat-composer"
                (submit)="send($event)"
                (dragover)="$event.preventDefault()"
                (drop)="onComposerDrop($event)"
              >
                <textarea
                  #composer
                  class="reply-textarea"
                  rows="2"
                  data-testid="composer"
                  [attr.aria-label]="'Message ' + row.session.title"
                  [placeholder]="'Message ' + row.session.title"
                  [value]="draft()"
                  (input)="onDraft($event)"
                  (paste)="onComposerPaste($event)"
                  (keydown.enter)="onEnter($event)"
                ></textarea>
                <g-attachment-picker
                  testid="session-attach"
                  [staged]="stagedAttachments()"
                  [disabled]="store.isBusy('send')"
                  (stagedChange)="onStagedChange($event)"
                />
                <div class="composer-actions">
                  <span class="muted composer-hint"
                    >Enter sends · Shift+Enter adds a line · paste or drop
                    files</span
                  >
                  <button
                    type="submit"
                    class="btn-primary"
                    data-testid="send"
                    gTip="Send; each message starts a run with this session's grants"
                    [disabled]="
                      (!draft().trim() && stagedAttachments().length === 0) ||
                      store.isBusy('send')
                    "
                  >
                    Send
                  </button>
                </div>
              </form>
            }
          } @else if (store.loaded()) {
            <p class="empty-state">
              That session does not exist.
              <a routerLink="/sessions">Back to sessions</a>
            </p>
          }
        </section>
      </div>
    </section>

    @if (newOpen()) {
      <div class="scrim" aria-hidden="true"></div>
      <div
        class="g-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="new-session-title"
        data-testid="new-session-dialog"
      >
        <form (submit)="create($event)">
          <h2 id="new-session-title">New session</h2>
          <p class="muted">
            A standing conversation with its own interactive agent. It starts
            with the deployment's default grants; customize team visibility,
            model, or grants below.
          </p>
          <label class="field">
            <span>Name</span>
            <input
              #newTitle
              type="text"
              data-testid="new-session-name"
              [value]="newName()"
              (input)="newName.set(inputValue($event))"
              placeholder="For example: Release help"
            />
          </label>
          <label class="field">
            <span>Visible to team (optional)</span>
            <input
              type="text"
              data-testid="new-session-team"
              [value]="newTeam()"
              (input)="newTeam.set(inputValue($event))"
              placeholder="Default team"
            />
          </label>
          <div class="field">
            <span>Model</span>
            <g-pill-select
              [options]="modelOptions()"
              [value]="newModel()"
              ariaLabel="New session model"
              align="left"
              testid="new-session-model"
              (valueChange)="newModel.set($event)"
            />
          </div>
          <label class="field">
            <span>First message (optional)</span>
            <textarea
              rows="4"
              data-testid="new-session-message"
              [value]="newMessage()"
              (input)="newMessage.set(inputValue($event))"
            ></textarea>
          </label>
          <g-attachment-picker
            testid="new-session-attach"
            [staged]="newAttachments()"
            [disabled]="store.isBusy('create')"
            (stagedChange)="newAttachments.set($event)"
          />
          @if (store.actionError(); as err) {
            <p
              class="banner banner-error"
              role="alert"
              data-testid="new-session-error"
            >
              {{ err }}
            </p>
          }
          <div class="dialog-actions">
            <button type="button" class="btn-text" (click)="closeNew()">
              Cancel
            </button>
            <button
              type="submit"
              class="btn-primary"
              data-testid="new-session-create"
              [disabled]="
                store.isBusy('create') ||
                (!newName().trim() &&
                  !newMessage().trim() &&
                  newAttachments().length === 0)
              "
            >
              Start session
            </button>
          </div>
        </form>
      </div>
    }

    @if (confirmDelete()) {
      <div class="scrim" aria-hidden="true"></div>
      <div
        class="g-dialog"
        role="alertdialog"
        aria-modal="true"
        aria-labelledby="delete-title"
        aria-describedby="delete-desc"
        data-testid="delete-dialog"
      >
        <h2 id="delete-title">
          Delete {{ store.selected()?.session?.title }}?
        </h2>
        <p id="delete-desc">
          The session goes away and its task is closed. Its messages stay on the
          closed task.
        </p>
        <div class="dialog-actions">
          <button
            #deleteCancel
            type="button"
            class="btn-text"
            (click)="cancelDelete()"
          >
            Cancel
          </button>
          <button
            type="button"
            class="btn-primary danger"
            data-testid="delete-confirm"
            [disabled]="store.isBusy('delete')"
            (click)="remove()"
          >
            Delete
          </button>
        </div>
      </div>
    }
  `,
})
export class SessionsPageComponent implements OnInit, OnDestroy {
  protected readonly store = inject(SessionsStore);
  protected readonly access = inject(AccessService);
  private readonly router = inject(Router);
  private readonly snackbar = inject(SnackbarService);
  private readonly doc = inject(DOCUMENT);

  readonly id = input<string>();
  readonly new = input<string>();

  protected readonly director = DIRECTOR_ID;
  protected readonly freshOpen = signal(false);
  protected readonly settingsOpen = signal(false);
  protected readonly teamDraft = signal("");
  protected readonly newOpen = signal(false);
  protected readonly newName = signal("");
  protected readonly newTeam = signal("");
  protected readonly newModel = signal("");
  protected readonly newMessage = signal("");
  protected readonly newAttachments = signal<AttachmentUpload[]>([]);
  protected readonly confirmDelete = signal(false);

  private readonly log = viewChild<ElementRef<HTMLElement>>("log");
  private readonly newTitle =
    viewChild<ElementRef<HTMLInputElement>>("newTitle");
  private readonly newBtn = viewChild<ElementRef<HTMLButtonElement>>("newBtn");
  private readonly deleteCancel =
    viewChild<ElementRef<HTMLButtonElement>>("deleteCancel");
  private readonly deleteBtn =
    viewChild<ElementRef<HTMLButtonElement>>("deleteBtn");
  private readonly chatTitle = viewChild<ElementRef<HTMLElement>>("chatTitle");
  private readonly composer =
    viewChild<ElementRef<HTMLTextAreaElement>>("composer");

  protected readonly canWrite = computed(() =>
    this.access.can("POST /api/tasks/{id}/comments"),
  );
  protected readonly canStop = computed(() =>
    this.access.can("POST /api/console-sessions/{id}/cancel"),
  );
  protected readonly canRestart = computed(() =>
    this.access.can("POST /api/console-sessions/{id}/restart"),
  );
  protected readonly canEditCaps = computed(() =>
    this.access.can("PUT /api/console-sessions/{id}"),
  );
  protected readonly canDelete = computed(() =>
    this.access.can("DELETE /api/console-sessions/{id}"),
  );
  protected readonly canCreate = computed(() =>
    this.access.can("POST /api/console-sessions"),
  );
  protected readonly comments = computed<Comment[]>(
    () => this.store.selectedDetail()?.comments ?? [],
  );
  protected readonly draft = computed(() =>
    this.store.draftFor(this.store.selectedId()),
  );
  protected readonly stagedAttachments = computed(() =>
    this.store.attachmentsFor(this.store.selectedId()),
  );
  protected readonly capRows = computed(() =>
    toggleRows(
      this.store.offered(),
      this.store.selected()?.session.capabilities,
    ),
  );
  protected readonly grants = computed(() =>
    otherGrants(this.store.selected()?.session, this.capRows()),
  );
  protected readonly apiGrants = computed(() =>
    apiGrantCount(this.store.selected()?.session),
  );
  protected readonly modelOptions = computed<PillOption[]>(() => {
    const models = this.store.models();
    const cur = this.store.selected()?.session.model;
    const all = cur && !models.includes(cur) ? [cur, ...models] : models;
    const def = this.store.defaultModel();
    return [
      {
        value: "",
        label: def ? `Deployment default (${def})` : "Deployment default",
      },
      ...all.map((m) => ({ value: m, label: m })),
    ];
  });
  protected readonly networkOptions = computed<PillOption[]>(() => [
    { value: "", label: "Deployment default" },
    ...this.store.networkOptions().map((v) => ({
      value: v,
      label: v === "open" ? "Open" : v === "none" ? "None" : v,
    })),
  ]);
  protected readonly activity = computed(() => {
    const t = this.store.selected()?.task;
    return t && (t.state === "running" || t.state === "queued")
      ? (t.activity ?? "")
      : "";
  });
  protected readonly showFresh = computed(() => {
    const t = this.store.selected()?.task;
    return this.canRestart() && !!t && t.state !== "closed";
  });

  private lastCount = 0;
  private lastSession = "";

  constructor() {
    effect(() => {
      const id = this.id() || DIRECTOR_ID;
      untracked(() => {
        this.store.select(id);
        this.freshOpen.set(false);
        this.settingsOpen.set(false);
        this.confirmDelete.set(false);
      });
    });
    effect(() => {
      const team = this.store.selected()?.session.team ?? "";
      untracked(() => this.teamDraft.set(team));
    });
    effect(() => {
      if (!this.new()) return;
      const allowed = this.canCreate();
      untracked(() => {
        if (allowed) this.openNew();
        void this.router.navigate([], {
          queryParams: { new: null },
          queryParamsHandling: "merge",
          replaceUrl: true,
        });
      });
    });
    effect(() => {
      const n = this.comments().length;
      const sid = this.store.selectedId();
      const el = this.log()?.nativeElement;
      if (!el) return;
      if (sid !== this.lastSession) {
        this.lastSession = sid;
        this.lastCount = 0;
      }
      const grew = n > this.lastCount;
      const first = this.lastCount === 0;
      this.lastCount = n;
      const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 120;
      if (grew && (first || nearBottom)) {
        setTimeout(() => (el.scrollTop = el.scrollHeight));
      }
    });
  }

  ngOnInit(): void {
    this.store.start();
  }

  ngOnDestroy(): void {
    this.store.stop();
  }

  protected statusOf = sessionStatus;
  protected human = isHuman;
  protected time = (iso?: string) => clock(iso);

  protected actorLabel(c: Comment): string {
    if (c.authorKind === "automation" && c.author === "console-session") {
      return "Grain";
    }
    return c.authorLabel || c.author || "Unknown";
  }

  protected holds(capId: string): boolean {
    return (this.store.selected()?.session.capabilities ?? []).includes(capId);
  }

  protected inputValue(ev: Event): string {
    return (ev.target as HTMLInputElement | HTMLTextAreaElement).value;
  }

  protected toggleSettingsPanel(): void {
    const next = !this.settingsOpen();
    this.settingsOpen.set(next);
    if (next) {
      this.teamDraft.set(this.store.selected()?.session.team ?? "");
    }
  }

  protected async saveModel(model: string): Promise<void> {
    if (await this.store.updateSettings({ model })) {
      this.snackbar.show(
        model ? `Model set to ${model}` : "Using default model",
      );
    }
  }

  protected async saveNetwork(networkAccess: string): Promise<void> {
    if (await this.store.updateSettings({ networkAccess })) {
      this.snackbar.show("Updated network access");
    }
  }

  protected async saveTeam(): Promise<void> {
    const team = this.teamDraft().trim();
    if (await this.store.updateSettings({ team })) {
      this.snackbar.show(
        team ? `Visible to team ${team}` : "Updated team visibility",
      );
    }
  }

  protected async adoptDefaults(): Promise<void> {
    if (await this.store.updateSettings({ adoptShippedDefaults: true })) {
      this.snackbar.show("Adopted shipped default capabilities");
    }
  }

  protected async onSessionGrantsChange(next: string[]): Promise<void> {
    await this.store.setCapabilities(next);
  }

  protected async applyNow(): Promise<void> {
    if (await this.store.applyNow()) {
      this.snackbar.show(
        "Stopped active turn; next message runs with new settings",
      );
    }
  }

  protected onDraft(ev: Event): void {
    this.store.setDraft(this.store.selectedId(), this.inputValue(ev));
  }

  protected onStagedChange(list: AttachmentUpload[]): void {
    this.store.setAttachments(this.store.selectedId(), list);
  }

  protected async onComposerPaste(ev: ClipboardEvent): Promise<void> {
    const files = filesFromTransfer(ev.clipboardData);
    if (!files.length) return;
    ev.preventDefault();
    const sid = this.store.selectedId();
    const cur = this.store.attachmentsFor(sid);
    const { uploads, error } = await readFilesAsUploads(files, cur.length);
    if (error) {
      this.store.actionError.set(error);
      return;
    }
    if (uploads.length) this.store.setAttachments(sid, [...cur, ...uploads]);
  }

  protected async onComposerDrop(ev: DragEvent): Promise<void> {
    ev.preventDefault();
    const files = filesFromTransfer(ev.dataTransfer);
    if (!files.length) return;
    const sid = this.store.selectedId();
    const cur = this.store.attachmentsFor(sid);
    const { uploads, error } = await readFilesAsUploads(files, cur.length);
    if (error) {
      this.store.actionError.set(error);
      return;
    }
    if (uploads.length) this.store.setAttachments(sid, [...cur, ...uploads]);
  }

  protected onEnter(ev: Event): void {
    const k = ev as KeyboardEvent;
    if (k.shiftKey || k.isComposing) return;
    ev.preventDefault();
    void this.store.send();
  }

  protected send(ev: Event): void {
    ev.preventDefault();
    void this.store.send();
  }

  protected async restart(carry: boolean): Promise<void> {
    if (await this.store.restart(carry)) {
      this.freshOpen.set(false);
      this.snackbar.show(
        carry
          ? "Started a fresh chat with the handover note"
          : "Started a fresh chat",
      );
    }
  }

  protected openNew(): void {
    this.newName.set("");
    this.newTeam.set("");
    this.newModel.set("");
    this.newMessage.set("");
    this.newAttachments.set([]);
    this.newOpen.set(true);
    setTimeout(() => this.newTitle()?.nativeElement.focus());
  }

  protected closeNew(): void {
    this.newOpen.set(false);
    setTimeout(() => this.newBtn()?.nativeElement.focus());
  }

  protected async create(ev: Event): Promise<void> {
    ev.preventDefault();
    const id = await this.store.create({
      title: this.newName(),
      message: this.newMessage(),
      team: this.newTeam(),
      model: this.newModel(),
      attachments: this.newAttachments(),
    });
    if (id) {
      this.newOpen.set(false);
      await this.router.navigate(["/sessions", id]);
      this.snackbar.show("Session started");
      setTimeout(() =>
        (this.composer() ?? this.chatTitle())?.nativeElement.focus(),
      );
    }
  }

  protected async remove(): Promise<void> {
    const row = this.store.selected();
    if (!row) return;
    if (await this.store.remove(row.session.id)) {
      this.confirmDelete.set(false);
      this.snackbar.show(`Deleted ${row.session.title}`);
      await this.router.navigate(["/sessions"]);
      setTimeout(() => this.chatTitle()?.nativeElement.focus());
    }
  }

  protected cancelDelete(): void {
    this.confirmDelete.set(false);
    setTimeout(() => this.deleteBtn()?.nativeElement.focus());
  }

  protected onEscape(ev: Event): void {
    if (ev.defaultPrevented) return;
    const outer = this.doc.querySelector('[aria-modal="true"]');
    if (outer && !outer.closest("g-sessions-page")) return;
    if (this.newOpen()) {
      ev.preventDefault();
      this.closeNew();
    } else if (this.confirmDelete()) {
      ev.preventDefault();
      this.cancelDelete();
    } else if (this.freshOpen()) {
      ev.preventDefault();
      this.freshOpen.set(false);
    } else if (this.settingsOpen()) {
      ev.preventDefault();
      this.settingsOpen.set(false);
    }
  }

  private readonly focusDelete = effect(() => {
    if (this.confirmDelete()) {
      setTimeout(() => this.deleteCancel()?.nativeElement.focus());
    }
  });

  protected onTab(ev: KeyboardEvent): void {
    const dialog = this.doc.querySelector<HTMLElement>(
      'g-sessions-page [aria-modal="true"]',
    );
    if (!dialog) return;
    const items = Array.from(
      dialog.querySelectorAll<HTMLElement>(
        'button:not([disabled]), input, textarea, a[href], [tabindex]:not([tabindex="-1"])',
      ),
    );
    if (items.length === 0) return;
    const first = items[0];
    const last = items[items.length - 1];
    const active = this.doc.activeElement;
    if (ev.shiftKey && (active === first || !dialog.contains(active))) {
      ev.preventDefault();
      last.focus();
    } else if (!ev.shiftKey && (active === last || !dialog.contains(active))) {
      ev.preventDefault();
      first.focus();
    }
  }
}
