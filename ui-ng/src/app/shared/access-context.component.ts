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
import type { AccessDecision, AccessHolds } from "../api/models";
import {
  accessDecisionDetail,
  accessDecisionLabel,
  accessDecisionOutcome,
  accessHoldsList,
} from "./access-items";
import { ActorChipComponent } from "./actor-chip.component";
import { CappedMarkdownComponent } from "./capped-markdown.component";
import { formatStamp } from "./dispatch-status";

// The context a pending request_access card carries beside its items
// (AccessRequestView.earlier and .holds, pkg/ui/task_access_request.go),
// shared by the task page's card and the Inbox's so the two say it the
// same way, as classic's PendingAccess.jsx does.

// AccessEarlierComponent is what people answered earlier requests on
// this task that asked for any of the same items, latest first: a run
// asking again for what was denied shows the denial and its reason.
@Component({
  selector: "g-access-earlier",
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [ActorChipComponent, CappedMarkdownComponent],
  styleUrl: "./access-context.css",
  template: `
    @if (decisions().length) {
      <ul class="access-earlier-list" aria-label="Earlier decisions">
        @for (d of decisions(); track d.at + $index) {
          <li
            class="access-earlier"
            [class.is-granted]="outcome(d) === 'granted'"
            data-testid="access-earlier"
          >
            <div class="access-earlier-head">
              <span
                class="status-pill"
                [class.warn]="outcome(d) !== 'granted'"
                [class.healthy]="outcome(d) === 'granted'"
                >{{ label(d) }}</span
              >
              @if (d.by?.ID; as who) {
                <span class="muted">by</span>
                <g-actor-chip [label]="who" [kind]="d.by?.Kind" />
              }
              @if (d.at) {
                <span class="muted access-earlier-at">{{ stamp(d.at) }}</span>
              }
            </div>
            @if (detail(d); as text) {
              <p class="access-earlier-items">{{ text }}</p>
            }
            @if (d.reason) {
              <g-capped-markdown
                class="access-earlier-reason"
                testid="access-earlier-reason"
                noun="reason"
                label="Earlier reason"
                [text]="d.reason"
              />
            }
          </li>
        }
      </ul>
    }
  `,
})
export class AccessEarlierComponent {
  readonly decisions = input<readonly AccessDecision[]>([]);

  protected outcome = accessDecisionOutcome;
  protected label = accessDecisionLabel;
  protected detail = accessDecisionDetail;
  protected stamp(at: string): string {
    return formatStamp(at);
  }
}

// AccessHoldsComponent is what the task holds already -- reads, writes,
// network and grants -- so a person can see what the request adds.
@Component({
  selector: "g-access-holds",
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  styleUrl: "./access-context.css",
  template: `
    @if (holds()) {
      <div class="access-holds" data-testid="access-holds">
        <span class="access-holds-label">Already holds</span>
        @if (entries().length) {
          <ul class="access-holds-list" aria-label="Access already held">
            @for (h of entries(); track h) {
              <li class="access-hold">{{ h }}</li>
            }
          </ul>
        } @else {
          <span class="muted">nothing beyond its own repository</span>
        }
      </div>
    }
  `,
})
export class AccessHoldsComponent {
  readonly holds = input<AccessHolds | undefined>(undefined);

  protected readonly entries = computed(() => accessHoldsList(this.holds()));
}
