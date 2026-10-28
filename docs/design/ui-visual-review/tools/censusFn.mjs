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

// The in-page census function shared by census.mjs and shoot.mjs --census.
export const census = () => {
  const parse = (c) => {
    const m = c.match(/rgba?\(([^)]+)\)/);
    if (!m) return null;
    const [r, g, b, a = 1] = m[1].split(",").map((x) => parseFloat(x));
    return { r, g, b, a: isNaN(a) ? 1 : a };
  };
  const blend = (fg, bg) => ({
    r: fg.r * fg.a + bg.r * (1 - fg.a),
    g: fg.g * fg.a + bg.g * (1 - fg.a),
    b: fg.b * fg.a + bg.b * (1 - fg.a),
    a: 1,
  });
  const lum = ({ r, g, b }) => {
    const f = (v) => {
      v /= 255;
      return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
    };
    return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
  };
  const ratio = (a, b) => {
    const [l1, l2] = [lum(a), lum(b)].sort((x, y) => y - x);
    return (l1 + 0.05) / (l2 + 0.05);
  };
  const hex = ({ r, g, b }) =>
    "#" + [r, g, b].map((v) => Math.round(v).toString(16).padStart(2, "0")).join("");
  const bodyBg = parse(getComputedStyle(document.body).backgroundColor);
  const htmlBg = parse(getComputedStyle(document.documentElement).backgroundColor);
  const effectiveBg = (el) => {
    let bg = { r: 255, g: 255, b: 255, a: 1 };
    const chain = [];
    for (let e = el; e; e = e.parentElement) {
      const c = parse(getComputedStyle(e).backgroundColor);
      if (c && c.a > 0) chain.push(c);
    }
    chain.push(bodyBg?.a ? bodyBg : htmlBg?.a ? htmlBg : bg);
    // compose from the back
    let out = chain[chain.length - 1];
    for (let i = chain.length - 2; i >= 0; i--) out = blend(chain[i], out);
    return out;
  };
  const items = [];
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  let n;
  while ((n = walker.nextNode())) {
    const text = n.textContent.replace(/\s+/g, " ").trim();
    if (!text) continue;
    const el = n.parentElement;
    if (!el) continue;
    const cs = getComputedStyle(el);
    if (cs.visibility === "hidden" || cs.display === "none" || parseFloat(cs.opacity) === 0) continue;
    const range = document.createRange();
    range.selectNodeContents(n);
    const r = range.getBoundingClientRect();
    if (r.width === 0 || r.height === 0) continue;
    if (r.bottom < 0 || r.right < 0 || r.top > innerHeight || r.left > innerWidth) continue;
    const fg0 = parse(cs.color);
    if (!fg0) continue;
    const bg = effectiveBg(el);
    const fg = fg0.a < 1 ? blend(fg0, bg) : fg0;
    const size = parseFloat(cs.fontSize);
    const weight = parseInt(cs.fontWeight, 10);
    const large = size >= 24 || (size >= 18.66 && weight >= 700);
    const cr = ratio(fg, bg);
    items.push({
      text: text.slice(0, 40),
      size,
      weight,
      fg: hex(fg),
      bg: hex(bg),
      ratio: Math.round(cr * 100) / 100,
      large,
      pass: cr >= (large ? 3 : 4.5),
      tag: el.tagName.toLowerCase(),
      cls: (el.className && el.className.baseVal === undefined ? el.className : "").toString().slice(0, 60),
      transform: cs.textTransform,
      family: cs.fontFamily.split(",")[0],
    });
  }
  const sizes = {};
  const weights = {};
  const fgs = {};
  const bgs = {};
  for (const i of items) {
    sizes[i.size] = (sizes[i.size] || 0) + 1;
    weights[i.weight] = (weights[i.weight] || 0) + 1;
    fgs[i.fg] = (fgs[i.fg] || 0) + 1;
    bgs[i.bg] = (bgs[i.bg] || 0) + 1;
  }
  const failures = items.filter((i) => !i.pass);
  const uniqFail = {};
  for (const f of failures) {
    const k = `${f.fg} on ${f.bg}`;
    if (!uniqFail[k]) uniqFail[k] = { ...f, count: 0, samples: [] };
    uniqFail[k].count++;
    if (uniqFail[k].samples.length < 3) uniqFail[k].samples.push(f.text);
  }
  const svgs = document.querySelectorAll("svg").length;
  const buttons = document.querySelectorAll("button").length;
  const chips = document.querySelectorAll(".MuiChip-root").length;
  const sidebar = document.querySelector("nav, aside, .sidebar, [class*=sidebar]");
  const sb = sidebar ? sidebar.getBoundingClientRect() : null;
  const lowercase = items.filter((i) => i.transform === "lowercase").length;
  return {
    textNodes: items.length,
    sizes,
    weights,
    fgColours: Object.keys(fgs).length,
    bgColours: Object.keys(bgs).length,
    topFg: Object.entries(fgs).sort((a, b) => b[1] - a[1]).slice(0, 8),
    lowercaseTransformed: lowercase,
    svgs,
    buttons,
    chips,
    sidebar: sb && { x: sb.x, w: sb.width, h: sb.height },
    failures: Object.values(uniqFail).sort((a, b) => b.count - a.count),
    smallest: items.filter((i) => i.size < 12).length,
  };
};

