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
  type NodeCategory,
  type NodeGlyphKind,
  type Workstream,
  type WorkstreamNode,
  compareTaskIds,
} from "./workstream-model";

export type LayoutMode = "split" | "list" | "graph";

export const GRAPH_NODE_W = 200;
export const GRAPH_NODE_H = 44;
export const GRAPH_DX = 236;
export const GRAPH_DY = 56;
export const GRAPH_PAD_X = 24;
export const GRAPH_PAD_Y = 20;

export const PACK_LEAF_THRESHOLD = 8;
export const PACK_DONE_THRESHOLD = 2;
export const MAX_VISIBLE_CHILDREN = 40;

export interface TreeRow {
  id: string;
  node: WorkstreamNode;
  depth: number;
  parentId: string | null;
  childIds: string[];
  hasChildren: boolean;
  guideLevels: number[];
}

export interface PositionedGraphNode {
  id: string;
  parentId: string | null;
  depth: number;
  x: number;
  y: number;
  w: number;
  h: number;
  title: string;
  category: NodeCategory;
  glyph: NodeGlyphKind;
  isRoot: boolean;
  isPack: boolean;
  packedCount?: number;
  packedIds?: string[];
  node: WorkstreamNode | null;
}

export interface GraphEdge {
  fromId: string;
  toId: string;
  path: string;
  onAncestorPath: boolean;
}

export interface DepArc {
  fromId: string;
  toId: string;
  path: string;
  hot: boolean;
}

export interface PortalPin {
  id: string;
  targetTaskId: string;
  targetTitle: string;
  targetStateText: string;
  streamKey: string | null;
  streamTitle: string | null;
  toNodeId: string;
  x: number;
  y: number;
  w: number;
}

export interface TreeLayout {
  nodes: PositionedGraphNode[];
  byId: ReadonlyMap<string, PositionedGraphNode>;
  edges: GraphEdge[];
  depArcs: DepArc[];
  portals: PortalPin[];
  ancestorIds: ReadonlySet<string>;
  flatOrder: string[];
  width: number;
  height: number;
  hiddenDoneCount: number;
}

export interface LayoutOptions {
  hideDone?: boolean;
  expandAll?: boolean;
  allDeps?: boolean;
  selectedId?: string | null;
  allTasksById?: ReadonlyMap<string, WorkstreamNode>;
  taskToStream?: ReadonlyMap<string, Workstream>;
}

export interface ThumbNode {
  x: number;
  y: number;
  w: number;
  h: number;
  category: NodeCategory;
  isRoot: boolean;
}

export interface ThumbEdge {
  path: string;
}

export interface ThumbLayout {
  nodes: ThumbNode[];
  edges: ThumbEdge[];
  overflowCount: number;
  width: number;
  height: number;
}

export interface RelRef {
  taskId: string;
  title: string;
  stateText: string;
  category: NodeCategory;
  glyph: NodeGlyphKind;
  streamKey: string | null;
  streamTitle: string | null;
  crossStream: boolean;
}

export interface NodeRelationships {
  filedBy: RelRef | null;
  waitsOn: RelRef[];
  waitedOnBy: RelRef[];
  nextUp: RelRef[];
  blockingChain: RelRef[];
}

function buildRawChildrenMap(
  stream: Workstream,
): Map<string, WorkstreamNode[]> {
  const children = new Map<string, WorkstreamNode[]>();
  for (const n of stream.nodes) children.set(n.id, []);
  for (const n of stream.nodes) {
    if (!n.parentId || n.id === stream.root.id) continue;
    const arr = children.get(n.parentId);
    if (arr) arr.push(n);
    else children.get(stream.root.id)?.push(n);
  }
  return children;
}

function computeAncestorSet(
  stream: Workstream,
  selectedId: string | null | undefined,
): Set<string> {
  const out = new Set<string>();
  if (!selectedId || !stream.byId.has(selectedId)) {
    out.add(stream.root.id);
    return out;
  }
  let cur: string | null = selectedId;
  let guard = 0;
  while (cur && guard++ < 64) {
    out.add(cur);
    const n = stream.byId.get(cur);
    if (!n) break;
    cur = n.parentId;
  }
  return out;
}

function hasUndoneInSubtree(
  nodeId: string,
  children: ReadonlyMap<string, WorkstreamNode[]>,
  byId: ReadonlyMap<string, WorkstreamNode>,
  selectedId: string | null | undefined,
  memo: Map<string, boolean>,
): boolean {
  const cached = memo.get(nodeId);
  if (cached !== undefined) return cached;
  const n = byId.get(nodeId);
  if (!n) return false;
  if (nodeId === selectedId || n.category !== "done") {
    memo.set(nodeId, true);
    return true;
  }
  const kids = children.get(nodeId) ?? [];
  for (const k of kids) {
    if (hasUndoneInSubtree(k.id, children, byId, selectedId, memo)) {
      memo.set(nodeId, true);
      return true;
    }
  }
  memo.set(nodeId, false);
  return false;
}

function nodePriorityWeight(
  n: WorkstreamNode,
  selectedId?: string | null,
): number {
  if (n.id === selectedId) return 0;
  if (n.category === "person") return 1;
  if (n.category === "run") return 2;
  if (n.category === "failed") return 3;
  if (n.category === "blocked") return 4;
  if (n.category === "queued") return 5;
  return 6;
}

// buildTreeRows returns visible rows in depth-first pre-order for the
// APG tree pane (`role="tree"`), keeping done ancestors of open subtrees so
// the hierarchy never disconnects.
export function buildTreeRows(
  stream: Workstream,
  opts: {
    hideDone?: boolean;
    autoFoldDone?: boolean;
    collapsedIds?: ReadonlySet<string>;
    selectedId?: string | null;
  } = {},
): { rows: TreeRow[]; hiddenDoneCount: number; foldedDoneCount: number } {
  const rawChildren = buildRawChildrenMap(stream);
  const keepMemo = new Map<string, boolean>();
  const hiddenIds = new Set<string>();
  const autoFoldIds = new Set<string>();

  if (opts.hideDone || opts.autoFoldDone) {
    for (const n of stream.nodes) {
      if (n.id === stream.root.id) continue;
      if (
        !hasUndoneInSubtree(
          n.id,
          rawChildren,
          stream.byId,
          opts.selectedId,
          keepMemo,
        )
      ) {
        if (opts.hideDone) {
          hiddenIds.add(n.id);
        } else if (
          opts.autoFoldDone &&
          (rawChildren.get(n.id)?.length ?? 0) === 0
        ) {
          autoFoldIds.add(n.id);
        }
      }
    }
  }

  const rows: TreeRow[] = [];
  const collapsed = opts.collapsedIds ?? new Set<string>();

  function walk(
    node: WorkstreamNode,
    depth: number,
    guideLevels: number[],
    isLast: boolean,
  ): void {
    const visibleKids = (rawChildren.get(node.id) ?? []).filter(
      (k) => !hiddenIds.has(k.id) && !autoFoldIds.has(k.id),
    );
    rows.push({
      id: node.id,
      node,
      depth,
      parentId: node.parentId,
      childIds: visibleKids.map((k) => k.id),
      hasChildren: visibleKids.length > 0,
      guideLevels,
    });
    if (collapsed.has(node.id)) return;
    const childGuides =
      depth === 0 || isLast ? guideLevels : [...guideLevels, depth];
    visibleKids.forEach((k, idx) => {
      walk(k, depth + 1, childGuides, idx === visibleKids.length - 1);
    });
  }

  walk(stream.root, 0, [], true);
  return {
    rows,
    hiddenDoneCount: hiddenIds.size,
    foldedDoneCount: autoFoldIds.size,
  };
}

interface InternalTreeItem {
  id: string;
  parentId: string | null;
  title: string;
  category: NodeCategory;
  glyph: NodeGlyphKind;
  isRoot: boolean;
  isPack: boolean;
  packedCount?: number;
  packedIds?: string[];
  node: WorkstreamNode | null;
  children: InternalTreeItem[];
}

export function layoutTree(
  stream: Workstream,
  opts: LayoutOptions = {},
): TreeLayout {
  const rawChildren = buildRawChildrenMap(stream);
  const ancestorIds = computeAncestorSet(stream, opts.selectedId);
  const keepMemo = new Map<string, boolean>();
  const hiddenIds = new Set<string>();

  if (opts.hideDone) {
    for (const n of stream.nodes) {
      if (
        n.id !== stream.root.id &&
        !hasUndoneInSubtree(
          n.id,
          rawChildren,
          stream.byId,
          opts.selectedId,
          keepMemo,
        )
      ) {
        hiddenIds.add(n.id);
      }
    }
  }

  function buildItem(n: WorkstreamNode, depth: number): InternalTreeItem {
    let directKids = (rawChildren.get(n.id) ?? []).filter(
      (k) => !hiddenIds.has(k.id),
    );
    let packItem: InternalTreeItem | null = null;

    if (!opts.expandAll && directKids.length > PACK_LEAF_THRESHOLD) {
      const doneLeaves = directKids.filter(
        (k) =>
          k.id !== opts.selectedId &&
          k.category === "done" &&
          (rawChildren.get(k.id) ?? []).filter((gk) => !hiddenIds.has(gk.id))
            .length === 0,
      );
      if (doneLeaves.length > PACK_DONE_THRESHOLD) {
        const foldedSet = new Set(doneLeaves.map((k) => k.id));
        directKids = directKids.filter((k) => !foldedSet.has(k.id));
        packItem = {
          id: `pack:${n.id}`,
          parentId: n.id,
          title: `${doneLeaves.length} done leaves`,
          category: "done",
          glyph: "done",
          isRoot: false,
          isPack: true,
          packedCount: doneLeaves.length,
          packedIds: doneLeaves.map((k) => k.id),
          node: null,
          children: [],
        };
      }
    }

    let overflowItem: InternalTreeItem | null = null;
    const totalBudget = packItem
      ? MAX_VISIBLE_CHILDREN - 1
      : MAX_VISIBLE_CHILDREN;
    if (!opts.expandAll && directKids.length > totalBudget) {
      const cap = totalBudget - 1;
      const prioritized = [...directKids].sort(
        (a, b) =>
          nodePriorityWeight(a, opts.selectedId) -
            nodePriorityWeight(b, opts.selectedId) ||
          compareTaskIds(a.id, b.id),
      );
      const kept = prioritized.slice(0, cap);
      const keptSet = new Set(kept.map((k) => k.id));
      const dropped = directKids.filter((k) => !keptSet.has(k.id));
      directKids = directKids.filter((k) => keptSet.has(k.id));
      overflowItem = {
        id: `more:${n.id}`,
        parentId: n.id,
        title: `+${dropped.length} more tasks`,
        category: "queued",
        glyph: "queued",
        isRoot: false,
        isPack: true,
        packedCount: dropped.length,
        packedIds: dropped.map((k) => k.id),
        node: null,
        children: [],
      };
    }

    const childItems = directKids.map((k) => buildItem(k, depth + 1));
    if (packItem) childItems.push(packItem);
    if (overflowItem) childItems.push(overflowItem);

    return {
      id: n.id,
      parentId: n.id === stream.root.id ? null : (n.parentId ?? stream.root.id),
      title: n.title,
      category: n.category,
      glyph: n.glyph,
      isRoot: n.id === stream.root.id,
      isPack: false,
      node: n,
      children: childItems,
    };
  }

  const rootItem = buildItem(stream.root, 0);

  // Reingold-Tilford style leaf-row placement: each leaf advances `nextRow`,
  // while internal nodes center across their first and last child's y row.
  let nextRow = 0;
  let maxDepth = 0;
  const rowOf = new Map<string, number>();
  const depthOf = new Map<string, number>();
  const flatOrder: string[] = [];
  const allItems: InternalTreeItem[] = [];

  function assignRows(item: InternalTreeItem, depth: number): number {
    depthOf.set(item.id, depth);
    if (depth > maxDepth) maxDepth = depth;
    allItems.push(item);
    if (!item.isPack) flatOrder.push(item.id);
    if (item.children.length === 0) {
      const r = nextRow++;
      rowOf.set(item.id, r);
      return r;
    }
    const ys = item.children.map((c) => assignRows(c, depth + 1));
    const mid = (ys[0] + ys[ys.length - 1]) / 2;
    rowOf.set(item.id, mid);
    return mid;
  }

  assignRows(rootItem, 0);

  const visibleItemIds = new Set(allItems.map((it) => it.id));
  const packedItemIdOf = new Map<string, string>();
  for (const it of allItems) {
    if (it.packedIds?.length) {
      for (const pid of it.packedIds) packedItemIdOf.set(pid, it.id);
    }
  }
  for (const hid of hiddenIds) {
    let cur = stream.byId.get(hid)?.parentId ?? null;
    while (cur && !visibleItemIds.has(cur)) {
      cur = stream.byId.get(cur)?.parentId ?? null;
    }
    if (cur) packedItemIdOf.set(hid, cur);
  }

  let leftExtra = 0;
  for (const it of allItems) {
    if ((depthOf.get(it.id) ?? 0) !== 0 || !it.node) continue;
    for (const depId of it.node.deps) {
      if (
        !visibleItemIds.has(depId) &&
        !packedItemIdOf.has(depId) &&
        it.node.crossStreamDeps.includes(depId)
      ) {
        const pw = Math.max(68, 28 + depId.length * 7);
        leftExtra = Math.max(leftExtra, pw + 10);
      }
    }
  }

  const posNodes: PositionedGraphNode[] = allItems.map((it) => {
    const depth = depthOf.get(it.id) ?? 0;
    const row = rowOf.get(it.id) ?? 0;
    return {
      id: it.id,
      parentId: it.parentId,
      depth,
      x: GRAPH_PAD_X + leftExtra + depth * GRAPH_DX,
      y: GRAPH_PAD_Y + row * GRAPH_DY,
      w: GRAPH_NODE_W,
      h: GRAPH_NODE_H,
      title: it.title,
      category: it.category,
      glyph: it.glyph,
      isRoot: it.isRoot,
      isPack: it.isPack,
      packedCount: it.packedCount,
      packedIds: it.packedIds,
      node: it.node,
    };
  });

  const byId = new Map<string, PositionedGraphNode>();
  for (const pn of posNodes) byId.set(pn.id, pn);

  const hiddenParentOf = new Map<string, PositionedGraphNode>();
  for (const [hid, targetItemId] of packedItemIdOf.entries()) {
    const pn = byId.get(targetItemId);
    if (pn) hiddenParentOf.set(hid, pn);
  }

  const edges: GraphEdge[] = [];
  for (const pn of posNodes) {
    if (!pn.parentId) continue;
    const p = byId.get(pn.parentId);
    if (!p) continue;
    const x1 = p.x + p.w;
    const y1 = p.y + p.h / 2;
    const x2 = pn.x;
    const y2 = pn.y + pn.h / 2;
    const mx = (x1 + x2) / 2;
    edges.push({
      fromId: p.id,
      toId: pn.id,
      path: `M${x1},${y1} C${mx},${y1} ${mx},${y2} ${x2},${y2}`,
      onAncestorPath: ancestorIds.has(p.id) && ancestorIds.has(pn.id),
    });
  }

  // Build in-stream dependsOn arcs + cross-stream portal pins.
  const depArcs: DepArc[] = [];
  const depArcKeys = new Set<string>();
  const portals: PortalPin[] = [];
  const seenPortalIds = new Set<string>();
  const portalCountForNode = new Map<string, number>();
  let maxPortalBottom = 0;

  for (const pn of posNodes) {
    if (!pn.node) continue;
    for (const depId of pn.node.deps) {
      const from = byId.get(depId) ?? hiddenParentOf.get(depId);
      if (from && from.id !== pn.id) {
        const arcKey = `${from.id}->${pn.id}`;
        if (depArcKeys.has(arcKey)) continue;
        const hot =
          pn.id === opts.selectedId ||
          from.id === opts.selectedId ||
          depId === opts.selectedId;
        const sameDepthClose =
          from.parentId === pn.parentId &&
          Math.abs(from.y - pn.y) <= GRAPH_DY * 1.1;
        if (!opts.allDeps && !hot && (sameDepthClose || depArcs.length >= 12)) {
          continue;
        }
        depArcKeys.add(arcKey);
        const x1 = from.x + from.w / 2;
        const y1 = from.y + from.h;
        const x2 = pn.x + pn.w / 2;
        const y2 = pn.y;
        const dy = Math.max(24, Math.abs(y2 - y1) * 0.45);
        const path =
          from.x + from.w <= pn.x
            ? `M${from.x + from.w},${from.y + from.h * 0.72} C${(from.x + from.w + pn.x) / 2},${from.y + from.h * 0.72} ${(from.x + from.w + pn.x) / 2},${pn.y + pn.h * 0.72} ${pn.x},${pn.y + pn.h * 0.72}`
            : `M${x1},${y1} C${x1},${y1 + dy} ${x2},${y2 - dy} ${x2},${y2}`;
        depArcs.push({ fromId: from.id, toId: pn.id, path, hot });
      } else if (pn.node.crossStreamDeps.includes(depId)) {
        const key = `${depId}->${pn.id}`;
        if (seenPortalIds.has(key)) continue;
        seenPortalIds.add(key);
        const ext = opts.allTasksById?.get(depId);
        const targetStream = opts.taskToStream?.get(depId) ?? null;
        const idx = portalCountForNode.get(pn.id) ?? 0;
        portalCountForNode.set(pn.id, idx + 1);
        const w = Math.max(68, 28 + depId.length * 7);
        const x = Math.max(4, pn.x - w - 6);
        const y = pn.y + 4 + idx * 21;
        maxPortalBottom = Math.max(maxPortalBottom, y + 20);
        portals.push({
          id: key,
          targetTaskId: depId,
          targetTitle: ext?.title ?? `Task #${depId}`,
          targetStateText: ext?.stateText ?? "External",
          streamKey: targetStream?.key ?? null,
          streamTitle: targetStream?.title ?? null,
          toNodeId: pn.id,
          x,
          y,
          w,
        });
      }
    }
  }

  const width = Math.max(
    480,
    GRAPH_PAD_X * 2 + leftExtra + maxDepth * GRAPH_DX + GRAPH_NODE_W,
  );
  const height = Math.max(
    180,
    GRAPH_PAD_Y * 2 + Math.max(0, nextRow - 1) * GRAPH_DY + GRAPH_NODE_H,
    maxPortalBottom + 12,
  );

  return {
    nodes: posNodes,
    byId,
    edges,
    depArcs,
    portals,
    ancestorIds,
    flatOrder,
    width,
    height,
    hiddenDoneCount: hiddenIds.size,
  };
}

// thumbLayout computes a compact top-down miniature SVG tree for Cards view
// inside a 280x64 coordinate box, capping individual boxes at 24 nodes.
export function thumbLayout(stream: Workstream): ThumbLayout {
  const WIDTH = 280;
  const HEIGHT = 64;
  const MAX_NODES = 24;

  const rawChildren = buildRawChildrenMap(stream);
  const capped = stream.nodes.slice(0, MAX_NODES);
  const allowed = new Set(capped.map((n) => n.id));
  const overflowCount = Math.max(0, stream.nodes.length - capped.length);

  const childrenOf = new Map<string, WorkstreamNode[]>();
  for (const n of capped) {
    childrenOf.set(
      n.id,
      (rawChildren.get(n.id) ?? []).filter((k) => allowed.has(k.id)),
    );
  }

  let nextCol = 0;
  let maxDepth = 0;
  const colOf = new Map<string, number>();
  const depthOf = new Map<string, number>();

  function place(node: WorkstreamNode, depth: number): number {
    depthOf.set(node.id, depth);
    if (depth > maxDepth) maxDepth = depth;
    const kids = childrenOf.get(node.id) ?? [];
    if (kids.length === 0) {
      const c = nextCol++;
      colOf.set(node.id, c);
      return c;
    }
    const cs = kids.map((k) => place(k, depth + 1));
    const mid = (cs[0] + cs[cs.length - 1]) / 2;
    colOf.set(node.id, mid);
    return mid;
  }

  place(stream.root, 0);

  const cols = Math.max(1, nextCol);
  const levels = Math.max(1, maxDepth + 1);
  const padX = 12;
  const padY = 8;
  const nw = Math.min(
    26,
    Math.max(8, Math.floor((WIDTH - padX * 2) / cols) - 4),
  );
  const nh = Math.min(
    10,
    Math.max(5, Math.floor((HEIGHT - padY * 2) / levels) - 4),
  );
  const dx = cols > 1 ? (WIDTH - padX * 2 - nw) / (cols - 1) : 0;
  const dy = levels > 1 ? (HEIGHT - padY * 2 - nh) / (levels - 1) : 0;

  const coordOf = new Map<string, { x: number; y: number }>();
  const nodes: ThumbNode[] = capped.map((n) => {
    const col = colOf.get(n.id) ?? 0;
    const depth = depthOf.get(n.id) ?? 0;
    const x = cols === 1 ? (WIDTH - nw) / 2 : padX + col * dx;
    const y = levels === 1 ? (HEIGHT - nh) / 2 : padY + depth * dy;
    coordOf.set(n.id, { x, y });
    return {
      x,
      y,
      w: nw,
      h: nh,
      category: n.category,
      isRoot: n.id === stream.root.id,
    };
  });

  const edges: ThumbEdge[] = [];
  for (const n of capped) {
    if (!n.parentId || n.id === stream.root.id) continue;
    const p = coordOf.get(n.parentId);
    const c = coordOf.get(n.id);
    if (!p || !c) continue;
    const x1 = p.x + nw / 2;
    const y1 = p.y + nh;
    const x2 = c.x + nw / 2;
    const y2 = c.y;
    const my = (y1 + y2) / 2;
    edges.push({ path: `M${x1},${y1} C${x1},${my} ${x2},${my} ${x2},${y2}` });
  }

  return { nodes, edges, overflowCount, width: WIDTH, height: HEIGHT };
}

function makeRef(
  n: WorkstreamNode,
  curStreamKey: string,
  taskToStream?: ReadonlyMap<string, Workstream>,
): RelRef {
  const s = taskToStream?.get(n.id);
  const cross = Boolean(s && s.key !== curStreamKey);
  return {
    taskId: n.id,
    title: n.title,
    stateText: n.stateText,
    category: n.category,
    glyph: n.glyph,
    streamKey: s?.key ?? null,
    streamTitle: s?.title ?? null,
    crossStream: cross,
  };
}

// blockingChain walks upstream `dependsOn` blockers (undone dependencies)
// across streams up to 8 hops so the inspector explains root blockers.
export function blockingChain(
  stream: Workstream,
  nodeId: string,
  allTasksById?: ReadonlyMap<string, WorkstreamNode>,
  taskToStream?: ReadonlyMap<string, Workstream>,
): RelRef[] {
  const start = stream.byId.get(nodeId);
  if (!start) return [];
  const out: RelRef[] = [];
  const visited = new Set<string>([start.id]);
  const queue: { id: string; depth: number }[] = start.deps.map((id) => ({
    id,
    depth: 1,
  }));

  while (queue.length > 0) {
    const { id: depId, depth } = queue.shift()!;
    if (depth > 8 || visited.has(depId)) continue;
    visited.add(depId);
    const dep = stream.byId.get(depId) ?? allTasksById?.get(depId);
    if (!dep || dep.category === "done") continue;
    out.push(makeRef(dep, stream.key, taskToStream));
    for (const nextDep of dep.deps) {
      if (!visited.has(nextDep)) queue.push({ id: nextDep, depth: depth + 1 });
    }
  }
  return out;
}

export function nodeRelationships(
  stream: Workstream,
  nodeId: string,
  allTasksById?: ReadonlyMap<string, WorkstreamNode>,
  taskToStream?: ReadonlyMap<string, Workstream>,
): NodeRelationships {
  const node = stream.byId.get(nodeId);
  if (!node) {
    return {
      filedBy: null,
      waitsOn: [],
      waitedOnBy: [],
      nextUp: [],
      blockingChain: [],
    };
  }

  const parent = node.parentId
    ? (stream.byId.get(node.parentId) ?? null)
    : null;
  const filedBy =
    parent && !parent.virtual
      ? makeRef(parent, stream.key, taskToStream)
      : null;

  const waitsOn: RelRef[] = [];
  for (const depId of node.deps) {
    const d = stream.byId.get(depId) ?? allTasksById?.get(depId);
    if (d) {
      waitsOn.push(makeRef(d, stream.key, taskToStream));
    } else {
      waitsOn.push({
        taskId: depId,
        title: `Task #${depId}`,
        stateText: node.task.dependsOnStates?.[depId] ?? "External",
        category: "blocked",
        glyph: "blocked",
        streamKey: null,
        streamTitle: null,
        crossStream: true,
      });
    }
  }

  const waitedOnBy: RelRef[] = [];
  const seenDependents = new Set<string>();
  const considerDependent = (other: WorkstreamNode) => {
    if (other.id === node.id || seenDependents.has(other.id)) return;
    if (other.deps.includes(node.id)) {
      seenDependents.add(other.id);
      waitedOnBy.push(makeRef(other, stream.key, taskToStream));
    }
  };
  for (const other of stream.nodes) considerDependent(other);
  if (allTasksById) {
    for (const other of allTasksById.values()) considerDependent(other);
  }

  // Next up: direct children or tasks waiting on this node once it finishes.
  const nextUpIds = new Set<string>();
  const nextUp: RelRef[] = [];
  for (const other of stream.nodes) {
    if (other.id === node.id) continue;
    if (other.parentId === node.id || other.deps.includes(node.id)) {
      if (!nextUpIds.has(other.id)) {
        nextUpIds.add(other.id);
        nextUp.push(makeRef(other, stream.key, taskToStream));
      }
    }
  }

  return {
    filedBy,
    waitsOn,
    waitedOnBy,
    nextUp,
    blockingChain: blockingChain(stream, nodeId, allTasksById, taskToStream),
  };
}
