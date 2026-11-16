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
  inject,
  input,
  output,
  signal,
} from "@angular/core";
import { NgTemplateOutlet } from "@angular/common";
import { GrainApi } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import {
  ConfirmDialogComponent,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import {
  type HostScriptRow,
  type PlacementServer,
  SCRIPT_LIMIT_FIELDS,
  type ScriptDraft,
  type ScriptTestResult,
  hostScripts,
  scriptDraft,
  scriptDraftErrors,
  scriptFromDraft,
  scriptLimits,
  scriptsPayload,
} from "./capability-placements";
import { errText } from "./settings-state.service";

const LIMIT_LABELS: Record<(typeof SCRIPT_LIMIT_FIELDS)[number], string> = {
  max_output: "Max output (bytes)",
  max_stdin: "Max stdin (bytes)",
  max_calls: "Max calls per grant",
  max_args: "Max arguments",
  max_arg_len: "Max argument length (bytes)",
};

// CapabilityHostScriptsComponent is the host-script server's card on
// Settings → Capabilities (docs/host-scripts.md): its saved scripts with
// their usage and limits; Add, Edit and Remove, which store the whole
// list in one PUT /api/capability-servers; and Test, which runs the
// saved script once through POST /api/capability-servers/test-script
// with an argv typed one field per argument -- no shell reads them.
@Component({
  selector: "g-cap-host-scripts",
  imports: [ConfirmDialogComponent, NgTemplateOutlet],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div
      class="s-card"
      id="host-scripts"
      data-testid="card-host-scripts"
      [class.s-dimmed]="locked()"
    >
      <div class="s-card-head">
        <div>
          <h3 class="s-card-title">Host scripts</h3>
          <p class="s-card-desc">
            Scripts the <code>{{ server().id }}</code> server lets a sandbox run
            on its side as <code>grain host-script &lt;name&gt;</code>, with the
            caller's arguments exactly and nothing else.
          </p>
        </div>
        @if (editingIndex() === null) {
          <button
            type="button"
            class="btn-tonal"
            data-testid="add-host-script"
            [disabled]="!canWrite()"
            (click)="startEdit(-1)"
          >
            Add script
          </button>
        }
      </div>

      @if (editingIndex() === -1) {
        <ng-container *ngTemplateOutlet="editor" />
      }

      @if (scripts().length === 0 && editingIndex() !== -1) {
        <p class="s-hint" data-testid="host-scripts-empty">
          No scripts yet. Add script saves one; a task then holds it as a
          <code>host-script</code> grant naming the script.
        </p>
      }

      <ul class="hs-list" aria-label="Host scripts">
        @for (s of scripts(); track s.name; let i = $index) {
          <li
            class="hs-item"
            [attr.data-testid]="'host-script-' + s.name"
            [attr.aria-label]="'Script ' + s.name"
          >
            @if (editingIndex() === i) {
              <ng-container *ngTemplateOutlet="editor" />
            } @else {
              <div class="hs-head">
                <div class="hs-title">
                  <h4 class="hs-name s-mono">{{ s.name }}</h4>
                  <p class="s-hint">{{ s.description }}</p>
                </div>
                <div class="s-row-actions">
                  <button
                    type="button"
                    class="btn-tonal s-btn-sm"
                    [attr.aria-expanded]="testing() === s.name"
                    [attr.aria-label]="'Test ' + s.name"
                    [disabled]="!canTest()"
                    (click)="toggleTest(s.name)"
                  >
                    Test
                  </button>
                  <button
                    type="button"
                    class="btn-tonal s-btn-sm"
                    [attr.aria-label]="'Edit ' + s.name"
                    [disabled]="!canWrite() || editingIndex() !== null"
                    (click)="startEdit(i)"
                  >
                    Edit
                  </button>
                  <button
                    type="button"
                    class="btn-text s-btn-sm s-btn-danger"
                    [attr.aria-label]="'Remove ' + s.name"
                    [disabled]="!canWrite() || editingIndex() !== null"
                    (click)="confirmRemove(i)"
                  >
                    Remove
                  </button>
                </div>
              </div>
              @if (s.usage) {
                <pre class="hs-pre" aria-label="Usage">{{ s.usage }}</pre>
              }
              @if (limits(s).length) {
                <ul class="cm-keys" [attr.aria-label]="'Limits of ' + s.name">
                  @for (l of limits(s); track l) {
                    <li class="status-pill neutral">{{ l }}</li>
                  }
                </ul>
              }
              @if (testing() === s.name) {
                <form
                  class="hs-test"
                  autocomplete="off"
                  [attr.aria-label]="'Test ' + s.name"
                  (submit)="$event.preventDefault(); runTest(s.name)"
                >
                  <p class="s-hint">
                    Runs the saved script once on the server's side with these
                    arguments exactly — no shell reads them. The call is logged
                    with its arguments (caller settings-test), so never pass a
                    secret. It is stopped after at most a minute.
                  </p>
                  @for (a of argv(); track $index; let k = $index) {
                    <div class="hs-arg">
                      <label
                        class="s-label"
                        [for]="'hs-arg-' + s.name + '-' + k"
                        >Argument {{ k + 1 }}</label
                      >
                      <input
                        class="s-input s-mono"
                        [id]="'hs-arg-' + s.name + '-' + k"
                        [value]="a"
                        (input)="setArg(k, $any($event.target).value)"
                      />
                      <button
                        type="button"
                        class="btn-text s-btn-sm"
                        [attr.aria-label]="'Remove argument ' + (k + 1)"
                        (click)="removeArg(k)"
                      >
                        Remove
                      </button>
                    </div>
                  }
                  <div class="s-inline">
                    <button
                      type="button"
                      class="btn-text s-btn-sm"
                      (click)="addArg()"
                    >
                      Add argument
                    </button>
                    <button
                      type="submit"
                      class="btn-primary s-btn-sm"
                      [disabled]="running() || !canTest()"
                    >
                      {{ running() ? "Running…" : "Run " + s.name }}
                    </button>
                  </div>
                  @if (result(); as r) {
                    <div
                      class="hs-result"
                      role="status"
                      data-testid="host-script-result"
                    >
                      @if (r.error) {
                        <div class="s-banner warn">{{ r.error }}</div>
                      } @else if (r.refused) {
                        <div class="s-banner warn">
                          Not run: {{ r.refused }}
                        </div>
                      } @else {
                        <ul class="cm-keys" aria-label="Outcome">
                          <li
                            class="status-pill"
                            [class.healthy]="r.exit_code === 0"
                            [class.error]="r.exit_code !== 0"
                          >
                            exit {{ r.exit_code }}
                          </li>
                          <li class="status-pill neutral">
                            {{ r.duration_ms }} ms
                          </li>
                          @if (r.timed_out) {
                            <li class="status-pill blast">timed out</li>
                          }
                          @if (r.truncated) {
                            <li class="status-pill blast">output truncated</li>
                          }
                        </ul>
                        <span class="s-label">stdout</span>
                        <pre class="hs-pre hs-out" aria-label="stdout">{{
                          r.stdout || "(empty)"
                        }}</pre>
                        <span class="s-label">stderr</span>
                        <pre class="hs-pre hs-out" aria-label="stderr">{{
                          r.stderr || "(empty)"
                        }}</pre>
                      }
                    </div>
                  }
                </form>
              }
            }
          </li>
        }
      </ul>
    </div>

    <ng-template #editor>
      @let d = draft();
      @let e = errors();
      <form
        class="hs-editor"
        autocomplete="off"
        data-testid="host-script-editor"
        [attr.aria-label]="
          editingIndex() === -1 ? 'New script' : 'Edit ' + d.name
        "
        (submit)="$event.preventDefault(); save()"
      >
        <div class="s-grid">
          <div class="s-field">
            <label class="s-label" for="hs-name">Name</label>
            <input
              id="hs-name"
              class="s-input s-mono"
              placeholder="deploy"
              [value]="d.name"
              [attr.aria-invalid]="e.name ? true : null"
              (input)="patch({ name: val($event) })"
            />
            @if (e.name) {
              <span class="s-field-error">{{ e.name }}</span>
            }
          </div>
          <div class="s-field">
            <label class="s-label" for="hs-timeout">Timeout (seconds)</label>
            <input
              id="hs-timeout"
              class="s-input s-num"
              inputmode="numeric"
              [value]="d.timeout"
              [attr.aria-invalid]="e.timeout ? true : null"
              (input)="patch({ timeout: val($event) })"
            />
            @if (e.timeout) {
              <span class="s-field-error">{{ e.timeout }}</span>
            }
          </div>
        </div>
        <div class="s-field">
          <label class="s-label" for="hs-desc">Description</label>
          <input
            id="hs-desc"
            class="s-input"
            placeholder="What the script does, for the agent's prompt"
            [value]="d.description"
            [attr.aria-invalid]="e.description ? true : null"
            (input)="patch({ description: val($event) })"
          />
          @if (e.description) {
            <span class="s-field-error">{{ e.description }}</span>
          }
        </div>
        <div class="s-field">
          <label class="s-label" for="hs-script">Script (bash)</label>
          <textarea
            id="hs-script"
            class="s-textarea s-mono"
            rows="8"
            spellcheck="false"
            placeholder='Runs as bash <file> "$@"; validate your own arguments'
            [value]="d.script"
            [attr.aria-invalid]="e.script ? true : null"
            (input)="patch({ script: val($event) })"
          ></textarea>
          @if (e.script) {
            <span class="s-field-error">{{ e.script }}</span>
          }
        </div>
        <div class="s-field">
          <label class="s-label" for="hs-usage">Usage</label>
          <textarea
            id="hs-usage"
            class="s-textarea s-mono"
            rows="3"
            spellcheck="false"
            placeholder="Shown by grain host-script <name> --grain-help and in the prompt"
            [value]="d.usage"
            (input)="patch({ usage: val($event) })"
          ></textarea>
        </div>
        <div class="s-grid">
          <div class="s-field">
            <label class="s-label" for="hs-cwd">Working directory</label>
            <input
              id="hs-cwd"
              class="s-input s-mono"
              placeholder="A per-grant scratch directory"
              [value]="d.cwd"
              [attr.aria-invalid]="e.cwd ? true : null"
              (input)="patch({ cwd: val($event) })"
            />
            @if (e.cwd) {
              <span class="s-field-error">{{ e.cwd }}</span>
            }
          </div>
          @for (f of limitFields; track f) {
            <div class="s-field">
              <label class="s-label" [for]="'hs-' + f">{{
                limitLabels[f]
              }}</label>
              <input
                class="s-input s-num"
                inputmode="numeric"
                placeholder="Server default"
                [id]="'hs-' + f"
                [value]="d[f]"
                [attr.aria-invalid]="e[f] ? true : null"
                (input)="patchLimit(f, val($event))"
              />
              @if (e[f]) {
                <span class="s-field-error">{{ e[f] }}</span>
              }
            </div>
          }
        </div>
        <div class="s-field">
          <label class="s-label" for="hs-env"
            >Environment (NAME=value per line)</label
          >
          <textarea
            id="hs-env"
            class="s-textarea s-mono"
            rows="2"
            spellcheck="false"
            placeholder="DEPLOY_TOKEN=$env:DEPLOY_TOKEN"
            [value]="d.env"
            [attr.aria-invalid]="e.env ? true : null"
            (input)="patch({ env: val($event) })"
          ></textarea>
          @if (e.env) {
            <span class="s-field-error">{{ e.env }}</span>
          } @else {
            <span class="s-hint"
              >A value <code>$env:NAME</code> is the server's own variable,
              bound to a secret in its environment — never a secret typed
              here.</span
            >
          }
        </div>
        <div class="s-toggle-row">
          <div class="s-toggle-copy">
            <span class="s-toggle-name">Approve each call</span>
            <span class="s-hint"
              >Each sandbox call waits for a person's approval before it
              runs.</span
            >
          </div>
          <button
            type="button"
            class="g-switch"
            role="switch"
            aria-label="Approve each call"
            [attr.aria-checked]="d.approve"
            (click)="patch({ approve: !d.approve })"
          ></button>
        </div>
        @if (saveError()) {
          <div class="s-banner warn" role="alert">{{ saveError() }}</div>
        }
        <div class="s-inline cm-actions">
          <button type="button" class="btn-text" (click)="cancelEdit()">
            Cancel
          </button>
          <button
            type="submit"
            class="btn-primary"
            [disabled]="saving() || hasErrors() || !canWrite()"
          >
            {{ saving() ? "Saving…" : "Save script" }}
          </button>
        </div>
      </form>
    </ng-template>

    @if (confirmReq(); as req) {
      <g-confirm-dialog [request]="req" (done)="onConfirmDone($event)" />
    }
  `,
  styleUrl: "./capability-placements.css",
})
export class CapabilityHostScriptsComponent {
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);
  private readonly snackbar = inject(SnackbarService);

  readonly server = input.required<PlacementServer>();
  readonly locked = input(false);
  readonly saved = output<void>();

  protected readonly limitFields = SCRIPT_LIMIT_FIELDS;
  protected readonly limitLabels = LIMIT_LABELS;
  protected readonly limits = scriptLimits;

  protected readonly scripts = computed(() => hostScripts(this.server()));

  // null: no form open; -1: a new script; otherwise the index edited.
  protected readonly editingIndex = signal<number | null>(null);
  protected readonly draft = signal<ScriptDraft>(scriptDraft());
  protected readonly saving = signal(false);
  protected readonly saveError = signal("");
  protected readonly errors = computed(() => {
    const at = this.editingIndex();
    const others = this.scripts()
      .filter((_, i) => i !== at)
      .map((s) => s.name);
    return scriptDraftErrors(this.draft(), others);
  });
  protected readonly hasErrors = computed(
    () => Object.keys(this.errors()).length > 0,
  );

  protected readonly testing = signal<string | null>(null);
  protected readonly argv = signal<string[]>([]);
  protected readonly running = signal(false);
  protected readonly result = signal<ScriptTestResult | null>(null);

  protected readonly confirmReq = signal<ConfirmRequest | null>(null);

  protected canWrite(): boolean {
    return this.access.can("PUT /api/capability-servers") && !this.locked();
  }

  protected canTest(): boolean {
    return this.access.can("POST /api/capability-servers/test-script");
  }

  protected val(e: Event): string {
    return (e.target as HTMLInputElement).value;
  }

  protected startEdit(i: number): void {
    this.draft.set(scriptDraft(i >= 0 ? this.scripts()[i] : undefined));
    this.saveError.set("");
    this.editingIndex.set(i);
    if (i >= 0) this.testing.set(null);
  }

  protected cancelEdit(): void {
    this.editingIndex.set(null);
    this.saveError.set("");
  }

  protected patch(p: Partial<ScriptDraft>): void {
    this.draft.update((d) => ({ ...d, ...p }));
  }

  protected patchLimit(
    f: (typeof SCRIPT_LIMIT_FIELDS)[number],
    v: string,
  ): void {
    this.draft.update((d) => ({ ...d, [f]: v }));
  }

  protected async save(): Promise<void> {
    const at = this.editingIndex();
    if (at === null || this.hasErrors()) return;
    const script = scriptFromDraft(this.draft());
    const list = this.scripts().slice();
    if (at < 0) list.push(script);
    else list[at] = script;
    this.saving.set(true);
    this.saveError.set("");
    try {
      await this.store(list);
      this.editingIndex.set(null);
      this.snackbar.show(`Saved script ${script.name}`);
    } catch (err) {
      this.saveError.set(errText(err));
    } finally {
      this.saving.set(false);
    }
  }

  protected confirmRemove(i: number): void {
    const s = this.scripts()[i];
    if (!s) return;
    this.confirmReq.set({
      title: "Remove script?",
      message:
        `Remove ${s.name}? Tasks holding a host-script grant on it can no ` +
        `longer run grain host-script ${s.name}.`,
      accept: "Remove",
      destructive: true,
      run: async () => {
        const list = this.scripts().filter((x) => x.name !== s.name);
        try {
          await this.store(list);
          if (this.testing() === s.name) this.testing.set(null);
          this.snackbar.show(`Removed script ${s.name}`);
        } catch (err) {
          this.snackbar.show(errText(err));
        }
      },
    });
  }

  protected async onConfirmDone(accepted: boolean): Promise<void> {
    const req = this.confirmReq();
    this.confirmReq.set(null);
    if (accepted && req) await req.run();
  }

  private async store(scripts: HostScriptRow[]): Promise<void> {
    await this.api.put(
      "PUT /api/capability-servers",
      undefined,
      scriptsPayload(this.server(), scripts),
    );
    this.saved.emit();
  }

  protected toggleTest(name: string): void {
    this.result.set(null);
    this.argv.set([]);
    this.testing.set(this.testing() === name ? null : name);
  }

  protected addArg(): void {
    this.argv.update((a) => [...a, ""]);
  }

  protected setArg(k: number, v: string): void {
    this.argv.update((a) => a.map((x, i) => (i === k ? v : x)));
  }

  protected removeArg(k: number): void {
    this.argv.update((a) => a.filter((_, i) => i !== k));
  }

  protected async runTest(name: string): Promise<void> {
    this.running.set(true);
    this.result.set(null);
    try {
      const r = await this.api.post<ScriptTestResult>(
        "POST /api/capability-servers/test-script",
        undefined,
        { id: this.server().id, script: name, argv: this.argv() },
      );
      // A result for a script the person has since closed is dropped.
      if (this.testing() === name) this.result.set(r ?? {});
    } catch (err) {
      if (this.testing() === name) this.result.set({ error: errText(err) });
    } finally {
      this.running.set(false);
    }
  }
}
