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
  ChangeDetectionStrategy,
  Component,
  ElementRef,
  type OnDestroy,
  ViewEncapsulation,
  afterNextRender,
  computed,
  effect,
  inject,
  input,
  output,
  signal,
  untracked,
  viewChild,
} from "@angular/core";
import { Router } from "@angular/router";
import { AccessService } from "../services/access.service";
import { KeyboardService, isTypingTarget } from "../services/keyboard.service";
import { SnackbarService } from "../services/snackbar.service";
import {
  ConfirmDialogComponent,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import { repoShort } from "../shared/chips";
import { SplitController } from "../shared/split-controller";
import { TipDirective } from "../shared/tip.directive";
import { TaskDetailComponent } from "../task/task-detail.component";
import {
  type LayoutMode,
  type PositionedGraphNode,
  buildTreeRows,
  layoutTree,
  nodeRelationships,
} from "./workstream-layout";
import {
  KLABEL,
  type NodeGlyphKind,
  type SystemKind,
  type Workstream,
  type WorkstreamNode,
  lastActivityLabel,
  progressSummaryText,
} from "./workstream-model";
import { WorkstreamsStore } from "./workstreams.store";

export const NODE_GLYPHS: Record<NodeGlyphKind, string> = {
  running: "M6 1.6a4.4 4.4 0 1 1-4.2 3.1M1 2.2l.9 2.6 2.6-.9",
  asked: "M1.8 1.8h8.4v8.4H1.8zM4.4 4.4h3.2v3.2H4.4z",
  merging: "M6 1.2L10.8 6 6 10.8 1.2 6z",
  held: "M6 .8L11.2 6 6 11.2.8 6z",
  queued: "M6 1.9a4.1 4.1 0 1 0 .01 0z",
  proposed: "M6 1.9a4.1 4.1 0 1 0 .01 0z",
  deferred: "M6 1.9a4.1 4.1 0 1 0 .01 0zM6 1.9v8.2",
  merged: "M6 .9a5.1 5.1 0 1 0 .01 0zM3.6 6.1l1.7 1.7 3.2-3.4",
  done: "M6 .9a5.1 5.1 0 1 0 .01 0zM3.6 6.1l1.7 1.7 3.2-3.4",
  closed: "M6 1.4a4.6 4.6 0 1 0 .01 0zM4 4l4 4M8 4l-4 4",
  failed: "M6 1.2l5.2 9.4H.8z",
  blocked: "M6 1.4a4.6 4.6 0 1 0 .01 0zM3.4 6h5.2",
};

export const WS_INSPECTOR_RATIO_KEY = "grain.ws.inspectorRatio";
const DEFAULT_INSPECTOR_RATIO = 0.42;
const MIN_INSPECTOR_RATIO = 0.3;
const MAX_INSPECTOR_RATIO = 0.55;

function clampInspRatio(r: number): number {
  if (!Number.isFinite(r)) return DEFAULT_INSPECTOR_RATIO;
  return Math.max(MIN_INSPECTOR_RATIO, Math.min(MAX_INSPECTOR_RATIO, r));
}

function loadInspRatio(): number {
  try {
    const raw = localStorage.getItem(WS_INSPECTOR_RATIO_KEY);
    if (!raw) return DEFAULT_INSPECTOR_RATIO;
    const parsed = Number.parseFloat(raw);
    return Number.isFinite(parsed)
      ? clampInspRatio(parsed)
      : DEFAULT_INSPECTOR_RATIO;
  } catch {
    return DEFAULT_INSPECTOR_RATIO;
  }
}

function saveInspRatio(r: number): void {
  try {
    localStorage.setItem(WS_INSPECTOR_RATIO_KEY, clampInspRatio(r).toFixed(4));
  } catch {
    // Ignore storage quota / privacy mode errors.
  }
}

const MODES: { id: LayoutMode; label: string }[] = [
  { id: "split", label: "Split" },
  { id: "list", label: "List" },
  { id: "graph", label: "Graph" },
];

@Component({
  selector: "g-workstream-detail",
  imports: [TipDirective, ConfirmDialogComponent, TaskDetailComponent],
  changeDetection: ChangeDetectionStrategy.OnPush,
  encapsulation: ViewEncapsulation.None,
  styleUrls: ["./workstream-detail.css", "./workstreams-shared.css"],
  host: {
    "(document:keydown)": "onKey($event)",
    "(window:resize)": "onWindowResize()",
  },
  template: `
    @let s = stream();
    <div
      class="ws-detail"
      [attr.inert]="confirming() ? '' : null"
      data-testid="ws-detail"
    >
      <header class="ws-dhead">
        <div class="ws-crumbs">
          <button
            type="button"
            class="btn-text"
            data-testid="ws-back"
            (click)="back.emit()"
          >
            ← Workstreams
          </button>
          <span class="mono">{{ s.key }}</span>
          @if (s.seriesCount && s.seriesCount > 1) {
            <span class="ws-chip-series" [gTip]="s.by.heuristic || ''"
              >Schedule · {{ s.seriesCount }} runs</span
            >
          } @else if (s.by.heuristic) {
            <span class="ws-chip-series" [gTip]="s.by.heuristic">
              {{ s.by.name }} via Director
            </span>
          }
        </div>

        <div class="ws-dtitle-row">
          <h1 id="ws-detail-title" data-testid="ws-detail-title">
            {{ s.title }}
          </h1>
          <div class="ws-dactions">
            @if (canCreate()) {
              <button
                type="button"
                class="btn-primary"
                data-testid="ws-add-task"
                (click)="addTask.emit(s)"
              >
                + Add a task
              </button>
            }
            @if (approveCount(); as apCnt) {
              <button
                type="button"
                class="btn-tonal"
                data-testid="ws-approve-proposals"
                [disabled]="wsStore.busy() !== null"
                (click)="confirmApprove(s)"
              >
                Approve {{ apCnt }}
                {{ apCnt === 1 ? "proposal" : "proposals" }}
              </button>
            }
            @if (s.counts.person > 0) {
              <button
                type="button"
                class="btn-outlined"
                data-testid="ws-open-inbox"
                (click)="openInbox.emit(s)"
              >
                Open in Inbox
              </button>
            }
            @if (pauseCount() > 0 || resumeCount() > 0) {
              @if (resumeCount() > 0 && pauseCount() === 0) {
                <button
                  type="button"
                  class="btn-outlined"
                  data-testid="ws-resume-stream"
                  [disabled]="wsStore.busy() !== null"
                  (click)="confirmResume(s)"
                >
                  Resume stream
                </button>
              } @else if (pauseCount() > 0) {
                <button
                  type="button"
                  class="btn-outlined"
                  data-testid="ws-pause-stream"
                  [disabled]="wsStore.busy() !== null"
                  (click)="confirmPause(s)"
                >
                  Pause stream
                </button>
              }
            }
          </div>
        </div>

        <div class="ws-dmeta">
          <span>Started by {{ whoText() }}</span>
          @if (s.owner) {
            <span>· owner {{ s.owner }}</span>
          }
          @if (s.repos.length) {
            <span class="mono">· {{ s.repos.map(short).join(", ") }}</span>
          }
          @if (lastText(); as la) {
            <span>· active {{ la }} ago</span>
          }
          @if (s.paused) {
            <span class="ws-chip-warn">Paused</span>
          } @else if (s.planning) {
            <span class="ws-chip-run">Planning</span>
          }
        </div>

        @if (s.goal) {
          <p class="ws-goal" data-testid="ws-goal">{{ s.goal }}</p>
        }

        <div class="ws-dprog">
          <div>
            <div class="ws-bar" role="img" [attr.aria-label]="progText()">
              @if (s.counts.done) {
                <i class="c-done" [style.flex-grow]="s.counts.done"></i>
              }
              @if (s.counts.run) {
                <i class="c-run" [style.flex-grow]="s.counts.run"></i>
              }
              @if (nonProposedPerson()) {
                <i class="c-person" [style.flex-grow]="nonProposedPerson()"></i>
              }
              @if (s.counts.proposed) {
                <i class="c-proposed" [style.flex-grow]="s.counts.proposed"></i>
              }
              @if (s.counts.blocked) {
                <i class="c-blocked" [style.flex-grow]="s.counts.blocked"></i>
              }
              @if (s.counts.failed) {
                <i class="c-failed" [style.flex-grow]="s.counts.failed"></i>
              }
              @if (s.counts.queued) {
                <i class="c-queued" [style.flex-grow]="s.counts.queued"></i>
              }
            </div>
            <div class="ws-ptext">
              <span>{{ progText() }}</span>
              <span class="mono">{{ s.counts.percent }}%</span>
            </div>
          </div>
          <div class="ws-legend">
            <span><i class="c-done"></i> Done {{ s.counts.done }}</span>
            <span><i class="c-run"></i> Running {{ s.counts.run }}</span>
            <span
              ><i class="c-person"></i> Asked/Held
              {{ nonProposedPerson() }}</span
            >
            <span
              ><i class="c-proposed"></i> Proposed {{ s.counts.proposed }}</span
            >
            <span
              ><i class="c-blocked"></i> Blocked {{ s.counts.blocked }}</span
            >
            <span><i class="c-failed"></i> Failed {{ s.counts.failed }}</span>
          </div>
        </div>
      </header>

      <div
        class="ws-dtoolbar"
        role="toolbar"
        aria-label="Workstream view controls"
      >
        <div class="segmented" role="group" aria-label="Layout mode">
          @for (m of modes; track m.id) {
            <button
              type="button"
              [attr.aria-pressed]="mode() === m.id"
              [attr.data-testid]="'ws-mode-' + m.id"
              (click)="setMode(m.id)"
            >
              {{ m.label }}
            </button>
          }
        </div>
        <button
          type="button"
          class="filter-chip"
          data-testid="ws-hide-done"
          [attr.aria-pressed]="hideDone()"
          (click)="hideDoneChange.emit(!hideDone())"
        >
          Hide done
          @if (treeData().hiddenDoneCount > 0) {
            <span class="tabular">({{ treeData().hiddenDoneCount }})</span>
          }
        </button>
        <button
          type="button"
          class="filter-chip"
          data-testid="ws-all-deps"
          [attr.aria-pressed]="allDeps()"
          (click)="allDepsChange.emit(!allDeps())"
        >
          Show all deps
        </button>
        @if (sp.collapsed()) {
          <button
            type="button"
            class="btn-tonal"
            data-testid="ws-show-tree"
            aria-controls="ws-tree-pane"
            [attr.aria-expanded]="!sp.collapsed()"
            (click)="restoreTree()"
          >
            Show tree
          </button>
        }
        @if (expandAll()) {
          <button
            type="button"
            class="btn-text"
            data-testid="ws-collapse-packs"
            (click)="expandAll.set(false)"
          >
            Re-fold done leaves
          </button>
        }
        <span class="toolbar-spacer"></span>
        <div
          class="ws-timeline"
          aria-label="9-day activity (outlined created, green completed)"
          gTip="9-day activity: outlined = created, green = done"
        >
          @for (d of s.days; track d.key) {
            <span
              class="ws-day"
              [class.is-today]="d.isToday"
              [attr.title]="
                d.label + ': ' + d.created + ' created, ' + d.done + ' done'
              "
            >
              @for (_ of range(min4(d.done)); track $index) {
                <i class="dn"></i>
              }
              @for (_ of range(min4(d.created)); track $index) {
                <i class="cr"></i>
              }
            </span>
          }
        </div>
      </div>

      <div
        #dbody
        class="ws-dbody"
        [class.is-collapsed]="sp.collapsed()"
        [class.is-dragging]="sp.dragging() || inspDragging()"
        [class.mode-split]="mode() === 'split'"
        [class.mode-list]="mode() === 'list'"
        [class.mode-graph]="mode() === 'graph'"
        [style.--ws-tree-w.%]="sp.ratioExact()"
        [style.--ws-insp-w.%]="inspRatioExact()"
      >
        <div class="ws-tpane" id="ws-tree-pane">
          <div class="ws-tbar">
            <span>Tasks ({{ treeData().rows.length }})</span>
            <span class="muted">Enter or click to inspect</span>
          </div>
          @let posMap = treePosMap();
          @let stopId = treeTabStopId();
          <ul
            class="ws-tree"
            role="tree"
            aria-label="Stream tasks"
            data-testid="ws-tree"
          >
            @for (row of treeData().rows; track row.id) {
              @let n = row.node;
              @let sel = activeNodeId() === n.id;
              @let meta = posMap.get(row.id);
              <li
                role="treeitem"
                [attr.aria-level]="row.depth + 1"
                [attr.aria-selected]="sel"
                [attr.aria-setsize]="meta?.size ?? null"
                [attr.aria-posinset]="meta?.pos ?? null"
                [attr.aria-expanded]="
                  row.hasChildren ? !collapsedIds().has(n.id) : null
                "
              >
                <button
                  type="button"
                  class="ws-ti"
                  [class.is-selected]="sel"
                  [class.is-done]="n.category === 'done'"
                  [style.--lvl]="row.depth"
                  [attr.tabindex]="stopId === n.id ? 0 : -1"
                  [attr.data-testid]="'ws-tree-node-' + n.id"
                  (click)="onTreeSelect(n.id)"
                  (keydown)="onTreeKey($event, row.id)"
                >
                  @for (g of row.guideLevels; track g) {
                    <span
                      class="ws-guide"
                      [style.--glvl]="g"
                      aria-hidden="true"
                    ></span>
                  }
                  @if (row.hasChildren) {
                    <span
                      class="ws-ti-toggle"
                      aria-hidden="true"
                      (click)="toggleCollapse(n.id, $event)"
                    >
                      {{ collapsedIds().has(n.id) ? "▸" : "▾" }}
                    </span>
                  } @else {
                    <span class="ws-ti-toggle" aria-hidden="true"></span>
                  }
                  <svg
                    class="ws-glyph"
                    [class]="'c-' + n.category"
                    width="12"
                    height="12"
                    viewBox="0 0 12 12"
                    aria-hidden="true"
                  >
                    <path
                      [attr.d]="glyphPath(n.glyph)"
                      fill="none"
                      stroke="currentColor"
                      stroke-width="1.6"
                      [attr.stroke-dasharray]="
                        n.glyph === 'proposed' ? '2.2 1.6' : null
                      "
                    />
                  </svg>
                  @if (!n.virtual) {
                    <span class="tid mono">#{{ n.id }}</span>
                  }
                  <span class="tt">{{ n.title }}</span>
                  @if (n.foldedReviews.length) {
                    <span class="muted tid"
                      >+{{ n.foldedReviews.length }} rev</span
                    >
                  }
                  <span class="tstate">{{ n.stateText }}</span>
                </button>
              </li>
            }
          </ul>
        </div>

        <div
          class="ws-resizer ws-tree-resizer"
          role="separator"
          tabindex="0"
          aria-orientation="vertical"
          aria-controls="ws-tree-pane"
          aria-label="Resize stream tree pane"
          data-testid="ws-tree-resizer"
          [attr.aria-valuemin]="sp.minPct()"
          [attr.aria-valuemax]="sp.maxPct()"
          [attr.aria-valuenow]="sp.ratioPct()"
          [attr.aria-valuetext]="
            sp.collapsed() ? 'Collapsed' : sp.ratioPct() + '%'
          "
          (pointerdown)="sp.onResizeStart($event)"
          (pointermove)="sp.onResizeMove($event)"
          (pointerup)="sp.onResizeEnd($event)"
          (pointercancel)="sp.onResizeEnd($event)"
          (lostpointercapture)="sp.onResizeEnd($event)"
          (dblclick)="sp.reset()"
          (keydown)="sp.onKey($event)"
        >
          <span class="ws-resizer-pill" aria-hidden="true"></span>
        </div>

        <div class="ws-gpane" data-testid="ws-graph-pane">
          @let g = graph();
          <div
            class="ws-graph-region"
            role="region"
            aria-label="Workstream dependency and parent graph"
          >
            <svg
              class="ws-graph"
              [attr.width]="g.width"
              [attr.height]="g.height"
              [attr.viewBox]="'0 0 ' + g.width + ' ' + g.height"
              data-testid="ws-graph"
            >
              <defs>
                <marker
                  id="ws-dep-arrow"
                  viewBox="0 0 8 8"
                  refX="7"
                  refY="4"
                  markerWidth="6"
                  markerHeight="6"
                  orient="auto"
                >
                  <path d="M0 1 L7 4 L0 7 Z" fill="context-stroke" />
                </marker>
              </defs>
              @for (e of g.edges; track e.fromId + ":" + e.toId) {
                @if (e.onAncestorPath) {
                  <path class="ws-edge glow" [attr.d]="e.path" />
                }
                <path
                  class="ws-edge"
                  [class.path]="e.onAncestorPath"
                  [attr.d]="e.path"
                />
              }
              @for (d of g.depArcs; track d.fromId + "->" + d.toId) {
                <path
                  class="ws-dep"
                  [class.hot]="d.hot"
                  [attr.d]="d.path"
                  marker-end="url(#ws-dep-arrow)"
                />
              }
              @for (p of g.portals; track p.id) {
                <g
                  class="ws-portal"
                  tabindex="0"
                  role="button"
                  [attr.aria-label]="
                    'Blocked by #' +
                    p.targetTaskId +
                    ' in ' +
                    (p.streamTitle || 'another stream')
                  "
                  [attr.data-testid]="'ws-portal-' + p.targetTaskId"
                  (click)="onPortalClick(p.streamKey, p.targetTaskId)"
                  (keydown.enter)="onPortalClick(p.streamKey, p.targetTaskId)"
                  (keydown.space)="
                    $event.preventDefault();
                    onPortalClick(p.streamKey, p.targetTaskId)
                  "
                >
                  <rect
                    [attr.x]="p.x"
                    [attr.y]="p.y"
                    [attr.width]="p.w"
                    height="18"
                    rx="9"
                  />
                  <text [attr.x]="p.x + 7" [attr.y]="p.y + 13">
                    ↗ #{{ p.targetTaskId }}
                  </text>
                </g>
              }
              @for (pn of g.nodes; track pn.id) {
                <g
                  [class]="gnodeClass(pn)"
                  tabindex="0"
                  role="button"
                  [attr.aria-label]="pn.title"
                  [attr.data-testid]="'ws-gnode-' + pn.id"
                  (click)="onGraphNodeClick(pn)"
                  (keydown.enter)="onGraphNodeClick(pn)"
                  (keydown.space)="
                    $event.preventDefault(); onGraphNodeClick(pn)
                  "
                >
                  @if (pn.category === "run" && !pn.isPack) {
                    @for (pulse of [0, 1, 2]; track pulse) {
                      <rect
                        class="ws-run-halo"
                        [attr.x]="pn.x"
                        [attr.y]="pn.y"
                        [attr.width]="pn.w"
                        [attr.height]="pn.h"
                        rx="10"
                        [style.animation-delay.s]="pulse * 0.8"
                      />
                    }
                  }
                  <rect
                    [attr.x]="pn.x"
                    [attr.y]="pn.y"
                    [attr.width]="pn.w"
                    [attr.height]="pn.h"
                    rx="10"
                  />
                  @if (!pn.isPack && pn.node && !pn.node.virtual) {
                    <text class="id" [attr.x]="pn.x + 10" [attr.y]="pn.y + 16">
                      #{{ pn.id }} · {{ pn.node.stateText }}
                    </text>
                  } @else {
                    <text class="id" [attr.x]="pn.x + 10" [attr.y]="pn.y + 16">
                      {{ pn.isPack ? "Expand" : "Goal" }}
                    </text>
                  }
                  <text [attr.x]="pn.x + 10" [attr.y]="pn.y + 33">
                    {{ clipTitle(pn.title, 25) }}
                  </text>
                </g>
              }
            </svg>
          </div>
          <div class="ws-glegend">
            <span>Lines: parent → child</span>
            <span>Dashed: waits on (dependsOn)</span>
            <span>↗ #id: cross-stream dependency</span>
          </div>
        </div>

        <div
          class="ws-resizer ws-insp-resizer"
          role="separator"
          tabindex="0"
          aria-orientation="vertical"
          aria-controls="ws-inspector"
          aria-label="Resize task inspector"
          data-testid="ws-inspector-resizer"
          aria-valuemin="30"
          aria-valuemax="55"
          [attr.aria-valuenow]="inspRatioPct()"
          [attr.aria-valuetext]="'Inspector ' + inspRatioPct() + ' percent'"
          (pointerdown)="onInspResizeStart($event)"
          (pointermove)="onInspResizeMove($event)"
          (pointerup)="onInspResizeEnd($event)"
          (pointercancel)="onInspResizeEnd($event)"
          (lostpointercapture)="onInspResizeEnd($event)"
          (dblclick)="resetInspRatio()"
          (keydown)="onInspResizerKey($event)"
        >
          <span class="ws-resizer-pill" aria-hidden="true"></span>
        </div>

        <aside
          #inspEl
          id="ws-inspector"
          class="ws-insp"
          [class.is-open]="inspMobileOpen()"
          tabindex="-1"
          aria-label="Selected task details"
          data-testid="ws-inspector"
        >
          <div class="ws-insp-mobile-bar">
            <strong>#{{ selectedRealTaskId() }}</strong>
            <button
              type="button"
              class="icon-btn"
              aria-label="Close inspector"
              (click)="closeMobileInspector()"
            >
              ×
            </button>
          </div>

          <section
            class="ws-rel"
            aria-label="In this stream"
            data-testid="ws-in-stream"
          >
            <h3>In this stream</h3>
            @let rel = rels();
            @if (rel.filedBy; as fb) {
              <div class="ws-rel-group">
                <span class="ws-rel-lbl">Filed by</span>
                <button
                  type="button"
                  class="ws-rel-btn"
                  (click)="jumpToRel(fb)"
                >
                  <span class="mono">#{{ fb.taskId }}</span>
                  <span class="tt">{{ fb.title }}</span>
                  <span class="muted">{{ fb.stateText }}</span>
                </button>
              </div>
            }
            @if (rel.blockingChain.length) {
              <div class="ws-rel-group">
                <span class="ws-rel-lbl">Blocking chain</span>
                @for (b of rel.blockingChain; track b.taskId) {
                  <button
                    type="button"
                    class="ws-rel-btn"
                    [attr.data-testid]="'ws-blocker-' + b.taskId"
                    (click)="jumpToRel(b)"
                  >
                    <span class="mono">#{{ b.taskId }}</span>
                    <span class="tt">{{ b.title }}</span>
                    @if (b.crossStream && b.streamTitle) {
                      <span class="muted">in {{ b.streamTitle }}</span>
                    } @else {
                      <span class="muted">{{ b.stateText }}</span>
                    }
                  </button>
                }
              </div>
            }
            @if (rel.waitsOn.length) {
              <div class="ws-rel-group">
                <span class="ws-rel-lbl">Waits on</span>
                @for (w of rel.waitsOn; track w.taskId) {
                  <button
                    type="button"
                    class="ws-rel-btn"
                    (click)="jumpToRel(w)"
                  >
                    <span class="mono">#{{ w.taskId }}</span>
                    <span class="tt">{{ w.title }}</span>
                    <span class="muted">{{ w.stateText }}</span>
                  </button>
                }
              </div>
            }
            @if (rel.waitedOnBy.length) {
              <div class="ws-rel-group">
                <span class="ws-rel-lbl">Waited on by</span>
                @for (u of rel.waitedOnBy; track u.taskId) {
                  <button
                    type="button"
                    class="ws-rel-btn"
                    (click)="jumpToRel(u)"
                  >
                    <span class="mono">#{{ u.taskId }}</span>
                    <span class="tt">{{ u.title }}</span>
                    <span class="muted">{{ u.stateText }}</span>
                  </button>
                }
              </div>
            }
            @if (rel.nextUp.length && !rel.waitedOnBy.length) {
              <div class="ws-rel-group">
                <span class="ws-rel-lbl">Next up</span>
                @for (nx of rel.nextUp; track nx.taskId) {
                  <button
                    type="button"
                    class="ws-rel-btn"
                    (click)="jumpToRel(nx)"
                  >
                    <span class="mono">#{{ nx.taskId }}</span>
                    <span class="tt">{{ nx.title }}</span>
                    <span class="muted">{{ nx.stateText }}</span>
                  </button>
                }
              </div>
            }
            @if (
              !rel.filedBy &&
              !rel.blockingChain.length &&
              !rel.waitsOn.length &&
              !rel.waitedOnBy.length &&
              !rel.nextUp.length
            ) {
              <p class="muted" style="margin:0;font-size:12px">
                Stream root — select a task to see blockers and dependents.
              </p>
            }
          </section>

          @if (selectedRealTaskId(); as tid) {
            <g-task-detail
              [id]="tid"
              [pane]="true"
              [closable]="false"
              [from]="'workstreams:' + s.key"
              (openTask)="onOpenTask($event)"
              (expand)="openFullTask(tid)"
              (acted)="wsStore.refresh(true)"
            />
          }
        </aside>
      </div>
    </div>

    @if (confirming(); as c) {
      <g-confirm-dialog [request]="c" (done)="onConfirm(c, $event)" />
    }
  `,
})
export class WorkstreamDetailComponent implements OnDestroy {
  protected readonly wsStore = inject(WorkstreamsStore);
  private readonly access = inject(AccessService);
  private readonly snackbar = inject(SnackbarService);
  private readonly keyboard = inject(KeyboardService);
  private readonly router = inject(Router);
  private readonly host = inject(ElementRef) as ElementRef<HTMLElement>;

  readonly stream = input.required<Workstream>();
  readonly nodeId = input<string | null>(null);
  readonly mode = input<LayoutMode>("split");
  readonly hideDone = input(false);
  readonly allDeps = input(false);
  readonly allNodesById = input.required<ReadonlyMap<string, WorkstreamNode>>();
  readonly taskToStream = input.required<ReadonlyMap<string, Workstream>>();

  readonly back = output<void>();
  readonly nodeIdChange = output<string>();
  readonly modeChange = output<LayoutMode>();
  readonly hideDoneChange = output<boolean>();
  readonly allDepsChange = output<boolean>();
  readonly addTask = output<Workstream>();
  readonly openInbox = output<Workstream>();
  readonly selectStream = output<{ key: string; nodeId?: string }>();

  protected readonly dbodyEl = viewChild<ElementRef<HTMLElement>>("dbody");
  private readonly inspEl = viewChild<ElementRef<HTMLElement>>("inspEl");
  private inspReturn: HTMLElement | null = null;
  private dbodyResizeObs: ResizeObserver | null = null;
  protected readonly dbodyStage = signal(0);
  protected readonly sp = new SplitController({
    ratioKey: "grain.ws.treeRatio",
    collapsedKey: "grain.ws.treeCollapsed",
    defaultRatio: 0.21,
    bounds: (stage) => ({
      min: Math.min(236, stage),
      max: Math.max(Math.min(236, stage), Math.min(320, stage - 280)),
    }),
    element: () => this.dbodyEl()?.nativeElement,
  });

  protected readonly inspRatio = signal(loadInspRatio());
  protected readonly inspDragging = signal(false);
  private inspDragFrom: number | null = null;

  private clampInspForStage(r: number): number {
    const base = clampInspRatio(r);
    const stage =
      this.dbodyStage() || this.dbodyEl()?.nativeElement?.clientWidth || 0;
    if (stage <= 0) return base;
    const isDesktopSplit =
      this.mode() === "split" &&
      (typeof window === "undefined" || window.innerWidth >= 1280);
    const treePx =
      isDesktopSplit && !this.sp.collapsed()
        ? Math.min(320, Math.max(236, (this.sp.ratioExact() / 100) * stage))
        : 0;
    const maxInspPx = Math.max(360, stage - treePx - 280);
    const minInspPx = Math.min(isDesktopSplit ? 380 : 440, maxInspPx);
    const clampedPx = Math.max(
      minInspPx,
      Math.min(Math.min(stage * MAX_INSPECTOR_RATIO, maxInspPx), base * stage),
    );
    return clampedPx / stage;
  }

  protected readonly effectiveInspRatio = computed(() =>
    this.clampInspForStage(this.inspRatio()),
  );
  protected readonly inspRatioExact = computed(() =>
    Number((this.effectiveInspRatio() * 100).toFixed(2)),
  );
  protected readonly inspRatioPct = computed(() =>
    Math.round(this.effectiveInspRatio() * 100),
  );

  private readonly onInspDragEscape = (ev: KeyboardEvent): void => {
    if (ev.key !== "Escape" || !this.inspDragging()) return;
    ev.preventDefault();
    ev.stopPropagation();
    if (this.inspDragFrom !== null) this.inspRatio.set(this.inspDragFrom);
    this.inspDragging.set(false);
    this.inspDragFrom = null;
    window.removeEventListener("keydown", this.onInspDragEscape, true);
  };

  protected readonly modes = MODES;
  protected readonly short = repoShort;
  protected readonly expandAll = signal(false);
  protected readonly collapsedIds = signal<ReadonlySet<string>>(new Set());
  protected readonly inspMobileOpen = signal(false);
  protected readonly confirming = signal<ConfirmRequest | null>(null);

  constructor() {
    afterNextRender(() => {
      this.sp.attach();
      this.measureDbody();
      const el = this.dbodyEl()?.nativeElement;
      if (el && typeof ResizeObserver === "function" && !this.dbodyResizeObs) {
        this.dbodyResizeObs = new ResizeObserver(() => this.measureDbody());
        this.dbodyResizeObs.observe(el);
      }
    });
    effect(() => {
      const s = this.stream();
      untracked(() => {
        if (!s.root.virtual) {
          void this.wsStore.ensureRootDetail(s.rootId);
        }
      });
    });
  }

  ngOnDestroy(): void {
    this.sp.detach();
    this.dbodyResizeObs?.disconnect();
    this.dbodyResizeObs = null;
    window.removeEventListener("keydown", this.onInspDragEscape, true);
  }

  protected onWindowResize(): void {
    this.sp.measure();
    this.measureDbody();
  }

  private measureDbody(): void {
    const w = this.dbodyEl()?.nativeElement?.clientWidth ?? 0;
    if (w > 0) this.dbodyStage.set(w);
  }

  protected restoreTree(): void {
    if (this.mode() === "graph") {
      this.setMode("split");
    }
    if (this.sp.collapsed()) {
      this.sp.toggleCollapsed();
    }
  }

  protected openFullTask(taskId: string): void {
    void this.router.navigate(["/task", taskId], {
      queryParams: { from: `workstreams:${this.stream().key}` },
    });
  }

  protected onInspResizeStart(ev: PointerEvent): void {
    if (ev.button !== 0) return;
    this.measureDbody();
    this.inspDragFrom = this.inspRatio();
    this.inspDragging.set(true);
    window.addEventListener("keydown", this.onInspDragEscape, true);
    (ev.currentTarget as HTMLElement).setPointerCapture?.(ev.pointerId);
    ev.preventDefault();
  }

  protected onInspResizeMove(ev: PointerEvent): void {
    if (!this.inspDragging()) return;
    const el = this.dbodyEl()?.nativeElement;
    if (!el) return;
    const rect = el.getBoundingClientRect();
    if (rect.width <= 0) return;
    this.dbodyStage.set(rect.width);
    const next = this.clampInspForStage((rect.right - ev.clientX) / rect.width);
    this.inspRatio.set(next);
  }

  protected onInspResizeEnd(ev?: Event): void {
    if (!this.inspDragging()) return;
    window.removeEventListener("keydown", this.onInspDragEscape, true);
    this.inspDragging.set(false);
    this.inspDragFrom = null;
    saveInspRatio(this.effectiveInspRatio());
    (ev?.currentTarget as HTMLElement | undefined)?.releasePointerCapture?.(
      (ev as PointerEvent).pointerId,
    );
  }

  protected resetInspRatio(): void {
    this.inspRatio.set(DEFAULT_INSPECTOR_RATIO);
    saveInspRatio(DEFAULT_INSPECTOR_RATIO);
  }

  protected onInspResizerKey(ev: KeyboardEvent): void {
    this.measureDbody();
    const cur = this.effectiveInspRatio();
    const step = ev.shiftKey ? 0.06 : 0.02;
    let next: number | null = null;
    // Moving divider left widens the right-hand inspector; moving right narrows it.
    if (ev.key === "ArrowLeft") next = this.clampInspForStage(cur + step);
    else if (ev.key === "ArrowRight") next = this.clampInspForStage(cur - step);
    else if (ev.key === "Home")
      next = this.clampInspForStage(MIN_INSPECTOR_RATIO);
    else if (ev.key === "End")
      next = this.clampInspForStage(MAX_INSPECTOR_RATIO);
    if (next === null) return;
    ev.preventDefault();
    ev.stopPropagation();
    this.inspRatio.set(next);
    saveInspRatio(next);
  }

  protected readonly activeNodeId = computed<string>(() => {
    const s = this.stream();
    const wanted = this.nodeId();
    if (wanted) {
      if (s.byId.has(wanted)) return wanted;
      const mapped = s.nodes.find((n) => n.foldedTaskIds.includes(wanted));
      if (mapped) return mapped.id;
    }
    return (
      s.waitingNodes[0]?.id ??
      s.runningNodes[0]?.id ??
      s.nodes.find((n) => !n.virtual)?.id ??
      s.root.id
    );
  });

  protected readonly selectedRealTaskId = computed<string>(() => {
    const s = this.stream();
    const n = s.byId.get(this.activeNodeId());
    if (n && !n.virtual) return n.id;
    return s.nodes.find((x) => !x.virtual)?.id ?? "";
  });

  protected readonly nonProposedPerson = computed(() =>
    Math.max(0, this.stream().counts.person - this.stream().counts.proposed),
  );

  protected readonly progText = computed(() =>
    progressSummaryText(this.stream()),
  );
  protected readonly lastText = computed(() =>
    lastActivityLabel(this.stream()),
  );
  protected readonly whoText = computed(() => {
    const by = this.stream().by;
    if (by.kind === "user") {
      const who = by.isYou ? "you" : by.name;
      return by.sub === "director" ? `${who} via Director` : who;
    }
    return KLABEL[by.sub as SystemKind] ?? by.name;
  });

  protected readonly canCreate = computed(() => {
    const repos = this.stream().repos;
    return repos.length
      ? repos.some((r) => this.access.can("POST /api/tasks", r))
      : this.access.can("POST /api/tasks");
  });
  protected readonly approveCount = computed(
    () => this.wsStore.approvePreview(this.stream()).nodes.length,
  );
  protected readonly pauseCount = computed(
    () => this.wsStore.pausePreview(this.stream()).nodes.length,
  );
  protected readonly resumeCount = computed(
    () => this.wsStore.resumePreview(this.stream()).nodes.length,
  );

  protected readonly treeData = computed(() =>
    buildTreeRows(this.stream(), {
      hideDone: this.hideDone(),
      collapsedIds: this.collapsedIds(),
      selectedId: this.activeNodeId(),
    }),
  );

  protected readonly treeTabStopId = computed(() => {
    const rows = this.treeData().rows;
    const active = this.activeNodeId();
    return rows.some((r) => r.id === active) ? active : (rows[0]?.id ?? "");
  });

  protected readonly treePosMap = computed(() => {
    const rows = this.treeData().rows;
    const byParent = new Map<string, string[]>();
    for (const r of rows) {
      const p = r.parentId ?? "";
      const list = byParent.get(p) ?? [];
      list.push(r.id);
      byParent.set(p, list);
    }
    const out = new Map<string, { size: number; pos: number }>();
    for (const group of byParent.values()) {
      group.forEach((id, i) => out.set(id, { size: group.length, pos: i + 1 }));
    }
    return out;
  });

  protected readonly graph = computed(() =>
    layoutTree(this.stream(), {
      hideDone: this.hideDone(),
      expandAll: this.expandAll(),
      allDeps: this.allDeps(),
      selectedId: this.activeNodeId(),
      allTasksById: this.allNodesById(),
      taskToStream: this.taskToStream(),
    }),
  );

  protected readonly rels = computed(() =>
    nodeRelationships(
      this.stream(),
      this.activeNodeId(),
      this.allNodesById(),
      this.taskToStream(),
    ),
  );

  protected glyphPath(k: NodeGlyphKind): string {
    return NODE_GLYPHS[k] ?? NODE_GLYPHS.queued;
  }

  protected clipTitle(t: string, max: number): string {
    return t.length > max ? `${t.slice(0, max - 1)}…` : t;
  }

  protected range(n: number): number[] {
    return Array.from({ length: Math.max(0, n) }, (_, i) => i);
  }

  protected min4(n: number): number {
    return Math.min(4, Math.max(0, n));
  }

  protected gnodeClass(pn: PositionedGraphNode): string {
    const parts = ["ws-gnode", pn.category];
    if (pn.isRoot) parts.push("root");
    if (pn.isPack) parts.push("pack");
    if (pn.id === this.activeNodeId()) parts.push("sel");
    return parts.join(" ");
  }

  protected setMode(m: LayoutMode): void {
    this.modeChange.emit(m);
  }

  protected onTreeSelect(id: string): void {
    this.openMobileInspector();
    this.nodeIdChange.emit(id);
  }

  protected onOpenTask(id: string): void {
    const s = this.stream();
    if (s.byId.has(id)) {
      this.nodeIdChange.emit(id);
      return;
    }
    const target = this.taskToStream().get(id);
    if (target) {
      this.selectStream.emit({ key: target.key, nodeId: id });
    }
  }

  protected onGraphNodeClick(pn: PositionedGraphNode): void {
    if (pn.isPack) {
      this.expandAll.set(true);
      return;
    }
    this.openMobileInspector();
    this.nodeIdChange.emit(pn.id);
  }

  private openMobileInspector(): void {
    if (!this.inspMobileOpen()) {
      this.inspReturn =
        typeof document !== "undefined"
          ? (document.activeElement as HTMLElement | null)
          : null;
    }
    this.inspMobileOpen.set(true);
  }

  protected closeMobileInspector(): void {
    this.inspMobileOpen.set(false);
    const back = this.inspReturn;
    this.inspReturn = null;
    setTimeout(() => {
      if (back?.isConnected) {
        back.focus();
      } else {
        this.host.nativeElement
          .querySelector<HTMLElement>(
            `[data-testid="ws-tree-node-${this.activeNodeId()}"]`,
          )
          ?.focus();
      }
    });
  }

  protected onPortalClick(streamKey: string | null, taskId: string): void {
    if (streamKey) {
      this.selectStream.emit({ key: streamKey, nodeId: taskId });
    }
  }

  protected jumpToRel(ref: {
    taskId: string;
    streamKey: string | null;
    crossStream: boolean;
  }): void {
    if (ref.crossStream && ref.streamKey) {
      this.selectStream.emit({ key: ref.streamKey, nodeId: ref.taskId });
      return;
    }
    this.nodeIdChange.emit(ref.taskId);
  }

  protected toggleCollapse(id: string, ev: Event): void {
    ev.stopPropagation();
    this.collapsedIds.update((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  protected onTreeKey(ev: KeyboardEvent, id: string): void {
    const rows = this.treeData().rows;
    const idx = rows.findIndex((r) => r.id === id);
    if (idx < 0) return;
    const cur = rows[idx];

    if (ev.key === "ArrowDown" || ev.key === "j") {
      ev.preventDefault();
      const next = rows[Math.min(rows.length - 1, idx + 1)];
      if (next) this.focusTreeNode(next.id);
    } else if (ev.key === "ArrowUp" || ev.key === "k") {
      ev.preventDefault();
      const prev = rows[Math.max(0, idx - 1)];
      if (prev) this.focusTreeNode(prev.id);
    } else if (ev.key === "ArrowRight") {
      ev.preventDefault();
      if (cur.hasChildren && this.collapsedIds().has(cur.id)) {
        this.toggleCollapse(cur.id, ev);
      } else if (cur.childIds[0]) {
        this.focusTreeNode(cur.childIds[0]);
      }
    } else if (ev.key === "ArrowLeft") {
      ev.preventDefault();
      if (cur.hasChildren && !this.collapsedIds().has(cur.id)) {
        this.toggleCollapse(cur.id, ev);
      } else if (cur.parentId) {
        this.focusTreeNode(cur.parentId);
      }
    } else if (ev.key === "Home") {
      ev.preventDefault();
      if (rows[0]) this.focusTreeNode(rows[0].id);
    } else if (ev.key === "End") {
      ev.preventDefault();
      const last = rows[rows.length - 1];
      if (last) this.focusTreeNode(last.id);
    } else if (ev.key === "Enter") {
      ev.preventDefault();
      this.onTreeSelect(id);
    }
  }

  private focusTreeNode(id: string): void {
    this.nodeIdChange.emit(id);
    setTimeout(() => {
      const btn = (
        this.host.nativeElement as HTMLElement
      ).querySelector<HTMLElement>(`[data-testid="ws-tree-node-${id}"]`);
      btn?.focus();
    });
  }

  protected onKey(ev: KeyboardEvent): void {
    if (ev.defaultPrevented || isTypingTarget(ev.target)) return;
    if (ev.metaKey || ev.ctrlKey || ev.altKey || ev.repeat) return;
    if (
      this.confirming() ||
      document.querySelector('[aria-modal="true"], .confirm-inline')
    ) {
      return;
    }
    const letters = this.keyboard.enabled();
    if (letters && ev.key === "m") {
      ev.preventDefault();
      const order: LayoutMode[] = ["split", "list", "graph"];
      this.setMode(order[(order.indexOf(this.mode()) + 1) % order.length]);
    } else if (letters && ev.key === "h") {
      ev.preventDefault();
      this.hideDoneChange.emit(!this.hideDone());
    } else if (letters && ev.key === "d") {
      ev.preventDefault();
      this.allDepsChange.emit(!this.allDeps());
    } else if (letters && ev.key === "o") {
      ev.preventDefault();
      this.openMobileInspector();
      setTimeout(() => this.inspEl()?.nativeElement.focus());
    } else if (letters && ev.key === "/") {
      ev.preventDefault();
      (
        document.querySelector(
          '[data-testid="view-filter"]',
        ) as HTMLElement | null
      )?.focus();
    } else if (letters && ev.key === "n" && this.canCreate()) {
      ev.preventDefault();
      this.addTask.emit(this.stream());
    } else if (ev.key === "Escape") {
      ev.preventDefault();
      if (this.inspMobileOpen()) {
        this.closeMobileInspector();
      } else {
        this.back.emit();
      }
    }
  }

  protected confirmApprove(s: Workstream): void {
    const preview = this.wsStore.approvePreview(s);
    if (!preview.nodes.length) return;
    this.confirming.set({
      title: `Approve ${preview.nodes.length} ${preview.nodes.length === 1 ? "proposal" : "proposals"}?`,
      message: `Queue all proposed tasks in "${s.title}" to run.`,
      accept: `Approve (${preview.nodes.length})`,
      destructive: false,
      scope: preview.scope,
      run: async () => {
        const res = await this.wsStore.approveProposals(s);
        this.snackbar.show(
          res.failed
            ? `Approved ${res.succeeded} of ${res.succeeded + res.failed}`
            : `Approved ${res.succeeded} ${res.succeeded === 1 ? "proposal" : "proposals"}`,
        );
      },
    });
  }

  protected confirmPause(s: Workstream): void {
    const preview = this.wsStore.pausePreview(s);
    if (!preview.nodes.length) return;
    this.confirming.set({
      title: `Pause ${preview.nodes.length} queued/proposed tasks?`,
      message: `Defers unstarted queued or proposed tasks in "${s.title}" until resumed.`,
      accept: `Pause (${preview.nodes.length})`,
      destructive: false,
      scope: preview.scope,
      actionVerb: "pausing tasks",
      run: async () => {
        const res = await this.wsStore.pauseStream(s);
        this.snackbar.show(
          res.failed
            ? `Paused ${res.succeeded} of ${res.succeeded + res.failed}`
            : `Paused ${res.succeeded} tasks in stream`,
        );
      },
    });
  }

  protected confirmResume(s: Workstream): void {
    const preview = this.wsStore.resumePreview(s);
    if (!preview.nodes.length) return;
    this.confirming.set({
      title: `Resume ${preview.nodes.length} deferred tasks?`,
      message: `Undefers paused tasks in "${s.title}" so workers pick them up again.`,
      accept: `Resume (${preview.nodes.length})`,
      destructive: false,
      scope: preview.scope,
      actionVerb: "resuming tasks",
      run: async () => {
        const res = await this.wsStore.resumeStream(s);
        this.snackbar.show(
          res.failed
            ? `Resumed ${res.succeeded} of ${res.succeeded + res.failed}`
            : `Resumed ${res.succeeded} tasks`,
        );
      },
    });
  }

  protected async onConfirm(c: ConfirmRequest, ok: boolean): Promise<void> {
    this.confirming.set(null);
    if (ok) await c.run();
  }
}
