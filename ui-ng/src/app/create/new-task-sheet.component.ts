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

import { NgTemplateOutlet } from "@angular/common";
import {
  ChangeDetectionStrategy,
  Component,
  ElementRef,
  ViewEncapsulation,
  computed,
  effect,
  inject,
  signal,
  untracked,
  viewChild,
} from "@angular/core";
import { Router } from "@angular/router";
import { GrainApi, splitRepo } from "../api/grain-api.service";
import type {
  AttachmentUpload,
  Composition,
  ConsoleSessionList,
  CustomFieldDeclaration,
  GrainConfig,
  Task,
  TaskDetail,
  Template,
} from "../api/models";
import { InboxStore } from "../inbox/inbox.store";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { DIRECTOR_ID, errorText } from "../sessions/session-helpers";
import { DirectorShaperComponent } from "../director/director-shaper.component";
import type { TaskShapeFields } from "../director/director-draft";
import {
  AttachmentPickerComponent,
  filesFromTransfer,
  readFilesAsUploads,
} from "../shared/attachments";
import { GrantsPickerComponent } from "../shared/grants-picker.component";
import { hasExplicitTargetAllowlist } from "../shared/instance-repos";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import { WorkstreamsStore } from "../workstreams/workstreams.store";
import { CreateTaskService } from "./create-task.service";
import {
  type FieldId,
  NO_REPO,
  type NewTaskDraft,
  type Problem,
  buildPayload,
  defaultCapabilitiesForRepo,
  directorMessage,
  emptyDraft,
  fromTemplateStem,
  hasHistory,
  lastBaseForRepo,
  resolveFields,
  targetableRepos,
  validate,
} from "./new-task-form";

const SANDBOX_TIERS = [
  { id: "default", label: "Default", cpus: 0, mem: 0, disk: 0 },
  { id: "small", label: "Small (2 CPU · 4 GB)", cpus: 2, mem: 4096, disk: 16 },
  {
    id: "medium",
    label: "Medium (4 CPU · 8 GB)",
    cpus: 4,
    mem: 8192,
    disk: 32,
  },
  {
    id: "large",
    label: "Large (8 CPU · 16 GB)",
    cpus: 8,
    mem: 16384,
    disk: 64,
  },
] as const;

const FRAMEWORKS: readonly PillOption[] = [
  { value: "", label: "Deployment default" },
  { value: "antigravity", label: "Antigravity" },
  { value: "claude", label: "Claude" },
  { value: "codex", label: "Codex" },
];

// NewTaskSheetComponent is the New task side sheet (spec D-23) with full
// 22-field parity, template launcher + composition preview, file attachments,
// capabilities/grants, sandbox shape, zero <select>s, and zero Classic links.
@Component({
  selector: "g-new-task-sheet",
  imports: [
    NgTemplateOutlet,
    PillSelectComponent,
    AttachmentPickerComponent,
    GrantsPickerComponent,
    DirectorShaperComponent,
  ],
  encapsulation: ViewEncapsulation.None,
  styleUrls: ["./new-task-sheet.component.css"],
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
        [class.has-shaper]="shapingOpen()"
        role="dialog"
        aria-modal="true"
        aria-labelledby="new-task-title"
        data-testid="new-task-sheet"
      >
        <div class="sheet-split">
          <form
            class="sheet-form"
            (submit)="submit($event)"
            (dragover)="$event.preventDefault()"
            (drop)="onSheetDrop($event)"
            (paste)="onSheetPaste($event)"
            novalidate
          >
            <div class="sheet-head">
              <div class="sheet-head-row">
                <span class="status-pill neutral">{{
                  draft().workstream
                    ? "Workstream"
                    : selectedTemplate()
                      ? "From template"
                      : draft().approved
                        ? "Starts on create"
                        : "Proposal"
                }}</span>
                <button
                  type="button"
                  class="icon-btn"
                  [attr.aria-label]="
                    draft().workstream
                      ? 'Close Start a workstream'
                      : 'Close New task'
                  "
                  data-testid="new-task-close"
                  (click)="close()"
                >
                  ×
                </button>
              </div>
              <h2 id="new-task-title" class="sheet-title">
                {{ draft().workstream ? "Start a workstream" : "New task" }}
              </h2>
            </div>

            <div class="sheet-body">
              @if (draft().workstream) {
                <p class="banner banner-info" data-testid="ws-start-banner">
                  State the goal and what done looks like. Its first run plans
                  the work and proposes tasks into one stream.
                </p>
              } @else if (draft().origin) {
                <p class="banner banner-info" data-testid="ws-origin-banner">
                  Filing into workstream
                  <strong>{{ originLabel() }}</strong
                  >.
                </p>
              }
              @if (!canFile()) {
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

              <!-- Optional Template Launcher & Live Composition Preview -->
              @if (!draft().workstream && templates().length > 0) {
                <div class="nt-template-box" data-testid="nt-template-section">
                  <div class="field">
                    <span>Start from template</span>
                    <g-pill-select
                      [options]="templateOptions()"
                      [value]="selectedTemplateId()"
                      ariaLabel="Start from template"
                      align="left"
                      testid="nt-template"
                      (valueChange)="pickTemplate($event)"
                    />
                  </div>

                  @if (selectedTemplate(); as tmpl) {
                    @for (tf of tmpl.fields ?? []; track tf.name) {
                      @if (tf.options?.length) {
                        <div class="field">
                          <span
                            >{{ tf.label || tf.name
                            }}{{ tf.required ? " *" : "" }}</span
                          >
                          <g-pill-select
                            [options]="templateFieldOptions(tf.options!)"
                            [value]="templateValues()[tf.name] || ''"
                            [ariaLabel]="tf.label || tf.name"
                            align="left"
                            [testid]="'nt-tfield-' + tf.name"
                            (valueChange)="setTemplateField(tf.name, $event)"
                          />
                        </div>
                      } @else {
                        <label class="field">
                          <span
                            >{{ tf.label || tf.name
                            }}{{ tf.required ? " *" : "" }}</span
                          >
                          <input
                            type="text"
                            [attr.data-testid]="'nt-tfield-' + tf.name"
                            [value]="templateValues()[tf.name] || ''"
                            (input)="setTemplateField(tf.name, val($event))"
                          />
                        </label>
                      }
                    }

                    @if (composition(); as comp) {
                      @if (comp.tasks[0]; as ct) {
                        <div
                          class="nt-preview-card"
                          data-testid="nt-composition-preview"
                        >
                          <strong>Preview: {{ ct.title }}</strong>
                          @for (cf of ct.fields.slice(0, 5); track cf.name) {
                            @if (cf.values?.length) {
                              <span class="muted">
                                {{ cf.name }}: {{ cf.values!.join(", ") }}
                                <em>({{ cf.source }})</em>
                              </span>
                            }
                          }
                        </div>
                      }
                    }
                  }
                </div>
              }

              <!-- Shown Core Fields -->
              @for (id of shown(); track id) {
                @if (
                  !selectedTemplate() ||
                  (id !== "title" && id !== "description")
                ) {
                  <ng-container
                    [ngTemplateOutlet]="fieldTpl"
                    [ngTemplateOutletContext]="{ $implicit: id }"
                  />
                }
              }

              <!-- Collapsible Capabilities & Repo Context Section -->
              @if (capsFields().length) {
                <button
                  type="button"
                  class="btn-text more-toggle"
                  data-testid="caps-options"
                  [attr.aria-expanded]="capsOpen()"
                  (click)="capsOpen.set(!capsOpen())"
                >
                  {{
                    capsOpen()
                      ? "Hide capabilities & context"
                      : "Capabilities & repo context" + capsSummarySuffix()
                  }}
                </button>
                @if (capsOpen()) {
                  <div class="nt-section-card" data-testid="nt-caps-section">
                    @for (id of capsFields(); track id) {
                      <ng-container
                        [ngTemplateOutlet]="fieldTpl"
                        [ngTemplateOutletContext]="{ $implicit: id }"
                      />
                    }
                  </div>
                }
              }

              <!-- Collapsible Advanced & Execution Options ("More options") -->
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
                  <div id="more-fields" class="more-fields nt-section-card">
                    @for (id of more(); track id) {
                      <ng-container
                        [ngTemplateOutlet]="fieldTpl"
                        [ngTemplateOutletContext]="{ $implicit: id }"
                      />
                    }
                    <!-- Reviewers & Auto-approve proposals -->
                    <div class="field">
                      <span>Reviewers (optional)</span>
                      @if (draft().reviewers.length) {
                        <ul class="chip-list" aria-label="Reviewers">
                          @for (rv of draft().reviewers; track rv) {
                            <li class="dep-chip">
                              {{ rv }}
                              <button
                                type="button"
                                class="chip-remove"
                                [attr.aria-label]="'Remove reviewer ' + rv"
                                (click)="removeReviewer(rv)"
                              >
                                ×
                              </button>
                            </li>
                          }
                        </ul>
                      }
                      <div class="nt-inline-row">
                        <input
                          type="text"
                          style="flex: 1"
                          data-testid="nt-reviewers-input"
                          placeholder="GitHub or LDAP username"
                          [value]="reviewerDraft()"
                          (input)="reviewerDraft.set(val($event))"
                        />
                        <button
                          type="button"
                          class="btn-tonal"
                          data-testid="nt-reviewers-add"
                          [disabled]="!reviewerDraft().trim()"
                          (click)="addReviewer()"
                        >
                          Add
                        </button>
                      </div>
                    </div>

                    @if (!draft().workstream) {
                      <label class="check-row">
                        <input
                          type="checkbox"
                          data-testid="nt-auto-approve-proposals"
                          [checked]="!!draft().autoApproveProposals"
                          (change)="
                            set('autoApproveProposals', checked($event))
                          "
                        />
                        <span>Auto-approve child proposals from this task</span>
                      </label>
                    }
                  </div>
                }
              }
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
                @if (draft().workstream) {
                  <div class="switch-row">
                    <button
                      type="button"
                      role="switch"
                      class="g-switch"
                      data-testid="auto-approve-switch"
                      aria-labelledby="sw-auto-approve"
                      [attr.aria-checked]="!!draft().autoApproveProposals"
                      (click)="
                        set(
                          'autoApproveProposals',
                          !draft().autoApproveProposals
                        )
                      "
                    >
                      <span class="sr-only"
                        >Auto-approve this stream's proposals</span
                      >
                    </button>
                    <span id="sw-auto-approve"
                      >Auto-approve this stream's proposals</span
                    >
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
                    data-testid="shape-director-btn"
                    [attr.aria-pressed]="shapingOpen()"
                    (click)="shapingOpen.set(!shapingOpen())"
                  >
                    {{
                      shapingOpen() ? "Hide Director" : "Shape with Director"
                    }}
                  </button>
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
                  [disabled]="busy() || !canFile()"
                >
                  {{
                    draft().workstream
                      ? draft().approved
                        ? "Start workstream"
                        : "Save stream proposal"
                      : draft().approved
                        ? "Create and start"
                        : "Save as proposal"
                  }}
                </button>
              </div>
            </div>
          </form>
          @defer (when shapingOpen()) {
            @if (shapingOpen()) {
              <g-director-shaper
                mode="create"
                [current]="shapeFields()"
                (applyDraft)="applyShapedDraft($event)"
                (applyAndSubmit)="applyAndSubmitShapedDraft($event)"
                (closed)="shapingOpen.set(false)"
              />
            }
          }
        </div>
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
              rows="5"
              data-testid="nt-description"
              placeholder="What should be done, and how will we know it is done?"
              [value]="draft().description"
              (input)="set('description', val($event))"
            ></textarea>
          </label>
        }
        @case ("attachments") {
          <div class="field">
            <span>{{ f.label || "Attachments" }}</span>
            <g-attachment-picker
              testid="nt-attachments"
              [dropzone]="true"
              [staged]="draft().attachments"
              [disabled]="busy()"
              (stagedChange)="set('attachments', $event)"
            />
          </div>
        }
        @case ("customFields") {
          @for (cf of declaredCustomFields(); track cf.name) {
            @if (cf.type === "bool") {
              <label class="check-row">
                <input
                  type="checkbox"
                  [attr.data-testid]="'nt-cf-' + cf.name"
                  [checked]="draft().customFields[cf.name] === 'true'"
                  (change)="
                    setCustomField(cf.name, checked($event) ? 'true' : '')
                  "
                />
                <span>{{ cf.label || cf.name }}</span>
              </label>
            } @else if (cf.type === "enum" && cf.options?.length) {
              <div class="field">
                <span>{{ cf.label || cf.name }}</span>
                <g-pill-select
                  [options]="enumFieldOptions(cf)"
                  [value]="customScalar(cf.name)"
                  [ariaLabel]="cf.label || cf.name"
                  align="left"
                  [testid]="'nt-cf-' + cf.name"
                  (valueChange)="setCustomField(cf.name, $event)"
                />
              </div>
            } @else {
              <label class="field">
                <span>{{ cf.label || cf.name }}</span>
                <input
                  [attr.type]="cf.type === 'number' ? 'number' : 'text'"
                  [attr.data-testid]="'nt-cf-' + cf.name"
                  [value]="customScalar(cf.name)"
                  (input)="setCustomField(cf.name, val($event))"
                />
              </label>
            }
          }
        }
        @case ("repo") {
          <div class="field">
            <span>{{ f.label || "Repository" }}</span>
            <g-pill-select
              [options]="repoOptions()"
              [value]="draft().repo"
              ariaLabel="Repository"
              align="left"
              testid="nt-repo"
              (valueChange)="pickRepo($event)"
            />
          </div>
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
            @if (repoBranches().length) {
              <div class="nt-inline-row" aria-label="Known branches">
                @for (br of repoBranches().slice(0, 6); track br) {
                  <button
                    type="button"
                    class="btn-text"
                    style="font-size: 11px; padding: 2px 6px; min-height: 22px"
                    (click)="set('base', br)"
                  >
                    {{ br }}
                  </button>
                }
              </div>
            }
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
        @case ("noPort") {
          @if (draft().repo !== noRepo) {
            <label class="check-row">
              <input
                type="checkbox"
                data-testid="nt-no-port"
                [checked]="draft().noPort"
                (change)="set('noPort', checked($event))"
              />
              <span>{{
                f.label || "Do not port change to paired branches"
              }}</span>
            </label>
          }
        }
        @case ("fixesBaseBranch") {
          @if (draft().repo !== noRepo) {
            <label class="check-row">
              <input
                type="checkbox"
                data-testid="nt-fixes-base"
                [checked]="draft().fixesBaseBranch"
                (change)="set('fixesBaseBranch', checked($event))"
              />
              <span>{{ f.label || "Fixes red base branch" }}</span>
            </label>
          }
        }
        @case ("grants") {
          <div class="field">
            <span>{{ f.label || "Capabilities & grants" }}</span>
            <g-grants-picker
              testid="nt-grants"
              [offered]="offeredCapabilities()"
              [held]="effectiveCapabilities()"
              [repo]="repoForAccess()"
              [disabled]="busy()"
              (heldChange)="set('capabilities', $event)"
            />
          </div>
        }
        @case ("reads") {
          <div class="field">
            <span>{{ f.label || "Extra read-only repositories" }}</span>
            @if (draft().reads.length) {
              <ul class="chip-list" aria-label="Read-only repositories">
                @for (r of draft().reads; track r) {
                  <li class="dep-chip">
                    {{ r }}
                    <button
                      type="button"
                      class="chip-remove"
                      [attr.aria-label]="'Remove read repository ' + r"
                      (click)="removeRead(r)"
                    >
                      ×
                    </button>
                  </li>
                }
              </ul>
            }
            <g-pill-select
              [options]="readRepoOptions()"
              value=""
              placeholder="Add read-only repository…"
              ariaLabel="Add read-only repository"
              align="left"
              testid="nt-reads"
              (valueChange)="addRead($event)"
            />
          </div>
        }
        @case ("writes") {
          <div class="field">
            <span>{{ f.label || "Additional write targets" }}</span>
            @if (draft().writes.length) {
              <ul class="chip-list" aria-label="Write target repositories">
                @for (
                  w of draft().writes;
                  track w.repo + ":" + (w.base || "")
                ) {
                  <li class="dep-chip">
                    {{ w.repo }}{{ w.base ? " (" + w.base + ")" : "" }}
                    <button
                      type="button"
                      class="chip-remove"
                      [attr.aria-label]="'Remove write target ' + w.repo"
                      (click)="removeWrite($index)"
                    >
                      ×
                    </button>
                  </li>
                }
              </ul>
            }
            <div class="nt-inline-row">
              <g-pill-select
                [options]="writeRepoOptions()"
                [value]="writeRepoDraft()"
                placeholder="Select repo…"
                ariaLabel="Additional write repository"
                align="left"
                testid="nt-writes-repo"
                (valueChange)="writeRepoDraft.set($event)"
              />
              <input
                type="text"
                style="flex: 1; min-width: 110px"
                data-testid="nt-writes-base"
                placeholder="Base branch"
                [value]="writeBaseDraft()"
                (input)="writeBaseDraft.set(val($event))"
              />
              <button
                type="button"
                class="btn-tonal"
                data-testid="nt-writes-add"
                [disabled]="!writeRepoDraft()"
                (click)="addWrite()"
              >
                Add
              </button>
            </div>
          </div>
        }
        @case ("model") {
          <div class="field">
            <span>{{ f.label || "Model" }}</span>
            <g-pill-select
              [options]="modelOptions(f.offered)"
              [value]="draft().model"
              ariaLabel="Model"
              align="left"
              testid="nt-model"
              (valueChange)="set('model', $event)"
            />
          </div>
        }
        @case ("agentBackendId") {
          <div class="field">
            <span>{{ f.label || "Connection (backend)" }}</span>
            <g-pill-select
              [options]="backendOptions()"
              [value]="draft().agentBackendId"
              ariaLabel="Connection backend"
              align="left"
              testid="nt-backend"
              (valueChange)="set('agentBackendId', $event)"
            />
          </div>
          <div class="field">
            <span>Agent framework</span>
            <g-pill-select
              [options]="frameworkOptions"
              [value]="draft().agentFramework"
              ariaLabel="Agent framework"
              align="left"
              testid="nt-framework"
              (valueChange)="set('agentFramework', $event)"
            />
          </div>
        }
        @case ("dependsOn") {
          <div class="field">
            <span>{{ f.label || "Depends on" }}</span>
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
            <g-pill-select
              [options]="dependableOptions()"
              value=""
              placeholder="Add a task this waits for…"
              ariaLabel="Depends on task"
              align="left"
              testid="nt-depends"
              (valueChange)="addDepId($event)"
            />
          </div>
        }
        @case ("networkAccess") {
          @if (!shapeIgnored()) {
            <div class="field">
              <span>{{ f.label || "Network access" }}</span>
              <g-pill-select
                [options]="networkOptions(f.offered)"
                [value]="draft().networkAccess"
                ariaLabel="Network access"
                align="left"
                testid="nt-network"
                (valueChange)="set('networkAccess', $event)"
              />
            </div>
          }
        }
        @case ("sandboxShape") {
          @if (!shapeIgnored()) {
            <div class="field" data-testid="nt-sandbox-shape">
              <span>{{ f.label || "Sandbox shape & resources" }}</span>
              <div class="nt-inline-row" role="group" aria-label="Sandbox tier">
                @for (tier of sandboxTiers; track tier.id) {
                  <button
                    type="button"
                    class="btn-outlined"
                    style="font-size: 11px; padding: 3px 8px; min-height: 26px"
                    [attr.data-testid]="'nt-tier-' + tier.id"
                    (click)="applySandboxTier(tier.cpus, tier.mem, tier.disk)"
                  >
                    {{ tier.label }}
                  </button>
                }
              </div>
              <div class="nt-shape-grid">
                <label class="field">
                  <span>CPUs</span>
                  <input
                    type="number"
                    min="0"
                    data-testid="nt-sandbox-cpus"
                    placeholder="Default"
                    [value]="draft().sandboxCpus || ''"
                    (input)="setNum('sandboxCpus', val($event))"
                  />
                </label>
                <label class="field">
                  <span>Memory (MB)</span>
                  <input
                    type="number"
                    min="0"
                    data-testid="nt-sandbox-mem"
                    placeholder="Default"
                    [value]="draft().sandboxMemoryMb || ''"
                    (input)="setNum('sandboxMemoryMb', val($event))"
                  />
                </label>
                <label class="field">
                  <span>Disk (GB)</span>
                  <input
                    type="number"
                    min="0"
                    data-testid="nt-sandbox-disk"
                    placeholder="Default"
                    [value]="draft().sandboxDiskGb || ''"
                    (input)="setNum('sandboxDiskGb', val($event))"
                  />
                </label>
              </div>
            </div>
          }
        }
        @case ("nothingNeededAction") {
          <div class="field">
            <span>{{ f.label || "If the agent finds nothing to do" }}</span>
            <g-pill-select
              [options]="nothingOptions"
              [value]="draft().nothingNeededAction"
              ariaLabel="If the agent finds nothing to do"
              align="left"
              testid="nt-nothing"
              (valueChange)="set('nothingNeededAction', $event)"
            />
          </div>
        }
        @case ("reviewTemplateId") {
          <div class="field">
            <span>{{ f.label || "Review template" }}</span>
            <g-pill-select
              [options]="reviewTemplateOptions()"
              [value]="draft().reviewTemplateId"
              ariaLabel="Review template"
              align="left"
              testid="nt-review-template"
              (valueChange)="set('reviewTemplateId', $event)"
            />
          </div>
        }
        @case ("foundIn") {
          <label class="field">
            <span>{{ f.label || "Found in release" }}</span>
            <input
              #foundInInput
              type="text"
              data-testid="nt-found-in"
              placeholder="v2.5.0-rc.2"
              autocomplete="off"
              spellcheck="false"
              [attr.aria-invalid]="error()?.field === 'foundIn' ? 'true' : null"
              [attr.aria-describedby]="
                error()?.field === 'foundIn' ? 'nt-found-in-error' : null
              "
              [value]="draft().foundIn"
              (input)="set('foundIn', val($event))"
            />
            @if (error()?.field === "foundIn") {
              <small
                id="nt-found-in-error"
                class="nt-field-error"
                role="alert"
                data-testid="nt-found-in-error"
                >{{ error()!.message }}</small
              >
            }
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
})
export class NewTaskSheetComponent {
  protected readonly svc = inject(CreateTaskService);
  private readonly api = inject(GrainApi);
  private readonly inbox = inject(InboxStore);
  private readonly wsStore = inject(WorkstreamsStore);
  private readonly access = inject(AccessService);
  private readonly snackbar = inject(SnackbarService);
  private readonly router = inject(Router);

  protected readonly noRepo = NO_REPO;
  protected readonly sandboxTiers = SANDBOX_TIERS;
  protected readonly frameworkOptions = FRAMEWORKS;
  protected readonly nothingOptions: readonly PillOption[] = [
    { value: "close", label: "Close the task" },
    { value: "ack", label: "Ask me to acknowledge it" },
  ];

  protected readonly config = signal<GrainConfig | null>(null);
  protected readonly templates = signal<Template[]>([]);
  protected readonly selectedTemplateId = signal("");
  protected readonly templateValues = signal<Record<string, string>>({});
  protected readonly composition = signal<Composition | null>(null);
  protected readonly repoBranches = signal<string[]>([]);
  protected readonly reachableRepos = signal<string[]>([]);
  protected readonly reachablePushRepos = signal<string[]>([]);

  protected readonly draft = signal<NewTaskDraft>(emptyDraft(null, []));
  protected readonly capsOpen = signal(false);
  protected readonly moreOpen = signal(false);
  protected readonly writeRepoDraft = signal("");
  protected readonly writeBaseDraft = signal("");
  protected readonly reviewerDraft = signal("");
  protected readonly busy = signal(false);
  protected readonly shapingOpen = signal(false);
  protected readonly error = signal<Problem | null>(null);

  protected readonly shapeFields = computed<TaskShapeFields>(() => {
    const d = this.draft();
    return {
      title: d.title,
      description: d.description,
      repo: d.repo === NO_REPO ? "" : d.repo,
      base: d.base,
      priority: 5,
      model: d.model,
      dependsOn: d.dependsOn,
    };
  });

  private readonly edited = new Set<keyof NewTaskDraft>();
  private generation = 0;

  private readonly sheet = viewChild<ElementRef<HTMLElement>>("sheet");
  private readonly titleInput =
    viewChild<ElementRef<HTMLInputElement>>("titleInput");
  private readonly branchInput =
    viewChild<ElementRef<HTMLInputElement>>("branchInput");
  private readonly foundInInput =
    viewChild<ElementRef<HTMLInputElement>>("foundInInput");

  private readonly knownTasks = computed(() => {
    const byId = new Map<string, Task>();
    for (const t of this.inbox.tasks()) byId.set(t.id, t);
    for (const t of this.wsStore.allTasks()) {
      if (!byId.has(t.id)) byId.set(t.id, t);
    }
    return [...byId.values()];
  });

  private readonly liveTasks = computed(() =>
    this.knownTasks().filter((t) => !t.consoleSession && !t.interactive),
  );

  protected readonly selectedTemplate = computed<Template | null>(
    () =>
      this.templates().find((t) => t.id === this.selectedTemplateId()) ?? null,
  );

  protected readonly templateOptions = computed<PillOption[]>(() => [
    { value: "", label: "None — write task directly" },
    ...this.templates().map((t) => ({
      value: t.id,
      label: t.declaredIn?.repo
        ? `${t.name || t.id} (${t.declaredIn.repo})`
        : t.name || t.id,
    })),
  ]);

  protected readonly reviewTemplateOptions = computed<PillOption[]>(() => [
    { value: "", label: "None" },
    ...this.templates().map((t) => ({
      value: t.id,
      label: t.name || t.id,
    })),
  ]);

  protected readonly originLabel = computed(() => {
    const orig = this.draft().origin?.trim() ?? "";
    if (!orig) return "";
    const ws = this.wsStore.byKey().get(orig);
    if (ws) return ws.title;
    if (orig.startsWith("task:")) {
      const t = this.knownTasks().find((x) => x.id === orig.slice(5));
      if (t?.title) return `#${t.id} ${t.title}`;
    }
    return orig;
  });

  protected readonly repos = computed(() => {
    const cfg = this.config();
    const base = targetableRepos(cfg, this.inbox.tasks());
    // Match classic NewTaskOverlay.jsx: when the deployment has an explicit
    // targetRepos allowlist, filing against any off-list repo is refused by
    // the server, so never merge GitHub-wide reachablePushRepos into target
    // or write pickers. Only offer reachablePushRepos when no allowlist is set.
    if (hasExplicitTargetAllowlist(cfg)) {
      return base;
    }
    const extra = this.reachablePushRepos();
    const seen = new Set(base.map((r) => r.toLowerCase()));
    const merged = [...base];
    for (const r of extra) {
      if (r && !seen.has(r.toLowerCase())) {
        seen.add(r.toLowerCase());
        merged.push(r);
      }
    }
    return merged.sort((a, b) => a.localeCompare(b));
  });

  protected readonly defaultTarget = computed(
    () => (this.config()?.["defaultTarget"] as string | undefined) ?? "",
  );

  protected readonly repoOptions = computed<PillOption[]>(() => {
    const f = this.fields().repo;
    const out: PillOption[] = [];
    if (!this.defaultTarget()) {
      out.push({ value: "", label: "Deployment default target" });
    }
    for (const r of this.repos()) {
      out.push({ value: r, label: r });
    }
    if (f.allowNoRepo !== false) {
      out.push({
        value: NO_REPO,
        label: "No repository (answer in a comment)",
      });
    }
    return out;
  });

  protected readonly readRepoOptions = computed<PillOption[]>(() => {
    const cur = new Set(this.draft().reads);
    const main = this.draft().repo;
    return [
      { value: "", label: "Add read-only repository…" },
      ...this.reachableRepos()
        .filter((r) => r !== main && !cur.has(r))
        .map((r) => ({ value: r, label: r })),
    ];
  });

  protected readonly writeRepoOptions = computed<PillOption[]>(() => {
    const main = this.draft().repo;
    return [
      { value: "", label: "Select repository…" },
      ...this.repos()
        .filter((r) => r !== main)
        .map((r) => ({ value: r, label: r })),
    ];
  });

  protected readonly declaredCustomFields = computed<CustomFieldDeclaration[]>(
    () => this.config()?.customFields ?? [],
  );

  protected readonly offeredCapabilities = computed(
    () => this.config()?.capabilities ?? [],
  );

  protected readonly effectiveCapabilities = computed<string[]>(() => {
    const explicit = this.draft().capabilities;
    if (explicit !== null) return explicit;
    return defaultCapabilitiesForRepo(this.config(), this.draft().repo);
  });

  protected readonly fields = computed(() =>
    resolveFields(this.config(), this.draft().repo, this.selectedTemplateId()),
  );

  private static readonly CAPS_SECTION_IDS = new Set<FieldId>([
    "grants",
    "reads",
    "writes",
  ]);

  protected readonly shown = computed(() => this.byVisibility("shown"));
  protected readonly capsFields = computed(() => {
    if (!this.access.can("POST /api/tasks", this.repoForAccess())) return [];
    return this.byVisibility("more").filter((id) =>
      NewTaskSheetComponent.CAPS_SECTION_IDS.has(id),
    );
  });
  protected readonly more = computed(() => {
    const hidden = !this.access.can("POST /api/tasks", this.repoForAccess())
      ? new Set(["model", "sandboxCpus"])
      : new Set();
    return this.byVisibility("more").filter(
      (id) =>
        !NewTaskSheetComponent.CAPS_SECTION_IDS.has(id) && !hidden.has(id),
    );
  });

  protected readonly capsSummarySuffix = computed(() => {
    const d = this.draft();
    const parts: string[] = [];
    const caps = this.effectiveCapabilities();
    if (caps.length) parts.push(`${caps.length} grants`);
    if (d.reads.length) parts.push(`${d.reads.length} reads`);
    if (d.writes.length) parts.push(`${d.writes.length} writes`);
    return parts.length ? ` (${parts.join(", ")})` : "";
  });

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
  protected readonly dependableOptions = computed<PillOption[]>(() => [
    { value: "", label: "Add a task this waits for…" },
    ...this.dependable().map((t) => ({
      value: t.id,
      label: `#${t.id} ${t.title}`,
    })),
  ]);
  protected readonly backendOptions = computed<PillOption[]>(() => [
    { value: "", label: "Automatic rotation" },
    ...(this.config()?.agentBackends ?? []).map((b) => ({
      value: b.id,
      label: b.name ? `${b.name} (${b.id})` : b.id,
    })),
  ]);

  protected readonly repoForAccess = computed(() => {
    const r = this.selectedTemplate()?.repo || this.draft().repo;
    return r && r !== NO_REPO ? r : undefined;
  });

  protected readonly canFile = computed(() => {
    const repo = this.repoForAccess();
    const tmpl = this.selectedTemplate();
    if (tmpl && repo && fromTemplateStem(repo, tmpl)) {
      return (
        this.access.can("POST /api/tasks/from-template", repo) ||
        this.access.can("POST /api/tasks", repo)
      );
    }
    return this.access.can("POST /api/tasks", repo);
  });

  protected readonly deniedReason = computed(() => {
    const repo = this.repoForAccess();
    const tmpl = this.selectedTemplate();
    if (tmpl && repo && fromTemplateStem(repo, tmpl)) {
      if (this.access.can("POST /api/tasks/from-template", repo)) return null;
      return this.access.deniedReason("POST /api/tasks/from-template", repo);
    }
    return this.access.deniedReason("POST /api/tasks", repo);
  });

  protected readonly canAskDirector = computed(
    () =>
      this.access.can("POST /api/tasks/{id}/comments") &&
      this.access.can("GET /api/console-sessions") &&
      this.access.can("PUT /api/console-sessions/{id}"),
  );

  constructor() {
    effect(() => {
      if (!this.svc.isOpen()) return;
      const prefill = this.svc.prefill();
      untracked(() => {
        this.error.set(null);
        this.capsOpen.set(false);
        this.moreOpen.set(false);
        this.selectedTemplateId.set("");
        this.templateValues.set({});
        this.composition.set(null);
        this.writeRepoDraft.set("");
        this.writeBaseDraft.set("");
        this.reviewerDraft.set("");
        this.edited.clear();

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
        if (prefill.origin || prefill.workstream) this.wsStore.ensureLoaded();
        void this.loadConfigAndSupporting(prefill);
      });
    });
  }

  private async loadConfigAndSupporting(
    prefill: Partial<NewTaskDraft>,
  ): Promise<void> {
    const gen = ++this.generation;
    try {
      const cfg = await this.api.config();
      if (gen !== this.generation || !this.svc.isOpen()) return;
      this.config.set(cfg);
      this.reseed(emptyDraft(cfg, this.inbox.tasks(), prefill));
    } catch {
      // Keep builtin defaults.
    }

    const canGet = typeof this.api.get === "function";
    const [tmplRes, reachRes] = await Promise.allSettled([
      canGet && this.access.can("GET /api/templates")
        ? this.api.get<Template[] | { templates?: Template[] }>(
            "GET /api/templates",
          )
        : Promise.resolve([]),
      canGet && this.access.can("GET /api/deployment/reachable-repos")
        ? this.api.get<string[] | { repos?: string[] }>(
            "GET /api/deployment/reachable-repos",
          )
        : Promise.resolve([]),
    ]);

    if (gen !== this.generation || !this.svc.isOpen()) return;
    if (tmplRes.status === "fulfilled") {
      const v = tmplRes.value;
      const list = Array.isArray(v) ? v : (v?.templates ?? []);
      this.templates.set(list);
    }
    if (reachRes.status === "fulfilled") {
      const v = reachRes.value as {
        repos?: Array<{ repo: string; push?: boolean } | string>;
      };
      const list = Array.isArray(v) ? v : (v?.repos ?? []);
      if (Array.isArray(list)) {
        this.reachableRepos.set(
          list.map((x) => (typeof x === "string" ? x : x.repo)),
        );
        this.reachablePushRepos.set(
          list
            .filter((x) => typeof x === "string" || x.push)
            .map((x) => (typeof x === "string" ? x : x.repo)),
        );
      }
    }
    void this.loadBranchesFor(this.draft().repo);
  }

  private async loadBranchesFor(repo: string): Promise<void> {
    this.repoBranches.set([]);
    if (
      typeof this.api.get !== "function" ||
      !repo ||
      repo === NO_REPO ||
      !this.access.can("GET /api/repos/{owner}/{name}/branches", repo)
    ) {
      return;
    }
    const { owner, name } = splitRepo(repo);
    if (!owner || !name) return;
    try {
      const res = await this.api.get<
        string[] | { branches?: ({ name?: string } | string)[] }
      >("GET /api/repos/{owner}/{name}/branches", { owner, name });
      const raw = Array.isArray(res) ? res : (res?.branches ?? []);
      const names = raw
        .map((b) => (typeof b === "string" ? b : (b?.name ?? "")))
        .filter(Boolean);
      if (this.draft().repo === repo) this.repoBranches.set(names);
    } catch {
      // Optional branch list.
    }
  }

  protected pickTemplate(id: string): void {
    this.selectedTemplateId.set(id);
    this.composition.set(null);
    const tmpl = this.templates().find((t) => t.id === id);
    if (!tmpl) {
      this.templateValues.set({});
      return;
    }
    const vals: Record<string, string> = {};
    for (const f of tmpl.fields ?? []) {
      vals[f.name] = f.default ?? "";
    }
    this.templateValues.set(vals);
    if (tmpl.repo) {
      this.pickRepo(tmpl.repo);
    } else if (tmpl.declaredIn?.repo && !this.draft().repo) {
      this.pickRepo(tmpl.declaredIn.repo);
    }
    void this.refreshComposition();
  }

  protected setTemplateField(name: string, value: string): void {
    this.templateValues.update((m) => ({ ...m, [name]: value }));
    void this.refreshComposition();
  }

  protected templateFieldOptions(opts: string[]): PillOption[] {
    return opts.map((o) => ({ value: o, label: o }));
  }

  private async refreshComposition(): Promise<void> {
    const tmpl = this.selectedTemplate();
    if (!tmpl || !this.access.can("GET /api/templates/{id}/composition")) {
      this.composition.set(null);
      return;
    }
    const qs = new URLSearchParams();
    const repo = tmpl.repo || this.draft().repo;
    if (repo && repo !== NO_REPO) qs.set("repo", repo);
    if (this.draft().base) qs.set("base", this.draft().base);
    qs.set("approved", String(this.draft().approved));
    for (const [k, v] of Object.entries(this.templateValues())) {
      if (v) qs.set(`field.${k}`, v);
    }
    try {
      const comp = await this.api.get<Composition>(
        "GET /api/templates/{id}/composition",
        { id: tmpl.id },
        qs.toString(),
      );
      if (this.selectedTemplateId() === tmpl.id) {
        this.composition.set(comp);
      }
    } catch {
      // Optional preview.
    }
  }

  private reseed(fresh: NewTaskDraft): void {
    this.draft.update((d) => {
      const next = { ...fresh };
      for (const k of this.edited) {
        (next as Record<string, unknown>)[k] = d[k];
      }
      return next;
    });
  }

  protected modelOptions(offered?: string[]): PillOption[] {
    const all = offered?.length ? offered : this.models();
    const def = this.defaultModel();
    return [
      {
        value: "",
        label: def ? `Deployment default (${def})` : "Deployment default",
      },
      ...all.map((m) => ({ value: m, label: m })),
    ];
  }

  protected networkOptions(offered?: string[]): PillOption[] {
    const vals = offered?.length ? offered : ["open", "none"];
    return [
      { value: "", label: "Deployment default" },
      ...vals.map((v) => ({
        value: v,
        label: v === "open" ? "Open" : v === "none" ? "None" : v,
      })),
    ];
  }

  protected enumFieldOptions(cf: CustomFieldDeclaration): PillOption[] {
    return [
      { value: "", label: "None" },
      ...(cf.options ?? []).map((o) => ({ value: o, label: o })),
    ];
  }

  protected customScalar(name: string): string {
    const v = this.draft().customFields[name];
    return Array.isArray(v) ? v.join(", ") : (v ?? "");
  }

  protected setCustomField(name: string, val: string): void {
    this.edited.add("customFields");
    this.draft.update((d) => ({
      ...d,
      customFields: { ...d.customFields, [name]: val },
    }));
  }

  private byVisibility(v: "shown" | "more"): FieldId[] {
    const f = this.fields();
    return (Object.keys(f) as FieldId[])
      .filter((id) => {
        if (id === "approved" || id === "atFront") return false;
        if (id === "customFields" && !this.declaredCustomFields().length) {
          return false;
        }
        return f[id].visibility === v;
      })
      .sort((a, b) => f[a].order - f[b].order);
  }

  protected fieldOf(id: FieldId) {
    return this.fields()[id];
  }

  protected set<K extends keyof NewTaskDraft>(k: K, v: NewTaskDraft[K]): void {
    this.edited.add(k);
    this.draft.update((d) => ({ ...d, [k]: v }));
    if ((k === "base" || k === "approved") && this.selectedTemplate()) {
      void this.refreshComposition();
    }
  }

  protected setNum(
    k: "sandboxCpus" | "sandboxMemoryMb" | "sandboxDiskGb",
    raw: string,
  ): void {
    const n = Math.max(0, Math.floor(Number(raw) || 0));
    this.set(k, n);
  }

  protected applySandboxTier(cpus: number, mem: number, disk: number): void {
    this.set("sandboxCpus", cpus);
    this.set("sandboxMemoryMb", mem);
    this.set("sandboxDiskGb", disk);
  }

  protected val(ev: Event): string {
    return (ev.target as HTMLInputElement | HTMLTextAreaElement).value;
  }

  protected checked(ev: Event): boolean {
    return (ev.target as HTMLInputElement).checked;
  }

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
    void this.loadBranchesFor(repo);
    if (this.selectedTemplate()) void this.refreshComposition();
  }

  protected addRead(repo: string): void {
    if (!repo) return;
    this.edited.add("reads");
    this.draft.update((d) =>
      d.reads.includes(repo) ? d : { ...d, reads: [...d.reads, repo] },
    );
  }

  protected removeRead(repo: string): void {
    this.edited.add("reads");
    this.draft.update((d) => ({
      ...d,
      reads: d.reads.filter((x) => x !== repo),
    }));
  }

  protected addWrite(): void {
    const repo = this.writeRepoDraft().trim();
    if (!repo) return;
    const base = this.writeBaseDraft().trim();
    this.edited.add("writes");
    this.draft.update((d) => ({
      ...d,
      writes: [...d.writes, { repo, ...(base ? { base } : {}) }],
    }));
    this.writeRepoDraft.set("");
    this.writeBaseDraft.set("");
  }

  protected removeWrite(idx: number): void {
    this.edited.add("writes");
    this.draft.update((d) => ({
      ...d,
      writes: d.writes.filter((_, i) => i !== idx),
    }));
  }

  protected addReviewer(): void {
    const r = this.reviewerDraft().trim();
    if (!r) return;
    this.edited.add("reviewers");
    this.draft.update((d) =>
      d.reviewers.includes(r) ? d : { ...d, reviewers: [...d.reviewers, r] },
    );
    this.reviewerDraft.set("");
  }

  protected removeReviewer(r: string): void {
    this.edited.add("reviewers");
    this.draft.update((d) => ({
      ...d,
      reviewers: d.reviewers.filter((x) => x !== r),
    }));
  }

  protected titleOf(id: string): string {
    return this.knownTasks().find((t) => t.id === id)?.title ?? "";
  }

  protected addDepId(id: string): void {
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

  protected async onSheetPaste(ev: ClipboardEvent): Promise<void> {
    const files = filesFromTransfer(ev.clipboardData);
    if (!files.length) return;
    ev.preventDefault();
    await this.appendUploads(files);
  }

  protected async onSheetDrop(ev: DragEvent): Promise<void> {
    ev.preventDefault();
    const files = filesFromTransfer(ev.dataTransfer);
    if (!files.length) return;
    await this.appendUploads(files);
  }

  private async appendUploads(files: File[]): Promise<void> {
    const cur = this.draft().attachments;
    const { uploads, error } = await readFilesAsUploads(files, cur.length);
    if (error) {
      this.error.set({ field: "title", message: error });
      return;
    }
    if (uploads.length) {
      this.set("attachments", [...cur, ...uploads] as AttachmentUpload[]);
    }
  }

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
    const tmpl = this.selectedTemplate();
    const tvals = this.templateValues();
    const problem = validate(d, this.fields(), tmpl, tvals);
    if (problem) {
      this.error.set(problem);
      if (problem.field === "foundIn") {
        if (this.fields().foundIn.visibility === "more") {
          this.moreOpen.set(true);
        }
        queueMicrotask(() => this.foundInInput()?.nativeElement.focus());
      } else {
        (problem.field === "title"
          ? this.titleInput()
          : this.branchInput()
        )?.nativeElement.focus();
      }
      return;
    }
    this.busy.set(true);
    this.error.set(null);
    try {
      let created: Task;
      const effectiveRepo =
        tmpl?.repo ||
        (d.repo === NO_REPO ? "" : d.repo || this.defaultTarget());
      const stem =
        tmpl && effectiveRepo ? fromTemplateStem(effectiveRepo, tmpl) : "";
      // POST /api/tasks/from-template takes only {repo, template, fields,
      // approved} (pkg/ui/tasks_from_template.go), so any other field that
      // differs from a fresh draft for this repo must go to POST /api/tasks.
      const fresh = emptyDraft(this.config(), this.inbox.tasks(), {
        repo: d.repo,
      });
      const differs = (a: unknown, b: unknown) =>
        JSON.stringify(a ?? null) !== JSON.stringify(b ?? null);
      const OVERRIDE_KEYS: (keyof NewTaskDraft)[] = [
        "base",
        "newBranch",
        "newBranchName",
        "autoMerge",
        "noPort",
        "fixesBaseBranch",
        "foundIn",
        "model",
        "agentFramework",
        "agentBackendId",
        "networkAccess",
        "nothingNeededAction",
        "reviewTemplateId",
        "reviewers",
        "promptExtension",
        "sandboxCpus",
        "sandboxMemoryMb",
        "sandboxDiskGb",
        "atFront",
        "origin",
        "parentId",
        "autoApproveProposals",
        "customFields",
      ];
      const hasExtraTaskOverrides =
        d.attachments.length > 0 ||
        d.dependsOn.length > 0 ||
        d.reads.length > 0 ||
        d.writes.length > 0 ||
        d.capabilities !== null ||
        OVERRIDE_KEYS.some((k) => differs(d[k], fresh[k]));

      const canOnlyPostFromTemplate =
        !this.access.can("POST /api/tasks", effectiveRepo) &&
        this.access.can("POST /api/tasks/from-template", effectiveRepo);
      if (
        tmpl &&
        stem &&
        effectiveRepo &&
        (canOnlyPostFromTemplate ||
          (!hasExtraTaskOverrides &&
            this.access.can("POST /api/tasks/from-template", effectiveRepo)))
      ) {
        created = await this.api.post<Task>(
          "POST /api/tasks/from-template",
          undefined,
          {
            repo: effectiveRepo,
            template: stem,
            ...(Object.keys(tvals).length ? { fields: tvals } : {}),
            approved: d.approved,
          },
        );
      } else {
        created = await this.api.post<Task>(
          "POST /api/tasks",
          undefined,
          buildPayload(
            d,
            this.config(),
            tmpl ? { templateId: tmpl.id, fields: tvals } : null,
          ),
        );
      }

      if (d.workstream && created?.id) {
        this.wsStore.recordStartedWorkstream(created.id);
        void this.wsStore.refresh(true);
      } else if (d.origin) {
        void this.wsStore.refresh(true);
      }
      this.svc.clearSaved();
      this.svc.close();
      this.announce(created, d.approved, !!d.workstream);
    } catch (err) {
      const msg = errorText(err);
      const isFoundIn = /found[- ]in|release tag/i.test(msg);
      if (isFoundIn && this.fields().foundIn.visibility === "more") {
        this.moreOpen.set(true);
      }
      this.error.set({
        field: isFoundIn ? "foundIn" : "form",
        message: `The task was not filed: ${msg}`,
      });
    } finally {
      this.busy.set(false);
    }
  }

  private announce(created: Task, approved: boolean, workstream = false): void {
    const id = created?.id;
    if (!id) {
      this.snackbar.show(
        workstream
          ? approved
            ? "Workstream started"
            : "Workstream proposal saved"
          : approved
            ? "Task filed"
            : "Proposal saved",
      );
      return;
    }
    const open = {
      label: "Open",
      run: () => {
        void this.router.navigate(
          workstream ? ["/workstreams", `task:${id}`] : ["/task", id],
        );
      },
    };
    if (workstream) {
      this.snackbar.show(
        approved
          ? `Started workstream #${id}; planning its tasks`
          : `Saved workstream #${id} as a proposal`,
        open,
      );
      return;
    }
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
        { body: directorMessage(d), attachments: d.attachments },
      );
      this.svc.clearSaved();
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

  protected applyShapedDraft(patch: Partial<TaskShapeFields>): void {
    this.draft.update((d) => ({
      ...d,
      ...(patch.title !== undefined ? { title: patch.title } : {}),
      ...(patch.description !== undefined
        ? { description: patch.description }
        : {}),
      ...(patch.repo !== undefined ? { repo: patch.repo || NO_REPO } : {}),
      ...(patch.base !== undefined ? { base: patch.base } : {}),
      ...(patch.model !== undefined ? { model: patch.model } : {}),
      ...(patch.dependsOn !== undefined ? { dependsOn: patch.dependsOn } : {}),
    }));
    if (patch.title !== undefined) this.edited.add("title");
    if (patch.description !== undefined) this.edited.add("description");
    if (patch.repo !== undefined) this.edited.add("repo");
    if (patch.base !== undefined) this.edited.add("base");
    if (patch.model !== undefined) this.edited.add("model");
    if (patch.dependsOn !== undefined) this.edited.add("dependsOn");
    this.snackbar.show("Applied Director suggestions to form");
  }

  protected applyAndSubmitShapedDraft(patch: Partial<TaskShapeFields>): void {
    this.applyShapedDraft(patch);
    void this.file();
  }

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

  protected onTab(ev: KeyboardEvent): void {
    const sheet = this.sheet()?.nativeElement;
    if (!this.svc.isOpen() || !sheet) return;
    const items = Array.from(
      sheet.querySelectorAll<HTMLElement>(
        'button:not([disabled]), input:not([disabled]), textarea:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])',
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
