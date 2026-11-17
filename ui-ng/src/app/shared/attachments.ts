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
  ChangeDetectorRef,
  Component,
  ElementRef,
  ViewEncapsulation,
  inject,
  input,
  output,
  signal,
  viewChild,
} from "@angular/core";
import { routePath } from "../api/grain-api.service";
import type { AttachmentMeta, AttachmentUpload } from "../api/models";
import { AccessService } from "../services/access.service";

export const MAX_ATTACHMENT_SIZE = 10 * 1024 * 1024;
export const MAX_ATTACHMENTS_PER_REQUEST = 10;

const INLINE_IMAGE_TYPES = new Set([
  "image/png",
  "image/jpeg",
  "image/gif",
  "image/webp",
  "image/bmp",
]);

export function isInlineImageType(contentType?: string): boolean {
  const base = (contentType || "").split(";")[0].trim().toLowerCase();
  return INLINE_IMAGE_TYPES.has(base);
}

export function attachmentHref(
  taskId: string,
  attachmentId: string | number,
): string {
  return routePath("GET /api/tasks/{id}/attachments/{attachmentId}", {
    id: taskId,
    attachmentId: String(attachmentId),
  });
}

export function formatBytes(bytes?: number): string {
  if (bytes === undefined || bytes === null || !Number.isFinite(bytes)) {
    return "";
  }
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk));
  }
  return btoa(binary);
}

export async function fileToUpload(file: File): Promise<AttachmentUpload> {
  if (typeof file.arrayBuffer === "function") {
    const buf = await file.arrayBuffer();
    return {
      filename: file.name || "attachment",
      contentType: file.type || "application/octet-stream",
      content: bytesToBase64(new Uint8Array(buf)),
    };
  }
  return new Promise<AttachmentUpload>((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => reject(new Error(`Could not read ${file.name}`));
    reader.onload = () => {
      const res = String(reader.result || "");
      const idx = res.indexOf("base64,");
      resolve({
        filename: file.name || "attachment",
        contentType: file.type || "application/octet-stream",
        content: idx >= 0 ? res.slice(idx + 7) : res,
      });
    };
    reader.readAsDataURL(file);
  });
}

export async function readFilesAsUploads(
  files: FileList | readonly File[] | null | undefined,
  existingCount = 0,
): Promise<{ uploads: AttachmentUpload[]; error?: string }> {
  const list = files ? Array.from(files) : [];
  if (!list.length) return { uploads: [] };
  if (existingCount + list.length > MAX_ATTACHMENTS_PER_REQUEST) {
    return {
      uploads: [],
      error: `Attach at most ${MAX_ATTACHMENTS_PER_REQUEST} files at once.`,
    };
  }
  for (const f of list) {
    if (f.size > MAX_ATTACHMENT_SIZE) {
      return {
        uploads: [],
        error: `${f.name} is larger than 10 MB (${formatBytes(f.size)}).`,
      };
    }
  }
  const uploads = await Promise.all(list.map(fileToUpload));
  return { uploads };
}

export function filesFromTransfer(dt: DataTransfer | null | undefined): File[] {
  if (!dt) return [];
  if (dt.files?.length) return Array.from(dt.files);
  const out: File[] = [];
  if (dt.items) {
    for (let i = 0; i < dt.items.length; i++) {
      const item = dt.items[i];
      if (item.kind === "file") {
        const f = item.getAsFile();
        if (f) out.push(f);
      }
    }
  }
  return out;
}

@Component({
  selector: "g-attachment-list",
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./attachments.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (attachments()?.length && taskId()) {
      <div class="att-list" data-testid="attachment-list">
        @for (a of attachments(); track a.id) {
          @if (canRead()) {
            <a
              class="att-card"
              [href]="href(a.id)"
              target="_blank"
              rel="noopener noreferrer"
              [attr.download]="isImage(a.contentType) ? null : a.filename"
              [attr.data-testid]="'attachment-' + a.id"
            >
              @if (isImage(a.contentType)) {
                <img
                  class="att-thumb"
                  [src]="href(a.id)"
                  [alt]="a.filename"
                  loading="lazy"
                />
              }
              <span class="att-row">
                <span class="att-name">{{ a.filename }}</span>
                @if (a.size) {
                  <span class="att-size">{{ bytes(a.size) }}</span>
                }
              </span>
            </a>
          } @else {
            <span class="att-card" [attr.data-testid]="'attachment-' + a.id">
              <span class="att-row">
                <span class="att-name">{{ a.filename }}</span>
                @if (a.size) {
                  <span class="att-size">{{ bytes(a.size) }}</span>
                }
              </span>
            </span>
          }
        }
      </div>
    }
  `,
})
export class AttachmentListComponent {
  private readonly access = inject(AccessService);

  readonly taskId = input.required<string>();
  readonly repo = input<string | undefined>(undefined);
  readonly attachments = input<readonly AttachmentMeta[] | undefined>([]);

  protected readonly isImage = isInlineImageType;
  protected readonly bytes = formatBytes;

  protected canRead(): boolean {
    return this.access.can(
      "GET /api/tasks/{id}/attachments/{attachmentId}",
      this.repo(),
    );
  }

  protected href(id: string | number): string {
    return attachmentHref(this.taskId(), id);
  }
}

@Component({
  selector: "g-attachment-picker",
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./attachments.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="att-picker-wrap">
      @if (dropzone()) {
        <div
          class="att-dropzone"
          [class.is-dragover]="dragging()"
          (dragover)="onDragOver($event)"
          (dragleave)="dragging.set(false)"
          (drop)="onDrop($event)"
          [attr.data-testid]="testid() ? testid() + '-dropzone' : null"
        >
          <span class="muted"
            >Drop files or paste images (up to 10 MB each)</span
          >
          <button
            type="button"
            class="btn-tonal"
            [disabled]="disabled()"
            [attr.data-testid]="testid() || 'attach-btn'"
            (click)="openPicker()"
          >
            Attach file
          </button>
        </div>
      } @else {
        <div class="att-picker-bar">
          <button
            type="button"
            class="btn-text"
            [disabled]="disabled()"
            [attr.data-testid]="testid() || 'attach-btn'"
            (click)="openPicker()"
          >
            Attach file
          </button>
        </div>
      }
      <input
        #fileInput
        type="file"
        multiple
        hidden
        [attr.data-testid]="(testid() || 'attach-btn') + '-input'"
        (change)="onFilesSelected($event)"
      />
      @if (fileError(); as err) {
        <p class="banner banner-error" role="alert">{{ err }}</p>
      }
      @if (staged().length) {
        <ul class="att-staged" aria-label="Staged attachments">
          @for (u of staged(); track $index) {
            <li
              class="att-staged-chip"
              [attr.data-testid]="'staged-att-' + $index"
            >
              <span>{{ u.filename }}</span>
              <button
                type="button"
                class="att-remove"
                [attr.aria-label]="'Remove ' + u.filename"
                (click)="removeAt($index)"
              >
                ×
              </button>
            </li>
          }
        </ul>
      }
    </div>
  `,
})
export class AttachmentPickerComponent {
  readonly staged = input<readonly AttachmentUpload[]>([]);
  readonly disabled = input<boolean>(false);
  readonly dropzone = input<boolean>(false);
  readonly testid = input<string>("");

  readonly stagedChange = output<AttachmentUpload[]>();

  protected readonly dragging = signal(false);
  protected readonly fileError = signal<string | null>(null);
  private readonly cdr = inject(ChangeDetectorRef);
  private readonly fileInput =
    viewChild<ElementRef<HTMLInputElement>>("fileInput");

  protected openPicker(): void {
    this.fileInput()?.nativeElement.click();
  }

  protected async onFilesSelected(ev: Event): Promise<void> {
    const inputEl = ev.target as HTMLInputElement;
    await this.ingest(inputEl.files);
    inputEl.value = "";
  }

  protected onDragOver(ev: DragEvent): void {
    ev.preventDefault();
    if (!this.disabled()) this.dragging.set(true);
  }

  protected async onDrop(ev: DragEvent): Promise<void> {
    ev.preventDefault();
    this.dragging.set(false);
    if (this.disabled()) return;
    await this.ingest(filesFromTransfer(ev.dataTransfer));
  }

  async ingest(
    files: FileList | readonly File[] | null | undefined,
  ): Promise<void> {
    const cur = this.staged();
    const { uploads, error } = await readFilesAsUploads(files, cur.length);
    if (error) {
      this.fileError.set(error);
      this.cdr.markForCheck();
      return;
    }
    this.fileError.set(null);
    if (uploads.length) {
      this.stagedChange.emit([...cur, ...uploads]);
    }
    this.cdr.markForCheck();
  }

  protected removeAt(idx: number): void {
    const next = this.staged().filter((_, i) => i !== idx);
    this.fileError.set(null);
    this.stagedChange.emit(next);
  }
}
