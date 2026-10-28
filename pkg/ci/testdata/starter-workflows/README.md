# GitHub starter workflow corpus

Unmodified snapshots from [actions/starter-workflows](https://github.com/actions/starter-workflows/tree/e3c451d60f119b71caebf13c98ac45da6e15b4b7), commit `e3c451d60f119b71caebf13c98ac45da6e15b4b7`. The upstream MIT license is included.

| File | Coverage | Current parser expectation |
| --- | --- | --- |
| ci/go.yml | Checkout, setup action, run steps, branch filters | Accepted |
| ci/node.js.yml | Version matrix, expressions, npm cache | Accepted |
| ci/python-package.yml | Matrix, multiline scripts | Accepted |
| ci/docker-publish.yml | Schedule, tags, permissions, registry publishing | Reject unsupported tag filters |
| code-scanning/codeql.yml | Schedule, dynamic runner expression | Reject unsupported runner |
| pages/static.yml | Manual dispatch, environment, artifact actions | Accepted |

Tests replace only GitHub's template placeholders, using explicit replacements in the test. The checked-in source stays unchanged. To refresh, choose one new upstream commit, fetch these same paths and LICENSE from that commit, update the pin here, and review changes in expectations.

These are parser and planning tests, not execution certification. They run offline under `go test ./pkg/ci`; they do not deploy Pages, publish images, obtain credentials, or prove act implements each action. The adjacent Kontur fixture additionally covers schedule + dispatch inputs, dependent jobs, and a specialized runner.

