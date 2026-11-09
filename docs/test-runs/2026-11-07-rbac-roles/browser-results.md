# Browser walkthrough results (item 4)

Generated from browser-walkthrough.mjs results.json. Each visit idled 4s with the page's own polling running, so 403s and toasts include background polls.

### Before the fix (build 840f8af)

| Person (role) | Viewport | Scheme | Pages | 403s | Other 4xx/5xx | Error toasts | Max overflow px | Max box shift px | Tab focus off-screen | Write controls enabled (pages) |
|---|---|---|---|---|---|---|---|---|---|---|
| vic@example.com (grain/viewer) | 1440 | light | 12 | 0 | 0 | 0 | 0 | 0 | 0 | Approve (/inbox) |
| vic@example.com (grain/viewer) | 1440 | dark | 12 | 0 | 0 | 0 | 0 | 0 | 0 | Approve (/inbox) |
| vic@example.com (grain/viewer) | 390 | light | 12 | 0 | 0 | 0 | 0 | 0 | 0 | Approve (/inbox); New task (12) |
| vic@example.com (grain/viewer) | 390 | dark | 12 | 0 | 0 | 0 | 0 | 0 | 0 | Approve (/inbox); New task (12) |
| dana@example.com (grain/contributor) | 1440 | light | 12 | 0 | 0 | 0 | 0 | 0 | 0 | Approve (/inbox); Edit (/tasks/@first); New board (/boards, /boards/@first) |
| dana@example.com (grain/contributor) | 1440 | dark | 12 | 0 | 0 | 0 | 0 | 0 | 0 | Approve (/inbox); Edit (/tasks/@first); New board (/boards, /boards/@first) |
| dana@example.com (grain/contributor) | 390 | light | 12 | 0 | 0 | 0 | 0 | 0 | 0 | Approve (/inbox); Edit (/tasks/@first); New board (/boards, /boards/@first); New task (12) |
| dana@example.com (grain/contributor) | 390 | dark | 12 | 0 | 0 | 0 | 0 | 0 | 0 | Approve (/inbox); Edit (/tasks/@first); New board (/boards, /boards/@first); New task (12) |

### After the fix

| Person (role) | Viewport | Scheme | Pages | 403s | Other 4xx/5xx | Error toasts | Max overflow px | Max box shift px | Tab focus off-screen | Write controls enabled (pages) |
|---|---|---|---|---|---|---|---|---|---|---|
| vic@example.com (grain/viewer) | 1440 | light | 12 | 0 | 0 | 0 | 0 | 0 | 0 | none |
| vic@example.com (grain/viewer) | 1440 | dark | 12 | 0 | 0 | 0 | 0 | 0 | 0 | none |
| vic@example.com (grain/viewer) | 390 | light | 12 | 0 | 0 | 0 | 0 | 0 | 0 | none |
| vic@example.com (grain/viewer) | 390 | dark | 12 | 0 | 0 | 0 | 0 | 0 | 0 | none |
| dana@example.com (grain/contributor) | 1440 | light | 12 | 0 | 0 | 0 | 0 | 0 | 0 | + New task (12); Decline (/inbox); Defer (/tasks/@first, /@bulk, /inbox); Edit (/tasks/@first); New board (/boards, /boards/@first) |
| dana@example.com (grain/contributor) | 1440 | dark | 12 | 0 | 0 | 0 | 0 | 0 | 0 | + New task (12); Decline (/inbox); Defer (/tasks/@first, /@bulk, /inbox); Edit (/tasks/@first); New board (/boards, /boards/@first) |
| dana@example.com (grain/contributor) | 390 | light | 12 | 0 | 0 | 0 | 0 | 0 | 0 | Decline (/inbox); Defer (/tasks/@first, /@bulk, /inbox); Edit (/tasks/@first); New board (/boards, /boards/@first); New task (12) |
| dana@example.com (grain/contributor) | 390 | dark | 12 | 0 | 0 | 0 | 0 | 0 | 0 | Decline (/inbox); Defer (/tasks/@first, /@bulk, /inbox); Edit (/tasks/@first); New board (/boards, /boards/@first); New task (12) |
