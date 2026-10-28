#!/usr/bin/env bash
# codex-surface.sh -- read codex's surface off the installed binary and
# write it down as Markdown: every subcommand and flag it will admit to,
# which of those flags live at the top level and which only under `exec`,
# whether the argv pkg/agent/codex builds is argv this codex actually
# parses, and what its interactive TUI does when it is started the way an
# interactive task starts it.
#
# Why this exists, and why it is not just scripts/agy-surface.sh with a
# different binary name. pkg/agent/codex puts two different argv on the
# command line -- `codex exec ...` for an unattended run, and a bare
# `codex ...` for a session somebody types into (terminal.go) -- and
# nothing in this repository could check either of them. The unattended
# one at least fails loudly the first time a run is dispatched. The
# interactive one is worse off: it is built once, when a human opens a
# console on a task, and a flag codex does not accept *there* is
# "error: unexpected argument" on that person's terminal, on a deployment
# nobody was testing, with the session dead before the first prompt.
#
# That is not hypothetical. The first run of this script is what found
# `--skip-git-repo-check` being passed at the top level, where codex takes
# it under `exec` alone -- a flag derived by reading Run and removing what
# makes it headless, which is the right derivation from the wrong premise
# that the two argv share a flag set.
#
# Usage:
#   scripts/codex-surface.sh [-o docs/codex-surface.md]
#
# The binary is $CODEX or $GRAIN_CODEX_PATH, defaulting to `codex` on
# $PATH -- the same resolution order the daemon's own -codex-path flag
# has, and the same one scripts/setup.sh documents for an operator-named
# copy.
#
# The two rules scripts/agy-surface.sh is written to hold to are the rules
# here as well, for the same reasons, and they are worth repeating because
# they decide what this file does when something goes wrong.
#
# Nothing here may fail the run. Every probe is a question this script
# does not know the answer to; a command that exits non-zero, times out or
# prints nothing records exactly that and the script carries on. Half the
# findings in this document *are* non-zero exits.
#
# And the output has to be stable enough that a diff means drift. Nothing
# run-specific goes in it -- no dates, no temporary paths, no session ids
# -- so two runs against one codex produce identical bytes and a run
# against a new one produces a diff that is entirely about codex.
#
# Unlike agy, codex is pinned: the Dockerfile fetches the release named by
# ARG CODEX_CLI_VERSION, and .github/workflows/codex-surface.yml installs
# that same release rather than the latest. So this capture is *of* a
# named version, the version is written into it, and
# pkg/agent/codex/surface_capture_test.go fails when the pin moves and the
# capture does not -- which is the one thing docs/agy-surface.md can never
# say about itself.

# File-wide, for the reason scripts/agy-surface.sh gives at this same
# spot: this script's output is Markdown emitted by `printf` with
# single-quoted format strings full of backticked identifiers and `$NAME`
# placeholders the scrubbers below write on purpose, and every one of them
# reads to the linter like an expansion someone forgot to quote.
# shellcheck disable=SC2016

# Not -e: see the first rule above. A probe's failure is an answer.
set -uo pipefail

CODEX="${CODEX:-${GRAIN_CODEX_PATH:-codex}}"

# Every codex invocation is wrapped in this. codex has an `app-server`, an
# `mcp-server` and a `remote-control` daemon among its subcommands, and a
# TUI that waits for a keystroke forever; none of them may hang a CI job.
TIMEOUT="${CODEX_SURFACE_TIMEOUT:-60}"

# How long a TUI probe is allowed to draw before it is killed. Longer than
# it sounds: codex plays a startup animation, and the screen this document
# is about is the one after it.
TUI_TIMEOUT="${CODEX_SURFACE_TUI_TIMEOUT:-15}"

# Per-capture caps, so one verbose probe cannot turn a reviewable document
# into a megabyte nobody reads.
MAX_LINES="${CODEX_SURFACE_MAX_LINES:-400}"
MAX_BYTES="${CODEX_SURFACE_MAX_BYTES:-65536}"

# Six backticks, for the reason agy-surface.sh uses them: some of what is
# captured here is itself fenced Markdown.
FENCE='``````'

# Byte-wise sorting and matching everywhere, so the document does not
# depend on the locale of whoever ran it.
export LC_ALL=C

# A placeholder where codex expects an OpenAI key. Nothing in this
# document costs a token: no probe below starts a model turn, and the
# credential exists only because codex's TUI shows its login screen
# instead of the screen this file is about when it cannot find one. It is
# not validated before that screen is past, which is what makes a fake one
# enough.
#
# Not a secret, and deliberately not read from the environment: a real key
# reaching this script would put a real conversation in the document.
API_KEY_PLACEHOLDER='codex-surface-not-a-real-key'

# The model name the flag probes pass to --model. A value, not a claim:
# codex does not resolve a model name while parsing its arguments, so
# what this says is which *flags* parse, never which models exist.
PROBE_MODEL='gpt-5-codex'

# Defaulted rather than assumed: scrub below rewrites $HOME out of every
# capture, and a HOME-less environment would abort under -u instead.
HOME="${HOME:-/root}"

out=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o|--output) out="${2:-}"; shift 2 ;;
    -h|--help)   sed -n '2,35p' "$0"; exit 0 ;;
    *)
      echo "codex-surface.sh: unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if ! command -v "$CODEX" >/dev/null 2>&1 && [[ ! -x "$CODEX" ]]; then
  echo "codex-surface.sh: no codex at '$CODEX'. Set CODEX or GRAIN_CODEX_PATH, or install the release the Dockerfile pins (ARG CODEX_CLI_VERSION) the way .github/workflows/codex-surface.yml does." >&2
  exit 2
fi

probe_root="$(mktemp -d "${TMPDIR:-/tmp}/codex-surface.XXXXXX")"
trap 'rm -rf "$probe_root"' EXIT

repo_root="$(cd "$(dirname "$0")/.." && pwd)"

if [[ -n "$out" ]]; then
  exec >"$out"
fi

# ---------------------------------------------------------------- helpers

# scrub removes everything about *this* run from a capture: terminal
# colour, the throwaway directories the probe ran in, the real HOME codex
# was installed into, the checkout it was run from, and the session ids
# codex stamps on its own output.
#
# It also drops one line codex prints ahead of almost every command --
# the PATH-aliases warning about a CODEX_HOME under a temporary directory.
# Not because it is uninteresting: grain builds exactly such a home for
# every run (`writeConfig` calls os.MkdirTemp("")), so it is a finding,
# and it is captured whole in its own block under "The binary" rather than
# repeated forty times over the top of everything else this file is about.
scrub() {
  sed -e 's/\x1b\[[0-9;?]*[a-zA-Z]//g' \
      -e 's/\r$//' \
      -e '/^WARNING: proceeding, even though we could not create PATH aliases:/d' \
      -e "s#${probe_root}#\$PROBE#g" \
      -e "s#${repo_root}#\$REPO#g" \
      -e "s#${PWD}#\$PWD#g" \
      -e "s#${HOME}#\$HOME#g" \
      -e "s/${API_KEY_PLACEHOLDER}/\$OPENAI_API_KEY/g" \
      -e 's/[0-9a-f]\{8\}-[0-9a-f]\{4\}-[0-9a-f]\{4\}-[0-9a-f]\{4\}-[0-9a-f]\{12\}/$UUID/g'
}

# limit applies the two caps, announcing either in the document.
limit() {
  awk -v max="$MAX_LINES" -v maxb="$MAX_BYTES" '
    NR > max { print "... [truncated at " max " lines by scripts/codex-surface.sh]"; exit }
    {
      bytes += length($0) + 1
      if (bytes > maxb) { print "... [truncated at " maxb " bytes by scripts/codex-surface.sh]"; exit }
      print
    }'
}

# block writes one capture: the command as a reader would type it, its
# output, and its exit status when that status was not zero. An empty
# capture says so, because "printed nothing" and "was never run" have to
# look different in a document read as evidence.
block() {
  local label="$1" status="$2" output="$3"
  while [[ "$output" == *$'\n' ]]; do output="${output%$'\n'}"; done
  printf '%sconsole\n$ %s\n' "$FENCE" "$label"
  if [[ -n "$output" ]]; then
    printf '%s\n' "$output" | scrub | limit
  else
    printf '[no output]\n'
  fi
  if [[ "$status" -ne 0 ]]; then
    printf '[exit %s]\n' "$status"
  fi
  printf '%s\n\n' "$FENCE"
}

# codex_run runs codex with a given CODEX_HOME and working directory, and
# leaves its combined output in codex_output and its status in
# codex_status. Globals rather than a captured stdout because `x="$(f)"`
# runs f in a subshell, where the exit status of the binary -- half of
# what this script is recording -- would be lost.
#
# TERM=dumb is on every one of these deliberately: it is what keeps a
# probe from starting the TUI and waiting for a keystroke that is never
# coming. The interactive section below is the one place that undoes it,
# with a pty of its own.
codex_output=""
codex_status=0
codex_run() {
  local home="$1" dir="$2"; shift 2
  codex_output="$(cd "$dir" && CODEX_HOME="$home" NO_COLOR=1 TERM=dumb CI=1 \
    OPENAI_API_KEY="$API_KEY_PLACEHOLDER" \
    timeout "$TIMEOUT" "$CODEX" "$@" 2>&1)"
  codex_status=$?
}

# codex_capture is the pair of them: run, and write the block.
codex_capture() {
  local home="$1" dir="$2"; shift 2
  codex_run "$home" "$dir" "$@"
  block "codex $*" "$codex_status" "$codex_output"
}

# fresh_home is a CODEX_HOME codex has never seen: no config, no auth, no
# recorded sessions.
#
# Numbered rather than mktemp'd, here and in the two directory helpers
# below, and that is about the second rule this script is written to: a
# random suffix survives `scrub` (which only knows the root it sits under)
# and lands in the document, where two runs against one codex then differ
# in a dozen places that are about nothing. `$PROBE/home-3` is the same
# directory in every capture.
probe_seq=0
probe_dir() {
  local dir
  probe_seq=$((probe_seq + 1))
  dir="$probe_root/$1-$probe_seq"
  mkdir -p "$dir"
  printf '%s' "$dir"
}

fresh_home() {
  probe_dir home
}

# grain_config is the config.toml pkg/agent/codex writes for one run, as
# `configTOML` builds it -- a copy, and the only copy in this script,
# named here so a reader can diff it against that function by eye. The
# MCP command is /bin/true rather than the grain binary: what is being
# measured is whether codex loads and accepts the *shape* of what grain
# writes, and a server that answers nothing still proves that much
# without this script needing a grain build.
grain_config() {
  cat <<'TOML'
# Written by grain for one run; see pkg/agent/codex.
approval_policy = "never"
sandbox_mode = "read-only"

[features]
code_mode_host = false

[mcp_servers.grain]
command = "/bin/true"
args = []
TOML
}

# grain_home is a CODEX_HOME holding that config, plus the credential the
# TUI probes need. auth.json is codex's own store for an API key; the
# daemon passes OPENAI_API_KEY in the environment instead, which is
# enough for `exec` and not enough to get past the TUI's login screen.
grain_home() {
  local home
  home="$(fresh_home)"
  grain_config >"$home/config.toml"
  printf '{"OPENAI_API_KEY":"%s"}\n' "$API_KEY_PLACEHOLDER" >"$home/auth.json"
  printf '%s' "$home"
}

# a_repo and not_a_repo are the two working directories every question
# about the git-repository check needs, since the whole point of
# --skip-git-repo-check is what codex does in the second one. A granule run
# has no local directory at all and leaves codex started in whatever the
# daemon's own working directory happens to be, which is the not-a-repo
# case in production.
a_repo() {
  local dir
  dir="$(probe_dir repo)"
  git -C "$dir" init -q >/dev/null 2>&1
  printf '%s' "$dir"
}

not_a_repo() {
  probe_dir plain
}

# subcommands_of pulls command names out of a clap `--help` listing: the
# rows indented exactly two spaces under a "Commands:" heading.
#
# Exactly two, which is stricter than agy's version of this function and
# has to be: clap wraps a long description onto continuation lines that
# are indented much further and start with a word, so a looser match reads
# "working tree [aliases: a]" as a subcommand named `working` and captures
# `codex working --help` under a heading claiming codex has one.
subcommands_of() {
  awk '
    /^[A-Za-z ]*[Cc]ommands:/ { inside = 1; next }
    /^[^[:space:]]/           { inside = 0 }
    inside && substr($0, 1, 3) ~ /^  [a-z]/ {
      line = $0
      sub(/^[[:space:]]+/, "", line)
      split(line, fields, /[[:space:]]+/)
      print fields[1]
    }' | sort -u
}

# flags_of pulls long-flag names out of the same listing: anything spelled
# --like-this in the options section, including the long half of a `-m,
# --model` pair.
flags_of() {
  grep -oE -- '--[a-z][a-z0-9-]*' | sort -u
}

# probe_flag asks the only question this document exists to answer for a
# given flag: does codex accept it *here*, at this position on the command
# line?
#
# `--help` is appended so that nothing runs. clap parses the whole argv
# before it acts on --help, so an argument it does not recognize is still
# an error and a recognized one still exits 0 -- which makes this a parse
# check that costs no model turn, no credential and no session file.
#
# What it does not check is the value: codex resolves neither a model name
# nor a path while parsing, so `accepted` means the flag is spelled and
# positioned correctly and nothing more.
flag_verdicts=""
probe_flag() {
  local position="$1" flag="$2" value="${3:-}"
  local home dir argv=() verdict detail
  home="$(fresh_home)"
  dir="$(not_a_repo)"
  [[ "$position" == "exec" ]] && argv+=("exec")
  argv+=("$flag")
  [[ -n "$value" ]] && argv+=("$value")
  argv+=("--help")

  codex_run "$home" "$dir" "${argv[@]}"
  if [[ "$codex_status" -eq 0 ]]; then
    verdict="accepted"
    detail=""
  else
    verdict="rejected"
    # codex's own first word about it, which is what somebody opening a
    # session would see on their terminal.
    detail="$(printf '%s\n' "$codex_output" | scrub | grep -m1 -i 'error' || true)"
  fi
  # Trailing space trimmed, so an accepted row is not a line of padding
  # that some editor strips on the next capture and calls drift.
  flag_verdicts+="$(printf '%-9s %-10s %-24s %s' "$verdict" "$position" "$flag" "$detail" | sed -e 's/[[:space:]]*$//')"$'\n'
}

# probe_argv is probe_flag for a whole command line: the argv
# pkg/agent/codex assembles, run as one thing, since a flag set that
# parses one at a time can still be refused together. `--help` is
# appended for the same reason and the same effect, and what is recorded
# is the verdict alone -- the help text it prints on success is already
# captured in full above.
argv_verdicts=""
probe_argv() {
  local label="$1"; shift
  local home dir verdict detail
  home="$(grain_home)"
  dir="$(not_a_repo)"
  codex_run "$home" "$dir" "$@" --help
  if [[ "$codex_status" -eq 0 ]]; then
    verdict="accepted"
    detail=""
  else
    verdict="rejected"
    detail="$(printf '%s\n' "$codex_output" | scrub | grep -m1 -i 'error' || true)"
  fi
  argv_verdicts+="$(printf '%-9s codex %s --help' "$verdict" "$*")"$'\n'
  argv_verdicts+="$(printf '          %s%s%s' "$label" "${detail:+ -- }" "$detail")"$'\n'
}

# tui_fragments starts codex's real terminal interface on a pty and
# reports what it drew.
#
# A pty and not a pipe, because codex refuses outright without one (that
# refusal is itself captured below), and python3 rather than `script`
# because the window size has to be set: a TUI given no dimensions draws
# nothing this document could quote.
#
# What comes back is a word list rather than a screen. codex positions
# every run of text with its own escape sequences, so stripping those
# leaves fragments rather than lines; they are sorted and de-duplicated,
# which also makes the capture stable across a startup animation that is
# never in the same place twice. It is enough to answer the question being
# asked here -- is codex asking this person something, and about what --
# and it is not a transcript of the screen.
tui_fragments() {
  local home="$1" dir="$2" raw
  raw="$probe_root/tui.$$.raw"
  if ! command -v python3 >/dev/null 2>&1; then
    printf '[no python3 on this machine, so the TUI was never started; this probe needs a pty]\n'
    return
  fi
  CODEX_TUI_DIR="$dir" CODEX_HOME="$home" TERM=xterm-256color NO_COLOR=1 \
    CODEX_TUI_TIMEOUT="$TUI_TIMEOUT" \
    python3 - "$CODEX" >"$raw" 2>&1 <<'PY'
import fcntl, os, pty, select, signal, struct, sys, termios, time

codex = sys.argv[1]
pid, fd = pty.fork()
if pid == 0:
    os.chdir(os.environ["CODEX_TUI_DIR"])
    # --no-alt-screen so the frames are written to the pty as ordinary
    # output instead of to an alternate screen this probe would have to
    # emulate to read.
    os.execvp(codex, [codex, "--no-alt-screen"])

fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 120, 0, 0))
deadline = time.time() + float(os.environ["CODEX_TUI_TIMEOUT"])
while time.time() < deadline:
    ready, _, _ = select.select([fd], [], [], 0.5)
    if not ready:
        continue
    try:
        data = os.read(fd, 65536)
    except OSError:
        break
    if not data:
        break
    sys.stdout.buffer.write(data)
sys.stdout.buffer.flush()
os.kill(pid, signal.SIGKILL)
os.waitpid(pid, 0)
PY
  # Escape sequences to newlines rather than to nothing: two words the TUI
  # drew in different places must not be joined into a third that was
  # never on the screen.
  sed -e 's/\x1b\[[0-9;?]*[a-zA-Z]/\n/g' \
      -e 's/\x1b\][^\x07\x1b]*\(\x07\|\x1b\\\)/\n/g' \
      -e 's/\x1b[()][A-Z0-9]/\n/g' \
      -e 's/\r/\n/g' "$raw" \
    | tr -d '\000' \
    | grep -oE "[A-Za-z][A-Za-z0-9,.:/@?!()'-]*( [A-Za-z0-9,.:/@?!()'-]+){0,12}" \
    | grep -E '[A-Za-z]{3}' \
    | scrub \
    | sed -e "s|${probe_root#/}|\$PROBE|g" \
    | sort -u \
    | limit
  rm -f "$raw"
}

# in_binary greps the binary's own string table, the way agy-surface.sh
# does and for the same reason: codex is a stripped release build, but a
# Rust binary still carries every serde field name and every literal path
# its own code names.
binary_path=""
strings_file=""
in_binary() {
  [[ -n "$binary_path" ]] || return 0
  if [[ -z "$strings_file" ]]; then
    strings_file="$probe_root/strings.txt"
    strings -n 4 -- "$binary_path" >"$strings_file" 2>/dev/null
  fi
  grep -aoE "$1" "$strings_file" | sort -u
}

# ----------------------------------------------------------------- header

cat <<'MARKDOWN'
<!-- Generated by scripts/codex-surface.sh. Do not edit by hand: the next
     dispatch of .github/workflows/codex-surface.yml overwrites it. -->

# codex's surface, read off the binary

Everything the pinned `codex` will say about itself, captured by
`scripts/codex-surface.sh` and regenerated on demand by
`.github/workflows/codex-surface.yml` (`workflow_dispatch` only, no
schedule, no credential). It is `docs/agy-surface.md`'s counterpart for
the other CLI `pkg/agent` drives, and it exists for one question that
document cannot answer: which flags codex takes at the top level, and
which it takes only under `exec`.

That question has two answers in this repository, because `pkg/agent/codex`
builds two different command lines -- `codex exec --json
--skip-git-repo-check ...` for an unattended run, and a bare `codex ...`
for an interactive session somebody types into (`terminal.go`) -- and
until this capture existed, the second one was asserted against no binary
at all. The first run of this script is what found `--skip-git-repo-check`
on the wrong side of that line.

Read it as evidence rather than as documentation: every section below is a
command and its output, the commands that failed included. A probe that
stopped answering is itself a finding.

Unlike agy, codex is pinned here: the Dockerfile names a release
(`ARG CODEX_CLI_VERSION`), the workflow installs that release rather than
the latest, and `pkg/agent/codex/surface_capture_test.go` fails when the
pin moves and this file does not -- so "the capture is old" is a question
this document can answer about itself.

MARKDOWN

# ------------------------------------------------------------ the binary

printf '## The binary\n\n'

codex_run "$(fresh_home)" "$(not_a_repo)" --version
# Read out of this one raw capture, before `scrub` takes it out of every
# block in the file, and written down under its own heading below.
path_alias_warning="$(printf '%s\n' "$codex_output" \
  | sed -e 's/\x1b\[[0-9;?]*[a-zA-Z]//g' -e "s#${probe_root}#\$PROBE#g" \
  | grep -m1 '^WARNING: proceeding' || true)"
block "codex --version" "$codex_status" "$codex_output"

version_line="$(printf '%s\n' "$codex_output" | scrub | grep -m1 . || true)"

# The install puts the binary wherever the release archive unpacked it
# (the Dockerfile and both workflows *find* it rather than assuming a
# path), so resolve it: the string-table reads below need the real file,
# and its digest is what says "the same binary as last time" on a day the
# version string alone would not.
resolved="$(command -v "$CODEX" 2>/dev/null || printf '%s' "$CODEX")"
binary_path="$(readlink -f "$resolved" 2>/dev/null || printf '%s' "$resolved")"
[[ -r "$binary_path" ]] || binary_path=""

printf '%sconsole\n' "$FENCE"
if [[ -n "$binary_path" ]]; then
  printf 'path   %s\n' "$(printf '%s' "$binary_path" | scrub)"
  printf 'size   %s bytes\n' "$(stat -c %s "$binary_path" 2>/dev/null || echo unknown)"
  printf 'sha256 %s\n' "$(sha256sum "$binary_path" 2>/dev/null | cut -d' ' -f1)"
else
  printf '[the binary could not be resolved or read; the string-table section below is empty]\n'
fi
printf '%s\n\n' "$FENCE"

# The pin, and the two lines pkg/agent/codex/surface_capture_test.go reads
# to decide whether this whole file is about the codex an image would
# carry. Spelled as a fixed pair of labelled lines rather than left for a
# test to find in prose.
printf 'The release this capture is of, against the release the image carries:\n\n'
pinned="[not read: no Dockerfile at \$REPO/Dockerfile]"
if [[ -r "$repo_root/Dockerfile" ]]; then
  pinned="$(sed -n 's/^ARG CODEX_CLI_VERSION=//p' "$repo_root/Dockerfile" | head -n1)"
  [[ -n "$pinned" ]] || pinned="[not read: no ARG CODEX_CLI_VERSION in \$REPO/Dockerfile]"
fi
printf '%sconsole\n' "$FENCE"
printf 'pinned by the Dockerfile   %s\n' "$pinned"
printf 'captured here              %s\n' "${version_line:-[codex --version printed nothing]}"
printf '%s\n\n' "$FENCE"

# The one line every other capture in this file has had removed from it
# (see `scrub`), kept once and here, because it is about how grain runs
# codex rather than about any one probe.
printf 'And one line codex prints ahead of almost everything, which every capture below has had removed from it and which is worth reading once. Every probe in this document runs with a `CODEX_HOME` under `/tmp`, because that is what `writeConfig` gives a real run -- `os.MkdirTemp("")` -- so this is what a grain run gets too:\n\n'
printf '%sconsole\n' "$FENCE"
if [[ -n "$path_alias_warning" ]]; then
  printf '%s\n' "$path_alias_warning"
else
  printf '[this codex printed no such warning]\n'
fi
printf '%s\n\n' "$FENCE"

# ---------------------------------------------------- commands and flags

printf '## Commands and flags\n\n'

help_home="$(fresh_home)"
help_dir="$(not_a_repo)"
codex_run "$help_home" "$help_dir" --help
top_help="$(printf '%s\n' "$codex_output" | scrub)"
block "codex --help" "$codex_status" "$codex_output"

codex_run "$help_home" "$help_dir" exec --help
exec_help="$(printf '%s\n' "$codex_output" | scrub)"
block "codex exec --help" "$codex_status" "$codex_output"

subs="$(printf '%s\n' "$top_help" | subcommands_of)"
if [[ -z "$subs" ]]; then
  printf 'No subcommands could be read out of that listing: its shape has changed, and `subcommands_of` in `scripts/codex-surface.sh` is what needs updating.\n\n'
fi

for sub in $subs; do
  # `help` is the listing above under another name; `exec` is captured
  # above, ahead of everything else, because it is the one this
  # repository runs; `update` would replace the very binary this document
  # is about.
  case "$sub" in
    help|exec|update) continue ;;
  esac
  printf '### `codex %s`\n\n' "$sub"
  codex_run "$help_home" "$help_dir" "$sub" --help
  sub_out="$codex_output"
  block "codex $sub --help" "$codex_status" "$sub_out"

  # One level deeper, which is where the interesting ones live: `codex
  # mcp` is a group, and its list/add are what read the config file
  # pkg/agent/codex writes.
  for inner in $(printf '%s\n' "$sub_out" | scrub | subcommands_of); do
    [[ "$inner" != "help" ]] || continue
    codex_capture "$help_home" "$help_dir" "$sub" "$inner" --help
  done
done

# ------------------------------------------------- where each flag lives

printf '## Where each flag lives\n\n'

printf 'The two listings above, read as sets. `codex` and `codex exec` are separate clap commands with separate flag sets, and a flag in one is not a flag in the other -- which is the single fact this whole document was added to record. `both` means the name is accepted in both positions; it does not mean it does the same thing in each.\n\n'

top_flags="$(printf '%s\n' "$top_help" | flags_of)"
exec_flags="$(printf '%s\n' "$exec_help" | flags_of)"

# Every membership test below is a here-string rather than a pipe into
# `grep -q`, and that is not style. `printf ... | grep -q` under `set -o
# pipefail` is a race: grep exits at the first match, printf takes SIGPIPE
# for the rest, and pipefail reports 141 for a pipeline that *did* match
# -- sometimes, depending on how much was left to write. The first capture
# has --approve-for-me listed as "both" and as "exec only" in the same
# table because of exactly that.

summary=""
while read -r flag; do
  [[ -n "$flag" ]] || continue
  where="top-level only"
  grep -qxF -- "$flag" <<<"$exec_flags" && where="both"
  summary+="$(printf '%-14s %s' "$where" "$flag")"$'\n'
done <<<"$top_flags"
while read -r flag; do
  [[ -n "$flag" ]] || continue
  grep -qxF -- "$flag" <<<"$top_flags" && continue
  summary+="$(printf '%-14s %s' "exec only" "$flag")"$'\n'
done <<<"$exec_flags"

block "which command each long flag belongs to" 0 "$(printf '%s' "$summary" | sort -k2)"

# ------------------------------------------------- the argv grain builds

printf '## The argv grain builds\n\n'

printf 'The listings above are what codex says. This section is what it does, asked one flag at a time and at the exact position `pkg/agent/codex` puts it: `codexArgs` for an unattended run, `TerminalCommand` for a session. Each row appends `--help` so that nothing runs -- clap parses the whole argv before it acts on `--help`, so an unrecognized argument is still an error and a recognized one still exits 0.\n\n'

printf '`accepted` here means the flag is spelled and positioned correctly. It is not a claim about the value: codex resolves neither a model name nor a path while parsing.\n\n'

printf 'This list is the one place in this document that names flags rather than discovering them, and `pkg/agent/codex/surface_capture_test.go` is what keeps it honest: a flag that package passes and this script does not probe fails that test rather than going unmeasured.\n\n'

for position in top-level exec; do
  probe_flag "$position" --skip-git-repo-check
  probe_flag "$position" --json
  probe_flag "$position" --model "$PROBE_MODEL"
done
block "does this codex accept the flag at that position" 0 "$flag_verdicts"

printf 'And the two argv whole, since a flag set that parses one flag at a time can still be refused together. The unattended one is shown without its trailing `-` -- the positional that tells codex to read the prompt from stdin, which `--help` would make meaningless -- and the session one is exactly what `TerminalCommand` returns.\n\n'

probe_argv "the unattended run: codexArgs, minus the stdin positional" \
  exec --json --skip-git-repo-check --model "$PROBE_MODEL"
probe_argv "the session: TerminalCommand" \
  --model "$PROBE_MODEL"
block "does this codex accept the whole command line" 0 "$argv_verdicts"

# ----------------------------------------------------- interactive mode

printf '## Interactive mode\n\n'

printf 'What `codex` does with no subcommand at all, which is what `TerminalCommand` runs. Its own `--help` opens by saying it: "If no subcommand is specified, options will be forwarded to the interactive CLI."\n\n'

printf 'First, without a terminal. Both probes below run in a directory that is not a git repository -- the case a granule run is always in, since it has no local directory and leaves codex started wherever the daemon happens to be.\n\n'

int_home="$(grain_home)"
plain_dir="$(not_a_repo)"
repo_dir="$(a_repo)"

codex_run "$int_home" "$plain_dir"
block "codex" "$codex_status" "$codex_output"
printf 'The same thing inside a git repository, so that the message above can be read for what it is about:\n\n'
codex_run "$int_home" "$repo_dir"
block "codex" "$codex_status" "$codex_output"

printf 'A refusal about the terminal rather than about the repository, in both. codex `exec` is the half of this binary that refuses to start outside a git repository ("Not inside a trusted directory and --skip-git-repo-check was not specified"), and it is the half that has a `--skip-git-repo-check` to be told otherwise with.\n\n'

printf 'So what does the interactive half do about a directory it does not trust? It asks. Below is the TUI started on a pty in each case, with a credential planted so the login screen is not what gets captured -- a word list rather than a screen, for the reason `tui_fragments` gives in the script.\n\n'

printf '### The TUI in a directory it has not been told to trust\n\n'
untrusted_frags="$(tui_fragments "$int_home" "$plain_dir")"
block "codex --no-alt-screen, on a pty, in a directory that is not a repository" 0 "$untrusted_frags"

printf '### The same, with that directory pre-trusted in `config.toml`\n\n'
printf 'codex records a decision like that under `[projects."<path>"] trust_level`, which is a thing `writeConfig` could write and does not. This is what it would buy.\n\n'

trusted_home="$(grain_home)"
{
  printf '\n[projects."%s"]\n' "$plain_dir"
  printf 'trust_level = "trusted"\n'
} >>"$trusted_home/config.toml"
trusted_frags="$(tui_fragments "$trusted_home" "$plain_dir")"

# A table of phrases rather than the second fragment list, and the reason
# is stability: the pre-trusted TUI is *past* the question and into a
# session, where it draws a rotating tip and whatever its MCP server is
# doing at that instant, so its word list differs between two runs against
# one codex. What is being measured here is one difference, and this is
# that difference and nothing else.
summary=""
while IFS= read -r phrase; do
  [[ -n "$phrase" ]] || continue
  before="not drawn"
  after="not drawn"
  grep -qF -- "$phrase" <<<"$untrusted_frags" && before="drawn"
  grep -qF -- "$phrase" <<<"$trusted_frags" && after="drawn"
  summary+="$(printf '%-28s %-12s %s' "$phrase" "$before" "$after")"$'\n'
done <<'PHRASES'
trust
untrusted
Yes, continue
Ask Codex to do anything
PHRASES
block "phrase                       untrusted    trust_level = \"trusted\"" 0 "$summary"

printf 'The trust question is gone in the second column and the prompt is there in both, which is the whole finding: an interactive codex started outside a repository asks the person who opened the session, and takes an answer. Nothing on that command line can pre-empt the question -- `--skip-git-repo-check` is `exec`'"'"'s, as the table above records -- but `config.toml` can, and the session shares its `CODEX_HOME` with the run.\n\n'

printf 'The slash commands a session gets, which is half of what somebody opens one for, are not in this document: they are typed into that TUI rather than printed by any probe. `codex --help` lists no flag that disables them, which is the only claim `terminal.go` needs from here.\n\n'

# ------------------------------- what it loads out of the config grain writes

printf '## What it loads out of the config grain writes\n\n'

printf 'A session and an unattended run of the same task share one `CODEX_HOME`, written by `writeConfig`, and everything that decides what either may reach is in it rather than on the command line. This section reads that file back through codex itself.\n\n'

config_home="$(grain_home)"
config_dir="$(not_a_repo)"

printf 'The file, as `configTOML` builds it (copied into this script, and diffable against that function by eye):\n\n'
printf '%stoml\n' "$FENCE"
grain_config
printf '%s\n\n' "$FENCE"

codex_capture "$config_home" "$config_dir" mcp list
codex_capture "$config_home" "$config_dir" --strict-config mcp list

printf '`--strict-config` is the interesting one: it makes codex error on a key it does not recognize, which turns "grain writes a key that is silently ignored" from a thing nobody could see into a non-zero exit. It is not passed in production -- an unattended run must not die over a config key -- so this probe is where that answer lives.\n\n'

# ---------------------------------------------------- the string table

printf '## The keys, out of the string table\n\n'

printf 'codex is a stripped release build, but a Rust binary carries every serde field name and every literal path its own code names. These are the keys `pkg/agent/codex` writes, looked for one at a time -- a name that stops appearing here is a key a new codex has renamed, and a config file full of them loads without complaint.\n\n'

summary=""
for key in approval_policy sandbox_mode mcp_servers code_mode_host \
           trust_level projects config.toml auth.json CODEX_HOME; do
  verdict="not in the string table"
  if [[ -n "$(in_binary "$key")" ]]; then
    verdict="present"
  fi
  summary+="$(printf '%-24s %s' "$verdict" "$key")"$'\n'
done
block "each name pkg/agent/codex depends on, in the binary's own strings" 0 "$summary"

printf 'And what codex itself will print about the repository check, which is where the flag this document is about actually lives. A Rust string table has no separators between its entries, so this is a fixed window around the match rather than a sentence:\n\n'
block "strings -n 4 codex | grep -aoE '.{0,60}trusted directory.{0,60}'" 0 "$(in_binary '.{0,60}trusted directory.{0,60}')"
