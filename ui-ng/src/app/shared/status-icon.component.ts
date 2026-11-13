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
  input,
} from "@angular/core";

const STATUS_SHAPES: Record<string, string> = {
  reply:
    "M2.5 3A1.5 1.5 0 0 1 4 1.5h8A1.5 1.5 0 0 1 13.5 3v6A1.5 1.5 0 0 1 12 10.5H6.2L3.3 13.2A.5.5 0 0 1 2.5 12.8V3Z",
  blast:
    "M8 1.2 14.8 8 8 14.8 1.2 8 8 1.2Zm-.7 4.3v3.4h1.4V5.5H7.3Zm0 4.6v1.4h1.4v-1.4H7.3Z",
  error:
    "M5.3 1.5h5.4l3.8 3.8v5.4l-3.8 3.8H5.3L1.5 10.7V5.3l3.8-3.8Zm.4 4.2L4.7 6.7 7 9l-2.3 2.3 1 1L8 10l2.3 2.3 1-1L9 9l2.3-2.3-1-1L8 8 5.7 5.7Z",
  healthy:
    "M8 1.5a6.5 6.5 0 1 0 0 13 6.5 6.5 0 0 0 0-13Zm2.9 4.4-3.6 4.2-2.1-2.1 1-1 1.1 1.1 2.6-3.1 1 .9Z",
  proposal:
    "M8 1.5a6.5 6.5 0 1 0 0 13 6.5 6.5 0 0 0 0-13Zm-.7 3.2h1.4v2.6h2.6v1.4H8.7v2.6H7.3V8.7H4.7V7.3h2.6V4.7Z",
  progress:
    "M8 2a6 6 0 1 0 6 6h-1.8A4.2 4.2 0 1 1 8 3.8V2Zm1.5.4L13.6 6H9.5V2.4Z",
  neutral:
    "M8 2.2a5.8 5.8 0 1 0 0 11.6 5.8 5.8 0 0 0 0-11.6Zm0 1.6a4.2 4.2 0 1 1 0 8.4 4.2 4.2 0 0 1 0-8.4Z",
};

export function statusShapeFor(tone: string, kind = ""): string {
  const k = kind.toLowerCase();
  if (k === "proposal" || k === "proposed") return STATUS_SHAPES["proposal"];
  if (k === "reply" || k === "asked" || k === "access" || k === "secret") {
    return STATUS_SHAPES["reply"];
  }
  if (
    k === "working" ||
    k === "running" ||
    k === "setup" ||
    k === "progress" ||
    k === "merging"
  ) {
    return STATUS_SHAPES["progress"];
  }
  if (k === "quiet" || k === "blast" || k === "warning") {
    return STATUS_SHAPES["blast"];
  }
  if (k === "error" || k === "failed" || k === "blocked") {
    return STATUS_SHAPES["error"];
  }
  if (k === "healthy" || k === "merged" || k === "passed" || k === "done") {
    return STATUS_SHAPES["healthy"];
  }

  const t = (tone || "").toLowerCase();
  if (t.includes("proposal")) return STATUS_SHAPES["proposal"];
  if (t.includes("setup")) return STATUS_SHAPES["progress"];
  if (t.includes("reply")) return STATUS_SHAPES["reply"];
  if (t.includes("blast") || t.includes("quiet") || t.includes("warn")) {
    return STATUS_SHAPES["blast"];
  }
  if (t.includes("error")) return STATUS_SHAPES["error"];
  if (t.includes("healthy") || t.includes("planned")) {
    return STATUS_SHAPES["healthy"];
  }
  return STATUS_SHAPES["neutral"];
}

// CVD-safe 12x12 geometric shape inside .status-pill so status is never
// conveyed by colour alone.
@Component({
  selector: "g-status-icon",
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { class: "status-icon-host", "aria-hidden": "true" },
  template: `
    <svg width="12" height="12" viewBox="0 0 16 16" aria-hidden="true">
      <path [attr.d]="path()" fill="currentColor" />
    </svg>
  `,
})
export class StatusIconComponent {
  readonly tone = input<string>("neutral");
  readonly kind = input<string>("");

  protected readonly path = computed(() =>
    statusShapeFor(this.tone(), this.kind()),
  );
}
