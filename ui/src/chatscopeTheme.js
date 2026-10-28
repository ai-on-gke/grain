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

// The chat widget's palette, mapped onto this shell's theme in one place.
//
// TaskChat.jsx draws an interactive task's conversation with
// @chatscope/chat-ui-kit-react, and the kit's look comes from one
// vendored stylesheet imported whole
// (@chatscope/chat-ui-kit-styles/dist/default/styles.css). That sheet
// carries a complete palette of its own -- a pale blue, a mid blue, a
// near-white, a blue-grey border, and Material's black-alpha text
// ramp -- and it paints *everything* inside the widget with them: the
// bubbles, the composer, the message separators, the typing indicator,
// the buttons, the conversation header, and the scroll bar on the
// message list. Nothing in grain's own theme (MUI plus style.css)
// reaches inside it.
//
// # Why this file exists rather than another override rule
//
// The failure mode of correcting that per element is that it never
// ends: the composer was reported as "light blue and the only ui
// element of that colour" (task 23), then the scroll bar was reported
// as blue and the only element of that colour (task 267), and the next
// one would have been a header, a separator or a focus ring. Each fix
// is right about the element it names and says nothing about the rest
// of the sheet.
//
// So the sheet is re-pointed at grain's theme as a whole instead.
// react-diff-view, the other vendored stylesheet in this tree, exposes
// its palette as CSS custom properties, and DiffView.jsx simply sets
// them (its own PALETTE); chat-ui-kit-styles has no such hooks -- it is
// Sass, compiled with its colours inlined as literals, and there is not
// one custom property in the shipped CSS. This file gives it the layer
// it does not have: every colour literal in the sheet is rewritten, as
// Vite loads it (vite.config.js's chatscopeTheme plugin), into a
// `var(--cs-...)` reference, and style.css maps that small `--cs-*`
// family onto the theme's own tokens. Because those tokens already
// change with the light/dark choice, so does the whole widget, with no
// second set of values to keep in step.
//
// What is deliberately *not* here: which element should be which
// colour. This is a colour-for-colour substitution of the library's
// palette, so the handful of places where grain wants something other
// than the library's own choice of role -- an outgoing bubble tinted
// rather than filled with the accent, a separator drawn as a rule
// rather than in the accent -- stay as rules in style.css, beside the
// rest of the widget's styling.
//
// # Keeping it complete
//
// The whole point is that nothing is left unmapped, so
// chatscopeTheme.test.js reads the installed stylesheet and fails if it
// contains a colour this file neither maps nor explicitly exempts. An
// upgrade of the kit that introduces a new colour is then a red test
// with the literal in its message, rather than another report six
// months later about one blue element.

// The library's palette, in the spelling its own stylesheet uses, with
// the name each colour has in the kit's Sass variables (themes/default/
// _variables.scss) beside it. Values are `var(--cs-*)`; style.css holds
// what those come to.
export const CHATSCOPE_PALETTE = {
  // $color-text, and the two greys the kit lightens it to for a
  // message's header and a conversation's secondary line.
  "rgba(0, 0, 0, 0.87)": "var(--cs-color-text)",
  "rgba(0, 0, 0, 0.6)": "var(--cs-color-text-medium)",
  "rgba(0, 0, 0, 0.38)": "var(--cs-color-text-disabled)",
  "rgba(77, 77, 77, 0.87)": "var(--cs-color-text-light)",
  "rgba(102, 102, 102, 0.87)": "var(--cs-color-text-medium)",
  "rgba(179, 179, 179, 0.87)": "var(--cs-color-text-disabled)",

  // $default-bg-color and $default-border-color: the ground every
  // container in the kit paints, and the line between them.
  "#fff": "var(--cs-bg)",
  "#d1dbe3": "var(--cs-border-color)",
  // The avatar group's own border, which is $color-white darkened
  // rather than the border colour -- same role here.
  "#f2f2f2": "var(--cs-border-color)",

  // $color-primary and its two relatives: the pale blue the composer
  // and an incoming bubble are filled with, the near-white behind a
  // conversation header, and the mid blue that is the kit's accent --
  // buttons, separators, the typing indicator, an outgoing bubble and
  // the message list's scroll thumb.
  "#c6e3fa": "var(--cs-color-primary)",
  "#f6fbff": "var(--cs-color-primary-light)",
  "#6ea9d7": "var(--cs-color-primary-dark)",
  "rgba(198, 227, 250, 0.38)": "var(--cs-color-primary-faded)",
  "rgba(110, 169, 215, 0.6)": "var(--cs-color-primary-dark-faded)",

  // The hover/active/selected states the kit derives from the accent.
  "#f3f8fc": "var(--cs-bg-hover)",
  "#d5e7f5": "var(--cs-bg-active)",
  "#2b6a9b": "var(--cs-color-selected)",
  // A dead declaration -- the rule that sets it sets $color-primary-light
  // over it on the next line -- but a colour in the sheet all the same.
  "#eee": "var(--cs-bg-hover)",

  // $color-danger, which is the unread badge and the do-not-disturb
  // bullet both.
  "#ec1212": "var(--cs-color-danger)",

  // A conversation's unread dot: a two-stop gradient and the shadow
  // under it.
  "#2e63e8": "var(--cs-unread-dot)",
  "#7c9df1": "var(--cs-unread-dot-light)",
  "#b3b3b3": "var(--cs-shadow-color)",
};

// Colours left exactly as the library drew them, each for a reason that
// is not "nobody has noticed it yet". chatscopeTheme.test.js treats
// this as the complete list of exceptions.
export const CHATSCOPE_EXEMPT = {
  // The specular gloss the kit lays over an avatar's presence bullet on
  // hover: white at varying transparency in a radial gradient, which is
  // a highlight on whatever is underneath rather than a colour from the
  // palette.
  white: "specular highlight on a presence bullet",
  "rgba(255, 255, 255, 0)": "specular highlight on a presence bullet",
  "rgba(255, 255, 255, 0.8)": "specular highlight on a presence bullet",

  // The presence palette (Status/Avatar): available green, away amber,
  // do-not-disturb red, and so on, each drawn as a light and a base
  // stop of one gradient. These are a semantic palette of their own
  // rather than the widget's chrome -- the thing this file is about --
  // and grain renders no Status or Avatar at all. Re-point them here if
  // it ever does.
  "#00d5a6": "presence: available",
  "#00a27e": "presence: available",
  "#ffb527": "presence: unavailable",
  "#a66d00": "presence: unavailable",
  "#ffdbb0": "presence: away",
  "#fc8b00": "presence: away",
  "#f89f9f": "presence: do not disturb",
  "#c2d1d9": "presence: invisible",
  "#fffccc": "presence: eager",
  "#ffee00": "presence: eager",
};

// A colour literal as it can appear in the kit's stylesheet. The
// negative lookahead on the keywords keeps `white-space: pre-wrap` --
// which the sheet does use -- from reading as the colour white.
const COLOR =
  /#[0-9a-fA-F]{3,8}\b|rgba?\([^)]*\)|\b(?:white|black)\b(?![-\w])/g;

const KEYWORDS = { white: [255, 255, 255, 1], black: [0, 0, 0, 1] };

// canonical turns the spellings of one colour into a single key, so the
// table above can be written the way the stylesheet reads while still
// matching a minified `rgba(0,0,0,.87)` or an upper-case `#FFF`.
export function canonical(literal) {
  const text = literal.trim().toLowerCase();
  if (KEYWORDS[text]) return KEYWORDS[text].join(" ");
  if (text.startsWith("#")) {
    const hex = text.slice(1);
    const wide = hex.length <= 4 ? [...hex].map((c) => c + c).join("") : hex;
    if (wide.length !== 6 && wide.length !== 8) return null;
    const byte = (i) => parseInt(wide.slice(i * 2, i * 2 + 2), 16);
    const alpha = wide.length === 8 ? byte(3) / 255 : 1;
    return [byte(0), byte(1), byte(2), alpha].join(" ");
  }
  const parts = text
    .slice(text.indexOf("(") + 1, text.lastIndexOf(")"))
    .split(/[,/\s]+/)
    .filter(Boolean)
    .map(Number);
  if (parts.length < 3 || parts.some(Number.isNaN)) return null;
  const alpha = parts.length > 3 ? parts[3] : 1;
  return [parts[0], parts[1], parts[2], alpha].join(" ");
}

function byCanonical(table) {
  const out = new Map();
  for (const [literal, value] of Object.entries(table)) {
    const key = canonical(literal);
    if (key) out.set(key, value);
  }
  return out;
}

const mapped = byCanonical(CHATSCOPE_PALETTE);
const exempt = byCanonical(CHATSCOPE_EXEMPT);

// themeChatscopeCss rewrites every mapped colour in the kit's
// stylesheet to the custom property standing for it. Anything unmapped
// is left alone rather than guessed at -- and reported by
// unmappedColors, which is what the test asserts on.
export function themeChatscopeCss(css) {
  return css.replace(COLOR, (literal) => {
    const key = canonical(literal);
    return (key && mapped.get(key)) || literal;
  });
}

// unmappedColors is every colour in the given stylesheet that is
// neither mapped nor exempt, in the spelling the sheet uses.
export function unmappedColors(css) {
  const found = new Set();
  for (const [literal] of css.matchAll(COLOR)) {
    const key = canonical(literal);
    if (!key || mapped.has(key) || exempt.has(key)) continue;
    found.add(literal);
  }
  return [...found].sort();
}

// The custom properties the rewrite can emit -- what style.css has to
// define for the widget to have any colour at all.
export function chatscopeVariables() {
  const names = new Set();
  for (const value of Object.values(CHATSCOPE_PALETTE)) {
    names.add(value.slice("var(".length, -1));
  }
  return [...names].sort();
}
