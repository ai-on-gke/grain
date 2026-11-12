import { ComponentFixture, TestBed } from "@angular/core/testing";
import { Component, signal } from "@angular/core";
import {
  ConversationComponent,
  ConversationMode,
} from "./conversation.component";
import type { Comment } from "../../api/models";

function mk(id: number, body = `message ${id}`): Comment {
  return {
    id,
    body,
    author: "agent",
    authorKind: "agent",
    createdAt: new Date().toISOString(),
  } as Comment;
}

@Component({
  imports: [ConversationComponent],
  template: `
    <div class="pane">
      <div class="sibling" data-testid="sibling">Why this needs a person</div>
      <g-conversation
        [taskId]="'42'"
        [comments]="comments()"
        [(mode)]="mode"
        [(draft)]="draft"
        [config]="{ placeholder: 'Type here', submitLabel: 'Send to #42' }"
        (send)="sent.push($event.body)"
      />
    </div>
  `,
})
class HostComponent {
  readonly comments = signal<Comment[]>([mk(1), mk(2)]);
  readonly mode = signal<ConversationMode>("inline");
  readonly draft = signal("");
  readonly sent: string[] = [];
}

describe("ConversationComponent", () => {
  let f: ComponentFixture<HostComponent>;
  const q = <T extends Element = HTMLElement>(sel: string) =>
    f.nativeElement.querySelector(sel) as T | null;
  const dialog = () => q<HTMLDialogElement>('[data-testid="convo-modal"]')!;

  beforeEach(async () => {
    // jsdom lacks the modal dialog API; mirror the browser's open/close.
    const proto = HTMLDialogElement.prototype as unknown as Record<
      string,
      unknown
    >;
    if (!proto["showModal"]) {
      proto["showModal"] = function (this: HTMLDialogElement) {
        this.setAttribute("open", "");
      };
    }
    if (!proto["close"]) {
      proto["close"] = function (this: HTMLDialogElement) {
        this.removeAttribute("open");
        this.dispatchEvent(new Event("close"));
      };
    }
    await TestBed.configureTestingModule({
      imports: [HostComponent],
    }).compileComponents();
    f = TestBed.createComponent(HostComponent);
    document.body.appendChild(f.nativeElement);
    f.detectChanges();
    await f.whenStable();
  });

  afterEach(() => f.nativeElement.remove());

  it("renders the thread inline with a count and a composer inside the card", () => {
    expect(f.nativeElement.querySelectorAll(".msg").length).toBe(2);
    expect(q(".convo-count")?.textContent?.trim()).toBe("2");
    const host = q(".convo-host")!;
    const ta = q<HTMLTextAreaElement>('[data-testid="task-composer"]')!;
    expect(host.contains(ta)).toBe(true);
    // Geometry (border-box, width 100%) is measured in the browser drive;
    // jsdom does not apply component stylesheets.
    expect(ta.closest(".convo-composer")).not.toBeNull();
    expect(dialog().hasAttribute("open")).toBe(false);
  });

  it("Expand flips the mode for the parent and marks the host so siblings can step aside", async () => {
    q<HTMLButtonElement>('[data-testid="convo-expand"]')!.click();
    f.detectChanges();
    await f.whenStable();
    expect(f.componentInstance.mode()).toBe("expanded");
    expect(q("g-conversation")!.classList.contains("is-expanded")).toBe(true);
    expect(
      q('[data-testid="convo-expand"]')!.getAttribute("aria-pressed"),
    ).toBe("true");
    q<HTMLButtonElement>('[data-testid="convo-expand"]')!.click();
    f.detectChanges();
    expect(f.componentInstance.mode()).toBe("inline");
    expect(q("g-conversation")!.classList.contains("is-expanded")).toBe(false);
  });

  it("full screen opens the dialog with the same thread and keeps the draft through close", async () => {
    f.componentInstance.draft.set("half-written reply");
    q<HTMLButtonElement>('[data-testid="convo-fullscreen"]')!.click();
    f.detectChanges();
    await f.whenStable();
    expect(f.componentInstance.mode()).toBe("fullscreen");
    expect(dialog().hasAttribute("open")).toBe(true);
    expect(dialog().querySelectorAll(".msg").length).toBe(2);
    const ta = dialog().querySelector<HTMLTextAreaElement>(
      '[data-testid="task-composer"]',
    )!;
    expect(ta.value).toBe("half-written reply");
    expect(q(".convo-host")).toBeNull();

    // Escape on a modal dialog fires close; the component returns to inline.
    dialog().close();
    f.detectChanges();
    await f.whenStable();
    expect(f.componentInstance.mode()).toBe("inline");
    expect(q(".convo-host")).not.toBeNull();
    expect(q<HTMLTextAreaElement>('[data-testid="task-composer"]')!.value).toBe(
      "half-written reply",
    );
  });

  it("Shift+F toggles full screen from inside the card but not while typing", async () => {
    const tools = q<HTMLButtonElement>('[data-testid="convo-expand"]')!;
    const ta = q<HTMLTextAreaElement>('[data-testid="task-composer"]')!;
    ta.dispatchEvent(
      new KeyboardEvent("keydown", { key: "F", shiftKey: true, bubbles: true }),
    );
    f.detectChanges();
    expect(f.componentInstance.mode()).toBe("inline");
    tools.dispatchEvent(
      new KeyboardEvent("keydown", { key: "F", shiftKey: true, bubbles: true }),
    );
    f.detectChanges();
    await f.whenStable();
    expect(f.componentInstance.mode()).toBe("fullscreen");
  });

  it("Shift+F outside the conversation does nothing", () => {
    q('[data-testid="sibling"]')!.dispatchEvent(
      new KeyboardEvent("keydown", { key: "F", shiftKey: true, bubbles: true }),
    );
    f.detectChanges();
    expect(f.componentInstance.mode()).toBe("inline");
  });

  it("Ctrl+Enter emits send with the draft", () => {
    f.componentInstance.draft.set("ship it");
    f.detectChanges();
    const ta = q<HTMLTextAreaElement>('[data-testid="task-composer"]')!;
    ta.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Enter",
        ctrlKey: true,
        bubbles: true,
      }),
    );
    expect(f.componentInstance.sent).toEqual(["ship it"]);
  });

  it("shows a New messages pill when messages arrive while scrolled up, and clears it on jump", async () => {
    const thread = q<HTMLElement>(".convo-thread")!;
    // jsdom has no layout: fake a scrolled-up thread.
    Object.defineProperty(thread, "scrollHeight", {
      configurable: true,
      value: 2000,
    });
    Object.defineProperty(thread, "clientHeight", {
      configurable: true,
      value: 300,
    });
    thread.scrollTop = 0;
    thread.dispatchEvent(new Event("scroll"));
    f.componentInstance.comments.set([mk(1), mk(2), mk(3), mk(4)]);
    f.detectChanges();
    await f.whenStable();
    f.detectChanges();
    const pill = q<HTMLButtonElement>('[data-testid="convo-jump"]');
    expect(pill).not.toBeNull();
    pill!.click();
    f.detectChanges();
    expect(q('[data-testid="convo-jump"]')).toBeNull();
    expect(thread.scrollTop).toBe(2000);
  });

  it("follows new messages without a pill when already at the bottom", async () => {
    f.componentInstance.comments.set([mk(1), mk(2), mk(3)]);
    f.detectChanges();
    await f.whenStable();
    f.detectChanges();
    expect(q('[data-testid="convo-jump"]')).toBeNull();
  });
});
