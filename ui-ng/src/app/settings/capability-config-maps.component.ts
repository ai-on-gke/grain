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
  DEFAULT_CONFIG_MAP_DIR,
  type PlacedFile,
  type PlacementMap,
  type PlacementServer,
  type PlacementValue,
  defaultFileMode,
  defaultMapDir,
  droppedPlacements,
  isSecretKeySet,
  newFile,
  newMap,
  placementErrors,
  placementPayload,
  placementValue,
} from "./capability-placements";
import { errText } from "./settings-state.service";

type Kind = "config" | "secret";

// CapabilityConfigMapsComponent is the config-map server's card on
// Settings → Capabilities: the config maps (plain files kept in
// grain_config) and secret maps (files whose values live in the secret
// store, write-only) it places in a sandbox that holds its grant, listed
// with where each file lands, and an editor that adds, edits and removes
// maps and their files and saves them in one PUT /api/capability-servers.
@Component({
  selector: "g-cap-config-maps",
  imports: [ConfirmDialogComponent, NgTemplateOutlet],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div
      class="s-card"
      id="config-maps"
      data-testid="card-config-maps"
      [class.s-dimmed]="locked()"
    >
      <div class="s-card-head">
        <div>
          <h3 class="s-card-title">Config maps and secret maps</h3>
          <p class="s-card-desc">
            Files the <code>{{ server().id }}</code> server places in a sandbox
            that holds its grant. A config map's files are saved in
            grain_config; a secret map's values are written to the encrypted
            secret store and never shown again.
          </p>
        </div>
        @if (!editing()) {
          <button
            type="button"
            class="btn-tonal"
            data-testid="edit-config-maps"
            [disabled]="!canWrite()"
            (click)="startEdit()"
          >
            Edit maps
          </button>
        }
      </div>

      @if (!editing()) {
        @if (stored().configMaps.length + stored().secretMaps.length === 0) {
          <p class="s-hint" data-testid="config-maps-empty">
            No maps yet. Edit maps adds a config map or a secret map; a grant on
            this server then places its files under
            <code class="s-mono">{{ defaultDir }}/&lt;name&gt;</code>.
          </p>
        } @else {
          <ul class="cm-list" aria-label="Configured maps">
            @for (m of stored().configMaps; track m.name) {
              <ng-container
                *ngTemplateOutlet="summary; context: { m, kind: 'config' }"
              />
            }
            @for (m of stored().secretMaps; track m.name) {
              <ng-container
                *ngTemplateOutlet="summary; context: { m, kind: 'secret' }"
              />
            }
          </ul>
        }
      } @else {
        <form
          class="cm-editor"
          autocomplete="off"
          data-testid="config-map-editor"
          (submit)="$event.preventDefault()"
        >
          @for (kind of kinds; track kind) {
            @let list =
              kind === "config" ? draft().configMaps : draft().secretMaps;
            <section
              class="cm-section"
              [attr.aria-label]="
                kind === 'config' ? 'Config maps' : 'Secret maps'
              "
            >
              <div class="s-subhead">
                <div>
                  <h4 class="s-subhead-title">
                    {{ kind === "config" ? "Config maps" : "Secret maps" }}
                  </h4>
                  <p class="s-hint">
                    @if (kind === "config") {
                      One name holding several plain configuration files, placed
                      together under its directory.
                    } @else {
                      One name holding several secret files, mode 0600 unless
                      set. Values are write-only.
                    }
                  </p>
                </div>
                <button
                  type="button"
                  class="btn-tonal s-btn-sm"
                  (click)="addMap(kind)"
                >
                  {{ kind === "config" ? "Add config map" : "Add secret map" }}
                </button>
              </div>
              @if (list.length === 0) {
                <p class="s-hint">
                  {{
                    kind === "config" ? "No config maps." : "No secret maps."
                  }}
                </p>
              }
              @for (m of list; track $index; let i = $index) {
                @let mapLabel =
                  (kind === "config" ? "Config map " : "Secret map ") +
                  (m.name.trim() || i + 1);
                <fieldset
                  class="cm-map"
                  [attr.data-testid]="kind + '-map-' + i"
                  [attr.aria-label]="mapLabel"
                >
                  <div class="cm-map-head">
                    <div class="s-field">
                      <label class="s-label" [for]="id(kind, i, 'name')"
                        >Name</label
                      >
                      <input
                        class="s-input s-mono"
                        [id]="id(kind, i, 'name')"
                        [value]="m.name"
                        [placeholder]="kind === 'config' ? 'tools' : 'deploy'"
                        [attr.aria-invalid]="
                          errors()[kind + ':' + i] ? true : null
                        "
                        (input)="patchMap(kind, i, { name: val($event) })"
                      />
                      @if (errors()[kind + ":" + i]; as e) {
                        <span class="s-field-error">{{ e }}</span>
                      }
                    </div>
                    <div class="s-field cm-grow">
                      <label class="s-label" [for]="id(kind, i, 'dir')"
                        >Directory</label
                      >
                      <input
                        class="s-input s-mono"
                        [id]="id(kind, i, 'dir')"
                        [value]="m.dir"
                        [placeholder]="defaultMapDir(m.name)"
                        (input)="patchMap(kind, i, { dir: val($event) })"
                      />
                    </div>
                    <button
                      type="button"
                      class="btn-text s-btn-sm s-btn-danger cm-remove"
                      [attr.aria-label]="'Remove ' + mapLabel"
                      (click)="removeMap(kind, i)"
                    >
                      Remove map
                    </button>
                  </div>
                  <div class="s-field">
                    <label class="s-label" [for]="id(kind, i, 'desc')"
                      >Description</label
                    >
                    <input
                      class="s-input"
                      [id]="id(kind, i, 'desc')"
                      [value]="m.description"
                      [placeholder]="
                        kind === 'config'
                          ? 'Optional note shown in the agent prompt'
                          : 'Optional note shown in the agent prompt; never a secret value'
                      "
                      (input)="patchMap(kind, i, { description: val($event) })"
                    />
                  </div>

                  @for (f of m.files; track f.id; let j = $index) {
                    @let fileLabel =
                      mapLabel + " file " + (f.key.trim() || j + 1);
                    <div
                      class="cm-file"
                      role="group"
                      [attr.aria-label]="fileLabel"
                      [attr.data-testid]="kind + '-map-' + i + '-file-' + j"
                    >
                      <div class="cm-file-head">
                        <div class="s-field">
                          <label class="s-label" [for]="id(kind, i, 'key', j)"
                            >Key</label
                          >
                          <input
                            class="s-input s-mono"
                            [id]="id(kind, i, 'key', j)"
                            [value]="f.key"
                            [placeholder]="
                              kind === 'config' ? 'settings.toml' : 'token'
                            "
                            [attr.aria-invalid]="
                              errors()[kind + ':' + i + ':' + j] ? true : null
                            "
                            (input)="
                              patchFile(kind, i, j, { key: val($event) })
                            "
                          />
                          @if (errors()[kind + ":" + i + ":" + j]; as e) {
                            <span class="s-field-error">{{ e }}</span>
                          }
                        </div>
                        <div class="s-field cm-mode">
                          <label class="s-label" [for]="id(kind, i, 'mode', j)"
                            >Mode</label
                          >
                          <input
                            class="s-input s-mono"
                            [id]="id(kind, i, 'mode', j)"
                            [value]="f.mode"
                            [placeholder]="
                              defaultFileMode(kind === 'secret', f.executable)
                            "
                            (input)="
                              patchFile(kind, i, j, { mode: val($event) })
                            "
                          />
                        </div>
                        <div class="s-field cm-grow">
                          <label class="s-label" [for]="id(kind, i, 'path', j)"
                            >Path override</label
                          >
                          <input
                            class="s-input s-mono"
                            [id]="id(kind, i, 'path', j)"
                            [value]="f.path"
                            placeholder="Optional absolute path instead of <dir>/<key>"
                            (input)="
                              patchFile(kind, i, j, { path: val($event) })
                            "
                          />
                        </div>
                        <button
                          type="button"
                          class="btn-text s-btn-sm s-btn-danger cm-remove"
                          [attr.aria-label]="'Remove ' + fileLabel"
                          (click)="removeFile(kind, i, j)"
                        >
                          Remove file
                        </button>
                      </div>
                      @if (kind === "config") {
                        <label class="cm-check">
                          <input
                            type="checkbox"
                            [checked]="f.executable"
                            (change)="
                              patchFile(kind, i, j, {
                                executable: $any($event.target).checked,
                              })
                            "
                          />
                          Executable
                        </label>
                        <div class="s-field">
                          <label
                            class="s-label"
                            [for]="id(kind, i, 'content', j)"
                            >File content</label
                          >
                          <textarea
                            class="s-textarea s-mono"
                            rows="4"
                            spellcheck="false"
                            [id]="id(kind, i, 'content', j)"
                            [value]="f.content"
                            (input)="
                              patchFile(kind, i, j, { content: val($event) })
                            "
                          ></textarea>
                        </div>
                      } @else {
                        @let isSet = keySet(m.name, f.key);
                        <div class="s-field">
                          <label
                            class="s-label"
                            [for]="id(kind, i, 'value', j)"
                          >
                            Secret value
                            <span
                              class="status-pill"
                              [class.healthy]="isSet"
                              [class.blast]="!isSet"
                              >{{ isSet ? "set" : "not set" }}</span
                            >
                          </label>
                          <input
                            class="s-input s-mono"
                            type="password"
                            autocomplete="new-password"
                            [id]="id(kind, i, 'value', j)"
                            [value]="typed()[f.id] || ''"
                            [placeholder]="isSet ? '••••••••' : ''"
                            [attr.aria-invalid]="
                              errors()[kind + ':' + i + ':' + j + ':value']
                                ? true
                                : null
                            "
                            (input)="typeSecret(f.id, val($event))"
                          />
                          @if (
                            errors()[kind + ":" + i + ":" + j + ":value"];
                            as e
                          ) {
                            <span class="s-field-error">{{ e }}</span>
                          }
                          <span class="s-hint">
                            {{
                              isSet
                                ? "Stored in the secret store; type a new value to replace it, or leave it blank to keep it."
                                : "Written to the secret store, never to grain_config."
                            }}
                          </span>
                        </div>
                      }
                    </div>
                  }
                  <div>
                    <button
                      type="button"
                      class="btn-text s-btn-sm"
                      [attr.aria-label]="'Add a file to ' + mapLabel"
                      (click)="addFile(kind, i)"
                    >
                      Add file
                    </button>
                  </div>
                </fieldset>
              }
            </section>
          }

          @if (saveError()) {
            <div
              class="s-banner warn"
              role="alert"
              data-testid="config-maps-error"
            >
              {{ saveError() }}
            </div>
          }
          <div class="s-inline cm-actions">
            <button type="button" class="btn-text" (click)="cancel()">
              Cancel
            </button>
            <button
              type="button"
              class="btn-primary"
              data-testid="save-config-maps"
              [disabled]="saving() || hasErrors() || !canWrite()"
              (click)="save()"
            >
              {{ saving() ? "Saving…" : "Save maps" }}
            </button>
          </div>
        </form>
      }
    </div>

    <ng-template #summary let-m="m" let-kind="kind">
      <li
        class="cm-item"
        [attr.data-testid]="kind + '-map-item-' + m.name"
        [attr.aria-label]="
          (kind === 'config' ? 'Config map ' : 'Secret map ') + m.name
        "
      >
        <div class="cm-item-head">
          <span class="cm-item-name s-mono">{{ m.name }}</span>
          <span class="status-pill neutral">{{
            kind === "config" ? "config map" : "secret map"
          }}</span>
          <span class="s-hint s-mono cm-dir">{{
            m.dir || defaultDir + "/" + m.name
          }}</span>
        </div>
        @if (m.description) {
          <p class="s-hint cm-item-desc">{{ m.description }}</p>
        }
        <ul class="cm-keys" [attr.aria-label]="'Files of ' + m.name">
          @for (f of m.files; track f.key) {
            @if (kind === "secret") {
              @let isSet = keySet(m.name, f.key);
              <li
                class="status-pill s-mono"
                [class.healthy]="isSet"
                [class.blast]="!isSet"
                [attr.title]="placedAt(m, f)"
              >
                {{ f.key }}: {{ isSet ? "set" : "not set" }}
              </li>
            } @else {
              <li
                class="status-pill neutral s-mono"
                [attr.title]="placedAt(m, f)"
              >
                {{ f.key }}
              </li>
            }
          } @empty {
            <li class="s-hint">No files</li>
          }
        </ul>
      </li>
    </ng-template>

    @if (confirmReq(); as req) {
      <g-confirm-dialog [request]="req" (done)="onConfirmDone($event)" />
    }
  `,
  styleUrl: "./capability-placements.css",
})
export class CapabilityConfigMapsComponent {
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);
  private readonly snackbar = inject(SnackbarService);

  readonly server = input.required<PlacementServer>();
  readonly locked = input(false);
  readonly saved = output<void>();

  protected readonly kinds: readonly Kind[] = ["config", "secret"];
  protected readonly defaultDir = DEFAULT_CONFIG_MAP_DIR;
  protected readonly defaultMapDir = defaultMapDir;
  protected readonly defaultFileMode = defaultFileMode;

  protected readonly stored = computed(() =>
    placementValue(this.server().config?.config),
  );
  protected readonly editing = signal(false);
  protected readonly draft = signal<PlacementValue>({
    configMaps: [],
    secretMaps: [],
  });
  // Secret values typed this edit, by file id; never read back.
  protected readonly typed = signal<Record<string, string>>({});
  protected readonly saving = signal(false);
  protected readonly saveError = signal("");
  protected readonly errors = computed(() =>
    placementErrors(this.draft(), this.server(), this.typed()),
  );
  protected readonly hasErrors = computed(
    () => Object.keys(this.errors()).length > 0,
  );
  protected readonly confirmReq = signal<ConfirmRequest | null>(null);

  protected canWrite(): boolean {
    return this.access.can("PUT /api/capability-servers") && !this.locked();
  }

  protected id(kind: Kind, i: number, field: string, j?: number): string {
    const s = this.server().id.replace(/[^a-zA-Z0-9]+/g, "-");
    return `cm-${s}-${kind}-${i}-${field}${j === undefined ? "" : "-" + j}`;
  }

  protected val(e: Event): string {
    return (e.target as HTMLInputElement).value;
  }

  protected keySet(map: string, key: string): boolean {
    return isSecretKeySet(this.server(), map, key);
  }

  protected placedAt(m: PlacementMap, f: PlacedFile): string {
    return f.path || `${m.dir || defaultMapDir(m.name)}/${f.key}`;
  }

  protected startEdit(): void {
    this.draft.set(structuredClone(this.stored()));
    this.typed.set({});
    this.saveError.set("");
    this.editing.set(true);
  }

  protected cancel(): void {
    this.editing.set(false);
    this.typed.set({});
    this.saveError.set("");
  }

  private listKey(kind: Kind): keyof PlacementValue {
    return kind === "config" ? "configMaps" : "secretMaps";
  }

  private updateList(
    kind: Kind,
    fn: (list: PlacementMap[]) => PlacementMap[],
  ): void {
    const k = this.listKey(kind);
    this.draft.update((d) => ({ ...d, [k]: fn(d[k]) }));
  }

  protected addMap(kind: Kind): void {
    this.updateList(kind, (l) => [...l, newMap()]);
  }

  protected removeMap(kind: Kind, i: number): void {
    const gone = this.draft()[this.listKey(kind)][i];
    this.updateList(kind, (l) => l.filter((_, n) => n !== i));
    if (gone) this.forget(gone.files);
  }

  protected patchMap(kind: Kind, i: number, patch: Partial<PlacementMap>) {
    this.updateList(kind, (l) =>
      l.map((m, n) => (n === i ? { ...m, ...patch } : m)),
    );
  }

  protected addFile(kind: Kind, i: number): void {
    this.updateList(kind, (l) =>
      l.map((m, n) => (n === i ? { ...m, files: [...m.files, newFile()] } : m)),
    );
  }

  protected removeFile(kind: Kind, i: number, j: number): void {
    const gone = this.draft()[this.listKey(kind)][i]?.files[j];
    this.updateList(kind, (l) =>
      l.map((m, n) =>
        n === i ? { ...m, files: m.files.filter((_, x) => x !== j) } : m,
      ),
    );
    if (gone) this.forget([gone]);
  }

  protected patchFile(
    kind: Kind,
    i: number,
    j: number,
    patch: Partial<PlacedFile>,
  ): void {
    this.updateList(kind, (l) =>
      l.map((m, n) =>
        n === i
          ? {
              ...m,
              files: m.files.map((f, x) => (x === j ? { ...f, ...patch } : f)),
            }
          : m,
      ),
    );
  }

  protected typeSecret(fileId: string, value: string): void {
    this.typed.update((t) => ({ ...t, [fileId]: value }));
  }

  private forget(files: PlacedFile[]): void {
    this.typed.update((t) => {
      const next = { ...t };
      for (const f of files) delete next[f.id];
      return next;
    });
  }

  // save asks first when the draft drops a map or file that was saved:
  // a sandbox holding the grant stops getting it, and a dropped secret
  // map key's stored value goes with it.
  protected save(): void {
    if (this.hasErrors()) return;
    const dropped = droppedPlacements(this.stored(), this.draft());
    if (dropped.length === 0) {
      void this.commit();
      return;
    }
    this.confirmReq.set({
      title: "Remove saved placements?",
      message:
        `Saving removes ${dropped.join("; ")}. Sandboxes that hold a grant ` +
        `on ${this.server().id} stop getting these files.`,
      accept: "Save and remove",
      destructive: true,
      run: () => this.commit(),
    });
  }

  protected async onConfirmDone(accepted: boolean): Promise<void> {
    const req = this.confirmReq();
    this.confirmReq.set(null);
    if (accepted && req) await req.run();
  }

  private async commit(): Promise<void> {
    this.saving.set(true);
    this.saveError.set("");
    try {
      await this.api.put(
        "PUT /api/capability-servers",
        undefined,
        placementPayload(this.server(), this.draft(), this.typed()),
      );
      this.editing.set(false);
      this.typed.set({});
      this.snackbar.show(`Saved ${this.server().id}'s maps`);
      this.saved.emit();
    } catch (err) {
      this.saveError.set(errText(err));
    } finally {
      this.saving.set(false);
    }
  }
}
