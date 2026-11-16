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
  effect,
  forwardRef,
  input,
  output,
  signal,
  untracked,
} from "@angular/core";
import {
  type JsonSchema,
  deref,
  fieldKind,
  initialValue,
  isFixedList,
  isHiddenProperty,
  isMultiline,
  isSecretField,
  isSecretLooking,
  isStringMap,
  pickVariant,
  unsupportedReason,
  validateValue,
  variantTitle,
} from "../shared/capability-args";
import { InfoPopoverComponent } from "../shared/info-popover.component";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";

export interface ServerConfigSecretsContext {
  fields: Record<string, "set" | "missing" | "unset">;
  values: Record<string, string>;
  managed: Record<string, { field: string; overridden?: boolean }>;
  canRotate: boolean;
}

const SECRET_STATE_LABELS: Readonly<Record<string, string>> = {
  set: "set",
  missing: "missing",
  unset: "not set",
};

function fieldLabel(
  name: string,
  schema: JsonSchema | null | undefined,
  required: boolean,
): string {
  const title = schema?.title || name;
  return required ? `${title} *` : title;
}

function asRecord(v: unknown): Record<string, unknown> {
  return v && typeof v === "object" && !Array.isArray(v)
    ? (v as Record<string, unknown>)
    : {};
}

@Component({
  selector: "g-capability-secret-field",
  imports: [InfoPopoverComponent],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="s-field" [attr.data-secret-field]="name()">
      <div class="caf-label-row">
        <label class="s-label" [attr.for]="fieldId()">{{ labelText() }}</label>
        <g-info-popover
          [title]="schema()?.description"
          [label]="schema()?.title || name()"
        />
        <span
          [class]="'status-pill ' + stateTone()"
          [attr.data-testid]="'secret-state-' + name()"
          >{{ stateLabel() }}</span
        >
      </div>
      <input
        [id]="fieldId()"
        class="s-input s-mono"
        type="password"
        autocomplete="new-password"
        [disabled]="disabled()"
        [value]="value()"
        [placeholder]="state() === 'set' ? '••••••••' : ''"
        [attr.aria-invalid]="error() ? 'true' : null"
        (input)="valueChange.emit($any($event.target).value)"
      />
      @if (error(); as err) {
        <span class="s-field-error">{{ err }}</span>
      } @else {
        <span class="s-hint">{{
          state() === "set"
            ? "Stored in the secrets store. Type a new value to replace it."
            : "Stored only in the secrets store."
        }}</span>
      }
    </div>
  `,
})
export class CapabilitySecretFieldComponent {
  readonly name = input.required<string>();
  readonly schema = input<JsonSchema | null>(null);
  readonly required = input<boolean>(false);
  readonly state = input<string>("unset");
  readonly value = input<string>("");
  readonly error = input<string | undefined>(undefined);
  readonly idPrefix = input<string>("cap-arg");
  readonly disabled = input<boolean>(false);

  readonly valueChange = output<string>();

  protected readonly fieldId = computed(
    () => `${this.idPrefix()}-${this.name()}`,
  );
  protected readonly labelText = computed(() =>
    fieldLabel(this.name(), this.schema(), this.required()),
  );
  protected readonly stateLabel = computed(
    () => SECRET_STATE_LABELS[this.state()] || this.state(),
  );
  protected readonly stateTone = computed(() => {
    const s = this.state();
    if (s === "set") return "healthy";
    if (s === "missing") return "warn";
    return "neutral";
  });
}

@Component({
  selector: "g-capability-managed-secret-field",
  imports: [InfoPopoverComponent, CapabilitySecretFieldComponent],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="s-field" [attr.data-managed-field]="name()">
      <div class="caf-secret-row">
        <span class="s-label">{{ schema()?.title || name() }}</span>
        <g-info-popover
          [title]="schema()?.description"
          [label]="schema()?.title || name()"
        />
        <span
          class="status-pill healthy"
          [attr.data-testid]="'managed-state-' + name()"
          >managed by grain</span
        >
        @if (canRotate()) {
          <button
            type="button"
            class="btn-tonal s-btn-sm"
            [disabled]="disabled()"
            (click)="rotate.emit(name())"
          >
            Rotate
          </button>
        }
        <button
          type="button"
          class="btn-text s-btn-sm"
          (click)="advanced.set(!advanced())"
        >
          {{ advanced() ? "Hide advanced" : "Advanced" }}
        </button>
      </div>
      <span class="s-hint">
        Generated by grain and kept in the encrypted secrets store; nobody needs
        to set it.
      </span>
      @if (advanced()) {
        <g-capability-secret-field
          [name]="name()"
          [schema]="overrideSchema()"
          [state]="state()"
          [value]="value()"
          [error]="error()"
          [idPrefix]="idPrefix()"
          [disabled]="disabled()"
          (valueChange)="valueChange.emit($event)"
        />
      }
    </div>
  `,
})
export class CapabilityManagedSecretFieldComponent {
  readonly name = input.required<string>();
  readonly schema = input<JsonSchema | null>(null);
  readonly state = input<string>("unset");
  readonly value = input<string>("");
  readonly canRotate = input<boolean>(false);
  readonly error = input<string | undefined>(undefined);
  readonly idPrefix = input<string>("cap-arg");
  readonly disabled = input<boolean>(false);

  readonly valueChange = output<string>();
  readonly rotate = output<string>();

  protected readonly advanced = signal(false);

  protected readonly overrideSchema = computed<JsonSchema>(() => ({
    ...(this.schema() || {}),
    title: `Override ${this.schema()?.title || this.name()}`,
    description: undefined,
  }));

  constructor() {
    effect(() => {
      if (this.value() || this.error()) {
        untracked(() => this.advanced.set(true));
      }
    });
  }
}

@Component({
  selector: "g-capability-object-fields",
  imports: [
    CapabilitySecretFieldComponent,
    CapabilityManagedSecretFieldComponent,
    forwardRef(() => CapabilityArgFieldComponent),
  ],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="caf-stack">
      @for (entry of visibleEntries(); track entry.name) {
        @if (!hiddenSet().has(entry.name)) {
          @let childPath = path() ? path() + "." + entry.name : entry.name;
          @let isSecret = isSecretProp(entry.name, entry.schema);
          @let managed = isSecret ? secrets()?.managed?.[entry.name] : null;
          @if (managed && !managed.overridden) {
            <g-capability-managed-secret-field
              [name]="entry.name"
              [schema]="entry.schema"
              [state]="secrets()?.fields?.[entry.name] || 'unset'"
              [value]="secrets()?.values?.[entry.name] || ''"
              [canRotate]="!!secrets()?.canRotate"
              [error]="errors()[childPath]"
              [idPrefix]="idPrefix()"
              [disabled]="disabled()"
              (valueChange)="
                secretChange.emit({ name: entry.name, value: $event })
              "
              (rotate)="rotateManaged.emit($event)"
            />
          } @else if (isSecret) {
            <g-capability-secret-field
              [name]="entry.name"
              [schema]="entry.schema"
              [required]="requiredSet().has(entry.name)"
              [state]="secrets()?.fields?.[entry.name] || 'unset'"
              [value]="secrets()?.values?.[entry.name] || ''"
              [error]="errors()[childPath]"
              [idPrefix]="idPrefix()"
              [disabled]="disabled()"
              (valueChange)="
                secretChange.emit({ name: entry.name, value: $event })
              "
            />
          } @else {
            <g-capability-arg-field
              [name]="entry.name"
              [root]="root()"
              [schema]="entry.schema"
              [required]="requiredSet().has(entry.name)"
              [value]="value()[entry.name]"
              [errors]="errors()"
              [path]="childPath"
              [idPrefix]="idPrefix()"
              [disabled]="disabled()"
              (valueChange)="setProp(entry.name, $event)"
            />
          }
        }
      }
      @if (hiddenNames().length > 0) {
        <div
          class="s-banner"
          [class.warn]="hiddenHasRequired()"
          [class.readonly]="!hiddenHasRequired()"
        >
          <span>
            {{ hiddenNames().join(", ") }}
            {{ hiddenNames().length === 1 ? "looks" : "look" }} like
            {{ hiddenNames().length === 1 ? "a secret" : "secrets" }} and
            {{ hiddenNames().length === 1 ? "is" : "are" }} not offered here:
            with: arguments must never carry secrets.
            @if (hiddenHasRequired()) {
              This method requires one, so it cannot be granted from the UI.
            }
          </span>
        </div>
      }
    </div>
  `,
})
export class CapabilityObjectFieldsComponent {
  readonly root = input<JsonSchema | null>(null);
  readonly schema = input<JsonSchema | null>(null);
  readonly value = input<Record<string, unknown>>({});
  readonly errors = input<Record<string, string>>({});
  readonly path = input<string>("");
  readonly idPrefix = input<string>("cap-arg");
  readonly secrets = input<ServerConfigSecretsContext | null>(null);
  readonly disabled = input<boolean>(false);

  readonly valueChange = output<Record<string, unknown>>();
  readonly secretChange = output<{ name: string; value: string }>();
  readonly rotateManaged = output<string>();

  protected readonly visibleEntries = computed(() => {
    const props = this.schema()?.properties || {};
    const r = this.root();
    return Object.entries(props)
      .map(([name, sub]) => ({ name, schema: deref(r, sub) }))
      .filter((e) => !isHiddenProperty(e.name, e.schema));
  });

  protected readonly requiredSet = computed(
    () => new Set(this.schema()?.required || []),
  );

  protected readonly hiddenNames = computed(() => {
    if (this.secrets()) return [];
    return this.visibleEntries()
      .filter((e) => isSecretLooking(e.name, e.schema))
      .map((e) => e.name);
  });

  protected readonly hiddenSet = computed(() => new Set(this.hiddenNames()));

  protected readonly hiddenHasRequired = computed(() => {
    const req = this.requiredSet();
    return this.hiddenNames().some((n) => req.has(n));
  });

  protected isSecretProp(
    _name: string,
    propSchema: JsonSchema | null,
  ): boolean {
    return (
      Boolean(this.secrets()) && this.path() === "" && isSecretField(propSchema)
    );
  }

  protected setProp(name: string, v: unknown): void {
    const next = { ...this.value() };
    if (v === undefined) delete next[name];
    else next[name] = v;
    this.valueChange.emit(next);
  }
}

@Component({
  selector: "g-capability-arg-field",
  imports: [
    InfoPopoverComponent,
    PillSelectComponent,
    forwardRef(() => CapabilityObjectFieldsComponent),
  ],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @let s = schema();
    @let kind = resolvedKind();
    @switch (kind) {
      @case ("choice") {
        @if (isFixedBool()) {
          <div class="s-field">
            <div class="caf-switch-line">
              <button
                type="button"
                class="g-switch"
                role="switch"
                [id]="fieldId()"
                [attr.aria-checked]="fixedBoolChecked()"
                [attr.aria-label]="lbl()"
                [attr.data-arg]="path()"
                data-arg-fixed="true"
                [disabled]="disabled() || value() === s?.const"
                (click)="valueChange.emit(s?.const)"
              ></button>
              <span class="s-label">{{ lbl() }}</span>
              <g-info-popover [title]="s?.description" [label]="infoTitle()" />
            </div>
            <span [class]="error() ? 's-field-error' : 's-hint'">
              {{ error() || "Fixed: this option only accepts this value." }}
            </span>
          </div>
        } @else {
          <div class="s-field">
            <div class="caf-label-row">
              <span class="s-label" [id]="fieldId() + '-label'">{{
                lbl()
              }}</span>
              <g-info-popover [title]="s?.description" [label]="infoTitle()" />
            </div>
            @if (choiceOptions().length > 12) {
              <input
                type="text"
                class="s-input"
                placeholder="Filter options…"
                [attr.aria-label]="'Filter ' + infoTitle()"
                [value]="choiceFilter()"
                [disabled]="disabled()"
                (input)="choiceFilter.set($any($event.target).value)"
              />
            }
            <g-pill-select
              [options]="filteredChoicePillOptions()"
              [value]="choiceSelectedIndex()"
              [ariaLabel]="lbl()"
              [placeholder]="required() ? 'Select…' : 'Not set'"
              align="left"
              [invalid]="!!error()"
              [disabled]="disabled()"
              [testid]="'arg-' + sanitizedPath()"
              (valueChange)="onChoicePick($event)"
            />
            @if (error(); as err) {
              <span class="s-field-error">{{ err }}</span>
            }
          </div>
        }
      }
      @case ("boolean") {
        <div class="s-field">
          <div class="caf-switch-line">
            <button
              type="button"
              class="g-switch"
              role="switch"
              [id]="fieldId()"
              [attr.aria-checked]="value() === true"
              [attr.aria-label]="lbl()"
              [attr.data-arg]="path()"
              [disabled]="disabled()"
              (click)="toggleBoolean()"
            ></button>
            <span class="s-label">{{ lbl() }}</span>
            <g-info-popover [title]="s?.description" [label]="infoTitle()" />
          </div>
          @if (error(); as err) {
            <span class="s-field-error">{{ err }}</span>
          }
        </div>
      }
      @case ("string") {
        <div class="s-field">
          <div class="caf-label-row">
            <label class="s-label" [attr.for]="fieldId()">{{ lbl() }}</label>
            <g-info-popover [title]="s?.description" [label]="infoTitle()" />
          </div>
          @if (multiline()) {
            <textarea
              [id]="fieldId()"
              class="s-textarea"
              [class.caf-code]="s?.format === 'code'"
              [rows]="s?.format === 'code' ? 6 : 3"
              [attr.data-arg]="path()"
              [attr.aria-invalid]="error() ? 'true' : null"
              [placeholder]="s?.pattern || ''"
              [disabled]="disabled()"
              [value]="stringValue()"
              (input)="onStringInput($any($event.target).value)"
            ></textarea>
          } @else {
            <input
              [id]="fieldId()"
              type="text"
              class="s-input"
              [attr.data-arg]="path()"
              [attr.aria-invalid]="error() ? 'true' : null"
              [placeholder]="s?.pattern || ''"
              [disabled]="disabled()"
              [value]="stringValue()"
              (input)="onStringInput($any($event.target).value)"
            />
          }
          @if (error(); as err) {
            <span class="s-field-error">{{ err }}</span>
          } @else if (s?.pattern) {
            <span class="s-hint">Pattern: {{ s?.pattern }}</span>
          }
        </div>
      }
      @case ("number") {
        <div class="s-field">
          <div class="caf-label-row">
            <label class="s-label" [attr.for]="fieldId()">{{ lbl() }}</label>
            <g-info-popover [title]="s?.description" [label]="infoTitle()" />
          </div>
          <input
            [id]="fieldId()"
            type="number"
            class="s-input s-num"
            [attr.data-arg]="path()"
            [attr.step]="s?.type === 'integer' ? 1 : 'any'"
            [attr.min]="s?.minimum ?? null"
            [attr.max]="s?.maximum ?? null"
            [attr.aria-invalid]="error() ? 'true' : null"
            [disabled]="disabled()"
            [value]="numberText()"
            (input)="onNumberInput($any($event.target).value)"
          />
          @if (error(); as err) {
            <span class="s-field-error">{{ err }}</span>
          } @else if (numberBounds()) {
            <span class="s-hint">{{ numberBounds() }}</span>
          }
        </div>
      }
      @case ("array") {
        @if (fixedList()) {
          <div
            class="caf-group"
            [class.is-error]="!!error()"
            role="group"
            [attr.aria-label]="lbl()"
            [attr.data-arg-fixed-list]="path()"
          >
            <div class="caf-label-row">
              <span class="s-label">{{ lbl() }}</span>
              <g-info-popover [title]="s?.description" [label]="infoTitle()" />
            </div>
            <div class="caf-fixed-chips">
              @for (item of fixedListOptions(); track $index) {
                <span class="status-pill neutral">{{ item }}</span>
              }
            </div>
            @if (fixedListMissing()) {
              <div>
                <button
                  type="button"
                  class="btn-tonal s-btn-sm"
                  [disabled]="disabled()"
                  (click)="valueChange.emit(fixedListOptions())"
                >
                  Restore fixed list
                </button>
              </div>
            }
            <span [class]="error() ? 's-field-error' : 's-hint'">
              {{
                error() ||
                  "Fixed: this option only accepts all of these together."
              }}
            </span>
          </div>
        } @else {
          <div
            class="caf-group"
            [class.is-error]="!!error()"
            role="group"
            [attr.aria-label]="lbl()"
            [attr.data-arg-list]="path()"
          >
            <div class="caf-label-row">
              <span class="s-label">{{ lbl() }}</span>
              <g-info-popover [title]="s?.description" [label]="infoTitle()" />
            </div>
            @for (item of arrayItems(); track $index) {
              <div class="caf-array-item">
                <div class="caf-array-body">
                  <g-capability-arg-field
                    [name]="arrayItemName(item, $index)"
                    [root]="root()"
                    [schema]="arrayItemSchema()"
                    [required]="true"
                    [value]="item"
                    [errors]="errors()"
                    [path]="path() + '[' + $index + ']'"
                    [idPrefix]="idPrefix()"
                    [disabled]="disabled()"
                    (valueChange)="updateArrayItem($index, $event)"
                  />
                </div>
                <button
                  type="button"
                  class="btn-text s-btn-sm"
                  [attr.aria-label]="'Remove ' + name() + ' ' + ($index + 1)"
                  [disabled]="disabled()"
                  (click)="removeArrayItem($index)"
                >
                  Remove
                </button>
              </div>
            }
            <div>
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                [disabled]="
                  disabled() ||
                  (s?.maxItems !== undefined &&
                    arrayItems().length >= s!.maxItems!)
                "
                (click)="addArrayItem()"
              >
                Add {{ name() }}
              </button>
            </div>
            @if (error(); as err) {
              <span class="s-field-error">{{ err }}</span>
            } @else if (arrayConstraints()) {
              <span class="s-hint">{{ arrayConstraints() }}</span>
            }
          </div>
        }
      }
      @case ("object") {
        @if (stringMap()) {
          <div
            class="caf-group"
            [class.is-error]="!!error()"
            role="group"
            [attr.aria-label]="lbl()"
            [attr.data-arg-map]="path()"
          >
            <div class="caf-label-row">
              <span class="s-label">{{ lbl() }}</span>
              <g-info-popover
                [title]="s?.description || mapValueSchema()?.description"
                [label]="infoTitle()"
              />
            </div>
            @for (row of mapRows(); track $index) {
              @let rowErr =
                row[0] ? errors()[path() ? path() + "." + row[0] : row[0]] : "";
              @let dup = isMapKeyDup(row[0], $index);
              <div class="caf-map-row">
                <div class="s-field caf-map-name">
                  <label class="s-label" [attr.for]="fieldId() + '-k-' + $index"
                    >Name</label
                  >
                  <input
                    [id]="fieldId() + '-k-' + $index"
                    type="text"
                    class="s-input s-mono"
                    [attr.data-arg-map-name]="path() + '[' + $index + ']'"
                    [attr.aria-invalid]="dup ? 'true' : null"
                    [disabled]="disabled()"
                    [value]="row[0]"
                    (input)="
                      updateMapRow($index, $any($event.target).value, row[1])
                    "
                  />
                  @if (dup) {
                    <span class="s-field-error">repeats an earlier name</span>
                  }
                </div>
                <div class="s-field caf-map-val">
                  <label class="s-label" [attr.for]="fieldId() + '-v-' + $index"
                    >Value</label
                  >
                  <input
                    [id]="fieldId() + '-v-' + $index"
                    type="text"
                    class="s-input s-mono"
                    [attr.data-arg-map-value]="path() + '[' + $index + ']'"
                    [attr.aria-invalid]="rowErr ? 'true' : null"
                    [placeholder]="mapValueExample()"
                    [disabled]="disabled()"
                    [value]="row[1]"
                    (input)="
                      updateMapRow($index, row[0], $any($event.target).value)
                    "
                  />
                  @if (rowErr) {
                    <span class="s-field-error">{{ rowErr }}</span>
                  }
                </div>
                <button
                  type="button"
                  class="btn-text s-btn-sm"
                  [attr.aria-label]="
                    'Remove ' + (row[0] || 'row ' + ($index + 1))
                  "
                  [disabled]="disabled()"
                  (click)="removeMapRow($index)"
                >
                  Remove
                </button>
              </div>
            }
            <div>
              <button
                type="button"
                class="btn-tonal s-btn-sm"
                [disabled]="disabled()"
                (click)="addMapRow()"
              >
                Add entry
              </button>
            </div>
            @if (error(); as err) {
              <span class="s-field-error">{{ err }}</span>
            }
          </div>
        } @else {
          <div
            class="caf-group"
            [class.is-error]="!!error()"
            role="group"
            [attr.aria-label]="lbl()"
          >
            <div class="caf-label-row">
              <span class="s-label">{{ lbl() }}</span>
              <g-info-popover [title]="s?.description" [label]="infoTitle()" />
            </div>
            <g-capability-object-fields
              [root]="root()"
              [schema]="s"
              [value]="objectValue()"
              [errors]="errors()"
              [path]="path()"
              [idPrefix]="idPrefix()"
              [disabled]="disabled()"
              (valueChange)="onObjectChange($event)"
            />
            @if (error(); as err) {
              <span class="s-field-error">{{ err }}</span>
            }
          </div>
        }
      }
      @case ("variant") {
        <div class="caf-stack">
          <div class="s-field">
            <div class="caf-label-row">
              <span class="s-label">{{ lbl() }}</span>
              <g-info-popover [title]="s?.description" [label]="infoTitle()" />
            </div>
            <g-pill-select
              [options]="variantPillOptions()"
              [value]="String(pickedVariant())"
              [ariaLabel]="lbl()"
              align="left"
              [disabled]="disabled()"
              [testid]="'arg-variant-' + sanitizedPath()"
              (valueChange)="onVariantPick($event)"
            />
            @if (
              activeVariantSchema()?.title && activeVariantSchema()?.description
            ) {
              <span class="s-hint">{{
                activeVariantSchema()?.description
              }}</span>
            }
          </div>
          @if (activeVariantSchema(); as sub) {
            <g-capability-arg-field
              [name]="variantOptionTitle(sub, pickedVariant())"
              [root]="root()"
              [schema]="sub"
              [required]="required()"
              [value]="value()"
              [errors]="errors()"
              [path]="path()"
              [idPrefix]="idPrefix()"
              [disabled]="disabled()"
              (valueChange)="valueChange.emit($event)"
            />
          }
          @if (error(); as err) {
            <span class="s-field-error">{{ err }}</span>
          }
        </div>
      }
    }
  `,
})
export class CapabilityArgFieldComponent {
  readonly name = input.required<string>();
  readonly root = input<JsonSchema | null>(null);
  readonly schema = input<JsonSchema | null>(null);
  readonly required = input<boolean>(false);
  readonly value = input<unknown>(undefined);
  readonly errors = input<Record<string, string>>({});
  readonly path = input<string>("");
  readonly idPrefix = input<string>("cap-arg");
  readonly disabled = input<boolean>(false);

  readonly valueChange = output<unknown>();

  protected readonly resolvedKind = computed(() => fieldKind(this.schema()));
  protected readonly error = computed(() => this.errors()[this.path()]);
  protected readonly sanitizedPath = computed(() =>
    this.path().replace(/[^A-Za-z0-9_-]/g, "-"),
  );
  protected readonly fieldId = computed(
    () => `${this.idPrefix()}-${this.sanitizedPath()}`,
  );
  protected readonly lbl = computed(() =>
    fieldLabel(this.name(), this.schema(), this.required()),
  );
  protected readonly infoTitle = computed(
    () => this.schema()?.title || this.name(),
  );

  // Choice
  protected readonly isFixedBool = computed(
    () => typeof this.schema()?.const === "boolean",
  );
  protected readonly fixedBoolChecked = computed(() => {
    const c = this.schema()?.const;
    return this.value() === c ? Boolean(c) : !c;
  });
  protected readonly choiceFilter = signal("");
  protected readonly choiceOptions = computed<unknown[]>(() => {
    const s = this.schema();
    if (!s) return [];
    if ("enum" in s && Array.isArray(s.enum)) return s.enum;
    return "const" in s ? [s.const] : [];
  });
  protected readonly choiceSelectedIndex = computed(() => {
    const s = this.schema();
    const opts = this.choiceOptions();
    const p = this.path();
    const isTopLevel = !p.includes(".") && !p.includes("[");
    const effective =
      this.value() === undefined && isTopLevel && s && "default" in s
        ? s.default
        : this.value();
    const idx = opts.findIndex(
      (o) => JSON.stringify(o) === JSON.stringify(effective),
    );
    return idx < 0 ? "" : String(idx);
  });
  protected readonly choicePillOptions = computed<PillOption[]>(() => {
    const s = this.schema();
    const labels = s?.["x-grain-enum-labels"] || {};
    const text = (o: unknown): string =>
      typeof o === "string" && typeof labels[o] === "string"
        ? labels[o]
        : typeof o === "string"
          ? o
          : JSON.stringify(o);
    const out: PillOption[] = [];
    if (!this.required()) {
      out.push({ value: "", label: "Not set" });
    }
    this.choiceOptions().forEach((o, i) => {
      out.push({ value: String(i), label: text(o) });
    });
    return out;
  });
  protected readonly filteredChoicePillOptions = computed<PillOption[]>(() => {
    const q = this.choiceFilter().trim().toLowerCase();
    const all = this.choicePillOptions();
    if (!q) return all;
    const cur = this.choiceSelectedIndex();
    return all.filter(
      (o) => o.value === cur || o.label.toLowerCase().includes(q),
    );
  });

  protected onChoicePick(v: string): void {
    const opts = this.choiceOptions();
    this.valueChange.emit(v === "" ? undefined : opts[Number(v)]);
  }

  // Boolean
  protected toggleBoolean(): void {
    const next = this.value() !== true;
    const s = this.schema();
    this.valueChange.emit(
      next ? true : this.required() || s?.default === true ? false : undefined,
    );
  }

  // String
  protected readonly multiline = computed(() => isMultiline(this.schema()));
  protected readonly stringValue = computed(() => {
    const v = this.value();
    return v === undefined || v === null ? "" : String(v);
  });
  protected onStringInput(v: string): void {
    this.valueChange.emit(v === "" ? undefined : v);
  }

  // Number
  protected readonly numberText = signal("");
  protected readonly numberBounds = computed(() => {
    const s = this.schema();
    if (!s) return "";
    return [
      s.minimum !== undefined && `≥ ${s.minimum}`,
      s.exclusiveMinimum !== undefined && `> ${s.exclusiveMinimum}`,
      s.maximum !== undefined && `≤ ${s.maximum}`,
      s.exclusiveMaximum !== undefined && `< ${s.exclusiveMaximum}`,
    ]
      .filter(Boolean)
      .join(", ");
  });
  protected onNumberInput(raw: string): void {
    this.numberText.set(raw);
    if (raw === "") this.valueChange.emit(undefined);
    else if (!Number.isNaN(Number(raw))) this.valueChange.emit(Number(raw));
  }

  // Array
  protected readonly fixedList = computed(
    () => this.required() && isFixedList(this.root(), this.schema()),
  );
  protected readonly arrayItemSchema = computed(() => {
    const s = this.schema();
    if (!s?.items || Array.isArray(s.items)) return null;
    return deref(this.root(), s.items);
  });
  protected readonly fixedListOptions = computed<unknown[]>(() => {
    const items = this.arrayItemSchema();
    return Array.isArray(items?.enum) ? items.enum : [];
  });
  protected readonly fixedListMissing = computed(
    () =>
      JSON.stringify(this.value()) !== JSON.stringify(this.fixedListOptions()),
  );
  protected readonly arrayItems = computed<unknown[]>(() => {
    const v = this.value();
    return Array.isArray(v) ? v : [];
  });
  protected readonly arrayConstraints = computed(() => {
    const s = this.schema();
    if (!s) return "";
    return [
      s.minItems !== undefined && `at least ${s.minItems}`,
      s.maxItems !== undefined && `at most ${s.maxItems}`,
      s.uniqueItems && "no repeats",
    ]
      .filter(Boolean)
      .join(", ");
  });

  protected arrayItemName(item: unknown, i: number): string {
    const rec = asRecord(item);
    return typeof rec["name"] === "string" && rec["name"]
      ? `${this.name()} ${i + 1}: ${rec["name"]}`
      : `${this.name()} ${i + 1}`;
  }

  protected updateArrayItem(i: number, v: unknown): void {
    const next = this.arrayItems().slice();
    next[i] = v;
    this.valueChange.emit(next);
  }

  protected removeArrayItem(i: number): void {
    const next = this.arrayItems().filter((_, j) => j !== i);
    this.valueChange.emit(
      next.length === 0 && !this.required() ? undefined : next,
    );
  }

  protected addArrayItem(): void {
    const itemSchema = this.arrayItemSchema();
    const fresh =
      initialValue(this.root(), itemSchema, false, true) ??
      (itemSchema?.type === "string" ? "" : undefined);
    this.valueChange.emit([...this.arrayItems(), fresh]);
  }

  // Object & StringMap
  protected readonly stringMap = computed(() =>
    isStringMap(this.root(), this.schema()),
  );
  protected readonly mapValueSchema = computed(() => {
    const ap = this.schema()?.additionalProperties;
    if (!ap || typeof ap !== "object") return null;
    return deref(this.root(), ap);
  });
  protected readonly mapValueExample = computed(() => {
    const ex = this.mapValueSchema()?.examples?.[0];
    return ex !== undefined ? String(ex) : "";
  });
  protected readonly mapRows = signal<[string, string][]>([]);
  protected readonly objectValue = computed(() => asRecord(this.value()));

  protected isMapKeyDup(k: string, idx: number): boolean {
    return k !== "" && this.mapRows().findIndex(([name]) => name === k) !== idx;
  }

  protected updateMapRow(i: number, k: string, v: string): void {
    const next = this.mapRows().slice();
    next[i] = [k, v];
    this.commitMapRows(next);
  }

  protected removeMapRow(i: number): void {
    this.commitMapRows(this.mapRows().filter((_, j) => j !== i));
  }

  protected addMapRow(): void {
    this.commitMapRows([...this.mapRows(), ["", ""]]);
  }

  private commitMapRows(next: [string, string][]): void {
    this.mapRows.set(next);
    const obj = Object.fromEntries(next.filter(([k]) => k !== ""));
    this.valueChange.emit(
      Object.keys(obj).length === 0 && !this.required() ? undefined : obj,
    );
  }

  protected onObjectChange(v: Record<string, unknown>): void {
    this.valueChange.emit(
      Object.keys(v).length === 0 && !this.required() ? undefined : v,
    );
  }

  // Variant
  protected readonly variantOptions = computed<(JsonSchema | null)[]>(() => {
    const s = this.schema();
    const raw = s?.oneOf || s?.anyOf || [];
    return raw.map((sub) => deref(this.root(), sub));
  });
  protected readonly pickedVariant = signal(0);
  protected readonly variantPillOptions = computed<PillOption[]>(() =>
    this.variantOptions().map((o, i) => ({
      value: String(i),
      label: variantTitle(o, i),
    })),
  );
  protected readonly activeVariantSchema = computed(
    () => this.variantOptions()[this.pickedVariant()] ?? null,
  );
  protected variantOptionTitle(s: JsonSchema | null, i: number): string {
    return variantTitle(s, i);
  }
  protected onVariantPick(v: string): void {
    const idx = Number(v);
    this.pickedVariant.set(idx);
    const sub = this.variantOptions()[idx];
    this.valueChange.emit(initialValue(this.root(), sub, false, true));
  }

  protected readonly String = String;

  constructor() {
    effect(() => {
      const v = this.value();
      const kind = this.resolvedKind();
      untracked(() => {
        if (kind === "number") {
          const cur = this.numberText();
          if (v === undefined ? cur !== "" : Number(cur) !== v) {
            this.numberText.set(v === undefined ? "" : String(v));
          }
        } else if (kind === "object" && this.stringMap()) {
          const curObj = Object.fromEntries(
            this.mapRows().filter(([k]) => k !== ""),
          );
          const nextObj = asRecord(v);
          if (JSON.stringify(curObj) !== JSON.stringify(nextObj)) {
            this.mapRows.set(
              Object.entries(nextObj).map(([k, x]) => [k, String(x ?? "")]),
            );
          }
        } else if (kind === "variant") {
          const opts = this.variantOptions();
          if (opts.length > 0) {
            this.pickedVariant.set(pickVariant(this.root(), opts, v));
          }
        }
      });
    });
  }
}

@Component({
  selector: "g-capability-args-form",
  imports: [
    InfoPopoverComponent,
    PillSelectComponent,
    CapabilityObjectFieldsComponent,
    CapabilityArgFieldComponent,
  ],
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./capability-args-form.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="caf-root">
      <div class="caf-head">
        <p class="s-hint">
          {{
            secrets()
              ? "Configuration is exported to grain/config as plain text, except write-only fields: what is typed into one goes only to the encrypted secrets store."
              : "Arguments are recorded on the task in plain text. Never put a secret in one; a server's credentials belong in Settings → Capabilities."
          }}
        </p>
        @if (canToggleJson()) {
          <button
            type="button"
            class="btn-text s-btn-sm"
            [attr.data-testid]="idPrefix() + '-json-toggle'"
            (click)="toggleJson()"
          >
            {{ asJson() ? "Edit as form" : "Edit as JSON" }}
          </button>
        }
      </div>

      @if (unsupported() || asJson()) {
        <div class="s-field">
          @if (unsupported(); as why) {
            <div class="s-banner readonly">
              <span>
                This schema uses a keyword the form cannot draw ({{ why }}), so
                it is edited as JSON and checked by the server.
              </span>
            </div>
          }
          <label class="s-label" [attr.for]="idPrefix() + '-json'"
            >Arguments (JSON)</label
          >
          <textarea
            [id]="idPrefix() + '-json'"
            class="s-textarea caf-code"
            rows="6"
            spellcheck="false"
            [disabled]="disabled()"
            [attr.aria-invalid]="
              jsonParseError() || jsonFieldErrorText() ? 'true' : null
            "
            [value]="jsonText()"
            (input)="onJsonInput($any($event.target).value)"
          ></textarea>
          @if (jsonParseError() || jsonFieldErrorText(); as err) {
            <span class="s-field-error">{{ err }}</span>
          }
        </div>
      } @else if (topKind() === "variant") {
        <div class="caf-stack">
          <div class="s-field">
            <div class="caf-label-row">
              <span class="s-label">{{ topVariantLabel() }}</span>
              <g-info-popover
                [title]="topSchema()?.description"
                [label]="topVariantLabel()"
              />
            </div>
            <g-pill-select
              [options]="topVariantPillOptions()"
              [value]="String(topPickedVariant())"
              [ariaLabel]="topVariantLabel()"
              align="left"
              [disabled]="disabled()"
              [testid]="idPrefix() + '-top-variant'"
              (valueChange)="onTopVariantPick($event)"
            />
            @if (activeTopVariant()?.title && activeTopVariant()?.description) {
              <span class="s-hint">{{ activeTopVariant()?.description }}</span>
            }
          </div>
          @if (activeTopVariant(); as sub) {
            @if (subKind(sub) === "object") {
              <g-capability-object-fields
                [root]="schema()"
                [schema]="sub"
                [value]="value()"
                [errors]="topVariantErrors()"
                path=""
                [idPrefix]="idPrefix()"
                [secrets]="secrets()"
                [disabled]="disabled()"
                (valueChange)="valueChange.emit($event)"
                (secretChange)="secretChange.emit($event)"
                (rotateManaged)="rotateManaged.emit($event)"
              />
            } @else {
              <g-capability-arg-field
                [name]="variantTitle(sub, topPickedVariant())"
                [root]="schema()"
                [schema]="sub"
                [required]="true"
                [value]="value()"
                [errors]="topVariantErrors()"
                path=""
                [idPrefix]="idPrefix()"
                [disabled]="disabled()"
                (valueChange)="onNonObjectTopChange($event)"
              />
            }
          }
        </div>
      } @else {
        <g-capability-object-fields
          [root]="schema()"
          [schema]="topSchema()"
          [value]="value()"
          [errors]="errors()"
          path=""
          [idPrefix]="idPrefix()"
          [secrets]="secrets()"
          [disabled]="disabled()"
          (valueChange)="valueChange.emit($event)"
          (secretChange)="secretChange.emit($event)"
          (rotateManaged)="rotateManaged.emit($event)"
        />
      }

      @if (errors()[""]; as wholeErr) {
        <div class="s-banner error" role="alert">
          <span>{{ wholeErr }}</span>
        </div>
      }
    </div>
  `,
})
export class CapabilityArgsFormComponent {
  readonly schema = input<JsonSchema | null>(null);
  readonly value = input<Record<string, unknown>>({});
  readonly errors = input<Record<string, string>>({});
  readonly idPrefix = input<string>("cap-arg");
  readonly secrets = input<ServerConfigSecretsContext | null>(null);
  readonly disabled = input<boolean>(false);

  readonly valueChange = output<Record<string, unknown>>();
  readonly secretChange = output<{ name: string; value: string }>();
  readonly rotateManaged = output<string>();

  protected readonly asJson = signal(false);
  protected readonly jsonText = signal("{}");
  protected readonly jsonParseError = signal("");
  protected readonly topPickedVariant = signal(0);

  protected readonly unsupported = computed(() =>
    unsupportedReason(this.schema()),
  );
  protected readonly topSchema = computed(() =>
    deref(this.schema(), this.schema()),
  );
  protected readonly topKind = computed(() => fieldKind(this.topSchema()));
  protected readonly canToggleJson = computed(
    () => !this.unsupported() && !this.secrets(),
  );

  protected readonly topVariantOptions = computed<(JsonSchema | null)[]>(() => {
    const top = this.topSchema();
    const subs = top?.oneOf || top?.anyOf || [];
    return subs.map((s) => deref(this.schema(), s));
  });
  protected readonly topVariantLabel = computed(
    () => this.topSchema()?.title || "Kind of arguments",
  );
  protected readonly topVariantPillOptions = computed<PillOption[]>(() =>
    this.topVariantOptions().map((o, i) => ({
      value: String(i),
      label: variantTitle(o, i),
    })),
  );
  protected readonly activeTopVariant = computed(
    () => this.topVariantOptions()[this.topPickedVariant()] ?? null,
  );
  protected readonly topVariantErrors = computed(() => {
    const sub = this.activeTopVariant();
    const own = sub ? validateValue(this.schema(), sub, this.value()) : {};
    const { "": _ignored, ...rest } = own;
    void _ignored;
    return { ...rest, ...this.errors() };
  });

  protected readonly jsonFieldErrorText = computed(() =>
    Object.entries(this.errors())
      .filter(([k]) => k !== "")
      .map(([k, msg]) => `${k}: ${msg}`)
      .join("; "),
  );

  protected readonly String = String;
  protected readonly variantTitle = variantTitle;

  constructor() {
    effect(() => {
      const s = this.schema();
      const v = this.value();
      const top = this.topSchema();
      untracked(() => {
        if (fieldKind(top) === "variant") {
          const opts = this.topVariantOptions();
          if (opts.length > 0) {
            this.topPickedVariant.set(pickVariant(s, opts, v));
          }
        }
        if (this.unsupported() || this.asJson()) {
          if (!this.jsonParseError()) {
            this.jsonText.set(JSON.stringify(v || {}, null, 2));
          }
        }
      });
    });
  }

  protected subKind(s: JsonSchema | null): string | null {
    return fieldKind(s);
  }

  protected toggleJson(): void {
    const next = !this.asJson();
    this.asJson.set(next);
    if (next) {
      this.jsonParseError.set("");
      this.jsonText.set(JSON.stringify(this.value() || {}, null, 2));
    }
  }

  protected onJsonInput(raw: string): void {
    this.jsonText.set(raw);
    try {
      const parsed = JSON.parse(raw || "{}") as unknown;
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
        this.jsonParseError.set("Arguments must be a JSON object.");
        return;
      }
      this.jsonParseError.set("");
      this.valueChange.emit(parsed as Record<string, unknown>);
    } catch (err) {
      this.jsonParseError.set(
        `Not JSON: ${err instanceof Error ? err.message : String(err)}`,
      );
    }
  }

  protected onTopVariantPick(v: string): void {
    const idx = Number(v);
    this.topPickedVariant.set(idx);
    const opt = this.topVariantOptions()[idx];
    const init = initialValue(this.schema(), opt, false, true);
    this.valueChange.emit(asRecord(init));
  }

  protected onNonObjectTopChange(v: unknown): void {
    this.valueChange.emit(asRecord(v));
  }
}
