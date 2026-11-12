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
  input,
} from "@angular/core";
import type { DiffPreviewFile, DiffPreviewHunk } from "../api/models";
import { type AnnotatedDiffLine, annotateDiffHunk } from "./code-highlight";

@Component({
  selector: "g-diff-viewer",
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="diff-viewer" [attr.data-testid]="testid() || 'diff-viewer'">
      @for (f of files(); track f.path + ($index + "")) {
        <div class="diff-file" data-testid="diff-file">
          <div class="diff-file-head">
            <span class="diff-path" [title]="f.path">{{
              f.oldPath ? f.oldPath + " → " + f.path : f.path
            }}</span>
            @if (f.status !== "modified") {
              <span class="diff-status">{{ f.status }}</span>
            }
            <span class="diff-stat">
              <span class="diff-stat-add">+{{ f.additions }}</span>
              <span class="diff-stat-del">−{{ f.deletions }}</span>
            </span>
          </div>
          @if (f.binary) {
            <p class="muted diff-note">Binary file</p>
          }
          @for (h of f.hunks; track $index) {
            <div
              class="diff-hunk"
              role="region"
              tabindex="0"
              [attr.aria-label]="f.path + ' ' + h.header"
            >
              <div class="diff-hunk-body">
                <div class="diff-hunk-head">{{ h.header }}</div>
                @for (l of linesOf(h); track $index) {
                  <div
                    [class]="'diff-line diff-line-' + l.kind"
                    data-testid="diff-line"
                  >
                    <span class="diff-ln" aria-hidden="true">{{
                      l.oldNo ?? ""
                    }}</span
                    ><span class="diff-ln" aria-hidden="true">{{
                      l.newNo ?? ""
                    }}</span
                    ><span class="diff-sign" aria-hidden="true">{{
                      l.kind === "add" ? "+" : l.kind === "del" ? "−" : " "
                    }}</span
                    ><span class="diff-text">
                      @for (seg of l.segments; track $index) {
                        @if (seg.changed) {
                          <mark class="diff-word">{{ seg.text }}</mark>
                        } @else {
                          {{ seg.text }}
                        }
                      }
                    </span>
                  </div>
                }
                @if (h.linesOmitted) {
                  <div class="diff-omitted muted">
                    … {{ h.linesOmitted }} lines omitted in preview
                  </div>
                }
              </div>
            </div>
          }
          @if (f.hunksOmitted) {
            <p class="muted diff-note">
              … {{ f.hunksOmitted }} more hunk{{
                f.hunksOmitted === 1 ? "" : "s"
              }}
              in this file
            </p>
          }
        </div>
      }
    </div>
  `,
})
export class DiffViewerComponent {
  readonly files = input.required<readonly DiffPreviewFile[]>();
  readonly testid = input<string>("");

  protected linesOf(h: DiffPreviewHunk): AnnotatedDiffLine[] {
    return annotateDiffHunk(h);
  }
}
