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
} from "@angular/core";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import { InfoPopoverComponent } from "../shared/info-popover.component";
import {
  type JsonSchema,
  type SchemaField,
  deref,
  enumLabel,
  fieldApplies,
  gcpCredentialMode,
  newItem,
  schemaFields,
} from "./capability-server";

// SecretSlot is what a write-only field shows instead of its value: the
// chip (set / missing / not set, or managed by grain) and what has been
// typed to replace it, which is sent as configSecrets and never stored
// in the entry itself.
export interface SecretSlot {
  state: "set" | "missing" | "unset";
  typed: string;
  managed?: { present: boolean; overridden: boolean };
}

const SECRET_CHIP: Record<SecretSlot["state"], string> = {
  set: "set",
  missing: "missing",
  unset: "not set",
};

// CapabilitySchemaFormComponent draws a capability server's configure
// schema (§4.5) as a form: strings, numbers, choices (with the schema's
// own x-grain-enum-labels), switches, lists one per line, lists of
// objects as repeated groups, nested objects, and JSON for anything
// else. It calls itself for a nested object or list item, and only
// top-level fields can be secrets.
@Component({
  selector: "g-cap-schema-form",
  imports: [PillSelectComponent, InfoPopoverComponent],
  changeDetection: ChangeDetectionStrategy.OnPush,
  encapsulation: ViewEncapsulation.None,
  template: `
    <div class="cap-form" [class.cap-form-nested]="!!path()">
      @for (f of fields(); track f.name) {
        @let id = idFor(f.name);
        @let err = errors()[pathFor(f.name)];
        @if (applies(f.name)) {
          <div
            class="s-field cap-field"
            [class.cap-field-wide]="
              f.kind === 'objects' ||
              f.kind === 'object' ||
              f.kind === 'json' ||
              f.kind === 'lines'
            "
            [attr.data-field]="pathFor(f.name)"
          >
            <div class="cap-field-label">
              @if (f.kind === "objects" || f.kind === "object") {
                <span class="s-label" [id]="id + '-label'">{{ f.label }}</span>
              } @else {
                <label class="s-label" [attr.for]="id">{{ f.label }}</label>
              }
              @if (f.required) {
                <span class="cap-req" aria-hidden="true">required</span>
              }
              @if (f.schema.description) {
                <g-info-popover
                  [text]="f.schema.description"
                  [label]="f.label"
                  [testid]="'info-' + pathFor(f.name)"
                />
              }
            </div>

            @if (f.secret && !path()) {
              @let slot = secrets()[f.name];
              <div class="cap-secret">
                @if (slot?.managed; as m) {
                  <span
                    class="status-pill"
                    [class.healthy]="m.present"
                    [class.blast]="!m.present"
                    [attr.data-testid]="'secret-chip-' + f.name"
                    >{{
                      m.overridden
                        ? "overridden"
                        : m.present
                          ? "managed by grain"
                          : "managed, not made yet"
                    }}</span
                  >
                  <button
                    type="button"
                    class="btn-text s-btn-sm"
                    [attr.data-testid]="'rotate-' + f.name"
                    [disabled]="disabled()"
                    (click)="rotate.emit()"
                  >
                    Rotate
                  </button>
                } @else {
                  <span
                    class="status-pill"
                    [class.healthy]="slot?.state === 'set'"
                    [class.blast]="slot?.state === 'missing'"
                    [class.neutral]="!slot || slot.state === 'unset'"
                    [attr.data-testid]="'secret-chip-' + f.name"
                    >{{ SECRET_CHIP[slot?.state ?? "unset"] }}</span
                  >
                }
              </div>
              <input
                class="s-input"
                type="password"
                autocomplete="new-password"
                [id]="id"
                [attr.data-testid]="'cfg-' + f.name"
                [attr.aria-invalid]="err ? true : null"
                [placeholder]="
                  slot?.managed
                    ? 'Type a value only to override it'
                    : slot?.state === 'set'
                      ? 'Leave empty to keep the stored value'
                      : 'Type the value to store'
                "
                [value]="slot?.typed ?? ''"
                [disabled]="disabled()"
                (input)="
                  secretChange.emit({
                    name: f.name,
                    value: $any($event.target).value,
                  })
                "
              />
            } @else {
              @switch (f.kind) {
                @case ("enum") {
                  <g-pill-select
                    align="left"
                    [testid]="'cfg-' + pathFor(f.name)"
                    [ariaLabel]="f.label"
                    [options]="enumOptions(f)"
                    [value]="str(value()[f.name])"
                    [placeholder]="
                      f.schema.default !== undefined
                        ? 'Not set (server default ' + f.schema.default + ')'
                        : 'Not set'
                    "
                    [disabled]="disabled()"
                    (valueChange)="
                      set(f.name, $event === '' ? undefined : $event)
                    "
                  />
                }
                @case ("boolean") {
                  <button
                    type="button"
                    class="g-switch"
                    role="switch"
                    [id]="id"
                    [attr.data-testid]="'cfg-' + pathFor(f.name)"
                    [attr.aria-checked]="value()[f.name] === true"
                    [attr.aria-label]="f.label"
                    [disabled]="disabled()"
                    (click)="
                      set(f.name, value()[f.name] === true ? undefined : true)
                    "
                  ></button>
                }
                @case ("number") {
                  <input
                    class="s-input s-num"
                    type="number"
                    [id]="id"
                    [attr.data-testid]="'cfg-' + pathFor(f.name)"
                    [attr.aria-invalid]="err ? true : null"
                    [attr.min]="f.schema.minimum ?? null"
                    [attr.max]="f.schema.maximum ?? null"
                    [placeholder]="
                      f.schema.default !== undefined
                        ? '' + f.schema.default
                        : ''
                    "
                    [value]="value()[f.name] ?? ''"
                    [disabled]="disabled()"
                    (input)="setNumber(f.name, $any($event.target).value)"
                  />
                }
                @case ("string") {
                  <input
                    class="s-input"
                    type="text"
                    spellcheck="false"
                    [id]="id"
                    [attr.data-testid]="'cfg-' + pathFor(f.name)"
                    [attr.aria-invalid]="err ? true : null"
                    [placeholder]="
                      f.schema.default !== undefined
                        ? '' + f.schema.default
                        : ''
                    "
                    [value]="str(value()[f.name])"
                    [disabled]="disabled()"
                    (input)="setText(f.name, $any($event.target).value)"
                  />
                }
                @case ("lines") {
                  <textarea
                    class="s-textarea cap-lines"
                    spellcheck="false"
                    [id]="id"
                    [attr.data-testid]="'cfg-' + pathFor(f.name)"
                    [attr.aria-invalid]="err ? true : null"
                    [attr.aria-describedby]="id + '-hint'"
                    [value]="lines(value()[f.name])"
                    [disabled]="disabled()"
                    (input)="setLines(f, $any($event.target).value)"
                  ></textarea>
                  <span class="s-hint" [id]="id + '-hint'">One per line.</span>
                }
                @case ("objects") {
                  @let items = list(value()[f.name]);
                  <div
                    class="cap-items"
                    role="group"
                    [attr.aria-labelledby]="id + '-label'"
                    [attr.data-testid]="'cfg-' + pathFor(f.name)"
                  >
                    @for (item of items; track $index; let i = $index) {
                      <fieldset
                        class="cap-item"
                        [attr.data-testid]="'cfg-' + pathFor(f.name) + '-' + i"
                      >
                        <legend class="cap-item-head">
                          <span class="cap-item-title">{{
                            itemTitle(f, item, i)
                          }}</span>
                        </legend>
                        <button
                          type="button"
                          class="btn-text s-btn-sm cap-item-remove"
                          [attr.aria-label]="'Remove ' + itemTitle(f, item, i)"
                          [disabled]="disabled()"
                          (click)="removeItem(f.name, i)"
                        >
                          Remove
                        </button>
                        @if (gcpMode() && f.name === "permission_sets") {
                          <p
                            class="s-hint cap-mode"
                            [attr.data-testid]="'gcp-mode-' + i"
                          >
                            {{ gcpCredentialMode(item) }}
                          </p>
                        }
                        <g-cap-schema-form
                          [root]="root()"
                          [schema]="itemSchema(f)"
                          [value]="item"
                          [errors]="errors()"
                          [path]="pathFor(f.name) + '.' + i"
                          [idPrefix]="idPrefix()"
                          [disabled]="disabled()"
                          (valueChange)="setItem(f.name, i, $event)"
                        />
                      </fieldset>
                    }
                    <div>
                      <button
                        type="button"
                        class="btn-tonal s-btn-sm"
                        [attr.data-testid]="'add-' + pathFor(f.name)"
                        [disabled]="disabled()"
                        (click)="addItem(f)"
                      >
                        Add {{ singular(f.name.replace("_", " ")) }}
                      </button>
                    </div>
                  </div>
                }
                @case ("object") {
                  <div
                    class="cap-items"
                    role="group"
                    [attr.aria-labelledby]="id + '-label'"
                  >
                    <g-cap-schema-form
                      [root]="root()"
                      [schema]="f.schema"
                      [value]="obj(value()[f.name])"
                      [errors]="errors()"
                      [path]="pathFor(f.name)"
                      [idPrefix]="idPrefix()"
                      [disabled]="disabled()"
                      (valueChange)="setObject(f.name, $event)"
                    />
                  </div>
                }
                @default {
                  <textarea
                    class="s-textarea s-mono"
                    spellcheck="false"
                    [id]="id"
                    [attr.data-testid]="'cfg-' + pathFor(f.name)"
                    [attr.aria-invalid]="err ? true : null"
                    [value]="json(value()[f.name])"
                    [disabled]="disabled()"
                    (change)="setJson(f.name, $any($event.target))"
                  ></textarea>
                  <span class="s-hint">JSON.</span>
                }
              }
            }
            @if (err) {
              <span
                class="s-field-error"
                [attr.data-testid]="'cfg-error-' + pathFor(f.name)"
                >{{ err }}</span
              >
            }
          </div>
        }
      } @empty {
        @if (!path()) {
          <p class="s-hint">The server takes no configuration fields.</p>
        }
      }
      @if (!path() && errors()[""]; as e) {
        <span class="s-field-error cap-field-wide" data-testid="cfg-error">{{
          e
        }}</span>
      }
    </div>
  `,
})
export class CapabilitySchemaFormComponent {
  readonly root = input.required<JsonSchema>();
  readonly schema = input.required<JsonSchema>();
  readonly value = input.required<Record<string, unknown>>();
  readonly secrets = input<Record<string, SecretSlot>>({});
  // A refusal keyed by field path ("permission_sets.0.project").
  readonly errors = input<Record<string, string>>({});
  readonly path = input("");
  readonly idPrefix = input("cfg");
  readonly disabled = input(false);
  // gcp: say what each permission set's credential mode does.
  readonly gcpMode = input(false);

  readonly valueChange = output<Record<string, unknown>>();
  readonly secretChange = output<{ name: string; value: string }>();
  readonly rotate = output<void>();

  protected readonly SECRET_CHIP = SECRET_CHIP;
  protected readonly gcpCredentialMode = gcpCredentialMode;

  protected readonly fields = computed(() =>
    schemaFields(this.root(), this.schema()),
  );

  protected pathFor(name: string): string {
    return this.path() ? `${this.path()}.${name}` : name;
  }

  protected idFor(name: string): string {
    return `${this.idPrefix()}-${this.pathFor(name).replace(/[^A-Za-z0-9_-]/g, "-")}`;
  }

  // A field that does not apply is not drawn, unless it holds a value:
  // the server would refuse it, so it stays where it can be cleared.
  protected applies(name: string): boolean {
    if (this.value()[name] !== undefined) return true;
    return fieldApplies(
      name,
      this.value(),
      deref(this.root(), this.schema()) ?? {},
    );
  }

  protected enumOptions(f: SchemaField): PillOption[] {
    const opts: PillOption[] = (f.schema.enum ?? []).map((v) => ({
      value: String(v),
      label: enumLabel(f.schema, String(v)),
    }));
    if (!f.required) {
      opts.unshift({ value: "", label: "Not set" });
    }
    return opts;
  }

  protected str(v: unknown): string {
    return v === undefined || v === null ? "" : String(v);
  }

  protected lines(v: unknown): string {
    return Array.isArray(v) ? v.map((x) => String(x)).join("\n") : "";
  }

  protected list(v: unknown): Record<string, unknown>[] {
    return Array.isArray(v)
      ? v.map(
          (x) =>
            (x && typeof x === "object" ? x : {}) as Record<string, unknown>,
        )
      : [];
  }

  protected obj(v: unknown): Record<string, unknown> {
    return v && typeof v === "object" && !Array.isArray(v)
      ? (v as Record<string, unknown>)
      : {};
  }

  protected json(v: unknown): string {
    return v === undefined ? "" : JSON.stringify(v, null, 2);
  }

  protected itemSchema(f: SchemaField): JsonSchema {
    return deref(this.root(), f.schema.items) ?? {};
  }

  protected itemTitle(
    f: SchemaField,
    item: Record<string, unknown>,
    i: number,
  ): string {
    const name = item["name"];
    return typeof name === "string" && name
      ? `${singular(f.label)} ${name}`
      : `${singular(f.label)} ${i + 1}`;
  }

  protected singular(label: string): string {
    return singular(label);
  }

  protected set(name: string, v: unknown): void {
    const next = { ...this.value() };
    if (v === undefined) delete next[name];
    else next[name] = v;
    this.valueChange.emit(next);
  }

  protected setText(name: string, text: string): void {
    this.set(name, text === "" ? undefined : text);
  }

  protected setNumber(name: string, text: string): void {
    if (text === "") this.set(name, undefined);
    else if (!Number.isNaN(Number(text))) this.set(name, Number(text));
  }

  protected setLines(f: SchemaField, text: string): void {
    const it = deref(this.root(), f.schema.items);
    const numeric = it?.type === "integer" || it?.type === "number";
    const vals = text
      .split("\n")
      .map((l) => l.trim())
      .filter(Boolean)
      .map((l) => (numeric && !Number.isNaN(Number(l)) ? Number(l) : l));
    this.set(f.name, vals.length ? vals : undefined);
  }

  protected setJson(name: string, el: HTMLTextAreaElement): void {
    const text = el.value.trim();
    if (!text) {
      el.setCustomValidity("");
      this.set(name, undefined);
      return;
    }
    try {
      this.set(name, JSON.parse(text));
      el.setCustomValidity("");
    } catch {
      el.setCustomValidity("Not valid JSON");
      el.reportValidity();
    }
  }

  protected setItem(name: string, i: number, v: Record<string, unknown>): void {
    const items = [...this.list(this.value()[name])];
    items[i] = v;
    this.set(name, items);
  }

  protected setObject(name: string, v: Record<string, unknown>): void {
    this.set(name, Object.keys(v).length ? v : undefined);
  }

  protected addItem(f: SchemaField): void {
    const items = [...this.list(this.value()[f.name])];
    items.push(newItem(this.root(), f.schema.items));
    this.set(f.name, items);
  }

  protected removeItem(name: string, i: number): void {
    const items = this.list(this.value()[name]).filter((_, j) => j !== i);
    this.set(name, items.length ? items : undefined);
  }
}

function singular(label: string): string {
  const l = label.trim();
  return l.endsWith("s") ? l.slice(0, -1) : l;
}
