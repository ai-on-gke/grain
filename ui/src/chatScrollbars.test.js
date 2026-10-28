import { describe, expect, it } from "vitest";
import { nameScrollbars } from "./chatScrollbars.js";

// The markup perfect-scrollbar leaves behind inside a chatscope chat:
// a .ps container with its rails, each holding a bare tabindex="0"
// thumb.
function chat() {
  const root = document.createElement("div");
  root.id = "chat-a";
  root.innerHTML = `
    <div class="cs-message-list">
      <div class="scrollbar-container cs-message-list__scroll-wrapper ps">
        <div class="ps__rail-y"><div class="ps__thumb-y" tabindex="0"></div></div>
      </div>
    </div>
    <div class="cs-message-input">
      <div class="scrollbar-container ps">
        <div class="ps__rail-x"><div class="ps__thumb-x" tabindex="0"></div></div>
      </div>
    </div>`;
  document.body.appendChild(root);
  return root;
}

function size(el, { scrollHeight, clientHeight, scrollTop = 0 }) {
  Object.defineProperty(el, "scrollHeight", {
    value: scrollHeight,
    configurable: true,
  });
  Object.defineProperty(el, "clientHeight", {
    value: clientHeight,
    configurable: true,
  });
  el.scrollTop = scrollTop;
}

describe("nameScrollbars", () => {
  it("makes every thumb a named scrollbar pointing at what it scrolls", () => {
    const root = chat();
    nameScrollbars(root, "Scroll messages");
    const [list, composer] = root.querySelectorAll(".ps");
    const y = root.querySelector(".ps__thumb-y");
    const x = root.querySelector(".ps__thumb-x");
    expect(y.getAttribute("role")).toBe("scrollbar");
    expect(y.getAttribute("aria-label")).toBe("Scroll messages");
    expect(y.getAttribute("aria-orientation")).toBe("vertical");
    expect(y.getAttribute("aria-controls")).toBe(list.id);
    expect(list.id).toBeTruthy();
    // Still a tab stop: it is the keyboard's only way to scroll.
    expect(y.getAttribute("tabindex")).toBe("0");
    expect(x.getAttribute("role")).toBe("scrollbar");
    expect(x.getAttribute("aria-label")).toBe("Scroll message");
    expect(x.getAttribute("aria-orientation")).toBe("horizontal");
    expect(x.getAttribute("aria-controls")).toBe(composer.id);
    expect(composer.id).not.toBe(list.id);
    root.remove();
  });

  it("reports where the view sits, and follows it", () => {
    const root = chat();
    const list = root.querySelector(".ps");
    const y = root.querySelector(".ps__thumb-y");
    size(list, { scrollHeight: 1000, clientHeight: 200, scrollTop: 800 });
    nameScrollbars(root, "Scroll messages");
    expect(y.getAttribute("aria-valuenow")).toBe("100");
    list.scrollTop = 200;
    nameScrollbars(root, "Scroll messages");
    expect(y.getAttribute("aria-valuenow")).toBe("25");
    expect(y.getAttribute("aria-valuemin")).toBe("0");
    expect(y.getAttribute("aria-valuemax")).toBe("100");
    root.remove();
  });

  it("is a no-op without a root", () => {
    expect(() => nameScrollbars(null, "x")).not.toThrow();
  });
});
