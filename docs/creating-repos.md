# Creating a repository from grain

Settings → Repos has two ways to give the deployment a repository:

- **Add repo** connects a repository that already exists. It adds the
  repository to `targetRepos`.
- **New repository** creates the repository on its forge and connects
  it in the same step. It adds the repository to `targetRepos` and
  writes its `repo_config` row.

Both work through `POST /api/repos/create` (see below), and an agent can
call it only under the dangerous `repos:create` scope
([agent-api.md](agent-api.md)).

## The form

| Field | Meaning |
|---|---|
| Host | The forge. The picker lists only hosts with a registered creator: `github.com` in core, plus whatever a layer above core registers, such as a creator for `*.forge.example` that covers every host of that forge this deployment has a git connection on. |
| Owner / Name | The new repository's `owner/name`. |
| Description | The repository's description on the forge. |
| Visibility | Private (the default) or public. |
| CI mode | The new `repo_config` row's `ci_mode`: `grain`, `github`, `none`, or the deployment default. |
| Template | Optional `owner/repo` to generate the repository from. |
| Add a README | On by default. It creates the default branch, so grain can work in the repository at once. |

The results panel shows one line per step, each marked done, skipped or
failed. A step that could not be done says exactly how to finish it by
hand: a command to run, a settings page to open, or both.

| Step | What it does |
|---|---|
| `create` | Creates the repository. An existing repository is **connected, not refused**: the step is marked skipped and the rest goes ahead. |
| `default-branch` | Waits, for up to 30 seconds, until the default branch exists. |
| `access` | Makes sure the deployment's GitHub App installation reaches the repository. |
| `connect` | Adds the repository to `targetRepos`. An empty `targetRepos` allows every repository, so it is left empty rather than narrowed to this one. |
| `repo-config` | Writes the `repo_config` row: CI mode, default capabilities, and CI reads. |

## Who can create what on github.com

**An org with the App installed and holding Administration: write.**
grain creates the repository with an installation token. GitHub adds a
repository an installation created to that installation's selection, so
no further step is needed.

**An org where the App is missing or lacks the permission.** The create
fails with a message that names what is missing (the installation, or
Administration: write) and links the place to fix it. That is either the
App's install page or the installation's settings page,
`https://github.com/organizations/<org>/settings/installations/<id>`.

**A personal account** (such as `sklodowska-dev`). GitHub refuses to let
an App installation token create a repository under a user
(`POST /user/repos`). grain uses the account's own token instead, if the
secret store holds one under:

```
github-user-token/<login>
```

That token can be a fine-grained personal access token with
Administration: write on the account's repositories, or a GitHub user
OAuth token. Set it with `grain secret set github-user-token/<login>`
or under Settings → Secrets. If the App's installation on that account
is limited to selected repositories, grain also uses this token to add
the new repository to the installation
(`PUT /user/installations/{id}/repositories/{repo_id}`).

Without the token, the create fails with a message that says so. It also
shows the command to create the repository by hand:

```
gh repo create <owner>/<name> --private --add-readme
```

After running it, use **Add repo**, or send the same request again, to
finish connecting the repository.

## The API

`POST /api/repos/create` takes:

| Field | Default | Meaning |
|---|---|---|
| `host` | `github.com` | The forge; `GET /api/repo-hosts` lists the ones a creator is registered for. |
| `owner`, `name` | required | The repository. |
| `visibility` | `private` | `private` or `public`. |
| `description` | | |
| `init` | `true` | Create a README on the default branch. |
| `template` | | `owner/repo` to generate from. |
| `ci_mode` | | `grain`, `github` or `none`, for the `repo_config` row. |
| `default_capabilities` | | Capability ids every task on the repository is filed holding. |
| `reads` | | Repositories (`owner/name`) the repository's grain CI jobs may read, stored as `repo-read:` CI capabilities. |

The answer is `200` with `repo`, `created`, `existed`, `connected`,
`complete` (no step failed), `default_branch` and `steps`. Each step has
`name`, `status` (`done`, `skipped` or `failed`), `detail`, and, where
there is one, `fix`, `command` and `link`. A request that names nothing
creatable is a `400`: a bad owner or name, a host with no creator, or an
unknown `ci_mode`.

## Other forges

A forge plugs in a `repocreate.Creator` with
`repocreate.Register("<host or glob>", creator)` from `init()`, the same
way an ambient git credential source registers
(`gitproxy.RegisterAmbientSource`). The creator only makes the
repository and makes sure the deployment can reach it. Connecting it is
core's, the same for every host. A code host with no repositories to
create, such as a monorepo, registers no creator.
