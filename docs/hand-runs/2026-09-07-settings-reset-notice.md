# Hand run: a settings reset on all three surfaces

`docs/test-plans/2026-09-06.md` section 6. What the plan asked for: on a
throwaway deployment whose state repository is on real GitHub, warn on a
pull request that drops a column, merge it and watch the journal line and
the pull-request comment, then make the comment undeliverable *for a real
reason* and watch the State pane carry what nobody could be told --
dismiss it, and confirm the dismissal survives a reload and a daemon
restart. Then merge a file that omits a dozen columns and read the capped
comment as a person rather than as a length assertion.

All four steps were run, against a real `grain daemon` on a throwaway
deployment, with real git and a real state repository. The refusals in
step 3 came from real `api.github.com`. The one thing that could not be
real is the *accepting* GitHub, for the reason in the next section.

**Two sittings.** Sections 1 to 4 and "What GitHub actually said" are the
run of 2026-09-07. Section 5 is a second sitting on 2026-09-08
(grain/task-319), which reached the 200-value cap that the first one ran
out of time for; it is written here rather than in a document of its own
because it is the same plan section on the same harness, and that harness
is now `docs/hand-runs/2026-09-07-settings-reset-notice/` rather than a
recipe to retype. The second sitting still had no GitHub credential --
"Still owed" at the end says what that leaves, and it is the same list
less item 4.

## What was real, and what was stood in for

**No GitHub credential.** This sandbox reaches `github.com` and can be
told anything by it, but it holds no credential that may write there: its
only git credential is for the local mirror this repository is cloned
from, and the deployment that dispatched the run keeps its own GitHub
credential behind a git proxy rather than handing it to the run. So no
throwaway repository could be created on GitHub, and no comment could
actually be posted to one. Everything below says which of the two halves
each finding came from.

**The deployment.** Real, and built from the shipped binary
(`go build ./cmd/grain`), not from a test harness:

    git init --bare /tmp/hr/grain-handrun/state.git
    git daemon --base-path=/tmp/hr --export-all --enable=receive-pack \
        --reuseaddr --listen=127.0.0.1 --detach
    echo "127.0.0.1 github.com" >> /etc/hosts     # git only; see below
    grain state -data-dir /tmp/hr/data adopt \
        -remote git://github.com/grain-handrun/state.git
    grain daemon -data-dir /tmp/hr/data -ui-addr 127.0.0.1:8420 \
        -sandbox-dir /tmp/hr/sandboxes

That remote is the whole trick, and it is worth spelling out because it
is what let the run split "GitHub" in two. `remoteHost` reads the host
out of the settings remote, `githubRepoFromRemote` answers only for
`github.com`, and `github.NewRealTransport` then maps that host to
`api.github.com` (`github.APIHost`). So a remote on `github.com` served
by a `git daemon` on this machine gives a deployment whose *git* is
entirely local and whose *REST calls go wherever `api.github.com`
resolves* -- which for the refusals below was the real one, untouched, and
for the accepted comment was a seventy-line stand-in on `127.0.0.1:443`,
serving a certificate the daemon was pointed at with `SSL_CERT_FILE`.

Everything grain does here is therefore real: a real merge into a real
repository, taken up by a real daemon on its own 30-second sync tick, a
real journal, the real REST client, the real marker file, the real pane
and rail API, the real CLI. What is simulated is one side of one
conversation, and only in the steps where GitHub had to say yes.

## 1. The check on the pull request

`grain state check` on a clone with `time_zone` deleted from
`tables/grain_config.json`:

    warning: tables/grain_config.json row 0 does not carry "time_zone", which
    grain_config has; grain_config is a settings table, so applying this
    replaces the whole row and that column lands on its default
    'America/Los_Angeles' rather than on whatever the running database holds
    schema:  16, which is what this build of grain knows
    loaded:  2 rows across 2 tables
    ok: grain can load this

As the plan expected: the column named, and the default it would land
on. Two things a reader of the plan should know about how that arrives on
a pull request.

**It is a warning, and the check passes.** Exit status 0. On a real state
repository the CI step is green and the sentence above is in the job's
log, which nobody opens for a green check. That is the right call --
omitting a column is legal and sometimes deliberate -- but "warns on the
pull request" means "is in the log of a check that passed", and the
comment this feature posts afterwards is, in practice, the first thing
anybody actually reads about the reset.

**Some columns cannot be omitted at all.** A first attempt at step 4
dropped 21 columns and the check *failed*:

    grain state: staterepo: inserting grain_config row 0: constraint failed:
    NOT NULL constraint failed: grain_config.poll_interval_ms (1299)

`poll_interval_ms`, `gemini_model` and `max_agent_turns` are NOT NULL
with no usable default, so a file that leaves one out cannot be imported
and the check refuses the change instead of warning about it. Worth
knowing, because the warning printed immediately above that failure says
the column "lands on its default NULL" -- a promise the schema then
breaks. Nothing is lost by it (the file is refused, in CI, before any
deployment sees it) but the two sentences disagree, and the check is
where somebody looks first.

## 2. The merge that got through

The stand-in `api.github.com` accepting, a merge that drops `time_zone`
from a deployment running `Europe/London`. Within one sync tick, the
journal:

    staterepo: the settings that just arrived did not mention 1 value this
    deployment was running on, and a settings file replaces whole rows, so it
    has gone back to the schema's default: tables/grain_config.json (id = 1):
    time_zone was 'Europe/London' and is now 'America/Los_Angeles'. If that
    was not meant, add the column back to the file with the value you want and
    merge that; the next export writes whatever the database holds back out
    either way.
    grain: said so on grain-handrun/state#7, where c54dc15bb960 was merged

The value that was lost is named, not just the default -- which is the
half `grain state check` cannot know and the whole reason this exists.
The comment arrived on the merged pull request as one `POST
/repos/grain-handrun/state/issues/7/comments`, 1103 bytes, and reads:

> **Merging this reset 1 settings value on the deployment.**
>
> grain replaces whole rows when it takes up a settings change, so a file
> that does not mention a column does not leave that column alone -- the
> column lands on the schema's default. [...] this is what it read in the
> moment before c54dc15bb960 replaced it:
>
>     tables/grain_config.json (id = 1): time_zone was 'Europe/London' and is now 'America/Los_Angeles'

And the third surface stayed quiet, which is the part of step 2 that is
easiest to forget to check: `GET /api/state-repo` reported no
`resetNotices`, `GET /api/config` no `stateRepoResetNotice`, and `grain
state status` had no `resets:` line. A delivered notice leaves nothing
behind on the host.

## 3. The notice nobody could be delivered

This is the step the plan said to insist on, and the only one no test can
produce honestly. It was run twice, against two different real refusals.

**A revoked credential, refused by real GitHub.** With
`api.github.com` resolving to the real thing and the deployment's
`tokenFile` holding a credential GitHub does not accept, the merge
applied and the notice did not get out:

    staterepo: the settings that just arrived did not mention 1 value ...
    time_zone was 'Europe/London' and is now 'America/Los_Angeles'. ...
    grain: cannot tell which pull request eb71764c9953 brought into
    grain-handrun/state (GitHub API error 401: "{\r\n  \"message\": \"Bad
    credentials\",\r\n  \"documentation_url\":
    \"https://docs.github.com/rest\",\r\n  \"status\": \"401\"\r\n}"), so the
    notice above about what it replaced has not been posted there

That is real GitHub's own 401, reaching grain's own code, on a merge that
really did cost this deployment its time zone. The pane carried it
(`GET /api/state-repo`):

```json
{
  "at": "2026-09-07T17:05:16.076031364Z",
  "commit": "eb71764c99535742133a99ad8c1ecf299b0c1f25",
  "reason": "grain could not tell which pull request brought eb71764c9953 into grain-handrun/state (GitHub API error 401: \"{\\r\\n  \\\"message\\\": \\\"Bad credentials\\\", ...}\"), so it could not post this there",
  "failed": true,
  "values": [
    { "path": "tables/grain_config.json", "column": "time_zone", "row": "id = 1",
      "from": "'Europe/London'", "to": "'America/Los_Angeles'" }
  ]
}
```

`GET /api/config` answered `"stateRepoResetNotice": true`, which is the
dot on the rail, and `grain state status` printed the same notice with
the same sentence and pointed at both ways to clear it. Three surfaces,
one rendering of one reason.

**Dismissal, a reload and a restart.** `POST
/api/state-repo/reset-notice` -- the pane's button -- emptied
`resetNotices` in the same response, `stateRepoResetNotice` went away
with it, and the marker file under the state repository's `.git` was
gone. Re-reading both endpoints (a reload) showed nothing. The daemon was
then stopped and started again: still nothing, on the pane, on the rail
and in `grain state status`. It is a deleted file, so this could hardly
have gone otherwise, but the plan asked for it to be watched and it was.

`grain state resets clear` -- the same button at a terminal -- was run
against a later notice and behaves the same, printing what it is about to
forget before forgetting it. One wrinkle, observed: the CLI edits the
marker file behind a running daemon's back, and the rail's dot is served
from a flag the daemon refreshes on its own sync tick or when the pane is
read. So for up to thirty seconds after a terminal dismissal,
`/api/config` still says `stateRepoResetNotice: true` while
`/api/state-repo` already says there is nothing there -- a dot pointing
at an empty pane. It clears itself, and a browser that polls both is only
briefly confused; nothing here needs a fix on the strength of one run,
but it is the kind of thing only a hand run sees.

**A credential that lost access, which is a 404.** The more likely
version of "the credential can no longer comment" is not a 403 at all.
GitHub hides a repository a credential cannot see behind 404, and it does
so on the exact endpoint this feature calls first. Replayed through the
deployment, byte for byte as captured from `api.github.com` (see below),
the pane and the journal say:

    grain: cannot tell which pull request b31848e406c9 brought into
    grain-handrun/state (GitHub API error 404: "{\r\n  \"message\": \"Not
    Found\",\r\n  \"documentation_url\":
    \"https://docs.github.com/rest/commits/commits#list-pull-requests-associated-with-a-commit\",\r\n
    \"status\": \"404\"\r\n}"), so it could not post this there

The notice is kept and the values are safe, which is what matters. But
read that as the operator who gets it: it says grain could not tell
*which pull request* -- which sounds like a missing pull request, not a
credential that lost its access -- and GitHub's own word for it, `Not
Found`, arrives inside an escaped JSON blob three lines long.
`github.IsPermissionDenied` cannot help here and correctly does not try
(it answers only for 403 bodies beginning "Resource not accessible by"),
so the branch of `stateResetNotice.announce` that says *"Give the
credential issue write access to that repository"* -- the one sentence
that would name the actual remedy -- is not the branch a lost-access
deployment reaches. This is the one finding of the run worth someone's
judgement rather than a patch; it is filed as a follow-up rather than
guessed at here.

## 4. Eighteen columns at once

A config row carrying eighteen non-default values -- worker counts,
sandbox shape, both `by_default` flags, three model names, a 543-character
prompt extension, the time zone -- merged against a file that omits every
one of them, as a settings file branched before an upgrade would. (The
plan says a dozen; eighteen is every column of `grain_config` that *can*
be omitted, the three NOT NULL ones from step 1 aside.)

One comment, 2505 bytes, one fenced block, one line per value, in the
order the tables are walked. Read as a person: it is readable. The
`was X and is now Y` shape does most of the work, the file path repeated
down the left is dull but is what makes the block safe to paste database
values into, and eighteen lines is comfortably scannable. The one line
that tests its own limits is the prompt extension:

    tables/grain_config.json (id = 1): prompt_extension was 'Answer in the
    deployment''s own voice, and when a task touches the settings tables say
    which columns it read and which ... (547 characters) and is now ''

`abbreviate` cut it at 120 characters and said how long the whole was,
which is exactly right for a comment and is the cap a reader actually
meets. Two cosmetic things about it, neither worth a change on its own:
the cut value keeps its opening `'` and loses its closing one, so it
reads as an unterminated literal; and the count (547) is of the SQL
literal, doubled apostrophes and quotes included, rather than of the 543
characters the operator typed.

**The 200-value cap was not reached**, because `resetCommentValues` is
200 and one `grain_config` row can only ever lose 18. What this sitting
read by eye is the per-value cap, which is the one a comment of ordinary
size actually shows; the next section is the other one.

## 5. The cap, on a deployment big enough to reach it

The second sitting, 2026-09-08, on the same sim harness: 70 repo configs
carrying a `prompt_extension` and a `setup_command` each, and 20
schedules carrying a `base`, an `every_n_hours`, a `time_of_day_minutes`,
an `overlap` and a `last_run_at` each. 240 values, seeded by one merge
and taken away by a second -- which is what a settings file branched
before an upgrade does to a deployment that has been configured for a
while, and the only shape of merge that can pass `resetCommentValues`.
Both merges, the files and the four minutes it takes are in
`2026-09-07-settings-reset-notice/`; it was run twice, from those files,
and produced the same comment to the byte.

**The check warns seven times about 240 values.** `grain state check` on
the stripping commit prints one warning per column and names only the
first row that is missing it -- two for `repo_config`, five for
`schedule` -- and exits 0. That is deliberate and `settingsResets` says
so ("the news is the column"), and it is right for a reviewer. It is
worth knowing what it means on the pull request, though: the green
check's log says seven lines about row 0, and the number 240 appears
nowhere until the deployment has already applied it.

**The journal takes all 240, on one line.** 34,995 characters of it,
values separated by `; `. Nothing truncates there, which is what the
comment's tail promises, so the promise is kept -- but a host whose log
is read through anything line-oriented has one 35KB line to deal with.

**The comment is 31,605 bytes**: 219 lines, 200 of them values, sent as
a 32,535-byte POST. `resetCommentValues`' doc comment predicts "roughly
30KB" and less than half of GitHub's 65,536, and both hold. The first
line says 240, which is the count of the whole rather than of what is
shown, and the last line inside the fence is

    ... and 40 more, in the deployment's journal

`capped-comment.txt` in that directory is the whole of it with four
stretches cut. It was composed by a real deployment out of a real
database and posted with the real REST client; what accepted it was the
stand-in again, as in section 2. Four things in it are worth more than
"the cap works":

**The 40 that are cut are not a random 40.** Values are gathered in
`SettingsTables` order, so the cut falls wherever 200 lands in that list
-- here, after all 140 `repo_config` values and 60 of the 100 `schedule`
ones. `grain_config` and `repo_config` are at the top of that list and
`schedule`, `board` and `suite` are near the bottom, so on any merge big
enough to be cut it is always the same tables that lose their notice.
That is not obviously the wrong answer -- the top of the list is roughly
the order of blast radius, and something has to be cut -- but "and 40
more" reads like a sample and is not one, and a deployment whose loss was
all in `schedule` could have its whole notice cut while a one-column
`grain_config` change is never cut at all.

**A datetime is named in a notation the file will not take back.** The
seeded `last_run_at` was `2026-09-05T01:00:00Z`, which is how the export
writes it and how it must be written back. The comment says:

    tables/schedule.json (id = 'handrun:nightly-11'): last_run_at was '2026-09-05 01:00:00 +0000 UTC' and is now NULL

`literal` renders strings and NULL as SQLite spells them and everything
else with `fmt.Sprint`, and a DATETIME arrives from the driver as a
`time.Time`, so it lands on Go's default layout. Every other line in the
comment can be pasted back into the file it names; this one cannot, and
the comment's own closing paragraph is an instruction to paste them back.
Filed as a follow-up rather than patched from a hand run, since the fix
is in `pkg/staterepo` and wants a test beside `resets_test.go`.

**The fence holds against values written to break it.** Three of the
seeded prompt extensions carry `**bold**`, pipes, `` `backticks` ``,
`[slow]` in brackets and -- the one that matters -- a triple backtick of
its own:

    tables/repo_config.json (owner = 'grain-handrun', name = 'service-11'): prompt_extension was 'End every summary with a fenced block: ``` ok ``` so the reviewer can copy it.' and is now ''

`abbreviate` had already flattened that value's newlines to spaces, so
its ``` sits mid-line, and a fence only closes on a ``` at the start of
one. Every line begins with `tables/`, which is exactly the argument
`resetComment`'s doc comment makes for the fence. So the argument is
sound as far as the bytes go -- and it is still bytes: nobody has watched
GitHub render this, which is item 1 of what is still owed, and this
comment is now the one worth rendering.

**The host keeps nothing, at 240 as at 1.** `GET /api/state-repo` came
back with no `resetNotices` key at all, `/api/config` with no
`stateRepoResetNotice`, and `grain state status` with no `resets:` line.
Section 2 checked that for a single value; it holds for a comment forty
times the size.

## What GitHub actually said

The wording the code has to keep recognising, captured from
`api.github.com` on 2026-09-07 at the two endpoints
`stateResetNotice.announce` uses. Verbatim, including the `\r\n` and the
fields grain's own error rendering carries through:

`POST /repos/{owner}/{repo}/issues/{n}/comments`, no credential at all:

```json
{
  "message": "Requires authentication",
  "documentation_url": "https://docs.github.com/rest",
  "status": "401"
}
```

The same, and `GET /repos/{owner}/{repo}/commits/{sha}/pulls`, with a
credential GitHub does not accept -- the revoked-credential case, and the
one the deployment above met for real:

```json
{
  "message": "Bad credentials",
  "documentation_url": "https://docs.github.com/rest",
  "status": "401"
}
```

`GET /repos/{owner}/{repo}/commits/{sha}/pulls` for a repository the
caller cannot see -- the lost-access case, 163 bytes, and note that the
`documentation_url` is the *endpoint's* own, not the generic one, which
is the only thing in the body that says which call was refused:

```json
{
  "message": "Not Found",
  "documentation_url": "https://docs.github.com/rest/commits/commits#list-pull-requests-associated-with-a-commit",
  "status": "404"
}
```

Those three bodies are now pinned in `pkg/github/github_test.go`
(`TestIsPermissionDeniedOnBodiesGitHubActuallySent`), so a later change to
that predicate is measured against text GitHub really sent rather than
against text somebody wrote from memory. All three are correctly *not*
permission denials: 401 is a credential to replace and 404 is access to
restore, and neither is the "a permission this credential can never hold"
that predicate exists to name.

Not captured, and still owed: the 403s. `Resource not accessible by
integration` and `Resource not accessible by personal access token` are
the two wordings `IsPermissionDenied` actually keys on, and provoking
either needs a credential that is valid and insufficient -- which is
precisely what this sandbox does not have. Same for a locked
conversation, which is a third wording again -- GitHub is documented to
answer 403 with `Unable to create comment because issue is locked.`,
which this predicate would not match and should not, but that sentence is
from the documentation and not from anything seen here -- and for a rate
limit, which was not provoked deliberately against a shared endpoint.

## Still owed

Item 4 was done on 2026-09-08 and is section 5 above. The other three
still need a GitHub credential that may write somewhere, which neither
sitting had: the second one asked for one (`request_secret`) rather than
guessing, so whatever runs next has the token these need and the harness
in `2026-09-07-settings-reset-notice/` to point at a real repository
(`harness.sh live <owner>/<repo> <token-file>`).

1. **A comment posted to real GitHub.** Every accepted comment in both
   sittings was accepted by a stand-in. What has still never happened is
   a deployment's own credential writing one of these under a real merged
   pull request, and the only way to find out that GitHub renders it as
   intended -- the fenced block especially, which is what protects
   database values from Markdown -- is to look at one. Section 5's
   comment is the one to look at: it has the values that were written to
   break a fence in it, and `capped-comment.txt` says what the page
   should say if the fence held.

2. **A 403 from a narrowed credential**, which is the case the code has a
   tailored sentence for and the case its stub tests exercise. A
   fine-grained token with no `issues: write` on the state repository
   would produce it in one merge.

3. **A locked conversation**, the plan's other suggested way to make the
   comment undeliverable. It refuses at `CreateComment` rather than at
   the lookup, so it is the one real refusal that would exercise the
   *second* GitHub call and the generic branch beneath
   `IsPermissionDenied`.

4. ~~**The 200-value cap.**~~ Done: section 5, and
   `seed-settings.py` is the tens of rows it needed.

Items 1 to 3 are one afternoon on a throwaway repository with a
fine-grained token. Two things the second sitting learned that whoever
does them should have to hand: the repository wants to be public, because
"look at it in a browser" against a private one needs a session and not
just a token; and the token has to be a fine-grained one on that
repository alone, since item 2 is *narrowing* it -- taking `issues: write`
off between two merges -- and a classic token's `repo` scope cannot be
narrowed that way.

Two findings of section 5 also want somebody's judgement rather than a
patch from a hand run, and neither needs a credential: the datetime
notation, which is filed as a follow-up, and what the cap cuts, which is
argued out there and left as a question.
