| Identity | Route | Status |
|---|---|---|
| reader | GET /api/config access.scopes has capabilities:admin/settings:write/secrets:write | false/false/false/false |
| reader | GET /api/settings | 200 |
| reader | GET /api/secrets | 200 |
| reader | PUT /api/settings {githubHost} | 403 |
| reader | PUT /api/secrets/reader-probe/token | 403 |
| reader | DELETE /api/secrets/unclaimed-thing/nope | 403 |
| reader | DELETE /api/secrets/reader-probe | 403 |
| writer | GET /api/config access.scopes has capabilities:admin/settings:write/secrets:write | false/false/false/false |
| writer | GET /api/settings | 200 |
| writer | GET /api/secrets | 200 |
| writer | PUT /api/settings {githubHost} | 403 |
| writer | PUT /api/secrets/writer-probe/token | 403 |
| writer | DELETE /api/secrets/unclaimed-thing/nope | 403 |
| writer | DELETE /api/secrets/writer-probe | 403 |
| admin | GET /api/config access.scopes has capabilities:admin/settings:write/secrets:write | true/true/true/true |
| admin | GET /api/settings | 200 |
| admin | GET /api/secrets | 200 |
| admin | PUT /api/settings {githubHost} | 200 |
| admin | PUT /api/secrets/admin-probe/token | 200 |
| admin | DELETE /api/secrets/unclaimed-thing/nope | 404 |
| admin | DELETE /api/secrets/admin-probe | 200 |
| capadmin | GET /api/config access.scopes has capabilities:admin/settings:write/secrets:write | false/true/false/false |
| capadmin | GET /api/settings | 200 |
| capadmin | GET /api/secrets | 200 |
| capadmin | PUT /api/settings {githubHost} | 403 |
| capadmin | PUT /api/secrets/capadmin-probe/token | 403 |
| capadmin | DELETE /api/secrets/unclaimed-thing/nope | 403 |
| capadmin | DELETE /api/secrets/capadmin-probe | 403 |
| secretsonly | GET /api/config access.scopes has capabilities:admin/settings:write/secrets:write | false/false/true/true |
| secretsonly | GET /api/settings | 200 |
| secretsonly | GET /api/secrets | 200 |
| secretsonly | PUT /api/settings {githubHost} | 200 |
| secretsonly | PUT /api/secrets/secretsonly-probe/token | 200 |
| secretsonly | DELETE /api/secrets/unclaimed-thing/nope | 404 |
| secretsonly | DELETE /api/secrets/secretsonly-probe | 200 |
