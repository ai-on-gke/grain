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

import { DOCUMENT, NgTemplateOutlet } from "@angular/common";
import {
  ChangeDetectionStrategy,
  Component,
  ElementRef,
  NgZone,
  OnDestroy,
  OnInit,
  ViewEncapsulation,
  computed,
  effect,
  inject,
  signal,
  viewChild,
} from "@angular/core";
import { RouterLink } from "@angular/router";
import type { AttachmentUpload } from "../api/models";
import { CreateTaskService } from "../create/create-task.service";
import { AccessService } from "../services/access.service";
import { DIRECTOR_ID } from "../sessions/session-view";
import { SessionsStore } from "../sessions/sessions.store";
import { ConversationComponent } from "../shared/conversation/conversation.component";
import { DialogTrapDirective } from "../shared/dialog-trap.directive";
import { StatusIconComponent } from "../shared/status-icon.component";
import { TipDirective } from "../shared/tip.directive";
import { DirectorContextService } from "./director-context.service";
import { extractGrainDrafts } from "./director-draft";

@Component({
  selector: "g-director-drawer",
  imports: [
    NgTemplateOutlet,
    RouterLink,
    ConversationComponent,
    StatusIconComponent,
    DialogTrapDirective,
    TipDirective,
  ],
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./director-drawer.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (isCompact()) {
      <div
        class="drawer-scrim"
        aria-hidden="true"
        (click)="directorCtx.close(true)"
      ></div>
      <aside
        class="director-drawer is-overlay"
        role="complementary"
        aria-label="Director"
        aria-modal="true"
        data-testid="director-drawer"
        gDialogTrap
        (gDialogClose)="directorCtx.close(true)"
      >
        <ng-container *ngTemplateOutlet="drawerInner" />
      </aside>
    } @else {
      <aside
        class="director-drawer"
        [class.is-wide]="wide()"
        role="complementary"
        aria-label="Director"
        data-testid="director-drawer"
      >
        <ng-container *ngTemplateOutlet="drawerInner" />
      </aside>
    }

    <ng-template #drawerInner>
      <header class="drawer-head">
        <div class="drawer-title-group">
          <h2 class="drawer-title" tabindex="-1" #titleEl>Director</h2>
          @let st = sessions.directorLiveStatus();
          <span [class]="'status-pill ' + st.tone" [gTip]="st.tip">
            <g-status-icon [tone]="st.tone" [kind]="st.label" />{{ st.label }}
          </span>
        </div>
        <div class="drawer-head-actions">
          @if (sessions.directorLiveStatus().live && canStop()) {
            <button
              type="button"
              class="btn-text"
              data-testid="director-drawer-stop"
              [disabled]="sessions.isBusy('stop')"
              (click)="sessions.stopTurnFor(directorId)"
            >
              Stop
            </button>
          }
          <button
            type="button"
            class="btn-text drawer-wide-btn"
            data-testid="director-drawer-wide"
            [gTip]="wide() ? 'Narrow drawer' : 'Widen drawer'"
            (click)="wide.set(!wide())"
          >
            {{ wide() ? "Compact" : "Wide" }}
          </button>
          <a
            class="btn-text"
            [routerLink]="['/sessions', directorId]"
            data-testid="director-drawer-full"
            gTip="Open Director in Sessions"
            (click)="directorCtx.close(false)"
          >
            Open full
          </a>
          <button
            type="button"
            class="icon-btn"
            aria-label="Close Director drawer"
            data-testid="director-drawer-close"
            gTip="Close Director (Esc)"
            (click)="directorCtx.close(true)"
          >
            ✕
          </button>
        </div>
      </header>

      @if (directorCtx.contextLine(); as ctxLine) {
        <div class="drawer-ctx-bar">
          <button
            type="button"
            class="drawer-ctx-pill"
            [class.is-active]="directorCtx.includeContext()"
            [attr.aria-pressed]="directorCtx.includeContext()"
            data-testid="director-context-toggle"
            gTip="Include current view context with your next Director message"
            (click)="
              directorCtx.includeContext.set(!directorCtx.includeContext())
            "
          >
            <span class="drawer-ctx-text">Context: {{ ctxLine }}</span>
            <span aria-hidden="true">{{
              directorCtx.includeContext() ? "✓" : "+"
            }}</span>
          </button>
        </div>
      }

      <div class="drawer-body">
        @if (sessions.actionError(); as err) {
          <p class="banner banner-error" role="alert">{{ err }}</p>
        }

        @if (latestDraft(); as draft) {
          <div
            class="drawer-draft-callout"
            data-testid="director-draft-callout"
          >
            <span class="drawer-draft-label"
              ><strong>Task draft:</strong>
              {{ draft.title || "Refined task" }}</span
            >
            <button
              type="button"
              class="btn-outlined"
              data-testid="director-open-draft-task"
              (click)="openDraftInNewTask(draft)"
            >
              Open in New task
            </button>
          </div>
        }

        @if (taskId(); as tid) {
          <g-conversation
            mode="expanded"
            data-testid="director-drawer-conversation"
            [taskId]="tid"
            [repo]="detail()?.repo"
            [comments]="comments()"
            [transcript]="sessions.directorTranscript()"
            [draft]="draft()"
            (draftChange)="draft.set($event)"
            [stagedAttachments]="staged()"
            (stagedAttachmentsChange)="staged.set($event)"
            [config]="{
              placeholder:
                directorCtx.includeContext() && directorCtx.contextLine()
                  ? 'Ask Director about this view…'
                  : 'Message Director…',
              submitLabel: 'Send',
              disabled: !canWrite() || sessions.isBusy('send'),
            }"
            (send)="onSend($event)"
          />
        } @else if (sessions.loaded()) {
          <p class="drawer-empty">
            No Director session is active in this deployment yet.
          </p>
        } @else {
          <p class="drawer-empty">Loading Director…</p>
        }
      </div>
    </ng-template>
  `,
})
export class DirectorDrawerComponent implements OnInit, OnDestroy {
  protected readonly sessions = inject(SessionsStore);
  protected readonly directorCtx = inject(DirectorContextService);
  private readonly access = inject(AccessService);
  private readonly createTask = inject(CreateTaskService);
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);

  protected readonly directorId = DIRECTOR_ID;
  protected readonly wide = signal(false);
  protected readonly isCompact = signal(false);
  protected readonly draft = signal("");
  protected readonly staged = signal<AttachmentUpload[]>([]);

  private readonly titleEl = viewChild<ElementRef<HTMLElement>>("titleEl");
  private mql: MediaQueryList | null = null;
  private readonly onMqlChange = (ev: MediaQueryListEvent): void => {
    this.zone.run(() => this.isCompact.set(ev.matches));
  };

  protected readonly detail = computed(() => this.sessions.directorDetail());
  protected readonly taskId = computed(
    () => this.sessions.directorRow()?.session.taskId ?? "",
  );
  protected readonly comments = computed(() => this.detail()?.comments ?? []);

  protected readonly latestDraft = computed(() => {
    const list = this.comments();
    for (let i = list.length - 1; i >= 0; i--) {
      const c = list[i];
      if (c.authorKind === "human") continue;
      const drafts = extractGrainDrafts(c.body ?? "");
      if (drafts.length > 0) return drafts[drafts.length - 1];
    }
    return null;
  });

  protected readonly canWrite = computed(() =>
    this.access.can(
      "POST /api/tasks/{id}/comments",
      this.detail()?.repo || undefined,
    ),
  );
  protected readonly canStop = computed(() =>
    this.access.can("POST /api/console-sessions/{id}/cancel"),
  );

  constructor() {
    effect(() => {
      if (this.directorCtx.isOpen()) {
        queueMicrotask(() => this.titleEl()?.nativeElement.focus());
      }
    });
  }

  ngOnInit(): void {
    this.sessions.setDirectorWatch("drawer", true);
    const win = this.doc.defaultView;
    if (win && typeof win.matchMedia === "function") {
      this.mql = win.matchMedia("(max-width: 1079px)");
      this.isCompact.set(this.mql.matches);
      this.mql.addEventListener?.("change", this.onMqlChange);
    }
  }

  ngOnDestroy(): void {
    this.sessions.setDirectorWatch("drawer", false);
    this.mql?.removeEventListener?.("change", this.onMqlChange);
  }

  protected async onSend(ev: {
    body: string;
    attachments: AttachmentUpload[];
  }): Promise<void> {
    const formatted = this.directorCtx.formatMessageWithContext(ev.body);
    if (!formatted && ev.attachments.length === 0) return;
    const ok = await this.sessions.sendTo(
      DIRECTOR_ID,
      formatted,
      ev.attachments,
    );
    if (ok) {
      this.draft.set("");
      this.staged.set([]);
    }
  }

  protected openDraftInNewTask(d: {
    title?: string;
    description?: string;
    repo?: string;
    base?: string;
    model?: string;
    dependsOn?: string[];
  }): void {
    this.createTask.open({
      title: d.title,
      description: d.description,
      repo: d.repo,
      base: d.base,
      model: d.model,
      dependsOn: d.dependsOn,
    });
  }
}
