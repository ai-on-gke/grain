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

import { fillScalar } from "./grain-mark.js";

// The item glyphs: one Chladni figure per kind of thing grain keeps a
// list of. The nav rail's list entries -- Repos, Schedules, Templates,
// Suites -- carried an invisible spacer dot each (`dot dot-all`), so the
// only thing telling them apart was their label. The destinations have
// figures of their own below (DESTINATION_GLYPHS), cut the same way and
// by the same rules, but they are a second set rather than more items: a
// destination is not a kind of thing grain keeps a list of.
//
// The set grows with the app, and grain/task-372 is the round that
// caught it up: a task, a branch and the inbox were all things this UI
// names on page after page with nothing but a word, while the kinds
// beside them carried a figure. Tasks and branches are kinds -- grain
// lists both -- so they are items; the inbox is one pane, so it is a
// destination.
//
// They are cut from the same plate as the brand mark (docs/brand.md):
// the filled regions of a square-plate eigenmode, inside the same
// circular clip. Every figure here is a **mode the mark's own cycle does
// not use**, so a nav icon is never the figure the sidebar mark is
// currently showing -- grain-mark.js's GLYPHS are 1·3 (+), 1·2 (+),
// 2·3 (−) and 2·3 (+), and item-glyphs.test.js holds all twelve off
// them, and off each other.
//
// "Not used" counts n·m and m·n as the same mode at s = +1, where the
// field is symmetric in n and m: 4·2 (+) *is* the repos ring, at
// whatever zoom. At s = −1 the swap negates the field instead, which
// fills the other half of the plate and draws a different figure -- that
// is what CI's 3·2 (−) does against the static mark's 2·3 (−) rosette,
// and why a transposed mode is not the near-duplicate it sounds like.
//
// Zoom is the only other dial, and the pack's own reason for it stands:
// mode numbers are resonances, so a figure is framed by scaling the
// field (grain-mark.js's `glyphZoom` does the same for two of the four
// mark glyphs) and never by interpolating n or m. Each zoom below is
// the framing where the figure reads as one shape at 16px rather than
// as texture.
//
//   repos      2·4 (+) at 2.2   a closed ring. A repo is the boundary
//                               everything else here sits inside: a
//                               task, a schedule and a suite all name
//                               one, and nothing names two. It is the
//                               only figure in the set that closes a
//                               single loop around its own middle and
//                               is nothing else: the task's mass below
//                               encloses a hole too, but as an opening
//                               cut in a solid rather than as a ring.
//   schedules  1·4 (−) at 1.7   two heavy posts either side of an open
//                               middle, a wedge narrowing into it from
//                               above and below: the recurring cadence of
//                               timed dispatches.
//   templates  4·5 (+) at 1.2   an even lattice: one cell stamped
//                               across the whole frame. A template is
//                               exactly that -- one task definition,
//                               used again and again.
//   suites     3·6 (−) at 1.4   six lobes arranged in clustered pairs:
//                               multiple composite task runs grouped
//                               into a single test suite.
//   processes  3·6 (+) at 1.2   a loop through three nodes: the
//                               Processes page, which lists schedules,
//                               templates, suites and triggers together
//                               (design/design/settings-panel.design.md §5.7) --
//                               standing work that comes round again,
//                               which the schedules' two posts, read as
//                               "time", would not say of a template.
//   ci         3·2 (−) at 1.4   winged gate arms closing on a channel
//                               between posts: the gate a commit goes
//                               through, and what a CI run is -- top
//                               and bottom posts framing the pipeline
//                               channel while two caliper arms converge
//                               where the verdict meets (docs/brand/
//                               ci-candidates.svg).
//   tasks      3·4 (−) at 1.6   four heavy lobes meeting at one centre:
//                               several components converged into a
//                               single cohesive unit of work.
//   branches   3·5 (+) at 2.4   a stem that opens into two down the
//                               middle and closes again above and
//                               below: one line of work leaving another
//                               and coming back to it, which is the
//                               whole life of a branch grain cuts. The
//                               split is the figure, so it is framed
//                               where the two halves are as wide as the
//                               stem that carries them.
//   boards     3·3 (+) at 1.0   columns and blocks: the arrangement a
//                               saved view gives the tasks inside it.
//                               It appears on the Boards page heading,
//                               where it names the kind, but not on each
//                               saved board in the rail, where repeating
//                               it would say "tasks" all the way down.
//                               Its straight walls are an intentional
//                               exception to the family resemblance
//                               below: literal recognition wins here,
//                               while placement keeps it out of the
//                               destination-only part of the rail.
//   user       2·5 (+) at 2.0   head and shoulders: a rounded head resting
//                               above an arched shoulder bust, the classic
//                               avatar silhouette mirrored across the plate.
//
// The figures are traced to paths by `npm run brand:assets`, into
// item-glyph-paths.js beside this file. This module is the design; that
// one is what it draws.
export const ITEM_GLYPHS = {
  tasks: { glyph: [3, 4, -1], zoom: 1.6, figure: "four lobes on one centre" },
  repos: { glyph: [2, 4, 1], zoom: 2.2, figure: "ring" },
  branches: { glyph: [3, 5, 1], zoom: 2.4, figure: "a stem split in two" },
  boards: { glyph: [3, 3, 1], zoom: 1.0, figure: "columns and blocks" },
  schedules: { glyph: [1, 4, -1], zoom: 1.7, figure: "two posts, open middle" },
  templates: { glyph: [4, 5, 1], zoom: 1.2, figure: "lattice" },
  suites: {
    glyph: [3, 6, -1],
    zoom: 1.4,
    figure: "six lobes in clustered pairs",
  },
  processes: {
    glyph: [3, 6, 1],
    zoom: 1.2,
    figure: "a loop through three nodes",
  },
  ci: {
    glyph: [3, 2, -1],
    zoom: 1.4,
    figure: "winged gate arms closing on a channel",
  },
  sandboxes: {
    glyph: [4, 1, -1],
    zoom: 1.7,
    figure: "nested chambers",
  },
  sessions: {
    glyph: [5, 1, -1],
    zoom: 1.5,
    figure: "dialogue bars across a channel",
  },
  user: {
    glyph: [2, 5, 1],
    zoom: 2.0,
    figure: "head and shoulders",
  },
};

// The destination glyphs: the panes that are one place rather than a
// list -- the rail's three footer entries, Settings, System and Metrics,
// the Inbox at the top of it, and the proxy cache in among the lists
// (grain/task-321: a deployment has exactly one of those, so it is a
// place and not a kind). They are not kinds of thing grain
// keeps a list of, so they are not item glyphs; the three at the foot
// were also the only entries in the rail that carried no marker at all,
// which left their labels starting a figure's width to the left of every
// entry above them (Sidebar.jsx).
//
// Same plate, same rules: a mode the mark's cycle does not use, a mode
// no item glyph uses, and a framing where the figure reads as one shape
// at 16px. What sets these apart as a family is what they are made of:
// the item figures are the plate's curved and pointed regions -- a
// diamond ring, a bowtie, a diagonal lattice, four lobes, a split stem,
// a filled mass -- and these are its straight-walled ones, upright boxes
// and posts. A reader who never learns which figure is which still sees
// that a destination is a different kind of entry.
//
//   settings   2·6 (+) at 1.6   a toothed gear with a hollow central
//                               hub. The settings are the machinery
//                               and enclosure everything else in this
//                               deployment runs inside, and the gear
//                               serves as the icon wherever settings or
//                               configuration are represented.
//   system     1·5 (+) at 1.4   a case with a core inside it: two walls,
//                               two caps and one solid block in the
//                               middle. The System pane is the machine
//                               this deployment runs on -- the host,
//                               and grain running inside it -- so it is
//                               the one figure with an outer wall and a
//                               centre of its own.
//   metrics    1·5 (−) at 1.5   tiered gauge between caps: two solid caps
//                               bounding horizontal measurement tiers,
//                               capturing throughput, latency, and
//                               execution statistics.
//   cache      1·6 (+) at 1.4   a crate: a wall all the way round a
//                               stack of layers. The proxy cache is
//                               the one pane about things this
//                               deployment is *keeping* -- packages and
//                               image layers a sandbox downloaded once
//                               so the next sandbox does not -- and a
//                               crate of stacked goods is what that is.
//                               It is the closed outline with something
//                               inside it, which is what holds it apart
//                               from the other three: the inbox tray's
//                               middle is empty because a tray is what
//                               fills up, and this one is full because
//                               a cache is what has already filled.
//   inbox      2·2 (+) at 2.4   a tray: heavy straight walls all the way
//                               round a middle that is left open. The
//                               inbox is the one pane that is about the
//                               reader rather than about the work, and a
//                               tray is what fills up while nobody is
//                               looking. It is the third closed outline
//                               in the two sets, so it is framed to be
//                               read against the other two rather than
//                               with them: the repo ring is a thin
//                               diamond, the settings frame a thin
//                               square drawn inside the circle, and this
//                               one is a wall thick enough to be the
//                               figure with the hole as its middle.
//
//   director   3·4 (+) at 1.4   portico and podium: the standing director
//                               session at the top of the rail.
//
//   memory     4·6 (+) at 1.5   ruled pages: the memory agent's notes
//                               for the selected team (MemoryPage.jsx,
//                               grain/task-3100). It is one place per
//                               team, what grain has written down rather
//                               than a list of things it keeps.
//   alerts     2·5 (−) at 1.4   bell dome over a flared lip and
//                               clapper: a rounded dome curving out to
//                               a brim with a tongue suspended in its
//                               mouth, mirrored across the plate (the
//                               minus partner of the user figure's own
//                               2·5 resonance; AlertsButton.jsx,
//                               docs/brand/alerts-candidates.svg).
//
// Saved board entries carry the boards figure [3, 3, 1] ("columns and blocks").
// Director and interactive console sessions carry their own dedicated figures.
export const DESTINATION_GLYPHS = {
  inbox: { glyph: [2, 2, 1], zoom: 2.4, figure: "tray" },
  director: { glyph: [3, 4, 1], zoom: 1.4, figure: "portico and podium" },
  cache: { glyph: [1, 6, 1], zoom: 1.4, figure: "crate of stacked layers" },
  alerts: {
    glyph: [2, 5, -1],
    zoom: 1.4,
    figure: "bell dome over a flared lip and clapper",
  },
  settings: { glyph: [2, 6, 1], zoom: 1.6, figure: "gear" },
  admin: { glyph: [1, 5, 1], zoom: 1.4, figure: "case around a core" },
  memory: { glyph: [4, 6, 1], zoom: 1.5, figure: "ruled pages" },
  metrics: {
    glyph: [1, 5, -1],
    zoom: 1.5,
    figure: "tiered gauge between caps",
  },
};

/** Every figure this module defines, by the name it is drawn under. */
export const GLYPH_SPECS = { ...ITEM_GLYPHS, ...DESTINATION_GLYPHS };

/**
 * The kinds of list that have a glyph, in the order the app reaches
 * them: the rail's own order, with branches beside the repos they hang
 * off -- a branch is listed on a repo's page rather than in the rail.
 */
export const ITEM_KINDS = Object.keys(ITEM_GLYPHS);

/** The destinations that have a glyph, in nav order. */
export const DESTINATION_KINDS = Object.keys(DESTINATION_GLYPHS);

/** Everything with a figure: the kinds and the destinations. */
export const GLYPH_KINDS = Object.keys(GLYPH_SPECS);

/**
 * The scalar an item glyph fills the positive region of, in pixels of a
 * W-sized box: the mode's own filled region (grain-mark.js's
 * `fillScalar`, so the fill rule is the mark's and not a second
 * definition of it) intersected with the frame.
 *
 * The circle is met as a scalar rather than as a clip, so a traced path
 * closes along the frame itself and every glyph is one self-contained
 * `<path>` -- no clipPath, and so no document-unique id, in an icon the
 * page renders eight or ten of.
 */
export function itemFillAt({ glyph, zoom }, W, x, y) {
  const c = W / 2;
  const R = W * 0.44;
  const [n, m, sg] = glyph;
  const Rf = R * zoom;
  // Both terms are positive inside their own region and cross zero at
  // its edge, so the smaller of the two is positive exactly on the
  // intersection -- and near the frame it is the frame's own distance,
  // which is what keeps the traced arc on the circle rather than a grid
  // cell away from it.
  const frame = (R - Math.hypot(x - c, y - c)) / R;
  return Math.min(fillScalar(n, m, sg, (x - c) / Rf, (y - c) / Rf), frame);
}
