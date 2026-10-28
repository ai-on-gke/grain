# The harness behind 2026-09-17-chat-keystroke.md

Four throwaway files, kept because the numbers in that write-up are
worth nothing if nobody can produce them again -- the same reason
`2026-09-07-panel-dashboard/` keeps its own. Nothing builds or runs any
of this: the JavaScript carries a `.txt` suffix so that prettier's own
gate (`make fmt-frontend`, which is `prettier --check .` over `ui/`)
never sees it, and none of it is part of grain.

It needs no daemon, no store and no seeded history, which is the one way
this harness is simpler than that one: what is being measured is a
component's own render cost, so the conversation is built in the browser
and nothing is fetched.

1. `main.jsx.txt` -- mounts `ui/src/components/TaskChat.jsx` on a
   synthetic task of N messages, alternating a ~2.2 KB agent answer
   (headings, a fenced diff, a table, backticked paths, a list, a
   blockquote) with a one-line human reply. It also stands in for
   `App.jsx`: it holds the task in state and `window.__bench.poll(i)`
   hands down a *fresh* task object with a changed `activity`, which is
   what a poll landing on a live task looks like after `keepIfSame`
   (`ui/src/poll.js`) has failed to bail out of it.

2. `index.html.txt`, `vite.config.mjs.txt` -- a production Vite build of
   just that, into `ui/bench/dist`. A production build on purpose: React's
   development build carries the extra work of `StrictMode`'s double
   render and prop-type checks, and none of that is what an operator was
   waiting on.

3. `drive.mjs.txt` -- the Playwright driver. Types characters into the
   composer one at a time, a frame apart, and reads Chromium's own
   `Performance.getMetrics` deltas around them (`ScriptDuration`,
   `LayoutDuration`, `Timestamp`), then does the same around ten
   `window.__bench.poll()` calls. `BENCH_N`, `BENCH_KEYS` and
   `BENCH_POLLS` override the defaults (`5,20,40,80`, 20 keys, 10 polls).

To run it, from a checkout with `ui/node_modules` installed and
Playwright's Chromium downloaded (`npx playwright install --with-deps
chromium`):

```sh
cd ui
mkdir -p bench
for f in main.jsx drive.mjs vite.config.mjs index.html; do
  cp ../docs/hand-runs/2026-09-17-chat-keystroke/$f.txt bench/$f
done
npx vite build --config bench/vite.config.mjs
npx vite preview --config bench/vite.config.mjs --port 5199 >/dev/null 2>&1 &
node bench/drive.mjs http://localhost:5199
rm -rf bench          # before `make fmt-frontend`, which checks all of ui/
```

`vite preview` binds `[::1]` and not `127.0.0.1`, so pass `localhost`
rather than the address -- half an hour was lost to that.
