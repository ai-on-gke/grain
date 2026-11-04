# Custom agent backends (removed)

Grain used to run a deployment-owned executable as the agent: a **Custom
command** connection (`framework: "custom"`), driven through a versioned
JSON protocol over stdin and stdout. That connection type was removed in
v3.2.0. Grain runs the Antigravity, Claude and Codex frameworks only.

A deployment that still has a custom command connection keeps it, disabled,
until somebody deletes it, and its tasks take the default model selection.
[Connections and models](connections-and-models.md#removed-custom-command-connections)
says what the upgrade does with one. Replace it with an Antigravity, Claude
or Codex connection; a connection's **CLI path** can point that framework at
a build of its CLI of your own.
