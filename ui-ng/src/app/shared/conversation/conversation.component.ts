import {
  ChangeDetectionStrategy,
  Component,
  ElementRef,
  HostListener,
  OnDestroy,
  ViewEncapsulation,
  afterRenderEffect,
  effect,
  inject,
  input,
  model,
  output,
  signal,
  untracked,
  viewChild,
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
import { DialogTrapDirective } from "../dialog-trap.directive";
import { isTypingTarget } from "../../services/keyboard.service";

export type ConversationMode = "inline" | "expanded" | "fullscreen";

// Pixels from the bottom within which the thread counts as "following" new
// messages; further up, new messages raise the jump pill instead.
const FOLLOW_SLACK_PX = 48;

export interface ComposerConfig {
  placeholder?: string;
  submitLabel?: string;
  secondaryLabel?: string;
  disabled?: boolean;
  submitDisabled?: boolean;
  submitTitle?: string;
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
    DialogTrapDirective,
  ],
  styleUrls: ["./conversation.css", "../conversation.css"],
  host: {
    class: "g-conversation",
    "[class.is-expanded]": 'mode() === "expanded"',
  },
  template: `
    <ng-template #contentTemplate>
      <div class="convo-header" [class.is-fullscreen]="mode() === 'fullscreen'">
        <span class="convo-title"
          >Conversation
          @if (comments().length) {
            <span class="convo-count tabular">{{ comments().length }}</span>
          }
        </span>
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
              [attr.aria-pressed]="mode() === 'expanded'"
              data-testid="convo-expand"
              [gTip]="mode() === 'expanded' ? 'Collapse' : 'Expand'"
              (click)="setMode(mode() === 'expanded' ? 'inline' : 'expanded')"
            >
              {{ mode() === "expanded" ? "⇱" : "⇲" }}
            </button>
            <button
              type="button"
              class="icon-btn"
              aria-label="Open full screen"
              data-testid="convo-fullscreen"
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
              data-testid="convo-close"
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
        [class.is-empty]="!comments().length && !transcript() && !olderCount()"
        role="log"
        aria-label="Conversation thread"
        tabindex="0"
        #thread
        (scroll)="onThreadScroll()"
      >
        @if (olderCount() > 0) {
          <button
            type="button"
            class="btn-outlined convo-older"
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
        @if (!comments().length && !transcript() && !olderCount()) {
          <p class="convo-empty" data-testid="convo-empty">
            No messages yet.
            @if (!config()?.hidden) {
              Anything you write below goes on this task.
            }
          </p>
        }
        @if (transcript()) {
          <pre
            class="inbox-live-transcript"
            data-testid="inbox-live-transcript"
            aria-label="Live run transcript"
            >{{ transcript() }}</pre>
        }
      </div>
      @if (unseen() > 0) {
        <button
          type="button"
          class="convo-jump"
          data-testid="convo-jump"
          (click)="jumpToLatest()"
        >
          New messages ↓
        </button>
      }

      @if (config()?.deniedReason) {
        <div data-testid="reply-blocked" class="convo-denied" role="note">
          {{ config()?.deniedReason }}
        </div>
      }
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
            rows="2"
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
          <div class="action-bar convo-actions">
            <span class="muted composer-hint"
              >Ctrl+Enter sends &middot; paste or drop files</span
            >
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
              [title]="config()?.deniedReason || config()?.submitTitle || ''"
              [disabled]="
                config()?.disabled ||
                config()?.submitDisabled ||
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

    <dialog
      #modal
      class="convo-modal"
      aria-label="Conversation"
      data-testid="convo-modal"
      (close)="onDialogClose()"
    >
      @if (mode() === "fullscreen") {
        <div
          class="convo-modal-inner"
          gDialogTrap
          (gDialogClose)="setMode('inline')"
        >
          <ng-container *ngTemplateOutlet="contentTemplate" />
        </div>
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

  readonly mode = model<ConversationMode>("inline");

  readonly send = output<{
    body: string;
    attachments: AttachmentUpload[];
    action?: string;
  }>();
  readonly loadEarlier = output<void>();

  readonly draft = model("");
  readonly stagedAttachments = model<AttachmentUpload[]>([]);

  /** Messages that arrived while the reader was scrolled up. */
  protected readonly unseen = signal(0);

  private readonly host = inject<ElementRef<HTMLElement>>(ElementRef);
  private readonly modal = viewChild<ElementRef<HTMLDialogElement>>("modal");
  private readonly thread = viewChild<ElementRef<HTMLElement>>("thread");
  private readonly composer =
    viewChild<ElementRef<HTMLTextAreaElement>>("composer");

  protected readonly human = isHuman;

  private following = true;
  private lastCount = 0;
  private lastTask = "";

  constructor() {
    effect(() => {
      this.taskId();
      untracked(() => {
        this.stagedAttachments.set([]);
        this.unseen.set(0);
      });
    });

    // Keep the dialog's open state in step with mode(), whichever side
    // changed it (a parent binding, the buttons, Shift+F).
    afterRenderEffect(() => {
      const m = this.mode();
      const el = this.modal()?.nativeElement;
      if (!el) return;
      if (m === "fullscreen" && !el.open) {
        if (typeof el.showModal === "function") el.showModal();
        else el.setAttribute("open", "");
        this.composer()?.nativeElement.focus();
        this.scrollToEnd();
      } else if (m !== "fullscreen" && el.open) {
        el.close();
      }
    });

    // Follow new messages when the reader is at the bottom; otherwise count
    // them for the "New messages" pill. A task switch always jumps to the end.
    afterRenderEffect(() => {
      const n = this.comments().length;
      const task = this.taskId();
      this.transcript();
      this.mode();
      this.watchThread();
      const el = this.thread()?.nativeElement;
      if (el && this.resize) {
        for (const child of Array.from(el.children)) this.resize.observe(child);
      }
      const switched = task !== this.lastTask;
      const grew = n > this.lastCount;
      if (switched || this.following) {
        this.scrollToEnd();
        this.following = true;
        if (this.unseen()) this.unseen.set(0);
      } else if (grew) {
        this.unseen.update((u) => u + (n - this.lastCount));
      }
      this.lastTask = task;
      this.lastCount = n;
    });
  }

  private resize?: ResizeObserver;
  private resizeTarget: Element | null = null;

  // Markdown renders after the first paint and images load later still, so
  // the thread keeps growing after we scrolled it. While the reader follows
  // the end, keep it pinned there whenever its content changes size.
  private watchThread() {
    const el = this.thread()?.nativeElement;
    if (!el || el === this.resizeTarget) return;
    if (typeof ResizeObserver === "undefined") return;
    this.resize?.disconnect();
    this.resize = new ResizeObserver(() => {
      if (this.following) this.scrollToEnd();
    });
    this.resize.observe(el);
    for (const child of Array.from(el.children)) this.resize.observe(child);
    this.resizeTarget = el;
  }

  // The full-screen view is a native <dialog> opened with showModal(): the
  // browser lifts it into the top layer and makes the rest of the document
  // inert, so it stays where Angular rendered it (no manual DOM moves).
  ngOnDestroy() {
    this.resize?.disconnect();
    const el = this.modal()?.nativeElement;
    if (el?.open) el.close();
  }

  @HostListener("document:keydown", ["$event"])
  onKey(ev: KeyboardEvent) {
    if (!ev.shiftKey || ev.key.toLowerCase() !== "f") return;
    if (ev.ctrlKey || ev.metaKey || ev.altKey || ev.defaultPrevented) return;
    // A capital F typed into the composer is text, not a shortcut.
    if (isTypingTarget(ev.target)) return;
    const t = ev.target as Node | null;
    const inside =
      !!t &&
      (this.host.nativeElement.contains(t) ||
        !!this.modal()?.nativeElement.contains(t));
    if (!inside) return;
    ev.preventDefault();
    this.setMode(this.mode() === "fullscreen" ? "inline" : "fullscreen");
  }

  protected setMode(m: ConversationMode) {
    this.mode.set(m);
  }

  protected onDialogClose() {
    if (this.mode() === "fullscreen") this.mode.set("inline");
  }

  protected onThreadScroll() {
    const el = this.thread()?.nativeElement;
    if (!el) return;
    this.following =
      el.scrollHeight - el.scrollTop - el.clientHeight <= FOLLOW_SLACK_PX;
    if (this.following && this.unseen()) this.unseen.set(0);
  }

  protected jumpToLatest() {
    this.following = true;
    this.unseen.set(0);
    this.scrollToEnd();
  }

  private scrollToEnd() {
    const el = this.thread()?.nativeElement;
    if (!el) return;
    // Jumps are instant: a smooth scroll started before late content lands
    // stops short of the real end.
    const prev = el.style.scrollBehavior;
    el.style.scrollBehavior = "auto";
    el.scrollTop = el.scrollHeight;
    el.style.scrollBehavior = prev;
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
    await this.stage(files);
  }

  protected async onDrop(ev: DragEvent): Promise<void> {
    ev.preventDefault();
    const files = filesFromTransfer(ev.dataTransfer);
    if (!files.length) return;
    await this.stage(files);
  }

  private async stage(files: File[]): Promise<void> {
    const cur = this.stagedAttachments();
    const { uploads } = await readFilesAsUploads(files, cur.length);
    if (uploads.length) this.stagedAttachments.set([...cur, ...uploads]);
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
    const c = this.config();
    if (c?.disabled) return;
    if (!action && c?.submitDisabled) return;
    const b = this.draft();
    const a = this.stagedAttachments();
    if (!b.trim() && !a.length) return;
    this.following = true;
    this.send.emit({ body: b, attachments: a, action });
  }
}
