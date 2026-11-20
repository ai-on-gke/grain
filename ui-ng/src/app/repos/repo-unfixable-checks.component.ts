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
  signal,
  untracked,
} from "@angular/core";
import { GrainApi, splitRepo } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import { errText } from "../settings/settings-state.service";

interface UnfixableChecksResponse {
  unfixableChecks?: string[] | null;
  grainCICheckName?: string;
}

export function sameCheckName(a: string, b: string): boolean {
  return a.trim().toLowerCase() === b.trim().toLowerCase();
}

// RepoUnfixableChecksComponent is the /next/ port of the classic Merge queue
// tab's "Checks no commit here can clear" section
// (GET/PUT /api/repos/{owner}/{name}/unfixable-checks).
//
// Each addition or removal replaces the whole list at once (no separate Save
// button or unsaved-edit draft). Refusals -- grain CI's own check name, a
// duplicate spelling, or a server 400 -- sit beside the box rather than in a
// page-wide banner and clear as soon as the box is edited or a clean write
// lands.
@Component({
  selector: "g-repo-unfixable-checks",
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  styles: `
    .repo-unfixable-list {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      list-style: none;
      margin: 0;
      padding: 0;
    }

    .repo-unfixable-chip {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      padding: 4px 8px 4px 10px;
      border-radius: var(--md-sys-shape-corner-small);
      border: 1px solid var(--md-sys-color-outline-variant);
      background: var(--md-sys-color-surface-container-high);
      color: var(--md-sys-color-on-surface);
      font-family: var(--grain-font-mono);
      font-size: 12px;
      line-height: 18px;
      max-width: 100%;
      overflow-wrap: anywhere;
    }

    .repo-unfixable-chip.is-readonly {
      padding-right: 10px;
    }

    .repo-unfixable-remove {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      width: 20px;
      height: 20px;
      padding: 0;
      border: 0;
      border-radius: var(--md-sys-shape-corner-full);
      background: transparent;
      color: inherit;
      font-size: 15px;
      line-height: 1;
      cursor: pointer;
    }

    .repo-unfixable-remove:hover:not(:disabled) {
      background: var(--md-sys-color-surface-container-highest);
    }

    .repo-unfixable-remove:disabled {
      opacity: 0.5;
      cursor: not-allowed;
    }

    .repo-unfixable-form {
      display: flex;
      flex-wrap: wrap;
      align-items: flex-end;
      gap: 10px;
    }

    .repo-unfixable-form .s-field {
      flex: 1 1 240px;
    }
  `,
  template: `
    <div class="s-card" data-testid="card-repo-unfixable-checks">
      <div class="s-card-head">
        <div>
          <h2 class="s-card-title">Checks no commit here can clear</h2>
          <p class="s-card-desc">
            Checks on {{ repo() }} that will never pass for a pull request
            grain&rsquo;s agents author, whatever is pushed to the branch — a
            CLA bot asserting who signed an agreement is the case this exists
            for. <strong>This does not exempt them.</strong> A pull request with
            one of these red is still not merged, and a person clears it or
            nothing merges; what grain stops spending is the base merge and the
            repair run it would have made trying to reach a green that is not
            reachable, saying so on the row and in one comment instead. Not for
            a job that is merely flaky: a real regression in a check named here
            still holds the queue, which is exactly why naming one is safe.
          </p>
        </div>
      </div>

      @if (configRepoOnly()) {
        <p class="s-hint" data-testid="unfixable-checks-config-managed">
          {{ repo() }}'s unfixable checks are managed in the config repository
          (tables/repo_config.json); edit them there.
        </p>
      } @else if (denied()) {
        <p
          class="s-hint"
          id="unfixable-checks-denied"
          data-testid="unfixable-checks-denied"
        >
          {{ denied() }}
        </p>
      }

      @if (loadError(); as err) {
        <p
          class="s-field-error"
          role="alert"
          data-testid="unfixable-checks-load-error"
        >
          Could not read unfixable checks: {{ err }}
        </p>
      } @else if (checks() === null) {
        <p class="s-hint">Loading unfixable checks…</p>
      } @else {
        @let list = checks()!;
        @if (list.length === 0) {
          <p class="s-hint" data-testid="unfixable-checks-empty">
            grain treats every check {{ repo() }} reports as one a commit could
            turn green.
          </p>
        } @else {
          <ul
            class="repo-unfixable-list"
            aria-label="Unfixable checks"
            data-testid="unfixable-checks-list"
          >
            @for (check of list; track check) {
              <li
                class="repo-unfixable-chip"
                [class.is-readonly]="readOnly()"
                data-testid="unfixable-check-chip"
              >
                <span>{{ check }}</span>
                @if (!readOnly()) {
                  <button
                    type="button"
                    class="repo-unfixable-remove"
                    [attr.aria-label]="'Stop declaring ' + check + ' unfixable'"
                    [attr.title]="'Stop declaring ' + check + ' unfixable'"
                    [attr.data-testid]="'remove-unfixable-check-' + check"
                    [disabled]="saving()"
                    (click)="remove(check)"
                  >
                    ×
                  </button>
                }
              </li>
            }
          </ul>
        }

        <form class="repo-unfixable-form" (submit)="add($event)">
          <div class="s-field">
            <label class="s-label" for="unfixable-check-input">
              Check that cannot pass
            </label>
            <input
              id="unfixable-check-input"
              class="s-input tabular s-mono"
              type="text"
              autocomplete="off"
              autocapitalize="off"
              spellcheck="false"
              placeholder="cla/google"
              data-testid="input-unfixable-check"
              [value]="draft()"
              [disabled]="readOnly() || saving()"
              [attr.aria-invalid]="saveError() ? 'true' : null"
              [attr.aria-describedby]="
                saveError() ? 'unfixable-checks-error' : 'unfixable-checks-hint'
              "
              (input)="onDraftInput($any($event.target).value)"
            />
          </div>
          <button
            type="submit"
            class="btn-primary s-btn-sm"
            data-testid="add-unfixable-check-btn"
            [disabled]="readOnly() || saving() || !draft().trim()"
          >
            Add
          </button>
        </form>

        @if (saveError(); as err) {
          <p
            id="unfixable-checks-error"
            class="s-field-error"
            role="alert"
            data-testid="unfixable-checks-error"
          >
            {{ err }}
          </p>
        }

        <p id="unfixable-checks-hint" class="s-hint">
          The check&rsquo;s exact name, as the pull request reports it — matched
          without case, never by part of a name. {{ grainCIName() }} cannot be
          named here: it judges this exact commit, so its verdict is always the
          branch&rsquo;s own.
        </p>
      }
    </div>
  `,
})
export class RepoUnfixableChecksComponent {
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);

  readonly repo = input.required<string>();
  readonly configRepoOnly = input(false);

  protected readonly checks = signal<string[] | null>(null);
  protected readonly grainCIName = signal("grain CI");
  protected readonly draft = signal("");
  protected readonly loadError = signal<string | null>(null);
  protected readonly saveError = signal<string | null>(null);
  protected readonly saving = signal(false);

  constructor() {
    effect(() => {
      this.repo();
      untracked(() => void this.load());
    });
  }

  protected readOnly(): boolean {
    return (
      this.configRepoOnly() ||
      !this.access.can(
        "PUT /api/repos/{owner}/{name}/unfixable-checks",
        this.repo(),
      )
    );
  }

  protected denied(): string {
    return this.access.deniedReason(
      "PUT /api/repos/{owner}/{name}/unfixable-checks",
      this.repo(),
    );
  }

  protected onDraftInput(next: string): void {
    this.draft.set(next);
    if (this.saveError()) this.saveError.set(null);
  }

  private async load(): Promise<void> {
    const key = "GET /api/repos/{owner}/{name}/unfixable-checks";
    this.checks.set(null);
    this.loadError.set(null);
    this.saveError.set(null);
    if (!this.access.can(key, this.repo())) {
      this.loadError.set(this.access.deniedReason(key, this.repo()));
      return;
    }
    try {
      const out = await this.api.get<UnfixableChecksResponse>(
        key,
        splitRepo(this.repo()),
      );
      this.checks.set([...(out?.unfixableChecks ?? [])]);
      if (out?.grainCICheckName) {
        this.grainCIName.set(out.grainCICheckName);
      }
    } catch (err) {
      this.loadError.set(errText(err));
    }
  }

  protected async add(ev: Event): Promise<void> {
    ev.preventDefault();
    if (this.readOnly() || this.saving()) return;
    const name = this.draft().trim();
    if (!name) return;
    const current = this.checks() ?? [];
    if (sameCheckName(name, this.grainCIName())) {
      this.saveError.set(
        `${this.grainCIName()} judges one exact commit on the branch, so its verdict is always that branch's own answer and declaring it here would change nothing. Grain would go on repairing it.`,
      );
      return;
    }
    if (current.some((c) => sameCheckName(c, name))) {
      this.saveError.set(
        `${this.repo()} already declares ${name} unfixable — check names are matched without case.`,
      );
      return;
    }
    if (await this.save([...current, name])) {
      this.draft.set("");
    }
  }

  protected async remove(check: string): Promise<void> {
    if (this.readOnly() || this.saving()) return;
    const current = this.checks() ?? [];
    await this.save(current.filter((other) => !sameCheckName(other, check)));
  }

  private async save(next: string[]): Promise<boolean> {
    this.saving.set(true);
    this.saveError.set(null);
    try {
      const out = await this.api.put<UnfixableChecksResponse>(
        "PUT /api/repos/{owner}/{name}/unfixable-checks",
        splitRepo(this.repo()),
        { unfixableChecks: next },
      );
      this.checks.set([...(out?.unfixableChecks ?? next)]);
      if (out?.grainCICheckName) {
        this.grainCIName.set(out.grainCICheckName);
      }
      return true;
    } catch (err) {
      this.saveError.set(errText(err));
      return false;
    } finally {
      this.saving.set(false);
    }
  }
}
