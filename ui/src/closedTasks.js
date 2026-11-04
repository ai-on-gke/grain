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

import { useCallback, useEffect, useRef, useState } from "react";
import api from "./api.js";

// Closed tasks, paged in beside the live list (grain task 2983).
//
// GET /api/tasks -- what App polls every POLL_INTERVAL_MS -- carries only
// the tasks that are not closed, without their descriptions: closed work
// is most of a deployment that has run a while (2,864 of 2,981 tasks on
// the one that measured it) and only grows, and a poll carrying it cost
// every tab 14.5 MB every three seconds. Closed tasks come from
// GET /api/tasks?state=closed&cursor=, newest-closed first, a page at a
// time, and are asked again only on CLOSED_REFRESH_MS, on the tab coming
// back into view, and when a task drops out of the live list -- which is
// what closing one looks like from here.

// CLOSED_PAGE_SIZE is how many closed tasks one page holds.
export const CLOSED_PAGE_SIZE = 200;

// CLOSED_REFRESH_MS is how often the newest page is asked again.
export const CLOSED_REFRESH_MS = 60_000;

// closedPagePath is the request for the page after cursor ("" for the
// newest).
export function closedPagePath(cursor = "") {
  return `/api/tasks?state=closed&limit=${CLOSED_PAGE_SIZE}&cursor=${encodeURIComponent(cursor)}`;
}

// readPage is a page's {tasks, nextCursor}, whatever came back: an answer
// that is not a page (a proxy's error object, an older server's bare
// list) holds what tasks it can and no next page.
export function readPage(body) {
  if (Array.isArray(body)) {
    return { tasks: body.filter((t) => t?.state === "closed"), nextCursor: "" };
  }
  return {
    tasks: Array.isArray(body?.tasks) ? body.tasks : [],
    nextCursor: typeof body?.nextCursor === "string" ? body.nextCursor : "",
  };
}

// closedAtOf is when t closed, as the server orders a page by it
// (pkg/ui/client.go's closedTime): its closing time, else its ending or
// filing time.
function closedAtOf(t) {
  const at = Date.parse(t.closedAt || t.endedAt || t.createdAt || "");
  return Number.isNaN(at) ? 0 : at;
}

// newestClosedFirst is the order the server pages in and the views draw
// closed tasks in when nothing else sorts them.
function newestClosedFirst(a, b) {
  return closedAtOf(b) - closedAtOf(a) || Number(b.id) - Number(a.id);
}

// mergeClosedPage is held with page's tasks put in: each replaces the row
// it names, and the rest are added, newest-closed first -- a task that
// has just closed goes at the top, as the old whole list put it. Held is
// returned as it is when the page changes nothing, so a quiet refresh
// re-renders nothing.
export function mergeClosedPage(held, page) {
  if (page.length === 0) return held;
  const byId = new Map(held.map((t, i) => [String(t.id), i]));
  let next = null;
  const added = [];
  for (const t of page) {
    const i = byId.get(String(t.id));
    if (i === undefined) {
      added.push(t);
    } else if (JSON.stringify(held[i]) !== JSON.stringify(t)) {
      next = next || held.slice();
      next[i] = t;
    }
  }
  if (added.length === 0) return next || held;
  return [...(next || held), ...added].sort(newestClosedFirst);
}

// mergeTaskLists is the list every view draws: the live tasks, then every
// closed task the live list does not also hold (one reopened since its
// page was read is live again, and the live row is the current one).
export function mergeTaskLists(live, closed) {
  if (closed.length === 0) return live;
  const ids = new Set(live.map((t) => String(t.id)));
  const extra = closed.filter((t) => !ids.has(String(t.id)));
  return extra.length === 0 ? live : [...live, ...extra];
}

// useClosedTasks holds the closed tasks paged in so far. refresh asks the
// newest page again; loadMore asks the page after the oldest held;
// hasMore says whether there is one. Nothing is asked until enabled --
// App's first live list having answered -- so the closed page never
// queues ahead of what the first screen needs.
export function useClosedTasks(enabled = true) {
  const [closed, setClosed] = useState([]);
  const [nextCursor, setNextCursor] = useState("");
  const [loading, setLoading] = useState(false);
  // deep is whether a page past the first has been read, whose cursor a
  // refresh of the first must not overwrite.
  const deep = useRef(false);
  const inFlight = useRef(false);

  const refresh = useCallback(async () => {
    if (inFlight.current) return;
    inFlight.current = true;
    try {
      const page = readPage(await api(closedPagePath("")));
      setClosed((prev) => mergeClosedPage(prev, page.tasks));
      if (!deep.current) setNextCursor(page.nextCursor);
    } catch (err) {
      console.warn("grain: reading closed tasks failed", err);
    } finally {
      inFlight.current = false;
    }
  }, []);

  const loadMore = useCallback(async () => {
    if (!nextCursor || inFlight.current) return;
    inFlight.current = true;
    setLoading(true);
    try {
      const page = readPage(await api(closedPagePath(nextCursor)));
      deep.current = true;
      setClosed((prev) => mergeClosedPage(prev, page.tasks));
      setNextCursor(page.nextCursor);
    } catch (err) {
      console.warn("grain: reading more closed tasks failed", err);
    } finally {
      inFlight.current = false;
      setLoading(false);
    }
  }, [nextCursor]);

  useEffect(() => {
    if (!enabled) return undefined;
    refresh();
    const interval = setInterval(() => {
      if (document.visibilityState !== "hidden") refresh();
    }, CLOSED_REFRESH_MS);
    let last = Date.now();
    const onVisible = () => {
      // On coming back into view, but still no more than once a minute.
      if (document.visibilityState !== "visible") return;
      if (Date.now() - last < CLOSED_REFRESH_MS) return;
      last = Date.now();
      refresh();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      clearInterval(interval);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [enabled, refresh]);

  return { closed, hasMore: nextCursor !== "", loading, refresh, loadMore };
}
