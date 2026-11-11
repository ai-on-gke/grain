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

import { ChangeDetectionStrategy, Component, inject } from "@angular/core";
import { SnackbarService } from "../services/snackbar.service";

// The one snackbar of the app. The live region is always in the page,
// so screen readers announce a message the moment it appears.
@Component({
  selector: "g-snackbar",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="snackbar-region" role="status" aria-live="polite">
      @if (snacks.current(); as s) {
        <div
          class="snackbar"
          data-testid="snackbar"
          (mouseenter)="snacks.pause()"
          (mouseleave)="snacks.resume()"
          (focusin)="snacks.pause()"
          (focusout)="snacks.resume()"
        >
          <span class="snackbar-text">{{ s.message }}</span>
          @if (s.action) {
            <button
              type="button"
              class="snackbar-action"
              data-testid="snackbar-action"
              (click)="snacks.runAction(s.id)"
            >
              {{ s.action.label }}
            </button>
          }
          <button
            type="button"
            class="snackbar-close"
            aria-label="Dismiss"
            (click)="snacks.dismiss(s.id)"
          >
            ×
          </button>
        </div>
      }
    </div>
  `,
})
export class SnackbarComponent {
  protected readonly snacks = inject(SnackbarService);
}
