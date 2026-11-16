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
import type { CapabilityOption } from "../api/models";
import { AccessService } from "../services/access.service";
import { errText } from "../settings/settings-state.service";
import { GrantsPickerComponent } from "../shared/grants-picker.component";

// RepoCICapabilitiesComponent is the /next/ port of the classic CI tab's
// "CI capabilities" card: the capabilities and grants every CI job in this
// repository is given, independent of the defaults its tasks start with.
// The set is replaced wholesale (PUT .../ci-capabilities takes the new
// list), and each change saves at once, as the classic card does, so
// there is no draft to guard. A refused change is said beside the picker
// and the picker goes back to what is stored.
@Component({
  selector: "g-repo-ci-capabilities",
  imports: [GrantsPickerComponent],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="s-card" data-testid="card-repo-ci-capabilities">
      <div class="s-card-head">
        <div>
          <h2 class="s-card-title">CI capabilities</h2>
          <p class="s-card-desc">
            Granted to every CI job in {{ repo() }}, starting with the next run.
            Each job gets its own credentials, which are revoked when it
            finishes. Task defaults are set on the Agents &amp; Grants tab.
          </p>
        </div>
      </div>

      @if (configRepoOnly()) {
        <p class="s-hint" data-testid="ci-capabilities-config-managed">
          {{ repo() }}'s CI capabilities are managed in the config repository
          (tables/repo_config.json); edit them there.
        </p>
      } @else if (denied()) {
        @let why = denied()!;
        <p
          class="s-hint"
          id="ci-capabilities-denied"
          data-testid="ci-capabilities-denied"
        >
          {{ why }}
        </p>
      }

      @if (loadError(); as err) {
        <p class="s-field-error" role="alert">
          Could not read CI capabilities: {{ err }}
        </p>
      } @else if (held()) {
        @let list = held()!;
        <div
          class="repo-ci-caps"
          role="group"
          aria-label="CI capabilities"
          [attr.aria-invalid]="saveError() ? 'true' : null"
          [attr.aria-describedby]="
            saveError()
              ? 'ci-capabilities-error'
              : denied() && !configRepoOnly()
                ? 'ci-capabilities-denied'
                : null
          "
        >
          @if (offered().length === 0 && list.length === 0) {
            <p class="s-hint" data-testid="ci-capabilities-none-offered">
              No capability is offered to CI on this deployment.
            </p>
          }
          <g-grants-picker
            testid="repo-ci-caps"
            [offered]="offered()"
            [held]="list"
            [repo]="repo()"
            [disabled]="readOnly() || saving()"
            (heldChange)="save($event)"
          />
        </div>
        @if (saveError(); as err) {
          <p
            id="ci-capabilities-error"
            class="s-field-error"
            role="alert"
            data-testid="ci-capabilities-error"
          >
            {{ err }}
          </p>
        }
      } @else {
        <p class="s-hint">Loading CI capabilities…</p>
      }
    </div>
  `,
})
export class RepoCICapabilitiesComponent {
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);

  readonly repo = input.required<string>();
  readonly configRepoOnly = input(false);
  // offered is /api/config's ciCapabilities: the capabilities that can
  // deliver material to a CI shell on this deployment.
  readonly offered = input<readonly CapabilityOption[]>([]);

  protected readonly held = signal<string[] | null>(null);
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
        "PUT /api/repos/{owner}/{name}/ci-capabilities",
        this.repo(),
      )
    );
  }

  protected denied(): string {
    return this.access.deniedReason(
      "PUT /api/repos/{owner}/{name}/ci-capabilities",
      this.repo(),
    );
  }

  private async load(): Promise<void> {
    const key = "GET /api/repos/{owner}/{name}/ci-capabilities";
    if (!this.access.can(key, this.repo())) {
      this.loadError.set(this.access.deniedReason(key, this.repo()));
      return;
    }
    try {
      const out = await this.api.get<{ ciCapabilities?: string[] | null }>(
        key,
        splitRepo(this.repo()),
      );
      this.held.set([...(out?.ciCapabilities ?? [])]);
      this.loadError.set(null);
    } catch (err) {
      this.loadError.set(errText(err));
    }
  }

  protected async save(next: string[]): Promise<void> {
    const before = this.held();
    this.held.set(next);
    this.saving.set(true);
    this.saveError.set(null);
    try {
      const out = await this.api.put<{ ciCapabilities?: string[] | null }>(
        "PUT /api/repos/{owner}/{name}/ci-capabilities",
        splitRepo(this.repo()),
        { ciCapabilities: next },
      );
      if (Array.isArray(out?.ciCapabilities)) {
        this.held.set([...out.ciCapabilities]);
      }
    } catch (err) {
      // Not stored, so not drawn as held.
      this.held.set(before);
      this.saveError.set(errText(err));
    } finally {
      this.saving.set(false);
    }
  }
}
