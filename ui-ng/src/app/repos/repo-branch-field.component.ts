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
  DestroyRef,
  ViewEncapsulation,
  computed,
  effect,
  inject,
  input,
  output,
  signal,
  untracked,
} from "@angular/core";
import { GrainApi, splitRepo } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import { BranchRefsService } from "../services/branch-refs.service";

interface BranchItem {
  name?: string;
}

// RepoBranchFieldComponent is the /next/ branch picker for comparing branches
// (mirroring classic ui/src/components/BranchField.jsx). It suggests branches
// from GitHub (via BranchRefsService) and grain's own recorded branches
// (GET /api/repos/{owner}/{name}/branches), hiding the dropdown once the box
// matches a single branch so it never covers the Compare button, and
// optionally warning after a brief pause when a typed branch does not exist
// on the remote (without blocking submission).
@Component({
  selector: "g-repo-branch-field",
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  styles: `
    .branch-field-wrap {
      position: relative;
      display: flex;
      flex-direction: column;
      gap: 6px;
      min-width: 0;
    }

    .branch-field-list {
      position: absolute;
      top: calc(100% + 2px);
      left: 0;
      right: 0;
      z-index: 20;
      margin: 0;
      padding: 4px 0;
      list-style: none;
      max-height: 240px;
      overflow-y: auto;
      border-radius: var(--md-sys-shape-corner-small);
      border: 1px solid var(--md-sys-color-outline-variant);
      background: var(--md-sys-color-surface-container-lowest);
      box-shadow: var(--md-sys-elevation-dialog);
    }

    .branch-field-opt {
      display: block;
      width: 100%;
      padding: 8px 12px;
      border: 0;
      background: transparent;
      color: var(--md-sys-color-on-surface);
      font-family: var(--grain-font-mono);
      font-size: 13px;
      line-height: 18px;
      text-align: left;
      cursor: pointer;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .branch-field-opt:hover,
    .branch-field-opt.is-highlighted {
      background: var(--md-sys-color-secondary-container);
      color: var(--md-sys-color-on-secondary-container);
    }
  `,
  template: `
    <div class="s-field branch-field-wrap">
      <label class="s-label" [attr.for]="inputId()">{{ label() }}</label>
      <input
        [id]="inputId()"
        class="s-input tabular s-mono"
        type="text"
        role="combobox"
        autocomplete="off"
        autocapitalize="off"
        spellcheck="false"
        [attr.data-testid]="testid() || null"
        [attr.placeholder]="placeholder() || null"
        [attr.aria-expanded]="showing()"
        aria-autocomplete="list"
        [attr.aria-controls]="showing() ? listId() : null"
        [attr.aria-activedescendant]="activeOptionId()"
        [attr.aria-describedby]="missing() ? missingId() : null"
        [required]="required()"
        [disabled]="disabled()"
        [value]="value()"
        (focus)="onFocus()"
        (blur)="onBlur()"
        (input)="onInput($any($event.target).value)"
        (keydown)="onKeydown($event)"
      />
      @if (showing()) {
        <ul
          class="branch-field-list"
          role="listbox"
          [id]="listId()"
          [attr.aria-label]="label() + ' suggestions'"
        >
          @for (branch of matches(); track branch; let i = $index) {
            <li role="presentation">
              <button
                type="button"
                role="option"
                class="branch-field-opt"
                [id]="optionId(i)"
                [class.is-highlighted]="i === highlight()"
                [attr.aria-selected]="i === highlight()"
                (mousedown)="pick(branch, $event)"
              >
                {{ branch }}
              </button>
            </li>
          }
        </ul>
      }
      @if (missing(); as miss) {
        <span
          [id]="missingId()"
          class="s-hint s-hint-warn"
          role="status"
          [attr.data-testid]="testid() ? testid() + '-missing' : null"
        >
          No branch named “{{ miss }}” in {{ repo() }} — {{ warnMissing() }}.
        </span>
      }
    </div>
  `,
})
export class RepoBranchFieldComponent {
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);
  private readonly branchRefs = inject(BranchRefsService);
  private readonly destroyRef = inject(DestroyRef);

  readonly repo = input.required<string>();
  readonly label = input.required<string>();
  readonly inputId = input.required<string>();
  readonly testid = input<string>("");
  readonly placeholder = input<string>("");
  readonly value = input<string>("");
  readonly options = input<readonly string[]>([]);
  readonly required = input<boolean>(false);
  readonly disabled = input<boolean>(false);
  readonly warnMissing = input<string>("");

  readonly valueChange = output<string>();

  protected readonly open = signal(false);
  protected readonly highlight = signal(0);
  protected readonly known = signal<string[]>([]);
  protected readonly missing = signal<string>("");

  private blurTimer: ReturnType<typeof setTimeout> | null = null;
  private warnTimer: ReturnType<typeof setTimeout> | null = null;
  private loadSeq = 0;

  protected readonly listId = computed(() => `${this.inputId()}-list`);
  protected readonly missingId = computed(() => `${this.inputId()}-missing`);

  protected readonly matches = computed(() => {
    const q = this.value().trim().toLowerCase();
    const seen = new Set<string>();
    const out: string[] = [];
    for (const branch of [...this.options(), ...this.known()]) {
      const clean = (branch ?? "").trim();
      if (!clean || seen.has(clean)) continue;
      seen.add(clean);
      if (q && !clean.toLowerCase().includes(q)) continue;
      out.push(clean);
      if (out.length === 8) break;
    }
    return out;
  });

  protected readonly showing = computed(() => {
    if (!this.open() || this.disabled()) return false;
    const list = this.matches();
    if (list.length === 0) return false;
    const settled = list.length === 1 && list[0] === this.value().trim();
    return !settled;
  });

  protected readonly activeOptionId = computed(() => {
    if (!this.showing()) return null;
    const idx = this.highlight();
    return idx >= 0 && idx < this.matches().length ? this.optionId(idx) : null;
  });

  constructor() {
    effect(() => {
      this.repo();
      untracked(() => {
        this.known.set([]);
        this.missing.set("");
      });
    });

    effect(() => {
      const repo = this.repo().trim();
      const text = this.value().trim();
      const warn = this.warnMissing();
      untracked(() => {
        this.clearWarnTimer();
        this.missing.set("");
        if (!warn || !text || !repo || !repo.includes("/")) return;
        const seq = ++this.loadSeq;
        this.warnTimer = setTimeout(() => {
          void this.checkMissing(repo, text, seq);
        }, 350);
      });
    });

    this.destroyRef.onDestroy(() => {
      if (this.blurTimer) clearTimeout(this.blurTimer);
      this.clearWarnTimer();
    });
  }

  protected optionId(i: number): string {
    return `${this.inputId()}-opt-${i}`;
  }

  protected onFocus(): void {
    if (this.blurTimer) {
      clearTimeout(this.blurTimer);
      this.blurTimer = null;
    }
    this.open.set(true);
    this.highlight.set(0);
    void this.loadSuggestions();
  }

  protected onBlur(): void {
    this.blurTimer = setTimeout(() => {
      this.open.set(false);
    }, 120);
  }

  protected onInput(next: string): void {
    this.open.set(true);
    this.highlight.set(0);
    this.valueChange.emit(next);
  }

  protected pick(branch: string, ev?: Event): void {
    ev?.preventDefault();
    this.valueChange.emit(branch);
    this.open.set(false);
  }

  protected onKeydown(ev: KeyboardEvent): void {
    if (!this.showing()) {
      if (ev.key === "ArrowDown" && this.matches().length > 0) {
        this.open.set(true);
        this.highlight.set(0);
        ev.preventDefault();
      }
      return;
    }
    const list = this.matches();
    if (ev.key === "ArrowDown") {
      ev.preventDefault();
      this.highlight.set((this.highlight() + 1) % list.length);
    } else if (ev.key === "ArrowUp") {
      ev.preventDefault();
      this.highlight.set((this.highlight() - 1 + list.length) % list.length);
    } else if (ev.key === "Enter") {
      const picked = list[this.highlight()];
      if (picked && picked !== this.value().trim()) {
        ev.preventDefault();
        this.pick(picked);
      }
    } else if (ev.key === "Escape") {
      ev.preventDefault();
      this.open.set(false);
    }
  }

  private clearWarnTimer(): void {
    if (this.warnTimer) {
      clearTimeout(this.warnTimer);
      this.warnTimer = null;
    }
  }

  private async loadSuggestions(): Promise<void> {
    const repo = this.repo().trim();
    if (!repo || !repo.includes("/") || repo.endsWith("/")) return;
    const names = await this.fetchAllBranches(repo);
    if (this.repo().trim() === repo) {
      this.known.set(names);
    }
  }

  private async checkMissing(
    repo: string,
    branch: string,
    seq: number,
  ): Promise<void> {
    // Only warn when GitHub's branch listing actually answered (non-null):
    // an unreadable remote means "cannot tell", never "missing".
    const refs = await this.branchRefs.branchesFor(repo);
    if (seq !== this.loadSeq || this.repo().trim() !== repo) return;
    if (refs === null) return;
    if (refs.has(branch)) return;
    const all = await this.fetchAllBranches(repo);
    if (seq !== this.loadSeq || this.repo().trim() !== repo) return;
    if (!all.includes(branch) && !this.options().includes(branch)) {
      this.missing.set(branch);
    }
  }

  private async fetchAllBranches(repo: string): Promise<string[]> {
    const p = splitRepo(repo);
    const grainReq =
      p.owner &&
      p.name &&
      this.access.can("GET /api/repos/{owner}/{name}/branches", repo)
        ? this.api
            .get<BranchItem[] | { branches?: BranchItem[] }>(
              "GET /api/repos/{owner}/{name}/branches",
              p,
            )
            .catch(() => null)
        : Promise.resolve(null);
    const [ghSet, grainRes] = await Promise.all([
      this.branchRefs.branchesFor(repo).catch(() => null),
      grainReq,
    ]);
    const out: string[] = [];
    const seen = new Set<string>();
    const add = (raw?: string) => {
      const clean = (raw ?? "").trim();
      if (!clean || seen.has(clean)) return;
      seen.add(clean);
      out.push(clean);
    };
    // Remote branches first (main, release branches, pushed feature branches),
    // then grain's own recorded branches.
    if (ghSet) {
      for (const b of ghSet) add(b);
    }
    const grainList = Array.isArray(grainRes)
      ? grainRes
      : (grainRes?.branches ?? []);
    for (const item of grainList) {
      add(typeof item === "string" ? item : item?.name);
    }
    return out;
  }
}
