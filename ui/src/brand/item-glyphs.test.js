import { describe, expect, it } from "vitest";

import { GLYPHS } from "./grain-mark.js";
import {
  DESTINATION_KINDS,
  GLYPH_KINDS,
  GLYPH_SPECS,
  ITEM_GLYPHS,
  ITEM_KINDS,
  itemFillAt,
} from "./item-glyphs.js";
import { ITEM_GLYPH_BOX, ITEM_GLYPH_PATHS } from "./item-glyph-paths.js";
import {
  ALERTS_CANDIDATES,
  CI_CANDIDATES,
} from "../../scripts/export-glyph-candidates.mjs";

// The nav rail's figures are the same kind of thing as the mark --
// filled regions of a square-plate eigenmode -- so they can be pinned
// down the same way, without a canvas or a browser: what each icon *is*,
// and that the committed path still draws it. See docs/brand.md.

const W = ITEM_GLYPH_BOX;

/** Loops of a committed glyph, parsed out of its one path. */
function loopsOf(kind) {
  return ITEM_GLYPH_PATHS[kind]
    .split("M ")
    .slice(1)
    .map((loop) =>
      loop
        .replace(/\s*Z\s*$/, "")
        .split(" L ")
        .map((p) => p.trim().split(/\s+/).map(Number)),
    );
}

/** Even-odd crossing count, the fill rule the glyphs are drawn with. */
function insideLoops(loops, x, y) {
  let inside = false;
  for (const loop of loops) {
    for (let i = 0, j = loop.length - 1; i < loop.length; j = i++) {
      const [xi, yi] = loop[i];
      const [xj, yj] = loop[j];
      if (yi > y !== yj > y && x < ((xj - xi) * (y - yi)) / (yj - yi) + xi)
        inside = !inside;
    }
  }
  return inside;
}

/** The glyph as a boolean mask of the authoring box, off the field. */
function fieldMask(kind, res) {
  const spec = GLYPH_SPECS[kind];
  const step = W / res;
  const mask = [];
  for (let j = 0; j < res; j++) {
    const row = [];
    for (let i = 0; i < res; i++)
      row.push(itemFillAt(spec, W, (i + 0.5) * step, (j + 0.5) * step) > 0);
    mask.push(row);
  }
  return mask;
}

describe("the item glyphs", () => {
  it("gives every kind of list a figure of its own", () => {
    // The nav rail's list entries, and the kinds the app has. CI joined
    // them when the runs got a section of their own in the rail rather
    // than a tab inside System (grain/task-354); tasks and branches in
    // grain/task-372, which is where the set caught up with the two
    // kinds this UI names most often and had never drawn.
    //
    // Branches sit beside repos rather than at the end because that is
    // where the app reaches them -- a branch is listed on a repo's own
    // page, not in the rail.
    expect(ITEM_KINDS).toEqual([
      "tasks",
      "repos",
      "branches",
      "boards",
      "schedules",
      "templates",
      "suites",
      "processes",
      "ci",
      "sandboxes",
      "sessions",
      "user",
    ]);
    for (const kind of ITEM_KINDS) {
      expect(ITEM_GLYPH_PATHS[kind]).toMatch(/^M /);
      expect(ITEM_GLYPHS[kind].figure).toBeTruthy();
    }
  });

  it("gives the rail's destinations one each as well", () => {
    // grain/task-58: Settings, System and Metrics are not kinds of thing
    // grain keeps a list of, which is why they are a second set rather
    // than more items -- but they were the only entries in the rail
    // carrying no marker at all, and no icon column either, so their
    // labels started a figure's width to the left of every entry above
    // them. The Inbox is a fourth of them (grain/task-372): one pane,
    // asked for by name from the top of the rail, rather than a list of
    // a kind of thing. Individual saved boards deliberately remain on
    // the invisible `dot dot-all`: their item glyph names the kind once
    // on the Boards page rather than repeating beside every task view.
    // The proxy cache is a fifth (grain/task-321), and the second one
    // that is not in the footer: a deployment runs one cache, so the
    // rail's entry for it is a place rather than a list of a kind of
    // thing, even though it sits among the entries that are. Memory is
    // a sixth on the same terms (grain/task-3100): one page per team.
    // Alerts is the top bar's standing-alerts panel beside System and
    // Settings (AlertsButton.jsx): one panel for the deployment rather
    // than a list of items.
    expect(DESTINATION_KINDS).toEqual([
      "inbox",
      "director",
      "cache",
      "alerts",
      "settings",
      "admin",
      "memory",
      "metrics",
    ]);
    for (const kind of DESTINATION_KINDS) {
      expect(ITEM_GLYPH_PATHS[kind]).toMatch(/^M /);
      expect(GLYPH_SPECS[kind].figure).toBeTruthy();
    }
  });

  it("takes no mode the mark's own cycle is already using", () => {
    // A nav icon should never be the figure the sidebar mark happens to
    // be showing at that moment -- the mark says whether agents are
    // working, and these say what a row is. Sign counts: 1·2 (−) is the
    // hourglass and 1·2 (+) is the mark's diamond, which are different
    // figures of the same resonance.
    const spinner = new Set(GLYPHS.map(String));
    for (const kind of GLYPH_KINDS)
      expect(spinner.has(String(GLYPH_SPECS[kind].glyph))).toBe(false);
  });

  it("counts n·m and m·n as one mode where the plate does", () => {
    // At s = +1 the field is symmetric in n and m, so 4·2 (+) *is* the
    // repos ring at whatever zoom -- a transposed mode number there
    // would be a second name for a figure already in the set, and the
    // rule above would not catch it because [4,2,1] is not [2,4,1].
    // At s = −1 the swap negates the field instead, which fills the
    // plate's other half and draws a different figure: that is what
    // CI's 3·2 (−) does against the static mark's 2·3 (−) rosette, so
    // this deliberately holds only for the symmetric sign.
    const seen = new Set();
    for (const glyph of [
      ...GLYPHS,
      ...GLYPH_KINDS.map((kind) => GLYPH_SPECS[kind].glyph),
    ]) {
      const [n, m, sg] = glyph;
      if (sg < 0) continue;
      const key = String([Math.min(n, m), Math.max(n, m)]);
      expect({ mode: `${n}·${m} (+)`, taken: seen.has(key) }).toEqual({
        mode: `${n}·${m} (+)`,
        taken: false,
      });
      seen.add(key);
    }
  });

  it("is a distinct integer resonance and framing for each", () => {
    const seen = new Set();
    for (const kind of GLYPH_KINDS) {
      const { glyph, zoom } = GLYPH_SPECS[kind];
      const [n, m, sg] = glyph;
      // Only integer mode numbers are real resonances; a fractional one
      // piles spurious features into the field (docs/brand.md).
      expect(Number.isInteger(n) && Number.isInteger(m)).toBe(true);
      expect(Math.abs(sg)).toBe(1);
      expect(zoom).toBeGreaterThan(0);
      seen.add(`${glyph}@${zoom}`);
    }
    expect(seen.size).toBe(GLYPH_KINDS.length);
  });

  it("draws pictures a reader can tell apart", () => {
    // Distinct modes are not the point -- distinct *figures* are, and at
    // this size two framings of two different resonances can still land
    // on much the same silhouette. The closest pair is the settings
    // frame against the repo ring at 27% of the frame -- the set's
    // closed outlines, one an upright square drawn inside the frame and
    // the other a diamond filling it -- with the split stem against that
    // same ring at 28% and the hourglass against the four lobes at 29%.
    // So a fifth is a floor with room under it, but well above the
    // couple of percent two near-identical framings would leave, which
    // is the failure worth catching.
    //
    // Weight is the other half of telling two figures apart and is not
    // measured here: the task's mass fills 70% of the frame and the
    // inbox tray 50%, so a third of the frame between them (33%)
    // understates how differently a solid and an outline read at 16px.
    //
    // At the other end is Sandboxes against Schedules, which are the
    // same resonance with n and m swapped (4·1 (−) and 1·4 (−)): at
    // this framing one is very nearly the other's inverse (see
    // item-glyphs.js), which is why a transposed mode is not the
    // near-duplicate it sounds like.
    const res = 48;
    const masks = Object.fromEntries(
      GLYPH_KINDS.map((kind) => [kind, fieldMask(kind, res)]),
    );
    for (const a of GLYPH_KINDS) {
      for (const b of GLYPH_KINDS) {
        if (a >= b) continue;
        let differing = 0;
        let inFrame = 0;
        for (let j = 0; j < res; j++) {
          for (let i = 0; i < res; i++) {
            const dx = (i + 0.5) / res - 0.5;
            const dy = (j + 0.5) / res - 0.5;
            if (Math.hypot(dx, dy) > 0.44) continue;
            inFrame++;
            if (masks[a][j][i] !== masks[b][j][i]) differing++;
          }
        }
        expect({
          pair: `${a}/${b}`,
          differing: differing / inFrame > 0.2,
        }).toEqual({
          pair: `${a}/${b}`,
          differing: true,
        });
      }
    }
  });

  it("commits, for each kind, the path its field draws", () => {
    // item-glyph-paths.js is generated (npm run brand:assets), so the
    // failure this exists to catch is the same one grain-mark.test.js
    // catches for the still: a figure changed in item-glyphs.js without
    // the export being re-run, which would leave the nav showing the
    // previous icons.
    //
    // The path is traced from the field, so away from the boundary the
    // two have to agree exactly; the only pixels where they can differ
    // are the ones the trace's straight segments cut across, and those
    // are exactly the pixels with a sign change in reach.
    for (const kind of GLYPH_KINDS) {
      const spec = GLYPH_SPECS[kind];
      const loops = loopsOf(kind);
      const field = (x, y) => itemFillAt(spec, W, x, y) > 0;
      let tested = 0;
      const disagreed = [];
      for (let y = 0; y < W; y++) {
        for (let x = 0; x < W; x++) {
          const px = x + 0.5;
          const py = y + 0.5;
          const filled = field(px, py);
          const onEdge = [
            [0.75, 0],
            [-0.75, 0],
            [0, 0.75],
            [0, -0.75],
          ].some(([dx, dy]) => field(px + dx, py + dy) !== filled);
          if (onEdge) continue;
          tested++;
          if (filled !== insideLoops(loops, px, py)) disagreed.push([px, py]);
        }
      }
      expect({ kind, tested: tested > 7000 }).toEqual({ kind, tested: true });
      expect({ kind, disagreed }).toEqual({ kind, disagreed: [] });
    }
  });

  it("keeps every candidate on the alerts and CI contact sheets legal and swap-ready", () => {
    expect(ALERTS_CANDIDATES).toHaveLength(8);
    expect(CI_CANDIDATES).toHaveLength(8);
    expect(ALERTS_CANDIDATES[0]).toMatchObject({
      glyph: GLYPH_SPECS.alerts.glyph,
      zoom: GLYPH_SPECS.alerts.zoom,
      figure: GLYPH_SPECS.alerts.figure,
      chosen: true,
    });
    expect(CI_CANDIDATES[0]).toMatchObject({
      glyph: GLYPH_SPECS.ci.glyph,
      zoom: GLYPH_SPECS.ci.zoom,
      figure: GLYPH_SPECS.ci.figure,
      chosen: true,
    });

    const modeKey = ([n, m, sg]) =>
      `${sg === 1 ? [Math.min(n, m), Math.max(n, m)] : [n, m]}:${sg}`;
    const reserved = new Set(GLYPHS.map(modeKey));
    for (const kind of GLYPH_KINDS) {
      if (kind === "alerts" || kind === "ci") continue;
      reserved.add(modeKey(GLYPH_SPECS[kind].glyph));
    }

    const candidateModes = new Set();
    for (const c of [...ALERTS_CANDIDATES, ...CI_CANDIDATES]) {
      const [n, m, sg] = c.glyph;
      expect(Number.isInteger(n) && Number.isInteger(m)).toBe(true);
      expect(Math.abs(sg)).toBe(1);
      expect(c.zoom).toBeGreaterThan(0);
      const key = modeKey(c.glyph);
      expect(reserved.has(key)).toBe(false);
      expect(candidateModes.has(key)).toBe(false);
      candidateModes.add(key);
    }
  });
});
