import { test, expect } from "./fixtures.js";

// Every colour inside an interactive task's chat, checked against the
// theme's own palette -- in both modes.
//
// # What this is for
//
// TaskChat.jsx draws the conversation with a vendored widget
// (@chatscope/chat-ui-kit-react) whose stylesheet carries a complete
// palette of its own. Twice now an element of it has been reported as
// "light blue and the only ui element of that color on the page": the
// composer (task 23), then the message list's scroll bar (task 267).
// Both reports were correct and neither was about the element it
// named -- the widget simply was not this application's colours, and
// whichever part of it somebody looked at next was going to be blue
// too. src/chatscopeTheme.js and the --cs-* block in style.css are the
// fix; this is what says it worked, and keeps saying so.
//
// # Why a palette sweep rather than a screenshot
//
// The other candidate was a screenshot of this pane in each mode. A
// screenshot would have caught today's scroll bar, but it is the wrong
// instrument for the failure this is about: its golden has to be
// replaced for every legitimate change to the pane's layout, spacing or
// copy, and when it does fail it says "these pixels differ" rather than
// which element is wrong and what colour it went. The next un-themed
// element is also usually a state a screenshot does not hold -- a
// hover, a focus ring, a disabled composer.
//
// So this asserts the property the fix actually has: no element in the
// chat computes a colour that is not one of the theme's own. It reads
// every custom property the app's stylesheets define, resolves each to
// a colour, and walks the chat -- every element, and the ::before and
// ::after of each, since the separator's rules and the scroll rails are
// drawn there -- comparing every colour-valued property against that
// set. A colour that is not in the palette fails the test by name, in
// whichever mode it appeared, whether or not anybody thought to assert
// on that element.
//
// Alpha is deliberately ignored in the comparison: MUI derives an
// outlined button's border and its hover fill from the same accent at
// lower opacity, and those are the theme's colour, used as the theme
// means it.

const MODES = ["light", "dark"];

// The colour-valued properties worth reading off an element, each with
// what has to be true for it to put any colour on the screen: a border
// colour on a border nobody draws is not a colour anybody can see, and
// reporting it would bury the ones that are. `true` is "always", since
// text and backgrounds paint unconditionally.
//
// background-image is read as well, for the gradients the kit draws its
// dots and bullets with. `fill` and `stroke` apply to SVG only, where
// they default to black on every element in the document and would
// otherwise be one false finding per element in the pane.
const COLOUR_PROPERTIES = {
  color: true,
  "background-color": true,
  "border-top-color": "border-top-style,border-top-width",
  "border-right-color": "border-right-style,border-right-width",
  "border-bottom-color": "border-bottom-style,border-bottom-width",
  "border-left-color": "border-left-style,border-left-width",
  "outline-color": "outline-style,outline-width",
  "text-decoration-color": "text-decoration-line",
  fill: "svg",
  stroke: "svg",
};

// audit runs in the page: it resolves the theme's palette, then reports
// every colour inside `root` that is not in it. Written as one function
// passed whole to page.evaluate, because none of it can be done from
// the test process -- these are computed values, which only the browser
// has.
function audit([selector, properties]) {
  const probe = document.createElement("span");
  probe.style.display = "none";
  document.body.append(probe);
  const parse = (value) => {
    probe.style.color = "rgb(1, 2, 3)";
    probe.style.color = value;
    const resolved = getComputedStyle(probe).color;
    if (resolved === "rgb(1, 2, 3)" && value.trim() !== "rgb(1, 2, 3)") {
      return null;
    }
    const parts = resolved.match(/[\d.]+/g);
    if (!parts || parts.length < 3) return null;
    const alpha = parts.length > 3 ? Number(parts[3]) : 1;
    return { key: parts.slice(0, 3).join(" "), alpha };
  };

  // The palette is every custom property the app's own stylesheets
  // declare, resolved on :root -- so it follows whichever mode is on
  // without this test holding a second copy of the values.
  const names = new Set();
  for (const sheet of document.styleSheets) {
    let rules;
    try {
      rules = sheet.cssRules;
    } catch {
      continue; // a cross-origin sheet, which this app has none of
    }
    for (const rule of rules) {
      if (!rule.style) continue;
      for (const property of rule.style) {
        if (property.startsWith("--")) names.add(property);
      }
    }
  }
  const root = getComputedStyle(document.documentElement);
  const palette = new Map();
  for (const name of names) {
    const colour = parse(root.getPropertyValue(name));
    if (colour) palette.set(colour.key, name);
  }

  const describe = (element, pseudo) => {
    const classes = [...element.classList].join(".");
    return `${element.tagName.toLowerCase()}${classes ? `.${classes}` : ""}${pseudo ?? ""}`;
  };

  // Whether this property paints anything on this element: a border
  // colour with no border under it, or an SVG property on an element
  // that is not SVG, is not a colour on the page.
  const paints = (element, style, condition) => {
    if (condition === true) return true;
    if (condition === "svg") return element.namespaceURI.endsWith("/svg");
    return condition.split(",").every((property) => {
      const value = style.getPropertyValue(property);
      return value && value !== "none" && value !== "hidden" && value !== "0px";
    });
  };

  const foreign = [];
  const seen = new Set();
  const container = document.querySelector(selector);
  if (!container) return { error: `no ${selector} on the page` };
  for (const element of [container, ...container.querySelectorAll("*")]) {
    for (const klass of element.classList) seen.add(klass);
    // MUI's own buttons are left to MUI, and they are the only
    // exception here. An outlined Button's disabled text and an
    // IconButton's resting icon are the neutral overlays its palette
    // derives for itself (action.disabled, action.active) -- black or
    // white at a low alpha, chosen by the mode, and not a custom
    // property anywhere. That is the MUI theme's business. It covers
    // the Send button and the attachment picker in the compose row and
    // nothing else: no part of the vendored widget is a MuiButtonBase,
    // and this must never be widened to `[class*="Mui"]`, which would
    // match the dialog the whole pane sits in and quietly excuse the
    // lot.
    if (element.closest(".MuiButtonBase-root")) continue;
    for (const pseudo of [null, "::before", "::after"]) {
      const style = getComputedStyle(element, pseudo);
      // An element with no generated content has no ::before to paint.
      if (pseudo && (style.content === "none" || style.content === "")) {
        continue;
      }
      const values = Object.entries(properties)
        .filter(([, condition]) => paints(element, style, condition))
        .map(([property]) => [property, style.getPropertyValue(property)]);
      const images = [...style.backgroundImage.matchAll(/rgba?\([^)]*\)/g)];
      for (const [match] of images) values.push(["background-image", match]);
      for (const [property, value] of values) {
        if (!value || value === "none" || value === "currentcolor") continue;
        const colour = parse(value);
        // Fully transparent is not a colour anybody can see.
        if (!colour || colour.alpha === 0) continue;
        if (palette.has(colour.key)) continue;
        foreign.push(`${describe(element, pseudo)} ${property}: ${value}`);
      }
    }
  }
  probe.remove();
  return { foreign: [...new Set(foreign)].sort(), seen: [...seen].sort() };
}

// The other half of "themed": a colour-for-colour substitution can put
// every value in the palette and still leave a message unreadable, by
// landing the ink and the fill on the same side of the mode -- which is
// exactly the risk in a dark theme, where the vendored default assumed
// a white ground. These are the three places a person actually reads
// text in this pane, checked against WCAG AA for body text.
const READABLE = [
  [".cs-message--incoming .cs-message__content", "an incoming bubble"],
  [".cs-message--outgoing .cs-message__content", "your own bubble"],
  [".cs-message-input__content-editor", "the composer"],
];

function contrast(pairs) {
  const rgb = (value) => (value.match(/[\d.]+/g) ?? []).map(Number);
  const relative = ([r, g, b]) => {
    const channel = (v) => {
      const s = v / 255;
      return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
    };
    return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
  };
  const ratio = (a, b) => {
    const [light, dark] = [relative(a), relative(b)].sort((x, y) => y - x);
    return (light + 0.05) / (dark + 0.05);
  };
  return pairs.map(([selector, label]) => {
    const element = document.querySelector(selector);
    if (!element) return { label, ratio: null };
    const ink = rgb(getComputedStyle(element).color);
    // The nearest ground that is actually opaque: a bubble's own fill,
    // or whatever shows through it.
    let ground = null;
    for (let node = element; node && !ground; node = node.parentElement) {
      const parts = rgb(getComputedStyle(node).backgroundColor);
      if (parts.length === 3 || parts[3] === 1) ground = parts.slice(0, 3);
    }
    return { label, ratio: ground ? ratio(ink, ground) : null };
  });
}

async function sweep(page, selector = ".task-chat") {
  const result = await page.evaluate(audit, [selector, COLOUR_PROPERTIES]);
  expect(result.error ?? null).toBeNull();
  return result;
}

async function setMode(page, mode) {
  await page.addInitScript(
    ([key, value]) => window.localStorage.setItem(key, value),
    ["grain.themeMode", mode],
  );
}

// An interactive task with both sides of a conversation in it: the
// operator's own words (the description, and a reply), and an agent's
// (posted through the messages endpoint, which is the route a run's own
// chat turn takes -- so the bubble is a real incoming one rather than a
// human comment dressed as one).
//
// Filed over the API rather than looked for in the demo store, which
// seeds no interactive task, and left behind afterwards, there being no
// way to delete one -- the same bargain e2e/phone.spec.js's chat tests
// make, and safe for the same reason: an interactive task is absent
// from the ordinary lists every other spec asserts on.
async function chatTask(page) {
  const filed = await page.request.post("/api/tasks", {
    data: {
      title: "E2E chat palette",
      body: "Does anything in here still come from the library's palette?",
      interactive: true,
    },
  });
  expect(filed.ok(), `POST /api/tasks: ${filed.status()}`).toBe(true);
  const task = await filed.json();
  const said = await page.request.post(`/api/tasks/${task.id}/messages`, {
    data: {
      body:
        "It should not: the sheet's colours are rewritten as Vite loads " +
        "it.\n\n```\nnpm run build\n```",
    },
  });
  expect(
    said.ok(),
    `POST /api/tasks/${task.id}/messages: ${said.status()}`,
  ).toBe(true);
  const replied = await page.request.post(`/api/tasks/${task.id}/comments`, {
    data: { body: "Then a scroll bar is the last of it." },
  });
  expect(
    replied.ok(),
    `POST /api/tasks/${task.id}/comments: ${replied.status()}`,
  ).toBe(true);
  // The agent's message parks the task on an answer, and the reply
  // above is that answer -- so this leaves nothing sitting in the
  // inbox another spec in the same worker reads (e2e/inbox.spec.js
  // answers the one parked question the demo store seeds).
  const after = await page.request.get(`/api/tasks/${task.id}`);
  expect(after.ok()).toBe(true);
  expect((await after.json()).state).not.toBe("awaiting_reply");
  return task;
}

async function openChat(page, task) {
  await page.goto(`/tasks/${task.id}`);
  // The chat renders only once the task has arrived and reads
  // interactive, so this wait covers the whole cold start -- bundle,
  // first /api/tasks, render -- which a worker starting beside three
  // others has been seen to need more than the default five seconds
  // for (phone.spec.js's openChatTask makes the same allowance).
  await expect(page.locator(".task-chat-window")).toBeVisible({
    timeout: 30000,
  });
  await expect(page.locator(".cs-message").last()).toBeVisible();
}

for (const mode of MODES) {
  test(`draws the chat in the theme's own colours (${mode})`, async ({
    page,
  }) => {
    await setMode(page, mode);
    const task = await chatTask(page);
    await openChat(page, task);
    expect(
      await page.evaluate(() => document.documentElement.dataset.theme),
    ).toBe(mode);

    const { foreign, seen } = await sweep(page);

    // The sweep has to have looked at the things that were reported, or
    // a pane that failed to render would pass it. The scroll bar is the
    // whole of task 267: perfect-scrollbar's thumb is a real element,
    // and it is the one whose background was the library's pale blue.
    expect(seen).toContain("cs-message-input__content-editor");
    expect(seen).toContain("cs-message__content");
    expect(seen).toContain("ps__thumb-y");

    expect(foreign, `these are not colours from the ${mode} palette`).toEqual(
      [],
    );

    for (const { label, ratio } of await page.evaluate(contrast, READABLE)) {
      expect(ratio, `no ground found under ${label}`).not.toBeNull();
      expect(
        ratio,
        `${label} is ${ratio?.toFixed(1)}:1 against its own fill in ${mode}`,
      ).toBeGreaterThanOrEqual(4.5);
    }
  });
}
