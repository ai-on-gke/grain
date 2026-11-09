| shape | task | route | approve | run-now | state after refused run-now |
|---|---|---|---|---|---|
| a | norepo | rest | refused(403) | refused(403) | proposed |
| a | norepo | mcp | refused | refused | proposed |
| a | norepo | cli | refused(exit 3) | refused(exit 3) | proposed |
| a | widgets | rest | ok | ok | (queued) |
| a | widgets | mcp | ok | ok | (queued) |
| a | widgets | cli | ok | ok | (queued) |
| a | other | rest | refused(403) | refused(403) | proposed |
| a | other | mcp | refused | refused | proposed |
| a | other | cli | refused(exit 3) | refused(exit 3) | proposed |
| b | norepo | rest | ok | ok | (queued) |
| b | norepo | mcp | ok | ok | (queued) |
| b | norepo | cli | ok | ok | (queued) |
| b | widgets | rest | ok | ok | (queued) |
| b | widgets | mcp | ok | ok | (queued) |
| b | widgets | cli | ok | ok | (queued) |
| b | other | rest | ok | ok | (queued) |
| b | other | mcp | ok | ok | (queued) |
| b | other | cli | ok | ok | (queued) |
| c | norepo | rest | refused(403) | refused(403) | proposed |
| c | norepo | mcp | refused | refused | proposed |
| c | norepo | cli | refused(exit 3) | refused(exit 3) | proposed |
| c | widgets | rest | refused(403) | refused(403) | proposed |
| c | widgets | mcp | refused | refused | proposed |
| c | widgets | cli | refused(exit 3) | refused(exit 3) | proposed |
| c | other | rest | refused(403) | refused(403) | proposed |
| c | other | mcp | refused | refused | proposed |
| c | other | cli | refused(exit 3) | refused(exit 3) | proposed |
