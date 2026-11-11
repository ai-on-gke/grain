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
  type OnDestroy,
  type OnInit,
  computed,
  effect,
  inject,
  input,
  signal,
  untracked,
  viewChild,
} from "@angular/core";
import { DOCUMENT } from "@angular/common";
import { Router, RouterLink } from "@angular/router";
import type { Comment } from "../api/models";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { TipDirective } from "../shared/tip.directive";
import {
  DIRECTOR_ID,
  apiGrantCount,
  authorOf,
  isHuman,
  otherGrants,
  renderMarkdown,
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

// SessionsPageComponent is Sessions (spec D-8): the standing
// conversations on the left, the selected one on the right with its
// capability toggles above the messages and the composer below. On a
// phone the list and the conversation are separate screens.
@Component({
  selector: "g-sessions-page",
  imports: [RouterLink, TipDirective],
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    "(document:keydown.escape)": "onEscape($event)",
    "(document:keydown.tab)": "onTab($event)",
    "(document:keydown.shift.tab)": "onTab($event)",
  },
  template: `
    <section class="view-panel is-active" aria-labelledby="sessions-headline">
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
                  [attr.aria-current]="
                    store.selectedId() === r.session.id ? 'page' : null
                  "
                  [attr.data-testid]="'session-' + r.session.id"
                >
                  <span class="session-item-top">
                    <span class="s-name">{{ r.session.title }}</span>
                    <span [class]="'status-pill ' + st.tone">{{
                      st.label
                    }}</span>
                  </span>
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
              <h2 class="chat-title" id="chat-title">
                {{ row.session.title }}
              </h2>
              <span
                [class]="'status-pill ' + st.tone"
                data-testid="chat-status"
                [gTip]="st.tip"
                tabindex="0"
                >{{ st.label }}</span
              >
              @if (st.live && canWrite()) {
                <button
                  type="button"
                  class="btn-text"
                  data-testid="stop"
                  gTip="Stop the run working on your last message; the conversation stays"
                  [disabled]="store.busy() === 'stop'"
                  (click)="store.stopTurn()"
                >
                  Stop
                </button>
              }
              <a
                class="btn-text"
                [href]="classicHref()"
                gTip="Open this session in the classic UI (attachments, grants by method, settings)"
                >Classic</a
              >
              @if (row.session.id !== director && canWrite()) {
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
                  [disabled]="!canWrite() || store.busy() === c.id"
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
              @if (apiGrants() > 0) {
                <a class="cap-api-link" [href]="classicHref()"
                  >{{ apiGrants() }} API grant{{
                    apiGrants() === 1 ? "" : "s"
                  }}
                  · manage in classic</a
                >
              }
              <span class="muted cap-note"
                >Changes apply from the next run.</span
              >
            </div>

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
                    [disabled]="store.busy() === 'restart'"
                    (click)="restart(true)"
                  >
                    Carry the handover note
                  </button>
                  <button
                    type="button"
                    class="btn-outlined"
                    data-testid="fresh-chat-clean"
                    [disabled]="store.busy() === 'restart'"
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

            @if (store.actionError(); as err) {
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
                    <span class="msg-author">{{ author(c) }}</span>
                    @if (c.handover) {
                      <span class="status-pill neutral">Handover note</span>
                    }
                    <time class="mono" [attr.datetime]="c.createdAt">{{
                      time(c.createdAt)
                    }}</time>
                  </span>
                  <div class="msg-body" [innerHTML]="html(c.body)"></div>
                </article>
              } @empty {
                @if (store.selectedDetail()) {
                  <p class="empty-state">No messages yet. Say hello.</p>
                } @else {
                  <p class="empty-state">Loading the conversation…</p>
                }
              }
            </div>

            @if (canWrite() && row.session.taskId) {
              <form class="chat-composer" (submit)="send($event)">
                <textarea
                  class="reply-textarea"
                  rows="2"
                  data-testid="composer"
                  [attr.aria-label]="'Message ' + row.session.title"
                  [placeholder]="'Message ' + row.session.title"
                  [value]="draft()"
                  (input)="onDraft($event)"
                  (keydown.enter)="onEnter($event)"
                ></textarea>
                <div class="composer-actions">
                  <span class="muted composer-hint"
                    >Enter sends · Shift+Enter adds a line</span
                  >
                  <button
                    type="submit"
                    class="btn-primary"
                    data-testid="send"
                    gTip="Send; each message starts a run with this session's grants"
                    [disabled]="!draft().trim() || store.busy() === 'send'"
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
            with the deployment's default grants; change them from the
            conversation.
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
            <span>First message (optional)</span>
            <textarea
              rows="4"
              data-testid="new-session-message"
              [value]="newMessage()"
              (input)="newMessage.set(inputValue($event))"
            ></textarea>
          </label>
          <div class="dialog-actions">
            <button type="button" class="btn-text" (click)="closeNew()">
              Cancel
            </button>
            <button
              type="submit"
              class="btn-primary"
              data-testid="new-session-create"
              [disabled]="store.busy() === 'create'"
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
            (click)="confirmDelete.set(false)"
          >
            Cancel
          </button>
          <button
            type="button"
            class="btn-primary danger"
            data-testid="delete-confirm"
            [disabled]="store.busy() === 'delete'"
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
  private readonly access = inject(AccessService);
  private readonly router = inject(Router);
  private readonly snackbar = inject(SnackbarService);
  private readonly doc = inject(DOCUMENT);

  // The :id route parameter (withComponentInputBinding); none on
  // /sessions, which shows the Director on a wide screen.
  readonly id = input<string>();

  protected readonly director = DIRECTOR_ID;
  protected readonly freshOpen = signal(false);
  protected readonly newOpen = signal(false);
  protected readonly newName = signal("");
  protected readonly newMessage = signal("");
  protected readonly confirmDelete = signal(false);

  private readonly log = viewChild<ElementRef<HTMLElement>>("log");
  private readonly newTitle =
    viewChild<ElementRef<HTMLInputElement>>("newTitle");
  private readonly newBtn = viewChild<ElementRef<HTMLButtonElement>>("newBtn");
  private readonly deleteCancel =
    viewChild<ElementRef<HTMLButtonElement>>("deleteCancel");
  private readonly deleteBtn =
    viewChild<ElementRef<HTMLButtonElement>>("deleteBtn");

  protected readonly canWrite = computed(() =>
    this.access.can("POST /api/tasks/{id}/comments"),
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
  // The activity line: what the run is doing, or why it is waiting.
  protected readonly activity = computed(() => {
    const t = this.store.selected()?.task;
    return t && (t.state === "running" || t.state === "queued")
      ? (t.activity ?? "")
      : "";
  });
  // "Start a fresh chat" is offered whenever the conversation is open,
  // as the classic page does; the spec's crowded-only gate waits on the
  // server reporting crowding reliably.
  protected readonly showFresh = computed(() => {
    const t = this.store.selected()?.task;
    return this.canWrite() && !!t && t.state !== "closed";
  });
  protected readonly classicHref = computed(() => {
    const id = this.store.selectedId();
    return id === DIRECTOR_ID ? "/director" : `/sessions/${id}`;
  });

  private lastCount = 0;

  constructor() {
    // Follows the route only: select() reads the store's own selection,
    // which must not become a dependency that undoes other selections.
    effect(() => {
      const id = this.id() || DIRECTOR_ID;
      untracked(() => {
        this.store.select(id);
        this.freshOpen.set(false);
        this.confirmDelete.set(false);
      });
    });
    // Keep the newest message in view when one arrives, unless the
    // reader has scrolled up to read something older.
    effect(() => {
      const n = this.comments().length;
      const el = this.log()?.nativeElement;
      if (!el) return;
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
  protected author = authorOf;
  protected human = isHuman;
  protected time = (iso?: string) => clock(iso);

  private readonly htmlCache = new Map<string, string>();
  protected html(body: string): string {
    let h = this.htmlCache.get(body);
    if (h === undefined) {
      h = renderMarkdown(body);
      this.htmlCache.set(body, h);
    }
    return h;
  }

  protected holds(capId: string): boolean {
    return (this.store.selected()?.session.capabilities ?? []).includes(capId);
  }

  protected inputValue(ev: Event): string {
    return (ev.target as HTMLInputElement | HTMLTextAreaElement).value;
  }

  protected onDraft(ev: Event): void {
    this.store.setDraft(this.store.selectedId(), this.inputValue(ev));
  }

  // Enter sends; Shift+Enter (and Enter while an IME is composing) adds
  // a line as usual.
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
    this.newMessage.set("");
    this.newOpen.set(true);
    setTimeout(() => this.newTitle()?.nativeElement.focus());
  }

  protected closeNew(): void {
    this.newOpen.set(false);
    setTimeout(() => this.newBtn()?.nativeElement.focus());
  }

  protected async create(ev: Event): Promise<void> {
    ev.preventDefault();
    const id = await this.store.create(this.newName(), this.newMessage());
    if (id) {
      this.newOpen.set(false);
      void this.router.navigate(["/sessions", id]);
      this.snackbar.show("Session started");
    }
  }

  protected async remove(): Promise<void> {
    const row = this.store.selected();
    if (!row) return;
    if (await this.store.remove(row.session.id)) {
      this.confirmDelete.set(false);
      this.snackbar.show(`Deleted ${row.session.title}`);
      void this.router.navigate(["/sessions"]);
    }
  }

  protected onEscape(ev: Event): void {
    if (this.newOpen()) {
      ev.preventDefault();
      this.closeNew();
    } else if (this.confirmDelete()) {
      ev.preventDefault();
      this.confirmDelete.set(false);
      setTimeout(() => this.deleteBtn()?.nativeElement.focus());
    } else if (this.freshOpen()) {
      ev.preventDefault();
      this.freshOpen.set(false);
    }
  }

  // The delete dialog opens with focus on Cancel, the safe choice.
  private readonly focusDelete = effect(() => {
    if (this.confirmDelete()) {
      setTimeout(() => this.deleteCancel()?.nativeElement.focus());
    }
  });

  // A modal keeps Tab inside itself.
  protected onTab(ev: KeyboardEvent): void {
    const dialog = this.doc.querySelector<HTMLElement>(
      'g-sessions-page [aria-modal="true"]',
    );
    if (!dialog) return;
    const items = Array.from(
      dialog.querySelectorAll<HTMLElement>(
        "button:not([disabled]), input, textarea, a[href]",
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
