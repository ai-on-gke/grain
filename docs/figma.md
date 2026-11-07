# Working on an app's UI with Figma and screenshots (operator and agent how-to)

A run improving the UI of your app needs two things grain gives it:

1. **The design.** The built-in `figma` capability server
   (`https://grain.dev/capabilities/figma`) lets a task read the Figma
   file it is implementing -- frames, layout, typography, colours, design
   tokens, designer comments -- and look at rendered frames, without a
   Figma credential ever entering the sandbox. For tooling that must call
   Figma itself, it can also place a short-lived OAuth access token.
2. **The result.** `grain screenshot` renders the app the run is working
   on in a real browser, at desktop and phone widths in light and dark,
   and makes one contact sheet of the shots. Attached to the run's closing
   note, the sheet shows in the task's conversation in grain (thumbnails
   that open full screen and page through) and in the pull request's
   description.

## Setting up Figma

Open **Settings → Capability servers → figma → Configure**.

### An account with a personal access token (quickest)

1. In Figma, **Settings → Security → Personal access tokens → Generate
   new token**, with read access to file content, metadata and comments.
2. Under **accounts**, add `{name: "default", auth_mode: "pat"}`.
3. In **personal_access_tokens** (write-only), enter
   `{"default": "figd_..."}`.
4. Save. The account's row on the card has a **Test** button: it asks
   Figma's `/v1/me` who the token belongs to and shows the handle and
   email, or Figma's refusal.

A personal access token does not expire and is never placed in a sandbox:
an account using one serves `figma#mcp` only.

### An account with OAuth 2.0 (token generation)

OAuth lets the server mint short-lived access tokens, and is the only
mode that can offer `figma#token`.

1. Create an OAuth app at <https://www.figma.com/developers/apps>. Add
   the callback URL the account's row shows,
   `<your grain URL>/settings/capabilities`.
2. Under **accounts**, add
   `{name: "design", auth_mode: "oauth", client_id: "<client id>"}`.
   `scopes` defaults to `file_content:read,file_metadata:read,file_comments:read`.
   Set `allow_sandbox_token: true` to offer `figma#token` for it.
3. In **client_secrets** (write-only), enter `{"design": "<client secret>"}`
   and save.
4. Press **Connect Figma** on the account's row. Figma's consent page
   opens in a popup; once you allow it, the popup closes and grain trades
   the code for a refresh token, which it keeps in the secret store
   (`refresh_tokens`) and never shows. **Test** then reports who it
   connected as.

### Narrowing what a task may read

`allowed_file_keys` on an account restricts it to those files (the key is
the part after `/design/` or `/file/` in a Figma URL). A grant can narrow
further with `file_keys`. `per_attempt` (default 60, at most 500) caps
Figma calls per attempt.

`dataPlaneToken` is managed by grain, as for every in-tree server (the
data plane listens on `127.0.0.1:4253`).

## Granting it

- `https://grain.dev/capabilities/figma#mcp` with `with: {account: <name>, file_keys?: [...]}`
  (`account` may be left out while only one is configured) gives the run
  an MCP toolset:
  - `parse_figma_url(url)` -- a Figma link's `file_key` and `node_id`;
  - `get_file(file_key | url, depth?)` -- pages, top-level frames,
    components and styles;
  - `get_design_context(file_key | url, node_id(s)?, depth?)` -- a frame's
    layout spec: sizes, auto-layout direction, padding and gaps, colours,
    strokes, radii, shadows and typography;
  - `export_images(file_key | url, node_id(s)?, format?, scale?)` --
    renders frames; PNG and JPG come back as images the agent looks at,
    with Figma's download URL beside each;
  - `get_styles_and_variables(file_key | url)` -- published styles and
    local variables (design tokens);
  - `get_comments(file_key | url)` -- what the designers said.
- `https://grain.dev/capabilities/figma#token` with `with: {account: <name>}`,
  offered only for oauth accounts with `allow_sandbox_token`, places a
  freshly refreshed access token (it lives as long as Figma's `expires_in` says) at `/home/debian/.figma-token`
  (mode 0600) for tools in the sandbox that call Figma themselves
  (`Authorization: Bearer $(cat /home/debian/.figma-token)`).

Every call and token mint is recorded in the attempt's audit on release.

## Screenshots of the app

In the sandbox, start the app's dev server, then:

```sh
grain screenshot --url http://127.0.0.1:5173 --pages /,/checkout --out screenshots/after
```

That writes `screenshots/after/<page>-<width>-<theme>.png` for each page
at desktop (1440×900) and phone (390×844) widths in light and dark, and
`screenshots/after/contact-sheet.png` with all of them. `grain screenshot
--help` lists everything else: `--widths desktop,tablet,phone`,
`--themes`, `--theme-key` for an app that keeps its own theme in
localStorage, `--storage`, `--cookie` and `--header` for a page behind a
sign-in, `--click` and `--type` to open a menu or fill a search box
first, `--wait-for` a selector, `--full-page`. A page error or an HTTP
error status fails the run unless `--allow-errors`.

To put the design beside the build, save a frame (the `url` that
`export_images` returns, with `curl -o`) and pass it with
`--reference figma/checkout.png`: it goes first on the contact sheet.

For a change, shoot the base first into `screenshots/before`, so the two
sets line up file for file.

Then attach the sheet to the closing note by path:

```json
{"attachments": [{"path": "screenshots/after/contact-sheet.png"}]}
```

grain reads it from the sandbox when the call is made (up to 10 MB). It
shows in the task's conversation as a thumbnail that opens full screen,
with the arrow keys paging through every image of the note, and when the
run pushed a branch grain also publishes it into the pull request's
description ([ui-screenshots.md](ui-screenshots.md#handing-them-on)).

Playwright comes from the app's own `node_modules` when it has one;
otherwise `grain screenshot` installs `playwright-core` once into
`~/.cache/grain/screenshot` and uses the browsers the sandbox image
pre-installs in `/var/cache/playwright`.
