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
  output,
  signal,
} from "@angular/core";
import type { AttachmentUpload } from "../api/models";
import { AccessService } from "../services/access.service";
import { DIRECTOR_ID } from "../sessions/session-helpers";
import { SessionsStore } from "../sessions/sessions.store";
import { ConversationComponent } from "../shared/conversation/conversation.component";
import { StatusIconComponent } from "../shared/status-icon.component";
import {
  type TaskShapeFields,
  buildShapingPrompt,
  diffDraftFields,
  extractGrainDrafts,
} from "./director-draft";

// Coplanar "Shape with Director" pane embedded inside Create Task and Edit
// Task so operators iterate on scope with Director and apply structured diffs
// right back into the form without navigating away.
@Component({
  selector: "g-director-shaper",
  imports: [ConversationComponent, StatusIconComponent],
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./director-shaper.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <section
      class="shaper-pane"
      aria-label="Shape task with Director"
      data-testid="director-shaper-pane"
    >
      <header class="shaper-head">
        <div class="shaper-head-meta">
          <h3>Shape with Director</h3>
          @let st = sessions.directorLiveStatus();
          <span [class]="'status-pill ' + st.tone">
            <g-status-icon [tone]="st.tone" [kind]="st.label" />{{ st.label }}
          </span>
        </div>
        <button
          type="button"
          class="btn-text"
          data-testid="shaper-close"
          (click)="closed.emit()"
        >
          Hide
        </button>
      </header>

      @if (proposedDraft(); as prop) {
        <div class="shaper-diff-card" data-testid="shaper-diff-card">
          <div class="shaper-diff-title">Proposed field changes</div>
          @if (diffs().length > 0) {
            <div class="shaper-diff-list">
              @for (d of diffs(); track d.field) {
                <div class="shaper-diff-row">
                  <span class="shaper-diff-label">{{ d.label }}</span>
                  <span class="shaper-diff-values">
                    <span class="shaper-diff-before">{{ d.before }}</span>
                    <span>{{ d.after }}</span>
                  </span>
                </div>
              }
            </div>
          } @else {
            <p class="muted" style="margin: 0; font-size: 12px">
              Form already matches Director's latest proposal.
            </p>
          }
          <div class="shaper-diff-actions">
            <button
              type="button"
              class="btn-outlined"
              data-testid="shaper-apply"
              (click)="applyDraft.emit(prop)"
            >
              Apply to form
            </button>
            <button
              type="button"
              class="btn-primary"
              data-testid="shaper-apply-save"
              (click)="applyAndSubmit.emit(prop)"
            >
              {{ mode() === "edit" ? "Apply & save" : "Apply & create" }}
            </button>
          </div>
        </div>
      }

      <div class="shaper-convo">
        @if (sessions.actionError(); as err) {
          <p class="banner banner-error" role="alert">{{ err }}</p>
        }
        @if (taskId(); as tid) {
          <g-conversation
            mode="expanded"
            data-testid="shaper-conversation"
            [taskId]="tid"
            [repo]="detail()?.repo"
            [comments]="comments()"
            [transcript]="sessions.directorTranscript()"
            [draft]="prompt()"
            (draftChange)="prompt.set($event)"
            [stagedAttachments]="staged()"
            (stagedAttachmentsChange)="staged.set($event)"
            [config]="{
              placeholder:
                'Ask Director to tighten scope, suggest acceptance criteria, or pick repo/priority…',
              submitLabel: 'Ask Director to shape',
              disabled: !canWrite() || sessions.isBusy('send'),
            }"
            (send)="onSend($event)"
          />
        } @else if (sessions.loaded()) {
          <p class="muted" style="font-size: 13px">
            No Director session is active in this deployment yet.
          </p>
        } @else {
          <p class="muted" style="font-size: 13px">Loading Director…</p>
        }
      </div>
    </section>
  `,
})
export class DirectorShaperComponent implements OnInit, OnDestroy {
  protected readonly sessions = inject(SessionsStore);
  private readonly access = inject(AccessService);

  readonly mode = input<"create" | "edit">("create");
  readonly current = input.required<TaskShapeFields>();
  readonly shapeTaskId = input<string | undefined>(undefined);
  readonly taskState = input<string | undefined>(undefined);
  readonly failureSummary = input<string | undefined>(undefined);

  readonly applyDraft = output<Partial<TaskShapeFields>>();
  readonly applyAndSubmit = output<Partial<TaskShapeFields>>();
  readonly closed = output<void>();

  protected readonly prompt = signal("");
  protected readonly staged = signal<AttachmentUpload[]>([]);

  protected readonly detail = computed(() => this.sessions.directorDetail());
  protected readonly taskId = computed(
    () => this.sessions.directorRow()?.session.taskId ?? "",
  );
  protected readonly comments = computed(() => this.detail()?.comments ?? []);

  protected readonly proposedDraft = computed(() => {
    const list = this.comments();
    for (let i = list.length - 1; i >= 0; i--) {
      const c = list[i];
      if (c.authorKind === "human") continue;
      const drafts = extractGrainDrafts(c.body ?? "");
      if (drafts.length > 0) return drafts[drafts.length - 1];
    }
    return null;
  });

  protected readonly diffs = computed(() => {
    const prop = this.proposedDraft();
    return prop ? diffDraftFields(this.current(), prop) : [];
  });

  protected readonly canWrite = computed(() => {
    const repo = this.detail()?.repo;
    return repo
      ? this.access.can("POST /api/tasks/{id}/comments", repo)
      : this.access.canGlobalScope("tasks:write");
  });

  ngOnInit(): void {
    this.sessions.setDirectorWatch("shaper", true);
  }

  ngOnDestroy(): void {
    this.sessions.setDirectorWatch("shaper", false);
  }

  protected async onSend(ev: {
    body: string;
    attachments: AttachmentUpload[];
  }): Promise<void> {
    const shaped = buildShapingPrompt(this.mode(), this.current(), ev.body, {
      taskId: this.shapeTaskId(),
      state: this.taskState(),
      failureSummary: this.failureSummary(),
    });
    const ok = await this.sessions.sendTo(DIRECTOR_ID, shaped, ev.attachments);
    if (ok) {
      this.prompt.set("");
      this.staged.set([]);
    }
  }
}
