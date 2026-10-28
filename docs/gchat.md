# Posting to Google Chat from agents (operator how-to)

The built-in `gchat` capability server (`https://grain.dev/capabilities/gchat`, [docs/capabilities.md](capabilities.md#google-chat-gchatpost)) lets a task post messages to Google Chat spaces you allowlist. It is one way: agents cannot read a space, and replies never reach grain. This page covers setting it up. Two terms describe where a credential comes from (spec §4.5.8):

- **configured**: a credential you store in grain's secret store (a webhook URL, a service-account key, a workload-identity config);
- **ambient**: nothing is stored. The server uses the identity its own process already has where it runs. In the container that is the container's Application Default Credentials (ADC) or metadata identity. On a Cloudtop where the server runs on the host through grain hostd (`run: host`), it is your own corp identity.

Open **Settings → Capability servers → gchat → Configure**. Every field below is in that form. The secret fields (`webhook_urls`, `app_credentials`, `dataPlaneToken`) are write-only: they are kept in the secret store, never exported to grain/config, and never shown back.

## Before anything else

- **`grain_url`**: the address people open grain at, such as `https://grain.example.com`. Every message links to `<grain_url>/tasks/<id>`.
- **`dataPlaneToken`**: managed by grain. Grain generates it (32 random bytes) into the secrets store the first time the server is attached and sends it on every tool call to the server's loopback listener (`127.0.0.1:4251`); **Rotate** replaces it. Set a value of at least 32 bytes under **Advanced** only to override it.
- **`display_name`** and **`avatar_url`** (optional): the default name and picture on every message's card header. Each space can override them.

## A space through a webhook (no app approval)

A webhook is the quickest route, and the usual one for an external Workspace space.

1. In Google Chat, open the space, then **Apps & integrations → Webhooks → Add webhook**. The name and avatar you give it here are the **sender name** every message shows. They are fixed when the webhook is created, and grain cannot change them per message. The card header (`display_name`) is what grain controls.
2. Copy the URL (`https://chat.googleapis.com/v1/spaces/…/messages?key=…&token=…`). The URL itself is the credential.
3. Under **spaces**, add `{name: "team-room", transport: "webhook", display_name: "Release bot"}`.
4. In **webhook_urls**, enter a JSON object from space name to URL: `{"team-room": "https://chat.googleapis.com/v1/spaces/…"}`.
5. Save, then press **Test**. The report lists `space team-room: webhook`.
6. The card lists each configured space with its usage. Press **Send test message** on `team-room`: it posts one message marked `[grain test]` and shows the message id Chat created, or Chat's refusal.

## A space through a Chat app

An app space calls the Chat API as a Chat app with the `chat.bot` scope. The message shows the app's own name, and the card header shows `display_name`.

1. Create or choose a Chat app in a Google Cloud project, configure it in the Chat API, and add it to the space. Note the space's resource name, `spaces/AAAA…`.
2. Under **apps**, add the app with a credential source:
   - **configured**: `{name: "grain-bot", credential_source: "configured", project: "my-project"}`, and in **app_credentials** a JSON object from app name to credential: `{"grain-bot": <service-account key JSON, or a workload-identity external_account config>}`. The service account has to be the Chat app's.
   - **ambient**: `{name: "corp-bot", credential_source: "ambient"}`, with nothing in `app_credentials`. The server uses whatever ADC its process has, as described below.
3. Under **spaces**, add `{name: "eng", transport: "app", space: "spaces/AAAA…", app: "grain-bot"}`.
4. Save and **Test**. The report says which identity each app resolved to, for example `app corp-bot (ambient): posts as you@example.com (authorized_user)`, and whether it can post to each of its spaces: `can post to spaces/AAAA…`, or the Chat API's refusal. A user credential (`authorized_user`) calls Chat as that user, so its messages show the user rather than an app. The report says that too.

### Ambient in the container

The server runs as `grain capability gchat` in the daemon's container and uses the container's ADC: `GOOGLE_APPLICATION_CREDENTIALS` in the server entry's `env`, or the metadata server's service account on GCE or GKE.

### Ambient on the host (Google-internal Chat)

On a Cloudtop, run the server on the host so it posts with your own identity. `scripts/setup.sh` (and `google/scripts/cloudtop-install.sh`) writes the hostd allowlist entry `grain-gchat`, with `extra_args: true` so the entry can pass `-listen`. In Settings, edit the gchat entry and set **Run** to `host`, **Command** to `grain-gchat`, and **Args** to `-listen 127.0.0.1:4251`. Make sure your ADC is in place (`gcloud auth application-default login`), then use `credential_source: ambient`. Test reports the identity it found.

## Side by side

One deployment can carry external webhook spaces and internal app spaces together. Each space names its own transport, and each app space names its own app and credential source.

## What each space allows

`allowed` on a space:

| Field | Default | Meaning |
|---|---|---|
| `threads` | `false` | allow `thread: "new"`, and replies into a thread a post returned |
| `max_len` | 2000 | longest message text (at most 4096) |
| `mentions` | `none` | `users` allows `<users/…>` mentions; `all` also allows `@all` |
| `per_hour` | 60 | most messages per hour to the space, across all tasks |

`per_attempt` (default 20) caps what one attempt posts. Each task posts in its own thread in each space. Messages must use Chat formatting (`*bold*`, `_italic_`, `` `code` ``, `<https://url|text>`), and are always stamped with the task and its link.

## Granting

Grant `https://grain.dev/capabilities/gchat#post` with `{space: "team-room"}`. The space picker lists the configured spaces only. Grant once per space. The agent gets a `post_message` tool and a prompt section saying which space it posts to and what that space allows.

## Audit and usage

When an attempt ends, its endpoint is closed. Its posts and refusals, with text, message and thread ids, are returned as that release's audit. The server's `usage` tool reports, per space, how many messages were posted, refused and failed since it started, and how many in the last hour. The server's `test_post` tool posts one message marked `[grain test]` to a space. Settings shows both on the gchat card, one row per configured space: its transport, its counts (`posted`, `refused`, `failed`, and this hour against `per_hour`), the time of its last post, and a **Send test message** button (`GET /api/capability-servers/gchat-usage?id=` and `POST /api/capability-servers/gchat-test` `{id, space}`). A test post counts against the space's hourly limit like any other.

## Live test

`go test ./pkg/capability/intree -run TestGChatLiveWebhook` posts one marked message to a real space when `GRAIN_GCHAT_LIVE_WEBHOOK` holds its webhook URL. Without it, the test is skipped. Keep the URL in the secret store (for example as `gchat-live-webhook`), never in a file.
