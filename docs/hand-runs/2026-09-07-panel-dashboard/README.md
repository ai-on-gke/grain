# The harness behind 2026-09-07-panel-dashboard.md

Three throwaway files, kept because the numbers in section 5 of that
write-up are worth nothing if nobody can produce them again. Nothing
builds or runs any of this: the Go and JavaScript files carry a `.txt`
suffix so that `go build ./...`, `go vet` and prettier never see them,
and none of it is part of grain.

1. `task-history.sh` -- reads this repository's git log and writes one
   line per merged pull request: the number, the branch, the first and
   last commit on it, the merge, the commit count, the branch head and
   its subject. 918 lines, about 25 seconds.

2. `seed-store.go.txt` -- `grain demo`'s seed with that file in place of
   its dozen fake tasks. Opens an embedded SQLite store, writes a task
   per line through `ui.Client` and `model.Store` (the same writes a
   human clicking or the daemon reconciling would take), gives each one
   a run and a `fixes` link, marks it completed and closed at the two
   real moments, adds the branches `origin/main` has not merged as the
   live tasks, and creates the two dashboards -- six panels and ten. Then
   serves `ui.NewServer` with every `/api/` call timed to its log, which
   is where that section's server-side numbers come from. Copy it to
   `handrun/main.go` in a checkout, `go build ./handrun`, run it.

3. `drive-browser.mjs.txt` -- the Playwright driver, one step per
   section of the write-up (`cost`, `narrow`, `states`, `reload`,
   `phone`, `addpanel`). Run it from `ui/`, where `@playwright/test`
   resolves, against the server above. The browser-side numbers are
   Chromium's own `Performance.getMetrics` deltas, taken cold and then
   over a 12-second steady window.
