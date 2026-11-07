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

import { Button, Typography } from "@mui/material";
import { useEffect, useRef } from "react";

// SCROLL_AHEAD is how far below the viewport the end of a list may still
// be when the next page is asked for, so a reader scrolling at an
// ordinary pace meets rows rather than the pager.
const SCROLL_AHEAD = "600px 0px";

// ClosedTasksMore is the end of a task view that can show closed work: the
// live list carries no closed tasks, and the closed ones are paged in
// newest-closed first (closedTasks.js), so this is how a reader reaches
// the ones closed before the pages already read. Scrolling to it is
// enough -- an IntersectionObserver on it asks for the next page once it
// comes within SCROLL_AHEAD of the viewport, and again after that page
// lands while it is still in view, so a list narrowed to a few closed
// tasks a page keeps reading until it fills the screen or runs out. The
// button stays for a keyboard, for a browser with no observer, and for
// saying what is happening; while the search box is set, the pages are
// the server's search of closed work (useClosedTasks), and it says so.
// Nothing is drawn once every closed task is in, or while the view could
// show none (`active` false: App's showsClosed).
export default function ClosedTasksMore({ closed, active = true }) {
  const ref = useRef(null);
  const shown = active && !!closed?.hasMore;
  const loading = !!closed?.loading;
  const loadMore = closed?.loadMore;
  useEffect(() => {
    const el = ref.current;
    if (!el || !shown || loading || !loadMore) return undefined;
    if (typeof IntersectionObserver === "undefined") return undefined;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) loadMore();
      },
      { rootMargin: SCROLL_AHEAD },
    );
    observer.observe(el);
    return () => observer.disconnect();
  }, [shown, loading, loadMore]);
  if (!shown) {
    // A search whose first page is still on its way has no next page
    // yet, and says it is looking rather than nothing.
    if (active && loading && closed?.searching) {
      return (
        <div className="closed-tasks-more" role="status">
          <Typography variant="caption" color="text.secondary">
            Searching closed tasks…
          </Typography>
        </div>
      );
    }
    return null;
  }
  const idle = closed.searching
    ? "Search older closed tasks"
    : "Load older closed tasks";
  const busy = closed.searching
    ? "Searching closed tasks…"
    : "Loading closed tasks…";
  return (
    <div className="closed-tasks-more" ref={ref}>
      <Button
        size="small"
        variant="outlined"
        disabled={loading}
        onClick={() => loadMore()}
      >
        {loading ? busy : idle}
      </Button>
    </div>
  );
}
