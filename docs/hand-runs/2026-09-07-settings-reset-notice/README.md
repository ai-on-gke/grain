# The harness behind 2026-09-07-settings-reset-notice.md

The write-up one directory up describes a deployment standing on a state
repository on `github.com` that is not GitHub, and a merge big enough to
reach the reset comment's 200-value cap. Neither is obvious to rebuild
from prose, and both were needed twice -- once for the run that had no
GitHub credential and once for the run that will have one -- so they are
here as three files rather than as a paragraph.

Nothing builds or runs any of this and none of it is part of grain.

1. `harness.sh` -- stands a throwaway `grain daemon` up on a state
   repository at `github.com/grain-handrun/state`, in either of two
   modes. `harness.sh sim` serves that repository from a `git daemon` on
   this machine and answers `api.github.com` with `standin-api.py`, which
   is the only way to watch grain post a comment without a credential
   that may write one. `harness.sh live <owner>/<repo> <token-file>`
   points the same deployment at a real repository and a real token, and
   touches neither `/etc/hosts` nor the certificate store. `harness.sh
   stop` puts the machine back.

   Its head comment spells out why a remote on `github.com` served
   locally splits GitHub in two, which is the whole trick sim mode rests
   on.

2. `standin-api.py` -- seventy lines of `http.server` behind a
   self-signed certificate on `127.0.0.1:443`, answering exactly the two
   calls `stateResetNotice.announce` makes and writing the comment it is
   sent to a file. Everything else gets 404, so a call nobody expected
   shows up as one rather than as a silent success.

3. `seed-settings.py` -- `seed` writes seventy repo configs and twenty
   schedules carrying values worth losing; `strip` takes those columns
   back out of the same file. Two merges of one clone, and the second one
   costs the deployment 240 values, which is what it takes to get past
   `resetCommentValues`. One `grain_config` row can only ever lose 18.

4. `capped-comment.txt` -- what grain composed for that merge, cut in
   four places that say how many lines they stand for.

The whole of section 4 of the write-up is:

    rm -rf /tmp/hr
    go build -o /tmp/grain ./cmd/grain
    ./harness.sh sim
    cd /tmp/hr/clone
    python3 <this dir>/seed-settings.py seed .
    git commit -am 'Seed the settings a later merge will take away' && git push
    sleep 45                       # the deployment's own tick takes it up
    git pull
    python3 <this dir>/seed-settings.py strip .
    git commit -am 'Drop them, the way a pre-upgrade branch would' && git push
    sleep 45
    grep 'staterepo:' /tmp/hr/daemon.log     # 240 values, on one line
    cat /tmp/hr/standin.log.comment          # 200 of them, and the tail

about four minutes, most of it waiting for two sync ticks. It was run
twice on 2026-09-08, the second time from these files exactly as they
stand, and produced the same 31605-byte comment both times.

Two things about this sandbox that cost the run time and are not
obvious: it has no `ps`, no `pgrep` and no `/bin/kill` (procps is not
installed, and `kill` is only a shell builtin, so `sudo kill` is "command
not found"), which is why the teardown reads `/proc` and hands root a
shell; and a `nohup ... &` from the agent's own `run_command` holds the
pipe open until the tool call times out, so anything long-lived wants
`setsid` and `</dev/null` as it gets here.
