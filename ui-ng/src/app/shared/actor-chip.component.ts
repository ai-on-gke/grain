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
} from "@angular/core";
import { initialsFromIdentity } from "../services/access.service";
import { CHLADNI_ROSETTE_PATH } from "./brand-mark";

export type ActorShapeKind = "human" | "agent" | "grain" | "service";

export function normalizeActorKind(
  kind: string | undefined,
  label: string | undefined,
): ActorShapeKind {
  const k = (kind || "").toLowerCase();
  const l = (label || "").trim().toLowerCase();
  if (k === "automation" || k === "grain" || l === "grain") return "grain";
  if (k === "agent") return "agent";
  if (
    k === "service" ||
    k === "system" ||
    l.startsWith("system:") ||
    l.startsWith("repo:")
  ) {
    return "service";
  }
  return "human";
}

// Shape-coded 20x20 actor chip: circle for people, 3px rounded square for
// agents/services, and 6px rounded square with Chladni rosette for Grain.
// Uses CSS attr(data-initials) for human/agent/service badges so DOM
// textContent and screen readers read the clean actor name without noise.
@Component({
  selector: "g-actor-chip",
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  styles: [
    `
      .actor {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        max-width: 100%;
        vertical-align: middle;
        font: 500 12px/16px var(--grain-font-body);
        color: var(--md-sys-color-on-surface);
        white-space: nowrap;
      }
      .actor:focus-visible {
        outline: 2px solid var(--md-sys-color-primary);
        outline-offset: 2px;
        border-radius: var(--md-sys-shape-corner-xs);
      }
      .actor-avatar[data-initials]::before {
        content: attr(data-initials);
      }
      .actor-avatar {
        display: inline-grid;
        place-items: center;
        width: 20px;
        height: 20px;
        flex: none;
        font: 500 10px/1 var(--grain-font-body);
      }
      .actor-human .actor-avatar {
        border-radius: var(--md-sys-shape-corner-full);
        background: var(--md-sys-color-tertiary-container);
        color: var(--md-sys-color-on-tertiary-container);
      }
      .actor-agent .actor-avatar {
        border-radius: 3px;
        background: var(--md-sys-color-secondary-container);
        color: var(--md-sys-color-on-secondary-container);
      }
      .actor-grain .actor-avatar {
        border-radius: 6px;
        background: var(--md-sys-color-primary-container);
        color: var(--md-sys-color-on-primary-container);
      }
      .actor-service .actor-avatar {
        border-radius: 3px;
        background: var(--md-sys-color-surface-container-highest);
        color: var(--md-sys-color-on-surface-variant);
      }
      .actor-name {
        overflow: hidden;
        text-overflow: ellipsis;
      }
      .actor-behalf {
        font-weight: 400;
        color: var(--md-sys-color-on-surface-variant);
      }
      .is-human .actor,
      .is-human .actor-behalf {
        color: inherit;
      }
    `,
  ],
  template: `
    <span [class]="'actor actor-' + resolvedKind()" data-testid="actor-chip">
      @if (resolvedKind() === "grain") {
        <span class="actor-avatar" aria-hidden="true">
          <svg width="14" height="14" viewBox="0 0 128 128" aria-hidden="true">
            <path
              style="clip-path: circle(44% at 50% 50%)"
              fill="currentColor"
              fill-rule="evenodd"
              [attr.d]="rosettePath"
            />
          </svg>
        </span>
      } @else {
        <span
          class="actor-avatar"
          [attr.data-initials]="initials()"
          aria-hidden="true"
        ></span>
      }
      @if (!avatarOnly()) {
        <span class="actor-name">{{ label() }}</span>
        @if (behalf()) {
          <span class="actor-behalf">for {{ behalf() }}</span>
        }
      }
    </span>
  `,
})
export class ActorChipComponent {
  readonly label = input<string>("");
  readonly kind = input<string | undefined>("human");
  readonly behalf = input<string | undefined>("");
  readonly avatarOnly = input<boolean>(false);

  protected readonly rosettePath = CHLADNI_ROSETTE_PATH;
  protected readonly resolvedKind = computed(() =>
    normalizeActorKind(this.kind(), this.label()),
  );
  protected readonly initials = computed(() =>
    initialsFromIdentity(this.label()),
  );
}
