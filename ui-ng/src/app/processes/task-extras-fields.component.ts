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
  computed,
  input,
  output,
  signal,
} from "@angular/core";
import type { CustomFieldDeclaration } from "../api/models";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import {
  NETWORK_ACCESS_OPTIONS,
  type WriteTarget,
  customFieldValueList,
} from "./processes.models";

/**
 * TaskExtrasFieldsComponent renders the Network access, Reads, Writes and
 * Custom fields controls shared by the Processes schedule and template
 * dialogs. Keeping them in one component guarantees both dialogs carry the
 * same options, chip lists, and testids (`sched-*` / `tmpl-*`).
 */
@Component({
  selector: "g-task-extras-fields",
  imports: [PillSelectComponent],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { style: "display: contents" },
  template: `
    <div class="field">
      <span>Network access</span>
      <g-pill-select
        [options]="networkAccessOptions"
        [value]="networkAccess()"
        ariaLabel="Network access"
        align="left"
        [testid]="prefix() + '-network'"
        (valueChange)="networkAccessChange.emit($event)"
      />
      <p
        class="muted proc-field-help"
        [attr.data-testid]="prefix() + '-network-help'"
      >
        Fixes the network of every task this {{ noun() }} files. A task with no
        network still reaches this deployment's git proxy.
      </p>
    </div>
    <div class="field">
      <span>Reads (extra read-only repositories)</span>
      @if (reads().length > 0) {
        <ul
          class="proc-chip-list"
          aria-label="Read-only repositories"
          [attr.data-testid]="prefix() + '-reads-chips'"
        >
          @for (r of reads(); track r) {
            <li class="proc-chip">
              <code class="proc-chip-label tabular">{{ r }}</code>
              <button
                type="button"
                class="proc-chip-remove"
                [attr.aria-label]="'Remove read repository ' + r"
                [attr.data-testid]="prefix() + '-remove-read-' + r"
                (click)="removeRead(r)"
              >
                ×
              </button>
            </li>
          }
        </ul>
      }
      <div class="proc-inline-row">
        @if (readRepoOptions().length > 1) {
          <g-pill-select
            [options]="readRepoOptions()"
            value=""
            placeholder="Add read-only repository…"
            ariaLabel="Add read-only repository"
            align="left"
            [testid]="prefix() + '-reads-select'"
            (valueChange)="addRead($event)"
          />
        }
        <input
          type="text"
          aria-label="Read-only repository (owner/name)"
          [attr.data-testid]="prefix() + '-reads-input'"
          placeholder="owner/name"
          [value]="readInput()"
          (input)="readInput.set($any($event.target).value)"
          (keydown.enter)="$event.preventDefault(); addReadFromInput()"
        />
        <button
          type="button"
          class="btn-tonal"
          [attr.data-testid]="prefix() + '-reads-add'"
          [disabled]="!readInput().trim()"
          (click)="addReadFromInput()"
        >
          Add
        </button>
      </div>
    </div>
    <div class="field">
      <span>Writes (additional write targets)</span>
      @if (writes().length > 0) {
        <ul
          class="proc-chip-list"
          aria-label="Additional write targets"
          [attr.data-testid]="prefix() + '-writes-chips'"
        >
          @for (w of writes(); track w.repo + ":" + (w.base || "")) {
            <li class="proc-chip">
              <code class="proc-chip-label tabular"
                >{{ w.repo }}{{ w.base ? " @ " + w.base : "" }}</code
              >
              <button
                type="button"
                class="proc-chip-remove"
                [attr.aria-label]="
                  'Remove write target ' +
                  w.repo +
                  (w.base ? ' @ ' + w.base : '')
                "
                [attr.data-testid]="prefix() + '-remove-write-' + $index"
                (click)="removeWrite($index)"
              >
                ×
              </button>
            </li>
          }
        </ul>
      }
      <div class="proc-inline-row">
        @if (writeRepoOptions().length > 1) {
          <g-pill-select
            [options]="writeRepoOptions()"
            [value]="selectedWriteRepo()"
            placeholder="Select repo…"
            ariaLabel="Additional write repository"
            align="left"
            [testid]="prefix() + '-writes-repo-select'"
            (valueChange)="$event && writeRepo.set($event)"
          />
        }
        <input
          type="text"
          aria-label="Additional write repository (owner/name)"
          [attr.data-testid]="prefix() + '-writes-repo-input'"
          placeholder="owner/name"
          [value]="writeRepo()"
          (input)="writeRepo.set($any($event.target).value)"
          (keydown.enter)="$event.preventDefault(); addWrite()"
        />
        <input
          type="text"
          aria-label="Additional write base branch"
          [attr.data-testid]="prefix() + '-writes-base-input'"
          placeholder="Default branch"
          [value]="writeBase()"
          (input)="writeBase.set($any($event.target).value)"
          (keydown.enter)="$event.preventDefault(); addWrite()"
        />
        <button
          type="button"
          class="btn-tonal"
          [attr.data-testid]="prefix() + '-writes-add'"
          [disabled]="!writeRepo().trim()"
          (click)="addWrite()"
        >
          Add
        </button>
      </div>
    </div>
    @if (declaredCustomFields().length > 0) {
      <div
        class="proc-field-grid"
        style="margin-top: 4px"
        [attr.data-testid]="prefix() + '-custom-fields'"
      >
        @for (cf of declaredCustomFields(); track cf.name) {
          <div class="field">
            <span>{{ cf.label || cf.name }}</span>
            @if (cf.multiple) {
              @if (customFieldList(cf.name).length > 0) {
                <ul
                  class="proc-chip-list"
                  [attr.aria-label]="cf.label || cf.name"
                  [attr.data-testid]="prefix() + '-cf-chips-' + cf.name"
                >
                  @for (v of customFieldList(cf.name); track v) {
                    <li class="proc-chip">
                      <span class="proc-chip-label">{{ v }}</span>
                      <button
                        type="button"
                        class="proc-chip-remove"
                        [attr.aria-label]="
                          'Remove ' + (cf.label || cf.name) + ' ' + v
                        "
                        (click)="removeCustomFieldItem(cf.name, v)"
                      >
                        ×
                      </button>
                    </li>
                  }
                </ul>
              }
              @if (cf.type === "enum" || (cf.options ?? []).length > 0) {
                <g-pill-select
                  [options]="customMultiEnumOptions(cf)"
                  value=""
                  placeholder="Add value…"
                  [ariaLabel]="cf.label || cf.name"
                  align="left"
                  [testid]="prefix() + '-cf-' + cf.name"
                  (valueChange)="addCustomFieldItem(cf.name, $event)"
                />
              } @else {
                <div class="proc-inline-row">
                  <input
                    [type]="
                      cf.type === 'number'
                        ? 'number'
                        : cf.type === 'date'
                          ? 'date'
                          : 'text'
                    "
                    [attr.aria-label]="cf.label || cf.name"
                    [attr.data-testid]="prefix() + '-cf-input-' + cf.name"
                    [value]="customInput()[cf.name] || ''"
                    (input)="setCustomInput(cf.name, $any($event.target).value)"
                    (keydown.enter)="
                      $event.preventDefault(); commitCustomInput(cf.name)
                    "
                  />
                  <button
                    type="button"
                    class="btn-tonal"
                    [attr.data-testid]="prefix() + '-cf-add-' + cf.name"
                    [disabled]="!(customInput()[cf.name] || '').trim()"
                    (click)="commitCustomInput(cf.name)"
                  >
                    Add
                  </button>
                </div>
              }
            } @else if (cf.type === "boolean" || $any(cf.type) === "bool") {
              <g-pill-select
                [options]="customBooleanOptions"
                [value]="customFieldScalar(cf.name)"
                [ariaLabel]="cf.label || cf.name"
                align="left"
                [testid]="prefix() + '-cf-' + cf.name"
                (valueChange)="setCustomField(cf.name, $event)"
              />
            } @else if (cf.type === "enum" || (cf.options ?? []).length > 0) {
              <g-pill-select
                [options]="customEnumOptions(cf)"
                [value]="customFieldScalar(cf.name)"
                [ariaLabel]="cf.label || cf.name"
                align="left"
                [testid]="prefix() + '-cf-' + cf.name"
                (valueChange)="setCustomField(cf.name, $event)"
              />
            } @else {
              <input
                [type]="
                  cf.type === 'number'
                    ? 'number'
                    : cf.type === 'date'
                      ? 'date'
                      : 'text'
                "
                [attr.aria-label]="cf.label || cf.name"
                [attr.data-testid]="prefix() + '-cf-' + cf.name"
                [value]="customFieldScalar(cf.name)"
                (input)="setCustomField(cf.name, $any($event.target).value)"
              />
            }
            @if (cf.description) {
              <span class="muted proc-field-help">{{ cf.description }}</span>
            }
          </div>
        }
      </div>
    }
  `,
})
export class TaskExtrasFieldsComponent {
  readonly prefix = input.required<string>();
  readonly noun = input.required<string>();
  readonly mainRepo = input<string>("");
  readonly knownRepos = input<readonly string[]>([]);
  readonly reachableRepos = input<readonly string[]>([]);
  readonly declaredCustomFields = input<readonly CustomFieldDeclaration[]>([]);

  readonly networkAccess = input<string>("");
  readonly reads = input<readonly string[]>([]);
  readonly writes = input<readonly WriteTarget[]>([]);
  readonly customFields = input<Record<string, string | string[]>>({});

  readonly networkAccessChange = output<string>();
  readonly readsChange = output<string[]>();
  readonly writesChange = output<WriteTarget[]>();
  readonly customFieldsChange = output<Record<string, string | string[]>>();

  protected readonly readInput = signal("");
  protected readonly writeRepo = signal("");
  protected readonly writeBase = signal("");
  protected readonly customInput = signal<Record<string, string>>({});

  protected readonly networkAccessOptions: PillOption[] =
    NETWORK_ACCESS_OPTIONS.map((o) => ({ value: o.value, label: o.label }));
  protected readonly customBooleanOptions: PillOption[] = [
    { value: "", label: "Not set" },
    { value: "true", label: "Yes" },
    { value: "false", label: "No" },
  ];

  protected readonly readRepoOptions = computed<PillOption[]>(() => {
    const seen = new Set<string>();
    const main = this.mainRepo().trim().toLowerCase();
    for (const r of this.reads()) seen.add(r.toLowerCase());
    if (main) seen.add(main);
    const candidates: string[] = [];
    for (const r of [...this.knownRepos(), ...this.reachableRepos()]) {
      const key = r.trim().toLowerCase();
      if (!key || seen.has(key)) continue;
      seen.add(key);
      candidates.push(r.trim());
    }
    candidates.sort((a, b) => a.localeCompare(b));
    return [
      { value: "", label: "Add read-only repository…" },
      ...candidates.map((r) => ({ value: r, label: r })),
    ];
  });

  protected readonly writeRepoOptions = computed<PillOption[]>(() => {
    const main = this.mainRepo().trim().toLowerCase();
    const candidates = this.knownRepos().filter(
      (r) => !main || r.trim().toLowerCase() !== main,
    );
    return [
      { value: "", label: "Select repo…" },
      ...candidates.map((r) => ({ value: r, label: r })),
    ];
  });

  protected readonly selectedWriteRepo = computed(() => {
    const cur = this.writeRepo();
    return this.writeRepoOptions().some((o) => o.value === cur) ? cur : "";
  });

  protected addRead(repo: string): void {
    const r = repo.trim();
    if (!r) return;
    const main = this.mainRepo().trim().toLowerCase();
    if (main && r.toLowerCase() === main) return;
    const list = this.reads();
    if (list.some((x) => x.toLowerCase() === r.toLowerCase())) return;
    this.readsChange.emit([...list, r]);
  }

  protected addReadFromInput(): void {
    const r = this.readInput().trim();
    if (!r) return;
    this.addRead(r);
    this.readInput.set("");
  }

  protected removeRead(repo: string): void {
    this.readsChange.emit(this.reads().filter((x) => x !== repo));
  }

  protected addWrite(): void {
    const repo = this.writeRepo().trim();
    if (!repo) return;
    const base = this.writeBase().trim();
    const list = this.writes();
    const idx = list.findIndex(
      (w) =>
        w.repo.toLowerCase() === repo.toLowerCase() &&
        (w.base ?? "").trim() === base,
    );
    const item: WriteTarget = base ? { repo, base } : { repo };
    const next =
      idx >= 0 ? list.map((w, i) => (i === idx ? item : w)) : [...list, item];
    this.writesChange.emit(next);
    this.writeRepo.set("");
    this.writeBase.set("");
  }

  protected removeWrite(idx: number): void {
    this.writesChange.emit(this.writes().filter((_, i) => i !== idx));
  }

  protected customFieldScalar(name: string): string {
    const v = this.customFields()[name];
    if (Array.isArray(v)) return v[0] ?? "";
    return v ?? "";
  }

  protected customFieldList(name: string): string[] {
    return customFieldValueList(this.customFields()[name]);
  }

  protected setCustomField(name: string, val: string): void {
    this.customFieldsChange.emit({ ...this.customFields(), [name]: val });
  }

  protected setCustomInput(name: string, val: string): void {
    this.customInput.update((m) => ({ ...m, [name]: val }));
  }

  protected addCustomFieldItem(name: string, val: string): void {
    const v = val.trim();
    if (!v) return;
    const m = this.customFields();
    const cur = customFieldValueList(m[name]);
    if (cur.includes(v)) return;
    this.customFieldsChange.emit({ ...m, [name]: [...cur, v] });
  }

  protected commitCustomInput(name: string): void {
    const raw = (this.customInput()[name] || "").trim();
    if (!raw) return;
    this.addCustomFieldItem(name, raw);
    this.customInput.update((m) => ({ ...m, [name]: "" }));
  }

  protected removeCustomFieldItem(name: string, val: string): void {
    const m = this.customFields();
    this.customFieldsChange.emit({
      ...m,
      [name]: customFieldValueList(m[name]).filter((x) => x !== val),
    });
  }

  protected customEnumOptions(cf: CustomFieldDeclaration): PillOption[] {
    const opts: PillOption[] = [{ value: "", label: "Not set" }];
    const seen = new Set<string>([""]);
    for (const o of cf.options ?? []) {
      if (!seen.has(o)) {
        seen.add(o);
        opts.push({ value: o, label: o });
      }
    }
    const cur = this.customFieldScalar(cf.name);
    if (cur && !seen.has(cur)) {
      opts.push({ value: cur, label: `${cur} (saved)` });
    }
    return opts;
  }

  protected customMultiEnumOptions(cf: CustomFieldDeclaration): PillOption[] {
    const chosen = new Set(this.customFieldList(cf.name));
    return [
      { value: "", label: "Add value…" },
      ...(cf.options ?? [])
        .filter((o) => !chosen.has(o))
        .map((o) => ({ value: o, label: o })),
    ];
  }
}
