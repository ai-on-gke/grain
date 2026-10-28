# Looking at a UI change

UI tasks used to pass on unit tests while the real page looked wrong. In
#4980 the header rail's tab counts sat off their labels and the "Add repo"
control floated mid-rail, with every jsdom and DOM assertion green. So
`.grain/settings.yml`'s `prompt-extension` now gives every agent working
in this repository a standing "UI changes" rule. Any change touching
`ui/` has to render the affected pages in a real browser, look at them,
and hand the screenshots on. This page covers how to do that.

## Taking screenshots

```sh
make ui-screenshots PAGES=/,/inbox OUT=screenshots/after
```

This builds the frontend, installs Playwright's Chromium, builds `grain`
from the checkout and runs `grain demo` over a throwaway store. That is
the same seeded server the e2e suite's workers use (`ui/e2e/fixtures.js`).
It then shoots every page in `PAGES` at desktop (1440×900) and phone
(390×844) widths, in light and dark. The theme goes through the app's own
`grain.themeMode` setting as well as `prefers-color-scheme`.

The make target is `ui/e2e/screenshot.mjs`. From `ui/`, once the frontend
is built and Playwright is installed, you can run the script directly:

```sh
node e2e/screenshot.mjs --pages /repos --widths desktop,tablet,phone --out screenshots/after
node e2e/screenshot.mjs --url http://127.0.0.1:8080 --code XXXX-... --pages /   # a server you already run
```

Output goes to `ui/screenshots/<OUT>/` (git ignores it):

- `<page>-<width>-<theme>.png` for each shot;
- `contact-sheet.png`, all of the shots on one image, which is the file to
  attach.

A control that has to be opened before the shot -- a menu, a collapsed
group -- is clicked with `--click SELECTOR` (`CLICK=` for the make
target), any Playwright selector, repeated to click several in order:

```sh
node e2e/screenshot.mjs --pages '/tasks/13#task-13-api-grants-header' \
  --click '#task-13-api-grants-header' --out screenshots/after
```

An uncaught page error fails the run, because a blank pane is not a
screenshot.

For a fix, take the before set on the base first, so the two directories
line up file for file:

```sh
git stash   # or check out the base
make ui-screenshots PAGES=/repos OUT=screenshots/before
git stash pop
make ui-screenshots PAGES=/repos OUT=screenshots/after
```

## Looking at them

Open the PNGs and check the following:

- alignment: badges and counts centred on their labels, baselines that
  line up;
- placement: actions in the slot they belong in, not floating;
- overflow, clipping and unexpected wrapping;
- spacing;
- contrast in both themes.

In a grain sandbox, `read_file` on a PNG returns its bytes, not the
image. Add `LOOK=1` (`--look` on the script) to send the shots to Gemini
and print what it sees. This needs `GEMINI_API_KEY` or
`~/.gemini-api-key`. `GEMINI_MODEL` overrides the model, and `--look=…`
replaces the question. The model is a second pair of eyes, not a
verdict: confirm what it reports and judge it yourself, since a row cut
off at the viewport's bottom edge is expected, for example. If you have
no way to see the images at all, say so in the closing note. Do not
claim the page looks right.

## Pinning a layout rule

Where the layout itself is the requirement, assert it in a Playwright
spec under `ui/e2e/` with bounding boxes, so it cannot quietly come back.
For example, a badge centred on its label:

```js
const label = await page.getByRole("tab", { name: /repos/i }).boundingBox();
const badge = await page.getByTestId("repos-count").boundingBox();
expect(Math.abs(badge.y + badge.height / 2 - (label.y + label.height / 2))).toBeLessThan(2);
```

The same check works for an action in the right-hand slot: compare its
`x + width` with the container's. `alerts-panel-layout.spec.js` and
`phone.spec.js` are existing examples.

## Handing them on

A grain run's pull request description is built from its commit messages,
which cannot carry an image. So attach `contact-sheet.png` (the after one,
plus the before one for a fix) to the run's `comment_on_issue` closing
note. Attach it by `path` (for example
`{"path": "ui/screenshots/after/contact-sheet.png"}`), which grain reads
from the sandbox when the call is made, up to 10 MB. Don't paste it as
base64 `content`: those bytes have to come back through a tool answer
first, and tool answers are cut at about 16 KB. The images preview in the task's conversation in grain. When the
run pushed a branch, grain also puts them in the pull request's
description, under an "Images" heading above grain's footer, so a
reviewer on GitHub sees them too. In the commit message, name the pages,
widths and themes you shot and what you checked.

How the images get there (`pkg/orchestrator/images.go`): when grain opens
or updates the pull request at the end of a run, it takes the newest note
the task's agent left with images on it. It commits those images to the
pull request's own repository as one commit with no parent, on
`refs/grain-images/<branch>/<comment id>`. That ref is outside
`refs/heads` and `refs/tags`, so no clone fetches it and nothing builds or
merges it. The description links each image by that commit's sha, so the
images are visible to the same people who can read the pull request. Only
PNG, JPEG, GIF and WebP files are published; other attachments stay in
grain's conversation. A description a human has rewritten is left alone.
