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

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
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

// SEARCH_DEBOUNCE_MS is how long the search box has to sit still before
// the server is asked to search closed work for it: one request for a
// word, not one per keystroke.
export const SEARCH_DEBOUNCE_MS = 300;

// closedPagePath is the request for the page after cursor ("" for the
// newest). search narrows it to the closed tasks whose title,
// description, external reference or id holds that text (pkg/ui's
// taskListQuery), owner to one owner's (compared case-insensitively),
// externalRef to the tasks carrying exactly that reference, repo to one
// repository's, and limit sets the page's size -- a caller that wants
// only the totals asks for a page of one.
export function closedPagePath(
  cursor = "",
  {
    search = "",
    owner = "",
    externalRef = "",
    repo = "",
    limit = CLOSED_PAGE_SIZE,
  } = {},
) {
  let path = `/api/tasks?state=closed&limit=${limit}&cursor=${encodeURIComponent(cursor)}`;
  if (search) path += `&search=${encodeURIComponent(search)}`;
  if (owner) path += `&owner=${encodeURIComponent(owner)}`;
  if (externalRef) path += `&externalRef=${encodeURIComponent(externalRef)}`;
  if (repo) path += `&repo=${encodeURIComponent(repo)}`;
  return path;
}

// closedQueryKey is one server question about closed work -- the search
// box's text (searchKey) and the owner and external reference the
// narrowing pins (taskFilters.js's closedServerFilters) -- as one string,
// "" when it asks nothing: what useClosedTasks compares to know whether
// an answer is still the one the views want.
export function closedQueryKey({ q = "", owner = "", externalRef = "" } = {}) {
  if (!q && !owner && !externalRef) return "";
  return JSON.stringify([q, owner, externalRef]);
}

// searchKey is the search box's text as matchesFilters compares it:
// trimmed and lower-cased (taskFilters.js's narrowTasks).
export function searchKey(text) {
  return String(text || "")
    .trim()
    .toLowerCase();
}

// markSearchHits is a server-searched page's tasks marked with the text
// that found them (searchHit), which is what lets taskFilters.js's
// matchesFilters keep a task the server matched on its description --
// a listed task carries none for the client to match itself.
export function markSearchHits(tasks, key) {
  return tasks.map((t) => ({ ...t, searchHit: key }));
}

// readPage is a page's {tasks, nextCursor}, whatever came back: an answer
// that is not a page (a proxy's error object, an older server's bare
// list) holds what tasks it can and no next page.
export function readPage(body) {
  if (Array.isArray(body)) {
    return {
      tasks: body.filter((t) => t?.state === "closed"),
      nextCursor: "",
      totals: null,
    };
  }
  const n = (v) => (typeof v === "number" ? v : 0);
  return {
    tasks: Array.isArray(body?.tasks) ? body.tasks : [],
    nextCursor: typeof body?.nextCursor === "string" ? body.nextCursor : "",
    // pkg/ui taskListPage's counts over every page; null from a server
    // that does not send them, which leaves the rail counting what it holds.
    totals:
      typeof body?.total === "number"
        ? {
            total: body.total,
            merged: n(body.merged),
            sessions: n(body.sessions),
          }
        : null,
  };
}

// unseenClosed is the closed work the server counts that no page read so
// far holds, split the way the rail counts it: closed and merged tasks,
// a session's own being counted under Sessions and so in neither. The
// rail adds them to its Closed, Merged and Tasks counts so they say how
// much there is rather than how much was paged in (2026-11-04
// walkthrough: 205 closed tasks read "Closed 200"). All zero until a page
// has said its totals.
export function unseenClosed(totals, held) {
  const none = { closed: 0, merged: 0 };
  if (!totals) return none;
  let sessions = 0;
  let merged = 0;
  for (const t of held) {
    if (t.consoleSession || t.interactive) sessions += 1;
    else if (t.prMergedAt) merged += 1;
  }
  const unseenSessions = Math.max(0, totals.sessions - sessions);
  const unseenMerged = Math.max(0, totals.merged - merged);
  const unseen = Math.max(0, totals.total - held.length);
  return {
    closed: Math.max(0, unseen - unseenSessions - unseenMerged),
    merged: Math.min(unseen, unseenMerged),
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
      continue;
    }
    // A refresh's row knows nothing of the search that found the held
    // one (markSearchHits), and must not make it unfindable again.
    const row =
      t.searchHit === undefined && held[i].searchHit !== undefined
        ? { ...t, searchHit: held[i].searchHit }
        : t;
    if (JSON.stringify(held[i]) !== JSON.stringify(row)) {
      next = next || held.slice();
      next[i] = row;
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

// stateFilterAdmitsClosed is whether the rail's state filter (App's
// stateFilter, TaskList's inState) lets a closed task into the list:
// every task, the blocked ones, and the Closed and Merged entries do;
// any other state is one a closed task is not in.
export function stateFilterAdmitsClosed(filter) {
  return ["all", "blocked", "closed", "merged"].includes(filter || "all");
}

// closedPartial is whether the closed tasks held are fewer than there
// are: true until the newest page has answered, then while the server's
// totals count closed work no page read so far holds (or, from a server
// that sends no totals, while there is a next page). A view counting
// closed work says its count is a floor ("200+") while this is true,
// rather than presenting what was paged in as the whole.
export function closedPartial({ answered, totals, held, nextCursor }) {
  if (!answered) return true;
  if (totals) return totals.total > held.length;
  return nextCursor !== "";
}

// useClosedTasks holds the closed tasks paged in so far. refresh asks the
// newest page again; loadMore asks the next page -- of the server's
// search for `search` while there is one, else after the oldest held --
// and hasMore says whether there is one. Nothing is asked until enabled
// -- App's first live list having answered -- so the closed page never
// queues ahead of what the first screen needs.
//
// search is the task views' search box, when they could show closed
// work at all (App passes "" otherwise). Searching the pages already
// held would miss every task closed before them, so the server is asked
// instead (GET /api/tasks?state=closed&search=, which also matches
// descriptions), SEARCH_DEBOUNCE_MS after the box goes quiet, and what
// it finds is drawn beside the rest while the box still holds that text,
// marked with the text that found it (markSearchHits).
//
// filters is the owner and external reference the narrowing pins with a
// single `owner:any:<v>` or `externalRef:any:<v>` condition
// (taskFilters.js's closedServerFilters), asked of the server the same
// way and alongside the text: answered only on the pages held, a
// condition matching one task deep in closed work kept the end of the
// short list in view and so had ClosedTasksMore page through every
// closed page until it turned up -- 15 requests and ~10 s against 3,000
// closed tasks (2026-11-07 walkthrough, F6). The views still apply the
// condition themselves to whatever comes back.
export function useClosedTasks(
  enabled = true,
  search = "",
  { owner = "", externalRef = "" } = NO_FILTERS,
) {
  const [closed, setClosed] = useState([]);
  const [nextCursor, setNextCursor] = useState("");
  const [loading, setLoading] = useState(false);
  // answered is whether the newest page has answered once.
  const [answered, setAnswered] = useState(false);
  // totals is the newest page's counts of every closed task (readPage),
  // null until a page has answered with them.
  const [totals, setTotals] = useState(null);
  // searched is the server search for the box's text: q the text it was
  // asked for, nextCursor its next page, loading while a page of it is
  // on its way.
  const [searched, setSearched] = useState(NO_SEARCH);
  // found is what the server's search has answered for searched.q, held
  // apart from the newest-closed pages in `closed`: a hit can be any
  // number of pages deeper than the oldest page read, and merged into
  // those pages it stayed drawn below them -- out of place, after a gap
  // of every task closed in between -- once the box was emptied
  // (2026-11-07 walkthrough). The views see it only while that search is
  // the box's.
  const [found, setFound] = useState(NO_TASKS);
  // deep is whether a page past the first has been read, whose cursor a
  // refresh of the first must not overwrite.
  const deep = useRef(false);
  const inFlight = useRef(false);
  const searchInFlight = useRef(false);
  const q = searchKey(search);
  const pinnedOwner = String(owner || "").trim();
  const pinnedRef = String(externalRef || "").trim();
  // key is the whole server question; searched.q holds the key it was
  // asked for.
  const key = closedQueryKey({
    q,
    owner: pinnedOwner,
    externalRef: pinnedRef,
  });

  const refresh = useCallback(async () => {
    if (inFlight.current) return;
    inFlight.current = true;
    try {
      const page = readPage(await api(closedPagePath("")));
      setClosed((prev) => mergeClosedPage(prev, page.tasks));
      setTotals(page.totals);
      setAnswered(true);
      if (!deep.current) setNextCursor(page.nextCursor);
    } catch (err) {
      console.warn("grain: reading closed tasks failed", err);
    } finally {
      inFlight.current = false;
    }
  }, []);

  // searchPage asks the server's search for query after cursor and
  // merges what it finds. Each ask takes the next generation, and an
  // answer that is no longer the newest -- the box or the narrowing has
  // moved on, or emptied -- is dropped rather than recorded as the
  // current search's page.
  const searchGen = useRef(0);
  const searchPage = useCallback(async (query, cursor) => {
    const asked = closedQueryKey(query);
    const gen = ++searchGen.current;
    searchInFlight.current = true;
    setSearched((prev) => ({
      q: asked,
      nextCursor: prev.q === asked ? prev.nextCursor : "",
      loading: true,
    }));
    try {
      const page = readPage(
        await api(
          closedPagePath(cursor, {
            search: query.q,
            owner: query.owner,
            externalRef: query.externalRef,
          }),
        ),
      );
      if (gen !== searchGen.current) return;
      // Not the page's totals: they count the search's matches, and the
      // rail's counts are of every closed task. Only a text search marks
      // its hits: an owner or external reference is on the listed task
      // for the views to match themselves.
      const hits = query.q ? markSearchHits(page.tasks, query.q) : page.tasks;
      setFound((prev) => (cursor ? mergeClosedPage(prev, hits) : hits));
      setSearched({ q: asked, nextCursor: page.nextCursor, loading: false });
    } catch (err) {
      console.warn("grain: searching closed tasks failed", err);
      if (gen === searchGen.current) setSearched({ ...NO_SEARCH, q: asked });
    } finally {
      if (gen === searchGen.current) searchInFlight.current = false;
    }
  }, []);

  useEffect(() => {
    if (!enabled || key === "") {
      searchGen.current += 1;
      searchInFlight.current = false;
      setSearched(NO_SEARCH);
      setFound(NO_TASKS);
      return undefined;
    }
    const query = { q, owner: pinnedOwner, externalRef: pinnedRef };
    // Only typing is debounced: a condition is picked, not typed, and
    // arrives whole.
    const timer = setTimeout(
      () => searchPage(query, ""),
      q ? SEARCH_DEBOUNCE_MS : 0,
    );
    return () => clearTimeout(timer);
  }, [enabled, key, q, pinnedOwner, pinnedRef, searchPage]);

  const searching = key !== "";
  const loadMore = useCallback(async () => {
    if (searching) {
      if (
        !searchInFlight.current &&
        searched.q === key &&
        searched.nextCursor
      ) {
        await searchPage(
          { q, owner: pinnedOwner, externalRef: pinnedRef },
          searched.nextCursor,
        );
      }
      return;
    }
    if (!nextCursor || inFlight.current) return;
    inFlight.current = true;
    setLoading(true);
    try {
      const page = readPage(await api(closedPagePath(nextCursor)));
      deep.current = true;
      setClosed((prev) => mergeClosedPage(prev, page.tasks));
      setTotals(page.totals);
      setNextCursor(page.nextCursor);
    } catch (err) {
      console.warn("grain: reading more closed tasks failed", err);
    } finally {
      inFlight.current = false;
      setLoading(false);
    }
  }, [
    nextCursor,
    searching,
    key,
    q,
    pinnedOwner,
    pinnedRef,
    searched,
    searchPage,
  ]);

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

  // shown is what the views draw: the newest-closed pages, and while the
  // box is set, what the server's search for it found.
  const shown = useMemo(
    () =>
      searching && searched.q === key && found.length > 0
        ? mergeClosedPage(closed, found)
        : closed,
    [searching, searched.q, key, found, closed],
  );
  const unseen = useMemo(() => unseenClosed(totals, shown), [totals, shown]);
  const partial = closedPartial({ answered, totals, held: shown, nextCursor });
  return {
    closed: shown,
    hasMore: searching
      ? searched.q === key && searched.nextCursor !== ""
      : nextCursor !== "",
    loading: searching ? searched.loading : loading,
    searching,
    refresh,
    loadMore,
    unseen,
    partial,
  };
}

// repoClosedCount is the closed tasks a page's totals count that the
// repo pages count -- every one but a session's own (state.js's
// repoRows leaves sessions out) -- or null from a server that sends no
// totals.
export function repoClosedCount(totals) {
  if (!totals) return null;
  return Math.max(0, totals.total - totals.sessions);
}

// withClosedCount is a repoRows row with its closed count taken from the
// server (repoClosedCount) rather than from the closed tasks paged in so
// far, and its total moved by the difference; the row as it is when
// there is no server count to put in.
export function withClosedCount(row, closed) {
  if (closed === null || closed === undefined) return row;
  const held = row.counts.closed || 0;
  if (closed === held) return row;
  const counts = { ...row.counts };
  if (closed > 0) counts.closed = closed;
  else delete counts.closed;
  return { ...row, counts, total: Math.max(0, row.total + closed - held) };
}

// useRepoClosedCount is how many closed tasks repo has, asked of the
// server -- a page of one of GET /api/tasks?state=closed&repo=, read for
// its totals -- while `enabled`: App's closed pages are the newest
// across every repo, so a repo's page counting only those would present
// a floor as its total. null until it answers, and when it cannot.
export function useRepoClosedCount(repo, enabled) {
  const [count, setCount] = useState(null);
  useEffect(() => {
    if (!enabled || !repo) {
      setCount(null);
      return undefined;
    }
    let live = true;
    api(closedPagePath("", { repo, limit: 1 }))
      .then((body) => {
        if (live) setCount(repoClosedCount(readPage(body).totals));
      })
      .catch((err) => {
        console.warn("grain: counting a repo's closed tasks failed", err);
        if (live) setCount(null);
      });
    return () => {
      live = false;
    };
  }, [repo, enabled]);
  return count;
}

// NO_SEARCH is useClosedTasks' search state with nothing searched.
const NO_SEARCH = Object.freeze({ q: "", nextCursor: "", loading: false });
const NO_TASKS = Object.freeze([]);
// NO_FILTERS is useClosedTasks' filters with nothing pinned.
const NO_FILTERS = Object.freeze({ owner: "", externalRef: "" });
