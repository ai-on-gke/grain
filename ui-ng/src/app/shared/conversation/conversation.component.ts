import {
  ChangeDetectionStrategy,
  Component,
  ElementRef,
  afterNextRender,
  input,
  model,
  output,
  signal,
  viewChild,
  ViewEncapsulation,
  OnDestroy,
  HostListener,
} from "@angular/core";
import { NgTemplateOutlet } from "@angular/common";
import { MarkdownDirective } from "../code-highlight";
import { ActorChipComponent } from "../actor-chip.component";
import { ago } from "../../tasks/task-view";
import { isHuman } from "../../sessions/session-view";
import {
  AttachmentListComponent,
  AttachmentPickerComponent,
  filesFromTransfer,
  readFilesAsUploads,
} from "../attachments";
import type { Comment, AttachmentUpload } from "../../api/models";
import { TipDirective } from "../tip.directive";

export interface ComposerConfig {
  placeholder?: string;
  submitLabel?: string;
  secondaryLabel?: string;
  disabled?: boolean;
  deniedReason?: string;
  hidden?: boolean;
  locked?: boolean;
}

@Component({
  selector: "g-conversation",
  changeDetection: ChangeDetectionStrategy.OnPush,
  encapsulation: ViewEncapsulation.None,
  imports: [
    NgTemplateOutlet,
    MarkdownDirective,
    ActorChipComponent,
    AttachmentListComponent,
    AttachmentPickerComponent,
    TipDirective,
  ],
  styleUrls: ["./conversation.css", "../conversation.css"],
  template: `
    <ng-template #contentTemplate>
      <div class="convo-header" [class.is-fullscreen]="mode() === 'fullscreen'">
        <span class="convo-title">Conversation</span>
        <span class="convo-tools">
          @if (mode() !== "fullscreen") {
            <button
              type="button"
              class="icon-btn"
              [attr.aria-label]="
                mode() === 'expanded'
                  ? 'Collapse conversation'
                  : 'Expand conversation'
              "
              [gTip]="mode() === 'expanded' ? 'Collapse' : 'Expand'"
              (click)="mode.set(mode() === 'expanded' ? 'inline' : 'expanded')"
            >
              {{ mode() === "expanded" ? "⤓" : "⤓" }}
            </button>
            <button
              type="button"
              class="icon-btn"
              aria-label="Open full screen"
              gTip="Full screen (Shift+F)"
              (click)="setMode('fullscreen')"
            >
              ⤢
            </button>
          } @else {
            <button
              type="button"
              class="icon-btn"
              aria-label="Close full screen"
              gTip="Close (Esc)"
              (click)="setMode('inline')"
            >
              ×
            </button>
          }
        </span>
      </div>

      <div
        class="convo-thread chat-log task-log"
        role="log"
        aria-label="Conversation thread"
        #threadScroll
      >
        @if (olderCount() > 0) {
          <button
            type="button"
            class="btn-outlined"
            style="align-self: flex-start"
            data-testid="load-older-comments"
            [disabled]="loadingOlder()"
            (click)="loadEarlier.emit()"
          >
            {{
              loadingOlder()
                ? "Loading earlier comments…"
                : "Load earlier comments (" + olderCount() + ")"
            }}
          </button>
        }

        @for (c of comments(); track c.id) {
          <article
            class="msg"
            [class.is-human]="human(c)"
            [class.is-handover]="c.handover"
            [class.is-question]="c.id === questionCommentId()"
            [attr.data-testid]="'tmsg-' + c.id"
          >
            <span class="msg-meta">
              <span class="msg-author">
                <g-actor-chip
                  [label]="actorLabel(c)"
                  [kind]="c.authorKind"
                  [behalf]="c.onBehalfOfLabel || c.onBehalfOf"
                />
              </span>
              @if (c.id === questionCommentId()) {
                <span class="status-pill reply">Question</span>
              }
              @if (c.handover) {
                <span class="status-pill neutral">Handover note</span>
              }
              <span class="msg-time">{{ time(c.createdAt) }}</span>
            </span>
            <div class="msg-body" [gMarkdown]="c.body || ''"></div>
            @if (c.attachments?.length) {
              <g-attachment-list
                [taskId]="taskId()"
                [repo]="repo()"
                [attachments]="c.attachments"
              />
            }
          </article>
        }
        @if (transcript()) {
          <pre
            class="inbox-live-transcript"
            data-testid="inbox-live-transcript"
            aria-label="Live run transcript"
            >{{ transcript() }}</pre>
        }
      </div>

      @if (!config()?.hidden) {
        <form
          class="chat-composer convo-composer"
          (submit)="onSubmit($event)"
          (dragover)="$event.preventDefault()"
          (drop)="onDrop($event)"
        >
          <textarea
            #composer
            class="reply-textarea"
            data-testid="task-composer"
            [attr.aria-label]="config()?.placeholder || 'Comment on this task'"
            [placeholder]="config()?.placeholder || 'Comment on this task'"
            [value]="draft()"
            [disabled]="config()?.disabled"
            (input)="draft.set($any($event.target).value)"
            (paste)="onPaste($event)"
            (keydown.control.enter)="onEnter($event)"
            (keydown.meta.enter)="onEnter($event)"
          ></textarea>
          <g-attachment-picker
            testid="task-attach"
            [staged]="stagedAttachments()"
            [disabled]="config()?.disabled || false"
            (stagedChange)="stagedAttachments.set($event)"
          />
          <div class="action-bar" style="margin-top: 8px;">
            @if (config()?.secondaryLabel) {
              <button
                type="button"
                class="btn-tonal"
                data-testid="act-send-secondary"
                [disabled]="
                  config()?.disabled ||
                  (!draft().trim() && !stagedAttachments().length)
                "
                (click)="submit('secondary')"
              >
                {{ config()?.secondaryLabel }}
              </button>
            }
            <button
              type="submit"
              class="btn-primary"
              data-testid="act-send"
              [disabled]="
                config()?.disabled ||
                (!draft().trim() && !stagedAttachments().length)
              "
            >
              {{ config()?.submitLabel || "Send" }}
            </button>
          </div>
        </form>
      }
    </ng-template>

    @if (mode() !== "fullscreen") {
      <div class="convo-host" [class.is-expanded]="mode() === 'expanded'">
        <ng-container *ngTemplateOutlet="contentTemplate" />
      </div>
    }

    <dialog #modal class="convo-modal" (close)="onDialogClose()">
      @if (mode() === "fullscreen") {
        <ng-container *ngTemplateOutlet="contentTemplate" />
      }
    </dialog>
  `,
})
export class ConversationComponent implements OnDestroy {
  readonly taskId = input.required<string>();
  readonly repo = input<string | undefined>(undefined);
  readonly comments = input<readonly Comment[]>([]);
  readonly transcript = input<string | null>(null);
  readonly config = input<ComposerConfig>();
  readonly questionCommentId = input<number | undefined>(undefined);

  readonly olderCount = input<number>(0);
  readonly loadingOlder = input<boolean>(false);

  readonly mode = model<"inline" | "expanded" | "fullscreen">("inline");

  readonly send = output<{
    body: string;
    attachments: AttachmentUpload[];
    action?: string;
  }>();
  readonly loadEarlier = output<void>();

  protected readonly draft = signal("");
  protected readonly stagedAttachments = signal<AttachmentUpload[]>([]);

  private readonly modal = viewChild<ElementRef<HTMLDialogElement>>("modal");
  private readonly composer =
    viewChild<ElementRef<HTMLTextAreaElement>>("composer");

  protected readonly human = isHuman;

  constructor() {
    afterNextRender(() => {
      if (this.modal()) {
        document.body.appendChild(this.modal()!.nativeElement);
      }
    });
  }

  ngOnDestroy() {
    if (this.modal()) {
      this.modal()!.nativeElement.remove();
    }
  }

  @HostListener("document:keydown.shift.f", ["$event"])
  onShiftF(ev: KeyboardEvent) {
    if (
      ev.target &&
      (ev.target as HTMLElement).closest("g-conversation, .convo-modal")
    ) {
      ev.preventDefault();
      this.setMode(this.mode() === "fullscreen" ? "inline" : "fullscreen");
    }
  }

  protected setMode(m: "inline" | "expanded" | "fullscreen") {
    if (m === "fullscreen") {
      this.mode.set(m);
      this.modal()?.nativeElement.showModal();
    } else {
      if (this.mode() === "fullscreen") {
        this.modal()?.nativeElement.close();
      }
      this.mode.set(m);
    }
  }

  protected onDialogClose() {
    if (this.mode() === "fullscreen") {
      this.mode.set("inline");
    }
  }

  protected actorLabel(c: Comment): string {
    return c.authorLabel || c.author || "";
  }

  protected time(t: string | undefined): string {
    return ago(t);
  }

  protected async onPaste(ev: ClipboardEvent): Promise<void> {
    const files = filesFromTransfer(ev.clipboardData);
    if (!files.length) return;
    ev.preventDefault();
    const cur = this.stagedAttachments();
    const { uploads, error } = await readFilesAsUploads(files, cur.length);
    if (error) {
      // TODO handle error
      return;
    }
    if (uploads.length) {
      this.stagedAttachments.set([...cur, ...uploads]);
    }
  }

  protected async onDrop(ev: DragEvent): Promise<void> {
    ev.preventDefault();
    const files = filesFromTransfer(ev.dataTransfer);
    if (!files.length) return;
    const cur = this.stagedAttachments();
    const { uploads, error } = await readFilesAsUploads(files, cur.length);
    if (error) {
      // TODO handle error
      return;
    }
    if (uploads.length) {
      this.stagedAttachments.set([...cur, ...uploads]);
    }
  }

  protected onEnter(ev: Event): void {
    if ((ev as KeyboardEvent).isComposing) return;
    ev.preventDefault();
    this.submit();
  }

  protected onSubmit(ev: Event): void {
    ev.preventDefault();
    this.submit();
  }

  protected submit(action?: string) {
    if (this.config()?.disabled) return;
    const b = this.draft();
    const a = this.stagedAttachments();
    if (!b.trim() && !a.length) return;
    this.send.emit({ body: b, attachments: a, action });
    this.draft.set("");
    this.stagedAttachments.set([]);
  }
}
