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
  ViewEncapsulation,
  effect,
  inject,
  input,
  output,
  signal,
  untracked,
} from "@angular/core";
import { NgTemplateOutlet } from "@angular/common";
import { GrainApi, splitRepo } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import { errText } from "../settings/settings-state.service";

// WorkOwnershipState is model.WorkOwnership's three values on the wire:
// "on", "off", or "" for "nobody has said".
export type WorkOwnershipState = "on" | "off" | "";

export interface RepoWorkItemRow {
  item: string;
  kind?: string;
  title?: string;
  override?: string;
  runs?: boolean;
  paused?: boolean;
  declaration?: { repo?: string; path?: string };
}

export interface RepoWorkOwnershipView {
  repo?: string;
  default?: string;
  defaultRuns?: boolean;
  items?: RepoWorkItemRow[] | null;
}

// saidLabel is what was actually said, never left blank for the empty
// case -- the one a reader is most likely to fill in for themselves
// (the classic pane's SaidChip, ui/src/components/RepoWorkOwnership.jsx).
export function saidLabel(said: string | undefined): string {
  if (said === "on") return "On, set here";
  if (said === "off") return "Off, set here";
  return "Nobody has said";
}

const STATES: readonly { state: WorkOwnershipState; text: string }[] = [
  { state: "on", text: "On" },
  { state: "off", text: "Off" },
  { state: "", text: "Say nothing" },
];

// RepoWorkOwnershipComponent is the /next/ port of the classic
// RepoWorkOwnership pane (grain/task-486): whether this deployment runs
// one repository's standing work, as the repository default and as the
// exception to it on each work item (the release, and every schedule).
//
// The setting is three-valued on purpose: "nobody has said" follows the
// default and moves with it, "off" survives the default being turned
// back on, and "on" survives it being turned off. So every row says both
// what was said and what the server resolves it to (runs), and offers
// the three states as three buttons rather than a checkbox that would
// have to spend one of its two positions on silence.
//
// Each button saves at once (PUT .../work-ownership takes one level per
// request), so there is no draft to guard. A refused save is said on the
// row it was made from. refreshKey moves when the page saved the release
// switch, which is the release item's own override; saved is the same
// news going the other way.
@Component({
  selector: "g-repo-work-ownership",
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="s-card" data-testid="card-repo-work-ownership">
      <div class="s-card-head">
        <div>
          <h2 class="s-card-title">Work ownership</h2>
          <p class="s-card-desc">
            Whether this deployment is the one running {{ repo() }}'s standing
            work, as against another deployment watching the same repository.
            The repository declares that the work exists; this says who does it.
          </p>
        </div>
      </div>

      @if (denied(); as why) {
        <p
          class="s-hint"
          id="work-ownership-denied"
          data-testid="work-ownership-denied"
        >
          {{ why }}
        </p>
      }

      @if (loadError(); as err) {
        <p
          class="s-field-error"
          role="alert"
          data-testid="work-ownership-load-error"
        >
          Could not read work ownership: {{ err }}
        </p>
      } @else if (work()) {
        @let w = work()!;
        <div class="s-toggle-row">
          <div class="s-toggle-copy">
            <span class="s-toggle-name"
              >Standing work on this deployment:
              {{ w.defaultRuns ? "on" : "off" }}</span
            >
            <span class="s-hint"
              >Covers this repository's schedules (declared, set up here, and
              workflow crons), its release, the base-branch check, post-merge
              jobs, automatic ports, and the triage and repair tasks grain files
              on its own. Tasks people file by hand, their pull requests' CI,
              the merge queue for them and their repair run either way.</span
            >
          </div>
          <button
            type="button"
            class="g-switch"
            role="switch"
            data-testid="switch-work-ownership-default"
            aria-label="Standing work on this deployment"
            [attr.aria-checked]="!!w.defaultRuns"
            [attr.aria-describedby]="denied() ? 'work-ownership-denied' : null"
            [disabled]="readOnly() || busy()"
            (click)="save({ default: w.defaultRuns ? 'off' : 'on' }, '')"
          ></button>
        </div>

        <p class="s-hint">
          Saying nothing is its own answer, and a different one from saying no:
          an item that says nothing follows the default and moves with it, one
          that says off stays off when the default is turned back on, and one
          that says on runs while the default is off.
        </p>

        <ul class="repo-work-list" aria-label="Work items">
          <li class="repo-work-row" data-testid="work-ownership-default-row">
            <div class="repo-work-copy">
              <span class="repo-work-name">Repository default</span>
              <span class="repo-work-pills">
                <span
                  class="status-pill"
                  [class]="saidTone(w.default)"
                  data-testid="work-ownership-default-said"
                  >{{ said(w.default) }}</span
                >
                <span
                  class="status-pill"
                  [class]="w.defaultRuns ? 'healthy' : 'neutral'"
                  >{{
                    w.defaultRuns
                      ? "Runs this repository's work"
                      : "Does not run this repository's work"
                  }}</span
                >
              </span>
            </div>
            <ng-container
              *ngTemplateOutlet="
                setButtons;
                context: {
                  label: 'the repository default',
                  said: w.default ?? '',
                  key: '',
                }
              "
            />
            @if (rowError()[""]; as err) {
              <span
                class="s-field-error repo-work-error"
                role="alert"
                id="work-ownership-err-default"
                >{{ err }}</span
              >
            }
          </li>
          @for (item of w.items ?? []; track item.item) {
            <li
              class="repo-work-row"
              [attr.data-testid]="'work-item-' + item.item"
            >
              <div class="repo-work-copy">
                <span class="repo-work-name">
                  {{ item.title || item.item }}
                  @if (item.title && item.title !== item.item) {
                    <code class="s-mono repo-work-id">{{ item.item }}</code>
                  }
                </span>
                <span class="repo-work-pills">
                  <span
                    class="status-pill"
                    [class]="item.runs ? 'healthy' : 'neutral'"
                    [attr.data-testid]="'work-item-runs-' + item.item"
                    >{{ item.runs ? "Runs" : "Not run" }}</span
                  >
                  <span
                    class="status-pill"
                    [class]="saidTone(item.override)"
                    [attr.data-testid]="'work-item-said-' + item.item"
                    >{{ said(item.override) }}</span
                  >
                  @if (item.paused) {
                    <span class="status-pill blast">Paused on its own row</span>
                  }
                </span>
                @if (item.declaration?.path) {
                  <span class="s-caption"
                    >declared in {{ item.declaration?.path }}</span
                  >
                }
                @if (item.kind === "unknown") {
                  <span class="s-caption"
                    >nothing here has this id — a decision left behind by
                    whatever it named</span
                  >
                }
              </div>
              <ng-container
                *ngTemplateOutlet="
                  setButtons;
                  context: {
                    label: item.title || item.item,
                    said: item.override ?? '',
                    key: item.item,
                  }
                "
              />
              @if (rowError()[item.item]; as err) {
                <span
                  class="s-field-error repo-work-error"
                  role="alert"
                  [id]="'work-ownership-err-' + item.item"
                  >{{ err }}</span
                >
              }
            </li>
          } @empty {
            <li class="s-hint" data-testid="work-ownership-empty">
              No work items: this repository declares no schedules here, none
              fires into it, and nothing has been said about one.
            </li>
          }
        </ul>

        <p class="s-caption">
          Grain resolves this before every piece of standing work it starts
          here. A skipped schedule says so on its own status and moves on to its
          next occurrence, so turning the work back on does not release a
          backlog of catch-up firings. The deployment's own configuration
          repository is checked whatever this says.
        </p>
      } @else {
        <p class="s-hint">Loading work ownership…</p>
      }
    </div>

    <ng-template #setButtons let-label="label" let-said="said" let-key="key">
      <div
        class="repo-work-buttons"
        role="group"
        [attr.aria-label]="label"
        [attr.aria-invalid]="rowError()[key] ? 'true' : null"
        [attr.aria-describedby]="
          rowError()[key]
            ? 'work-ownership-err-' + (key || 'default')
            : denied()
              ? 'work-ownership-denied'
              : null
        "
      >
        @for (s of states; track s.state) {
          <button
            type="button"
            class="repo-toggle-pill"
            [attr.aria-label]="label + ': ' + (s.state || 'say nothing')"
            [attr.aria-pressed]="said === s.state"
            [disabled]="readOnly() || busy()"
            (click)="setState(key, s.state)"
          >
            {{ s.text }}
          </button>
        }
      </div>
    </ng-template>
  `,
  imports: [NgTemplateOutlet],
})
export class RepoWorkOwnershipComponent {
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);

  readonly repo = input.required<string>();
  readonly refreshKey = input(0);
  readonly saved = output<void>();

  protected readonly states = STATES;
  protected readonly work = signal<RepoWorkOwnershipView | null>(null);
  protected readonly loadError = signal<string | null>(null);
  protected readonly busy = signal(false);
  // rowError is a refused save, keyed by the item it was made from ("" for
  // the repository default), said beside that row and cleared by the next
  // save from anywhere in the pane.
  protected readonly rowError = signal<Record<string, string>>({});

  constructor() {
    effect(() => {
      this.repo();
      this.refreshKey();
      untracked(() => void this.reload());
    });
  }

  protected said(s: string | undefined): string {
    return saidLabel(s);
  }

  protected saidTone(s: string | undefined): string {
    if (s === "on") return "healthy";
    if (s === "off") return "error";
    return "neutral";
  }

  protected readOnly(): boolean {
    return !this.access.can(
      "PUT /api/repos/{owner}/{name}/work-ownership",
      this.repo(),
    );
  }

  protected denied(): string {
    return this.access.deniedReason(
      "PUT /api/repos/{owner}/{name}/work-ownership",
      this.repo(),
    );
  }

  async reload(): Promise<void> {
    if (
      !this.access.can(
        "GET /api/repos/{owner}/{name}/work-ownership",
        this.repo(),
      )
    ) {
      this.loadError.set(
        this.access.deniedReason(
          "GET /api/repos/{owner}/{name}/work-ownership",
          this.repo(),
        ),
      );
      return;
    }
    try {
      this.work.set(
        await this.api.get<RepoWorkOwnershipView>(
          "GET /api/repos/{owner}/{name}/work-ownership",
          splitRepo(this.repo()),
        ),
      );
      this.loadError.set(null);
    } catch (err) {
      this.loadError.set(errText(err));
    }
  }

  protected setState(key: string, state: WorkOwnershipState): void {
    void this.save(key ? { item: key, state } : { default: state }, key);
  }

  protected async save(
    body: { default?: string; item?: string; state?: string },
    key: string,
  ): Promise<void> {
    this.busy.set(true);
    this.rowError.set({});
    try {
      const next = await this.api.put<RepoWorkOwnershipView>(
        "PUT /api/repos/{owner}/{name}/work-ownership",
        splitRepo(this.repo()),
        body,
      );
      if (next && typeof next === "object" && "items" in next) {
        this.work.set(next);
      } else {
        await this.reload();
      }
      this.saved.emit();
    } catch (err) {
      this.rowError.set({ [key]: errText(err) });
    } finally {
      this.busy.set(false);
    }
  }
}
