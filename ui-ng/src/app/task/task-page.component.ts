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
  computed,
  inject,
  input,
} from "@angular/core";
import { Router } from "@angular/router";
import { isTypingTarget } from "../services/keyboard.service";
import { PHONE_QUERY } from "../shared/split";
import { TABS, isTabId } from "../tasks/task-view";
import { type BackLink, TaskDetailComponent } from "./task-detail.component";

export { duration } from "./task-detail.component";
export type { TaskTab } from "./task-detail.component";

function isPhoneViewport(): boolean {
  return typeof matchMedia === "function" && matchMedia(PHONE_QUERY).matches;
}

// backFrom reads ?from= into the back link: "inbox" goes back to the
// Inbox, "tasks:<tab>" to that Tasks tab with this task selected (except on
// phones where the list does not show a side pane), and anything else to
// Tasks with this task selected.
export function backFrom(
  from: string | undefined,
  id: string,
  phone = isPhoneViewport(),
): BackLink {
  if (from === "inbox") return { label: "Inbox", commands: ["/inbox"] };
  if (from?.startsWith("workstreams:")) {
    return {
      label: "Workstream",
      commands: ["/workstreams", from.slice("workstreams:".length)],
      query: { node: id },
    };
  }
  const tab = from?.startsWith("tasks:") ? from.slice(6) : "";
  const def = isTabId(tab) ? TABS.find((t) => t.id === tab) : undefined;
  return {
    label: def ? `Tasks · ${def.label}` : "Tasks",
    commands: ["/tasks"],
    query: phone
      ? def
        ? { tab: def.id }
        : null
      : def
        ? { tab: def.id, task: id }
        : { task: id },
  };
}

// TaskPageComponent is the task page at full screen: the same body the
// Tasks pane shows, with a back link that names where the person came
// from. Escape goes back there with the same tab and selection.
@Component({
  selector: "g-task-page",
  imports: [TaskDetailComponent],
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { "(document:keydown.escape)": "onEscape($event)" },
  template: `
    <g-task-detail [id]="id()" [tab]="tab()" [from]="from()" [back]="back()" />
  `,
})
export class TaskPageComponent {
  private readonly router = inject(Router);

  readonly id = input.required<string>();
  readonly tab = input<string>();
  // ?from= (withComponentInputBinding): where the back link goes.
  readonly from = input<string>();

  protected readonly back = computed(() => backFrom(this.from(), this.id()));

  protected onEscape(ev: KeyboardEvent): void {
    // Menus, dialogs and fields keep their own Escape.
    if (ev.defaultPrevented || isTypingTarget(ev.target)) return;
    // if (document.querySelector('[aria-modal="true"]')) return;
    if ((ev.target as HTMLElement | null)?.closest?.(".confirm-inline")) return;
    ev.preventDefault();
    const b = this.back();
    void this.router.navigate(b.commands, {
      queryParams: b.query ?? {},
      replaceUrl: true,
    });
  }
}
