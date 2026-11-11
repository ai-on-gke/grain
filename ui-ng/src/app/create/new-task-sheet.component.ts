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
  computed,
  effect,
  inject,
  signal,
  untracked,
  viewChild,
} from "@angular/core";
import { NgTemplateOutlet } from "@angular/common";
import { Router } from "@angular/router";
import { GrainApi } from "../api/grain-api.service";
import type {
  ConsoleSessionList,
  GrainConfig,
  Task,
  TaskDetail,
} from "../api/models";
import { InboxStore } from "../inbox/inbox.store";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { DIRECTOR_ID, errorText } from "../sessions/session-view";
import { CLASSIC_NEW_TASK } from "../shell/nav";
import { CreateTaskService } from "./create-task.service";
import {
  type FieldId,
  type NewTaskDraft,
  buildPayload,
  directorMessage,
  emptyDraft,
  NO_REPO,
  type Problem,
  hasHistory,
  lastBaseForRepo,
  resolveFields,
  targetableRepos,
  validate,
} from "./new-task-form";

// NewTaskSheetComponent is the New task sheet (spec D-23): a 480px side
// sheet over a scrim on desktop and tablet, full screen on a phone, with
// focus kept inside and returned to the opener. It files the task, or
// hands the draft to the Director to file in conversation.
@Component({
  selector: "g-new-task-sheet",
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    "(document:keydown.escape)": "onEscape($event)",
    "(document:keydown)": "onKey($event)",
    "(document:keydown.tab)": "onTab($event)",
    "(document:keydown.shift.tab)": "onTab($event)",
  },
  template: `
    @if (svc.isOpen()) {
      <div class="scrim" aria-hidden="true" (click)="close()"></div>
      <div
        #sheet
        class="side-sheet"
        role="dialog"
        aria-modal="true"
        aria-labelledby="new-task-title"
        data-testid="new-task-sheet"
      >
        <form class="sheet-form" (submit)="submit($event)" novalidate>
          <div class="sheet-head">
            <div class="sheet-head-row">
              <span class="status-pill neutral">{{
                draft().approved ? "Starts on create" : "Proposal"
              }}</span>
              <button
                type="button"
                class="icon-btn"
                aria-label="Close New task"
                data-testid="new-task-close"
                (click)="close()"
              >
                ×
              </button>
            </div>
            <h2 id="new-task-title" class="sheet-title">New task</h2>
          </div>

          <div class="sheet-body">
            @if (!canCreate()) {
              <p class="banner" data-testid="new-task-denied">
                {{ deniedReason() }}
              </p>
            }
            @if (error(); as err) {
              <p
                id="new-task-error"
                class="banner banner-error"
                role="alert"
                data-testid="new-task-error"
              >
                {{ err.message }}
              </p>
            }
            @for (id of shown(); track id) {
              <ng-container
                [ngTemplateOutlet]="fieldTpl"
                [ngTemplateOutletContext]="{ $implicit: id }"
              />
            }
            @if (more().length) {
              <button
                type="button"
                class="btn-text more-toggle"
                data-testid="more-options"
                [attr.aria-expanded]="moreOpen()"
                aria-controls="more-fields"
                (click)="moreOpen.set(!moreOpen())"
              >
                {{ moreOpen() ? "Fewer options" : "More options" }}
              </button>
              @if (moreOpen()) {
                <div id="more-fields" class="more-fields">
                  @for (id of more(); track id) {
                    <ng-container
                      [ngTemplateOutlet]="fieldTpl"
                      [ngTemplateOutletContext]="{ $implicit: id }"
                    />
                  }
                </div>
              }
            }
            <p class="muted sheet-note">
              Attachments, capabilities, reads and writes, templates and sandbox
              shape are on the
              <a [href]="classicNew" data-testid="classic-form"
                >full form in the classic UI</a
              >.
            </p>
          </div>

          <div class="sheet-foot">
            <div class="sheet-switches">
              @if (fields().approved.visibility !== "hidden") {
                <div class="switch-row">
                  <button
                    type="button"
                    role="switch"
                    class="g-switch"
                    data-testid="approved-switch"
                    aria-labelledby="sw-approved"
                    [attr.aria-checked]="draft().approved"
                    (click)="set('approved', !draft().approved)"
                  >
                    <span class="sr-only">{{
                      fields().approved.label || "Approve and start now"
                    }}</span>
                  </button>
                  <span id="sw-approved">{{
                    fields().approved.label || "Approve and start now"
                  }}</span>
                </div>
              }
              @if (fields().atFront.visibility !== "hidden") {
                <div class="switch-row">
                  <button
                    type="button"
                    role="switch"
                    class="g-switch"
                    data-testid="front-switch"
                    aria-labelledby="sw-front"
                    [attr.aria-checked]="draft().atFront"
                    (click)="set('atFront', !draft().atFront)"
                  >
                    <span class="sr-only">{{
                      fields().atFront.label || "Put at front of queue"
                    }}</span>
                  </button>
                  <span id="sw-front">{{
                    fields().atFront.label || "Put at front of queue"
                  }}</span>
                </div>
              }
            </div>
            <div class="sheet-actions">
              <button type="button" class="btn-text" (click)="close()">
                Cancel
              </button>
              @if (canAskDirector()) {
                <button
                  type="button"
                  class="btn-outlined"
                  data-testid="ask-director"
                  [disabled]="busy()"
                  (click)="askDirector()"
                >
                  Ask the Director
                </button>
              }
              <button
                type="submit"
                class="btn-primary"
                data-testid="new-task-submit"
                [disabled]="busy() || !canCreate()"
              >
                {{ draft().approved ? "Create and start" : "Save as proposal" }}
              </button>
            </div>
          </div>
        </form>
      </div>
    }

    <ng-template #fieldTpl let-id>
      @let f = fieldOf(id);
      @switch (id) {
        @case ("title") {
          <label class="field">
            <span
              >{{ f.label || "Title" }} <span aria-hidden="true">*</span></span
            >
            <input
              #titleInput
              type="text"
              required
              aria-required="true"
              data-testid="nt-title"
              [attr.aria-invalid]="error()?.field === 'title' ? 'true' : null"
              [attr.aria-describedby]="
                error()?.field === 'title' ? 'new-task-error' : null
              "
              [value]="draft().title"
              (input)="set('title', val($event))"
            />
            @if (f.helpText) {
              <small class="muted">{{ f.helpText }}</small>
            }
          </label>
        }
        @case ("description") {
          <label class="field">
            <span>{{ f.label || "Description" }}</span>
            <textarea
              rows="6"
              data-testid="nt-description"
              placeholder="What should be done, and how will we know it is done?"
              [value]="draft().description"
              (input)="set('description', val($event))"
            ></textarea>
          </label>
        }
        @case ("repo") {
          <label class="field">
            <span>{{ f.label || "Repository" }}</span>
            <select
              data-testid="nt-repo"
              [value]="draft().repo"
              (change)="pickRepo(val($event))"
            >
              @if (!defaultTarget()) {
                <option value="" [selected]="draft().repo === ''">
                  Deployment default target
                </option>
              }
              @for (r of repos(); track r) {
                <option [value]="r" [selected]="r === draft().repo">
                  {{ r }}
                </option>
              }
              @if (f.allowNoRepo !== false) {
                <option [value]="noRepo" [selected]="draft().repo === noRepo">
                  No repository (answer in a comment)
                </option>
              }
            </select>
          </label>
        }
        @case ("base") {
          @if (draft().repo !== noRepo) {
            <label class="field">
              <span>{{
                draft().newBranch ? "Create it from" : f.label || "Base branch"
              }}</span>
              <input
                type="text"
                data-testid="nt-base"
                placeholder="The repository's default branch"
                [value]="draft().base"
                (input)="set('base', val($event))"
              />
            </label>
          }
        }
        @case ("newBranch") {
          @if (draft().repo !== noRepo) {
            <label class="check-row">
              <input
                type="checkbox"
                data-testid="nt-new-branch"
                [checked]="draft().newBranch"
                (change)="set('newBranch', checked($event))"
              />
              <span>{{ f.label || "Create a new branch for this work" }}</span>
            </label>
            @if (draft().newBranch) {
              <label class="field">
                <span>New branch name</span>
                <input
                  type="text"
                  #branchInput
                  data-testid="nt-new-branch-name"
                  [attr.aria-invalid]="
                    error()?.field === 'newBranchName' ? 'true' : null
                  "
                  [attr.aria-describedby]="
                    error()?.field === 'newBranchName' ? 'new-task-error' : null
                  "
                  [value]="draft().newBranchName"
                  (input)="set('newBranchName', val($event))"
                />
              </label>
            }
          }
        }
        @case ("autoMerge") {
          @if (draft().repo !== noRepo) {
            <label class="check-row">
              <input
                type="checkbox"
                data-testid="nt-auto-merge"
                [checked]="draft().autoMerge"
                (change)="set('autoMerge', checked($event))"
              />
              <span>{{
                f.label || "Merge on its own once CI and review pass"
              }}</span>
            </label>
          }
        }
        @case ("model") {
          <label class="field">
            <span>{{ f.label || "Model" }}</span>
            <select data-testid="nt-model" (change)="set('model', val($event))">
              <option value="" [selected]="draft().model === ''">
                Deployment default{{
                  defaultModel() ? " (" + defaultModel() + ")" : ""
                }}
              </option>
              @for (m of offeredOr(f.offered, models()); track m) {
                <option [value]="m" [selected]="m === draft().model">
                  {{ m }}
                </option>
              }
            </select>
          </label>
        }
        @case ("dependsOn") {
          <div class="field">
            <label for="nt-depends">{{ f.label || "Depends on" }}</label>
            @if (draft().dependsOn.length) {
              <ul class="chip-list" aria-label="Depends on">
                @for (id of draft().dependsOn; track id) {
                  <li class="dep-chip">
                    #{{ id }} {{ titleOf(id) }}
                    <button
                      type="button"
                      class="chip-remove"
                      [attr.aria-label]="'Remove dependency on #' + id"
                      (click)="removeDep(id)"
                    >
                      ×
                    </button>
                  </li>
                }
              </ul>
            }
            <select
              id="nt-depends"
              data-testid="nt-depends"
              (change)="addDep($event)"
            >
              <option value="" selected>Add a task this waits for…</option>
              @for (t of dependable(); track t.id) {
                <option [value]="t.id">#{{ t.id }} {{ t.title }}</option>
              }
            </select>
          </div>
        }
        @case ("networkAccess") {
          @if (!shapeIgnored()) {
            <label class="field">
              <span>{{ f.label || "Network access" }}</span>
              <select
                data-testid="nt-network"
                (change)="set('networkAccess', val($event))"
              >
                <option value="" [selected]="draft().networkAccess === ''">
                  Deployment default
                </option>
                @for (v of offeredOr(f.offered, networkValues); track v) {
                  <option [value]="v" [selected]="draft().networkAccess === v">
                    {{ v === "open" ? "Open" : v === "none" ? "None" : v }}
                  </option>
                }
              </select>
            </label>
          }
        }
        @case ("nothingNeededAction") {
          <label class="field">
            <span>{{ f.label || "If the agent finds nothing to do" }}</span>
            <select
              data-testid="nt-nothing"
              (change)="set('nothingNeededAction', val($event))"
            >
              <option
                value="close"
                [selected]="draft().nothingNeededAction === 'close'"
              >
                Close the task
              </option>
              <option
                value="ack"
                [selected]="draft().nothingNeededAction === 'ack'"
              >
                Ask me to acknowledge it
              </option>
            </select>
          </label>
        }
        @case ("promptExtension") {
          <label class="field">
            <span>{{ f.label || "Extra instructions for the agent" }}</span>
            <textarea
              rows="3"
              data-testid="nt-extra"
              [value]="draft().promptExtension"
              (input)="set('promptExtension', val($event))"
            ></textarea>
          </label>
        }
      }
    </ng-template>
  `,
  imports: [NgTemplateOutlet],
})
export class NewTaskSheetComponent {
  protected readonly svc = inject(CreateTaskService);
  private readonly api = inject(GrainApi);
  private readonly inbox = inject(InboxStore);
  private readonly access = inject(AccessService);
  private readonly snackbar = inject(SnackbarService);
  private readonly router = inject(Router);

  protected readonly classicNew = CLASSIC_NEW_TASK;
  protected readonly noRepo = NO_REPO;
  protected readonly networkValues = ["open", "none"];
  protected readonly config = signal<GrainConfig | null>(null);
  protected readonly draft = signal<NewTaskDraft>(emptyDraft(null, []));
  protected readonly moreOpen = signal(false);
  protected readonly busy = signal(false);
  protected readonly error = signal<Problem | null>(null);
  // edited is every field the person has changed since the sheet opened;
  // a late config read or a repo change re-seeds only the others.
  private readonly edited = new Set<keyof NewTaskDraft>();
  private generation = 0;

  private readonly sheet = viewChild<ElementRef<HTMLElement>>("sheet");
  private readonly titleInput =
    viewChild<ElementRef<HTMLInputElement>>("titleInput");
  private readonly branchInput =
    viewChild<ElementRef<HTMLInputElement>>("branchInput");

  // Live tasks a new one can wait for; standing session tasks are not
  // work anybody depends on.
  private readonly liveTasks = computed(() =>
    this.inbox.tasks().filter((t) => !t.consoleSession && !t.interactive),
  );
  protected readonly repos = computed(() =>
    targetableRepos(this.config(), this.inbox.tasks()),
  );
  protected readonly defaultTarget = computed(
    () => (this.config()?.["defaultTarget"] as string | undefined) ?? "",
  );
  protected readonly fields = computed(() =>
    resolveFields(this.config(), this.draft().repo),
  );
  protected readonly shown = computed(() => this.byVisibility("shown"));
  protected readonly more = computed(() => this.byVisibility("more"));
  protected readonly models = computed(
    () => (this.config()?.["models"] as string[] | undefined) ?? [],
  );
  protected readonly defaultModel = computed(
    () => (this.config()?.["defaultModel"] as string | undefined) ?? "",
  );
  protected readonly shapeIgnored = computed(
    () => !!this.config()?.["sandboxShapeIgnored"],
  );
  protected readonly dependable = computed(() => {
    const picked = new Set(this.draft().dependsOn);
    return this.liveTasks().filter((t) => !picked.has(t.id));
  });
  // Repo-scoped: a role may file on some repositories only.
  private readonly repoForAccess = computed(() => {
    const r = this.draft().repo;
    return r && r !== NO_REPO ? r : undefined;
  });
  protected readonly canCreate = computed(() =>
    this.access.can("POST /api/tasks", this.repoForAccess()),
  );
  protected readonly deniedReason = computed(() =>
    this.access.deniedReason("POST /api/tasks", this.repoForAccess()),
  );
  protected readonly canAskDirector = computed(() =>
    this.access.can("POST /api/tasks/{id}/comments"),
  );

  constructor() {
    // Each opening starts from a fresh draft with the opener's prefill,
    // reads the deployment's form settings, and puts focus on the title.
    effect(() => {
      if (!this.svc.isOpen()) return;
      const prefill = this.svc.prefill();
      untracked(() => {
        this.error.set(null);
        this.moreOpen.set(false);
        this.edited.clear();
        // A draft left by Cancel or Escape comes back when the sheet is
        // opened plainly again; an opener's prefill starts afresh.
        const saved = Object.keys(prefill).length ? null : this.svc.saved();
        if (saved) {
          this.draft.set(saved.draft);
          saved.edited.forEach((k) => this.edited.add(k));
        } else {
          this.draft.set(
            emptyDraft(this.config(), this.inbox.tasks(), prefill),
          );
          Object.keys(prefill).forEach((k) =>
            this.edited.add(k as keyof NewTaskDraft),
          );
        }
        setTimeout(() => this.titleInput()?.nativeElement.focus());
        void this.loadConfig(prefill);
      });
    });
  }

  private async loadConfig(prefill: Partial<NewTaskDraft>): Promise<void> {
    const gen = ++this.generation;
    try {
      const cfg = await this.api.config();
      // A later opening (or a closed sheet) owns the draft now.
      if (gen !== this.generation || !this.svc.isOpen()) return;
      this.config.set(cfg);
      this.reseed(emptyDraft(cfg, this.inbox.tasks(), prefill));
    } catch {
      // The builtin defaults stand; the server still applies its own.
    }
  }

  // reseed takes fresh defaults for every field nobody has changed.
  private reseed(fresh: NewTaskDraft): void {
    this.draft.update((d) => {
      const next = { ...fresh };
      for (const k of this.edited) {
        (next as Record<string, unknown>)[k] = d[k];
      }
      return next;
    });
  }

  protected offeredOr(offered: string[] | undefined, all: string[]): string[] {
    return offered?.length ? offered : all;
  }

  private byVisibility(v: "shown" | "more"): FieldId[] {
    const f = this.fields();
    return (
      (Object.keys(f) as FieldId[])
        // The two switches live in the footer, not the field list.
        .filter(
          (id) =>
            id !== "approved" && id !== "atFront" && f[id].visibility === v,
        )
        .sort((a, b) => f[a].order - f[b].order)
    );
  }

  protected fieldOf(id: FieldId) {
    return this.fields()[id];
  }

  protected set<K extends keyof NewTaskDraft>(k: K, v: NewTaskDraft[K]): void {
    this.edited.add(k);
    this.draft.update((d) => ({ ...d, [k]: v }));
  }

  protected val(ev: Event): string {
    return (ev.target as HTMLInputElement).value;
  }

  protected checked(ev: Event): boolean {
    return (ev.target as HTMLInputElement).checked;
  }

  // pickRepo re-applies that repository's form defaults to untouched
  // fields, and suggests its last base only when the base was not typed
  // and the repository has history to suggest from (NewTaskOverlay.jsx).
  protected pickRepo(repo: string): void {
    const tasks = this.inbox.tasks();
    const baseEdited = this.edited.has("base");
    this.edited.add("repo");
    this.reseed(emptyDraft(this.config(), tasks, { repo }));
    this.draft.update((d) => ({
      ...d,
      repo,
      base:
        !baseEdited && hasHistory(tasks, repo)
          ? lastBaseForRepo(tasks, repo)
          : d.base,
      newBranch: repo === NO_REPO ? false : d.newBranch,
    }));
  }

  protected titleOf(id: string): string {
    return this.inbox.tasks().find((t) => t.id === id)?.title ?? "";
  }

  protected addDep(ev: Event): void {
    const sel = ev.target as HTMLSelectElement;
    const id = sel.value;
    sel.value = "";
    if (!id) return;
    this.edited.add("dependsOn");
    this.draft.update((d) =>
      d.dependsOn.includes(id) ? d : { ...d, dependsOn: [...d.dependsOn, id] },
    );
  }

  protected removeDep(id: string): void {
    this.draft.update((d) => ({
      ...d,
      dependsOn: d.dependsOn.filter((x) => x !== id),
    }));
  }

  // Ctrl/Cmd+Enter files from anywhere in the open sheet.
  protected onKey(ev: KeyboardEvent): void {
    if (!this.svc.isOpen() || ev.key !== "Enter" || ev.isComposing) return;
    if (!(ev.ctrlKey || ev.metaKey)) return;
    const sheet = this.sheet()?.nativeElement;
    if (sheet && !sheet.contains(document.activeElement)) return;
    ev.preventDefault();
    void this.file();
  }

  protected submit(ev: Event): void {
    ev.preventDefault();
    void this.file();
  }

  private async file(): Promise<void> {
    if (this.busy()) return;
    const d = this.draft();
    const problem = validate(d, this.fields());
    if (problem) {
      this.error.set(problem);
      (problem.field === "title"
        ? this.titleInput()
        : this.branchInput()
      )?.nativeElement.focus();
      return;
    }
    this.busy.set(true);
    this.error.set(null);
    try {
      const created = await this.api.post<Task>(
        "POST /api/tasks",
        undefined,
        buildPayload(d, this.config()),
      );
      this.svc.clearSaved();
      this.svc.close();
      void this.inbox.refresh();
      this.announce(created, d.approved);
    } catch (err) {
      this.error.set({
        field: "title",
        message: `The task was not filed: ${errorText(err)}`,
      });
    } finally {
      this.busy.set(false);
    }
  }

  // announce confirms the filing. A proposal can be discarded from the
  // snackbar (closing it is all that discarding is); a started task
  // cannot be taken back that way -- it may already be running, and a
  // requested branch is already being cut -- so it only offers Open.
  private announce(created: Task, approved: boolean): void {
    const id = created?.id;
    if (!id) {
      this.snackbar.show(approved ? "Task filed" : "Proposal saved");
      return;
    }
    const open = {
      label: "Open",
      run: () => {
        void this.router.navigate(["/task", id]);
      },
    };
    if (approved) {
      this.snackbar.show(`Filed #${id}; it starts when a slot is free`, open);
      return;
    }
    this.snackbar.show(
      `Saved #${id} as a proposal in the Inbox`,
      {
        label: "Discard",
        run: async () => {
          await this.api.post(
            "POST /api/tasks/{id}/close",
            { id },
            { close_pull_request: false },
          );
          void this.inbox.refresh();
          this.snackbar.show(`Discarded proposal #${id}`);
        },
      },
      undefined,
      open,
    );
  }

  // askDirector hands the draft to the Director as a message, so it can
  // ask what is missing and file the task itself, and opens that
  // conversation.
  protected async askDirector(): Promise<void> {
    const d = this.draft();
    if (!d.title.trim() && !d.description.trim()) {
      this.error.set({
        field: "title",
        message: "Say what you need first: a title or a description.",
      });
      this.titleInput()?.nativeElement.focus();
      return;
    }
    this.busy.set(true);
    this.error.set(null);
    try {
      const list = await this.api.get<ConsoleSessionList>(
        "GET /api/console-sessions",
      );
      const director = list.sessions?.find((s) => s.id === DIRECTOR_ID);
      if (!director?.taskId) {
        throw new Error("this deployment has no Director session");
      }
      await this.api.post<TaskDetail>(
        "POST /api/tasks/{id}/comments",
        { id: director.taskId },
        { body: directorMessage(d), attachments: [] },
      );
      this.svc.clearSaved();
      // Navigate first: the opener may not survive it, so focus goes to
      // the conversation instead of back.
      await this.router.navigate(["/sessions", DIRECTOR_ID]);
      this.svc.close(false);
      this.snackbar.show("Sent to the Director");
    } catch (err) {
      this.error.set({
        field: "title",
        message: `The Director did not get it: ${errorText(err)}`,
      });
    } finally {
      this.busy.set(false);
    }
  }

  // close keeps the draft for the next plain opening, so Cancel, Escape
  // or a stray click on the scrim loses nothing.
  protected close(): void {
    const d = this.draft();
    if (d.title.trim() || d.description.trim()) {
      this.svc.keep(d, [...this.edited]);
    }
    this.svc.close();
  }

  protected onEscape(ev: Event): void {
    if (!this.svc.isOpen() || ev.defaultPrevented) return;
    ev.preventDefault();
    this.close();
  }

  // Tab stays inside the sheet while it is open.
  protected onTab(ev: KeyboardEvent): void {
    const sheet = this.sheet()?.nativeElement;
    if (!this.svc.isOpen() || !sheet) return;
    const items = Array.from(
      sheet.querySelectorAll<HTMLElement>(
        'button:not([disabled]), input:not([disabled]), textarea:not([disabled]), select:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])',
      ),
    ).filter((el) => el.offsetParent !== null || el === document.activeElement);
    if (items.length === 0) return;
    const first = items[0];
    const last = items[items.length - 1];
    const active = document.activeElement;
    if (ev.shiftKey && (active === first || !sheet.contains(active))) {
      ev.preventDefault();
      last.focus();
    } else if (!ev.shiftKey && (active === last || !sheet.contains(active))) {
      ev.preventDefault();
      first.focus();
    }
  }
}
