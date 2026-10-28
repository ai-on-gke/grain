#!/usr/bin/env bash
# claude-surface.sh -- read claude's surface off the installed binary and
# write it down as Markdown: every subcommand and flag it will admit to,
# whether the argv pkg/agent/claude builds is argv this claude actually
# parses -- in print mode and in a session, which are the two the package
# builds -- and what its terminal interface does when it is started the
# way an interactive task starts it.
#
# Why this exists, and why it is the third of these rather than the first.
# pkg/agent/claude puts two different argv on the command line: `claude -p
# ...` for an unattended run (claude.go, plus retry.go's --max-turns and
# --resume), and the same thing minus print mode for a session somebody
# types into (terminal.go). The second was derived from the first by hand
# -- take away `-p` and the two flags that exist to make print mode
# parseable -- and nothing in this repository had ever run `claude --help`
# to check either against a binary.
#
# That derivation is exactly the one that put `codex exec`'s
# --skip-git-repo-check on an interactive codex session, where it is
# "error: unexpected argument" on the terminal of whoever opened it, on
# whichever deployment tried first, with the session dead before the first
# prompt. scripts/codex-surface.sh's first run is what found it. This
# script is that measurement for the remaining framework.
#
# Usage:
#   scripts/claude-surface.sh [-o docs/claude-surface.md]
#
# The binary is $CLAUDE or $GRAIN_CLAUDE_PATH, defaulting to `claude` on
# $PATH -- the same resolution order the daemon's own -claude-path flag
# has, and the same one scripts/setup.sh documents for an operator-named
# copy.
#
# The two rules scripts/agy-surface.sh and scripts/codex-surface.sh are
# written to hold to are the rules here as well, for the same reasons, and
# they are worth repeating because they decide what this file does when
# something goes wrong.
#
# Nothing here may fail the run. Every probe is a question this script
# does not know the answer to; a command that exits non-zero, times out or
# prints nothing records exactly that and the script carries on. Almost
# every probe below exits non-zero on purpose -- see "How a flag is
# probed".
#
# And the output has to be stable enough that a diff means drift. Nothing
# run-specific goes in it -- no dates, no temporary paths, no session ids
# -- so two runs against one claude produce identical bytes and a run
# against a new one produces a diff that is entirely about claude.
#
# Claude is pinned by ARG CLAUDE_CLI_VERSION in the Dockerfile. The capture
# records that pin and the test compares it with the binary's version, so an
# upgrade cannot land without regenerating and reviewing this surface.
#
# How a flag is probed, which is the one place this script cannot copy its
# codex counterpart. codex is clap: appending `--help` makes it parse the
# whole argv and reject an unknown flag before printing anything, so a
# `--help` probe is a free parse check. claude is commander, and its
# `--help` (and `--version`) fire the moment the parser reaches them,
# ahead of the unknown-option check -- `claude --no-such-flag --help`
# exits 0 and prints the help. So the probe here runs the argv for real,
# with stdin closed and no credential in the environment, and reads what
# comes back:
#
#   error: unknown option '--no-such-flag'                     -> rejected
#   Error: Input must be provided either through stdin ...     -> accepted
#
# claude requires a prompt on stdin or in argv before it does anything at
# all, so "the parser got all the way past this argv and then found it had
# nothing to do" is a complete parse check that starts no model turn,
# needs no credential and costs no token. Commander's own refusals are the
# lower-case `error:` lines; claude's own runtime complaints are
# `Error:`, which is what makes the two tellable apart.

# File-wide, for the reason scripts/agy-surface.sh gives at this same
# spot: this script's output is Markdown emitted by `printf` with
# single-quoted format strings full of backticked identifiers and `$NAME`
# placeholders the scrubbers below write on purpose, and every one of them
# reads to the linter like an expansion someone forgot to quote.
# shellcheck disable=SC2016

# Not -e: see the first rule above. A probe's failure is an answer.
set -uo pipefail

CLAUDE="${CLAUDE:-${GRAIN_CLAUDE_PATH:-claude}}"

# Every claude invocation is wrapped in this. claude has subcommands that
# serve (`gateway`, `remote-control`), subcommands that talk to the
# network (`cloud`, `ultrareview`) and a terminal interface that waits for
# a keystroke forever; none of them may hang a CI job.
TIMEOUT="${CLAUDE_SURFACE_TIMEOUT:-60}"

# How long a terminal-interface probe is allowed to draw before it is
# killed. Longer than it sounds: claude renders its first screen over
# several frames, and the screen this document is about is the last of
# them.
TUI_TIMEOUT="${CLAUDE_SURFACE_TUI_TIMEOUT:-15}"

# Per-capture caps, so one verbose probe cannot turn a reviewable document
# into a megabyte nobody reads.
MAX_LINES="${CLAUDE_SURFACE_MAX_LINES:-400}"
MAX_BYTES="${CLAUDE_SURFACE_MAX_BYTES:-65536}"

# Six backticks, for the reason agy-surface.sh uses them: some of what is
# captured here is itself fenced Markdown.
FENCE='``````'

# Byte-wise sorting and matching everywhere, so the document does not
# depend on the locale of whoever ran it.
export LC_ALL=C

# The model name the flag probes pass to --model. A value, not a claim:
# claude does not resolve a model name while parsing its arguments, so
# what this says is which *flags* parse, never which models exist.
PROBE_MODEL='claude-sonnet-5'

# The tool name the --allowedTools probes pass. One of the names
# allowedTools() actually builds (pkg/agent/claude), rather than a
# placeholder, so the value in the document is the shape of the real one.
PROBE_TOOL='mcp__grain-sandbox__run_command'

# The session id the --resume probes pass. A UUID because claude checks
# the *shape* of this one before it looks for the session: a non-UUID gets
# "is not a UUID and does not match any session title", which is a finding
# about the value rather than about the flag. `scrub` rewrites every UUID
# to $UUID, this one included, so what lands in the document is stable.
PROBE_SESSION_ID='00000000-0000-0000-0000-000000000000'

# Defaulted rather than assumed: scrub below rewrites $HOME out of every
# capture, and a HOME-less environment would abort under -u instead.
HOME="${HOME:-/root}"

out=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o|--output) out="${2:-}"; shift 2 ;;
    -h|--help)   sed -n '2,32p' "$0"; exit 0 ;;
    *)
      echo "claude-surface.sh: unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if ! command -v "$CLAUDE" >/dev/null 2>&1 && [[ ! -x "$CLAUDE" ]]; then
  echo "claude-surface.sh: no claude at '$CLAUDE'. Set CLAUDE or GRAIN_CLAUDE_PATH, or install the CLI the way the Dockerfile and .github/workflows/claude-surface.yml do (https://claude.ai/install.sh)." >&2
  exit 2
fi

probe_root="$(mktemp -d "${TMPDIR:-/tmp}/claude-surface.XXXXXX")"
trap 'rm -rf "$probe_root"' EXIT

repo_root="$(cd "$(dirname "$0")/.." && pwd)"

if [[ -n "$out" ]]; then
  exec >"$out"
fi

# ---------------------------------------------------------------- helpers

# scrub removes everything about *this* run from a capture: terminal
# colour, the throwaway directories the probe ran in, the real HOME claude
# was installed into, the checkout it was run from, and the session ids
# claude stamps on its own output.
scrub() {
  sed -e 's/\x1b\[[0-9;?]*[a-zA-Z]//g' \
      -e 's/\r$//' \
      -e "s#${probe_root}#\$PROBE#g" \
      -e "s#${repo_root}#\$REPO#g" \
      -e "s#${PWD}#\$PWD#g" \
      -e "s#${HOME}#\$HOME#g" \
      -e 's/[0-9a-f]\{8\}-[0-9a-f]\{4\}-[0-9a-f]\{4\}-[0-9a-f]\{4\}-[0-9a-f]\{12\}/$UUID/g'
}

# limit applies the two caps, announcing either in the document.
limit() {
  awk -v max="$MAX_LINES" -v maxb="$MAX_BYTES" '
    NR > max { print "... [truncated at " max " lines by scripts/claude-surface.sh]"; exit }
    {
      bytes += length($0) + 1
      if (bytes > maxb) { print "... [truncated at " maxb " bytes by scripts/claude-surface.sh]"; exit }
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
  # The label is scrubbed too, and not only the output: a probe's own
  # command line carries the throwaway config path it was given, and a
  # heading reading `$PROBE/config-1/mcp.json` is the difference between a
  # document two runs agree on and one they do not.
  printf '%sconsole\n$ %s\n' "$FENCE" "$(printf '%s' "$label" | scrub)"
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

# claude_run runs claude with a given HOME and working directory, and
# leaves its combined output in claude_output and its status in
# claude_status. Globals rather than a captured stdout because `x="$(f)"`
# runs f in a subshell, where the exit status of the binary -- half of
# what this script is recording -- would be lost.
#
# Four things about that environment, each of them load-bearing.
#
# HOME is a throwaway directory, because claude keeps everything it
# remembers under $HOME/.claude and $HOME/.claude.json: a probe must not
# read the settings of whoever ran this, and must not leave any.
#
# stdin is /dev/null. It is what makes the probes above safe *and* what
# makes them work: claude with nothing on stdin and no prompt in argv
# refuses to start a turn, which is the "accepted" answer this script
# reads, and a probe that inherited a terminal's stdin would instead sit
# waiting for one.
#
# No credential reaches it. ANTHROPIC_API_KEY and CLAUDE_CODE_OAUTH_TOKEN
# are removed from the environment of every probe rather than merely left
# unset, so that a machine that has one cannot turn a probe into a
# conversation. Nothing in this document costs a token.
#
# TERM=dumb, and the autoupdater and telemetry off, keep the document
# stable: no terminal interface starts by accident, no probe replaces the
# very binary this file is about half way through, and no probe reports
# itself to anything. Production sets none of these -- the interactive
# section below is the one place that undoes the first of them, with a pty
# of its own.
claude_output=""
claude_status=0
claude_run() {
  local home="$1" dir="$2"; shift 2
  claude_output="$(cd "$dir" && env -u ANTHROPIC_API_KEY -u CLAUDE_CODE_OAUTH_TOKEN \
    HOME="$home" NO_COLOR=1 TERM=dumb CI=1 \
    DISABLE_AUTOUPDATER=1 DISABLE_TELEMETRY=1 DISABLE_ERROR_REPORTING=1 \
    DISABLE_NON_ESSENTIAL_MODEL_CALLS=1 \
    timeout "$TIMEOUT" "$CLAUDE" "$@" </dev/null 2>&1)"
  claude_status=$?
}

# show_argv renders a command line the way a reader would have to type it.
# `"$*"` would not: this repository passes `--tools ""`, and an empty
# argument printed as itself is nothing at all -- `claude --tools
# --mcp-config ...`, which is a different command line and one claude
# would parse differently.
show_argv() {
  local arg out=""
  for arg in "$@"; do
    if [[ -z "$arg" || "$arg" == *[[:space:]]* ]]; then
      out+=" \"$arg\""
    else
      out+=" $arg"
    fi
  done
  printf '%s' "${out# }"
}

# claude_capture is the pair of them: run, and write the block.
claude_capture() {
  local home="$1" dir="$2"; shift 2
  claude_run "$home" "$dir" "$@"
  block "claude $(show_argv "$@")" "$claude_status" "$claude_output"
}

# fresh_home is a HOME claude has never seen: no settings, no credential,
# no recorded sessions, no onboarding.
#
# Numbered rather than mktemp'd, here and in the two directory helpers
# below, and that is about the second rule this script is written to: a
# random suffix survives `scrub` (which only knows the root it sits under)
# and lands in the document, where two runs against one claude then differ
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

# grain_config is the --mcp-config file pkg/agent/claude writes for one
# run, as `mcpConfigJSON` builds it -- a copy, and the only copy in this
# script, named here so a reader can diff it against that function by eye.
# The command is /bin/true rather than the grain binary: what is being
# measured is whether claude accepts the *shape* of what grain writes, and
# a server that answers nothing still proves that much without this script
# needing a grain build.
grain_config() {
  cat <<'JSON'
{"mcpServers":{"grain":{"command":"/bin/true","args":["mcpserver","-sandbox","$PROBE/sandbox"]}}}
JSON
}

# mcp_config writes that file and returns its path, once: every probe that
# passes --mcp-config passes this same path, so the document says
# `$PROBE/config-2/mcp.json` everywhere rather than a fresh number each
# time.
mcp_config_path=""
mcp_config() {
  if [[ -z "$mcp_config_path" ]]; then
    local dir
    dir="$(probe_dir config)"
    mcp_config_path="$dir/mcp.json"
    grain_config >"$mcp_config_path"
  fi
  printf '%s' "$mcp_config_path"
}

# stop_settings is a --settings file of the shape pkg/agent/claude's
# writeStopSettings stages for every run and session: one Stop hook. Its
# command is `true`, so a probe that gets as far as a turn ends normally.
stop_settings_path=""
stop_settings() {
  if [[ -z "$stop_settings_path" ]]; then
    local dir
    dir="$(probe_dir settings)"
    stop_settings_path="$dir/settings.json"
    printf '%s\n' '{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"true","timeout":30}]}]}}' >"$stop_settings_path"
  fi
  printf '%s' "$stop_settings_path"
}

# onboarded_home is a HOME with claude's first-run questions already
# answered, which is what the interactive probes need: a fresh HOME puts
# the theme picker on the screen and nothing else, and the screen this
# document is about is the one after it.
#
# Not a credential: an interactive claude with no login gets as far as the
# workspace-trust question either way, and that question is what the
# section is about.
onboarded_home() {
  local home
  home="$(fresh_home)"
  mkdir -p "$home/.claude"
  printf '{"hasCompletedOnboarding":true,"theme":"dark"}\n' >"$home/.claude.json"
  printf '%s' "$home"
}

# a_repo and not_a_repo are two working directories, because claude reads
# the directory it was started in: a session's is the daemon's own
# (terminal.go sets no Dir), which on a deployed host is neither a
# checkout nor anywhere a run's files are.
a_repo() {
  local dir
  dir="$(probe_dir repo)"
  git -C "$dir" init -q >/dev/null 2>&1
  printf '%s' "$dir"
}

not_a_repo() {
  probe_dir plain
}

# subcommands_of pulls command names out of a commander `--help` listing:
# the rows indented exactly two spaces under a "Commands:" heading.
#
# Exactly two, which is what keeps a wrapped description -- indented much
# further, and starting with a word -- from being read as a subcommand of
# its own. `stop|kill <id>` and `plugin|plugins` are one command each with
# an alias, so the alias half is cut: what is wanted is a name to ask for
# help on, and either half would do.
subcommands_of() {
  awk '
    /^[A-Za-z ]*[Cc]ommands:/ { inside = 1; next }
    /^[^[:space:]]/           { inside = 0 }
    inside && substr($0, 1, 3) ~ /^  [a-z]/ {
      line = $0
      sub(/^[[:space:]]+/, "", line)
      split(line, fields, /[[:space:]]+/)
      split(fields[1], names, "|")
      print names[1]
    }' | sort -u
}

# flags_of pulls long-flag names out of the same listing: anything spelled
# --like-this in the options section, including the long half of a `-p,
# --print` pair.
#
# Upper case is in the character class on purpose. claude's flag set is
# not uniformly kebab-case -- `--allowedTools` and `--disallowedTools` are
# camelCase, and `--allowedTools` is one of the four this repository
# depends on -- so a codex-shaped `--[a-z][a-z0-9-]*` would read that flag
# as `--allowed` and quietly report a flag that does not exist.
flags_of() {
  grep -oE -- '--[a-zA-Z][a-zA-Z0-9-]*' | sort -u
}

# verdict_of reads one probe's answer: did claude's parser accept this
# argv, or refuse it?
#
# The distinction is the one the header describes. commander refuses in
# lower case -- `error: unknown option '--x'`, `error: option '--x
# <y>' argument missing` -- and claude's own runtime complaints are
# capitalised (`Error: Input must be provided ...`, `No conversation
# found ...`). So a lower-case `error:` line is a refusal of the command
# line, and anything else means the parser got past every flag in it.
#
# What "accepted" does not mean: that the value is good, that the flag
# does anything in this position, or that the run would have worked.
# claude lists half a dozen flags as "only works with --print" and takes
# them silently without it.
verdict=""
verdict_detail=""
verdict_of() {
  local first
  if grep -q '^error: ' <<<"$claude_output"; then
    verdict="rejected"
  else
    verdict="accepted"
  fi
  # claude's own first word about it, which is what somebody running this
  # command would see -- kept for both verdicts, because on an accepted
  # row it is the evidence that the parser got to the end and stopped for
  # a reason of its own.
  first="$(printf '%s\n' "$claude_output" | scrub | grep -m1 -E '^(error|Error|No conversation)' || true)"
  [[ -n "$first" ]] || first="$(printf '%s\n' "$claude_output" | scrub | grep -m1 . || true)"
  verdict_detail="$(cut -c1-120 <<<"$first")"
}

# probe_flag asks the only question this document exists to answer for a
# given flag: does claude accept it *here*, in the argv this package
# builds for this position?
#
# `print` is `claude -p <flag>`, the unattended run's shape; `session` is
# `claude <flag>`, the shape terminal.go hands pkg/console. Every value
# the flag takes is passed, because a variadic option with nothing after
# it is a different parse from one with a value -- and `--tools ""`, which
# this repository passes to empty claude's native roster, is an empty
# value rather than no value at all.
flag_verdicts=""
probe_flag() {
  local position="$1" flag="$2"; shift 2
  local home dir argv=()
  home="$(fresh_home)"
  dir="$(not_a_repo)"
  [[ "$position" == "print" ]] && argv+=("-p")
  argv+=("$flag" "$@")

  claude_run "$home" "$dir" "${argv[@]}"
  verdict_of
  # Trailing space trimmed, so a row is not a line of padding that some
  # editor strips on the next capture and calls drift.
  flag_verdicts+="$(printf '%-9s %-8s %-22s %s' "$verdict" "$position" "$flag" "$verdict_detail" | sed -e 's/[[:space:]]*$//')"$'\n'
}

# probe_argv is probe_flag for a whole command line: the argv
# pkg/agent/claude assembles, run as one thing, since a flag set that
# parses one flag at a time can still be refused together -- and claude's
# variadic options (`--tools <tools...>`, `--mcp-config <configs...>`,
# `--allowedTools <tools...>`) make what follows a flag part of how it
# parses.
argv_verdicts=""
probe_argv() {
  local label="$1"; shift
  local home dir shown
  home="$(fresh_home)"
  dir="$(not_a_repo)"
  claude_run "$home" "$dir" "$@"
  verdict_of
  shown="$(printf 'claude %s' "$(show_argv "$@")" | scrub)"
  argv_verdicts+="$(printf '%-9s %s' "$verdict" "$shown")"$'\n'
  argv_verdicts+="$(printf '          %s%s%s' "$label" "${verdict_detail:+ -- }" "$verdict_detail")"$'\n'
}

# tui_fragments starts claude's real terminal interface on a pty and
# reports what it drew.
#
# A pty and not a pipe, because claude treats a non-terminal stdout as
# print mode whatever else is on the command line (its own --print help
# says so, and the probe above this section captures it) -- so without one
# there is no interactive claude to look at. python3 rather than `script`
# because the window size has to be set: a terminal interface given no
# dimensions draws nothing this document could quote.
#
# What comes back is a word list rather than a screen, for the reason
# scripts/codex-surface.sh gives: claude positions every run of text with
# its own escape sequences, so stripping those leaves fragments rather
# than lines. Sorted and de-duplicated, which also makes the capture
# stable across a first screen that is drawn in several frames.
tui_fragments() {
  local home="$1" dir="$2" raw
  shift 2
  raw="$probe_root/tui.$$.raw"
  if ! command -v python3 >/dev/null 2>&1; then
    printf '[no python3 on this machine, so the terminal interface was never started; this probe needs a pty]\n'
    return
  fi
  CLAUDE_TUI_DIR="$dir" CLAUDE_TUI_TIMEOUT="$TUI_TIMEOUT" \
    env -u ANTHROPIC_API_KEY -u CLAUDE_CODE_OAUTH_TOKEN \
    HOME="$home" TERM=xterm-256color NO_COLOR=1 \
    DISABLE_AUTOUPDATER=1 DISABLE_TELEMETRY=1 DISABLE_ERROR_REPORTING=1 \
    DISABLE_NON_ESSENTIAL_MODEL_CALLS=1 \
    python3 - "$CLAUDE" "$@" >"$raw" 2>&1 <<'PY'
import fcntl, os, pty, select, signal, struct, sys, termios, time

claude, args = sys.argv[1], sys.argv[2:]
pid, fd = pty.fork()
if pid == 0:
    os.chdir(os.environ["CLAUDE_TUI_DIR"])
    os.execvp(claude, [claude] + args)

fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 120, 0, 0))
deadline = time.time() + float(os.environ["CLAUDE_TUI_TIMEOUT"])
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
  # Escape sequences to newlines rather than to nothing: two words the
  # interface drew in different places must not be joined into a third
  # that was never on the screen.
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
# and codex-surface.sh do. claude ships as a ~200MB single-file build with
# its whole JavaScript bundle inside it, so every flag name, every
# environment variable it reads and every JSON key it looks for is in
# there as a literal.
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
<!-- Generated by scripts/claude-surface.sh. Do not edit by hand: the next
     dispatch of .github/workflows/claude-surface.yml overwrites it. -->

# claude's surface, read off the binary

Everything the installed `claude` will say about itself, captured by
`scripts/claude-surface.sh` and regenerated on demand by
`.github/workflows/claude-surface.yml` (`workflow_dispatch` only, no
schedule, no credential). It is `docs/agy-surface.md` and
`docs/codex-surface.md`'s counterpart for the third CLI `pkg/agent`
drives, and it exists for one question neither of those can answer: is the
argv `pkg/agent/claude` builds argv this claude parses?

There are two of them, and they were derived from each other by hand.
`claude -p --tools "" --mcp-config <file> --strict-mcp-config
--allowedTools <names> --output-format stream-json --verbose` is the
unattended run (`claude.go`, plus `--max-turns` and `--resume` from
`retry.go`), and the same thing minus print mode and the two flags that
make print mode parseable is the interactive session (`terminal.go`).
Until this capture existed, neither had been checked against a binary --
and that same derivation is what put `codex exec`'s `--skip-git-repo-check`
on an interactive codex session, where the first thing whoever opened it
saw was `error: unexpected argument`.

Read it as evidence rather than as documentation: every section below is a
command and its output, the commands that failed included. A probe that
stopped answering is itself a finding.

Claude is pinned in the Dockerfile. This capture records the same version,
and `pkg/agent/claude/surface_capture_test.go` fails when the pin changes
until the surface is regenerated and reviewed.

MARKDOWN

# ------------------------------------------------------------ the binary

printf '## The binary\n\n'

claude_run "$(fresh_home)" "$(not_a_repo)" --version
block "claude --version" "$claude_status" "$claude_output"

version_line="$(printf '%s\n' "$claude_output" | scrub | grep -m1 . || true)"

# The install puts the binary behind a symlink into a versioned directory
# (`~/.local/bin/claude` -> `.../versions/<version>`), so resolve it: the
# string-table reads below need the real file, and its digest is what says
# "the same binary as last time" on a day the version string alone would
# not.
resolved="$(command -v "$CLAUDE" 2>/dev/null || printf '%s' "$CLAUDE")"
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

# The two lines pkg/agent/claude/surface_capture_test.go reads to decide
# what this file is about. Spelled as a fixed pair of labelled lines
# rather than left for a test to find in prose -- and the first of them is
# read out of the Dockerfile rather than asserted here, so that a pin
# appearing in it shows up in this document on the next capture.
printf 'How the image gets its claude, against the claude that answered here:\n\n'
pinned="not pinned: the Dockerfile runs https://claude.ai/install.sh, which serves the current release"
if [[ -r "$repo_root/Dockerfile" ]]; then
  from_dockerfile="$(sed -n 's/^ARG CLAUDE_CLI_VERSION=//p' "$repo_root/Dockerfile" | head -n1)"
  [[ -z "$from_dockerfile" ]] || pinned="pinned: ARG CLAUDE_CLI_VERSION=$from_dockerfile"
else
  pinned="[not read: no Dockerfile at \$REPO/Dockerfile]"
fi
printf '%sconsole\n' "$FENCE"
printf 'installed by the Dockerfile   %s\n' "$pinned"
printf 'captured here                 %s\n' "${version_line:-[claude --version printed nothing]}"
printf '%s\n\n' "$FENCE"

# ---------------------------------------------------- commands and flags

printf '## Commands and flags\n\n'

help_home="$(fresh_home)"
help_dir="$(not_a_repo)"
claude_run "$help_home" "$help_dir" --help
top_help="$(printf '%s\n' "$claude_output" | scrub)"
block "claude --help" "$claude_status" "$claude_output"

subs="$(printf '%s\n' "$top_help" | subcommands_of)"
if [[ -z "$subs" ]]; then
  printf 'No subcommands could be read out of that listing: its shape has changed, and `subcommands_of` in `scripts/claude-surface.sh` is what needs updating.\n\n'
fi

for sub in $subs; do
  # `help` is the listing above under another name; `install` and
  # `migrate-installer` would replace the very binary this document is
  # about; `update` likewise.
  case "$sub" in
    help|install|migrate-installer|update) continue ;;
  esac
  printf '### `claude %s`\n\n' "$sub"
  claude_run "$help_home" "$help_dir" "$sub" --help
  sub_out="$claude_output"
  block "claude $sub --help" "$claude_status" "$sub_out"

  # One level deeper, which is where the interesting ones live: `claude
  # mcp` is a group, and its list/add are the commands that read MCP
  # configuration at all.
  for inner in $(printf '%s\n' "$sub_out" | scrub | subcommands_of); do
    [[ "$inner" != "help" ]] || continue
    claude_capture "$help_home" "$help_dir" "$sub" "$inner" --help
  done
done

# ------------------------------------------------- where each flag lives

printf '## Where each flag lives\n\n'

printf 'Every long flag `claude --help` listed, sorted. This is the set the two argv below are drawn from, and a flag that leaves it is a flag that has been renamed or withdrawn -- which is a diff on this list rather than a surprise in production.\n\n'

top_flags="$(printf '%s\n' "$top_help" | flags_of)"
block "the long flags claude --help lists" 0 "$top_flags"

printf 'Two things this list is not.\n\n'

printf 'It is not the whole flag set. `--max-turns`, which `retry.go` passes on every capped attempt, is nowhere in it, and the probe table below finds this claude accepting it all the same: a flag can be undocumented and live. That is the argument for probing rather than reading.\n\n'

printf 'And it is not a flag set the subcommands share. `claude` and `claude mcp list` are separate commander commands, the way `codex` and `codex exec` are, and the top-level options do not reach the subcommand:\n\n'

claude_capture "$help_home" "$help_dir" mcp list --mcp-config "$(mcp_config)" --strict-mcp-config

printf 'Nothing in this repository runs `claude mcp` -- `pkg/agent/claude` passes `--mcp-config` to the session and the run themselves -- so that refusal is here as the shape of the answer, not as a bug. It is what `--skip-git-repo-check` looked like on codex.\n\n'

# ------------------------------------------------- the argv grain builds

printf '## The argv grain builds\n\n'

printf 'The listing above is what claude says. This section is what it does, asked one flag at a time and in the exact argv `pkg/agent/claude` builds: `print` is `claude -p <flag>`, the unattended run'"'"'s shape (`Run`, `attemptArgs`), and `session` is `claude <flag>`, the shape `TerminalCommand` hands `pkg/console`.\n\n'

printf 'Every probe here is run for real, with stdin closed and both `ANTHROPIC_API_KEY` and `CLAUDE_CODE_OAUTH_TOKEN` removed from the environment. Appending `--help` -- which is how the codex capture parses without running -- proves nothing against claude: commander acts on `--help` the moment it reaches it, so `claude --no-such-flag --help` exits 0 and prints the help. Running it instead is safe because claude will not start a turn without a prompt: what an accepted argv gets is `Error: Input must be provided either through stdin or as a prompt argument`, which is the parser having read every flag and then found nothing to do. No probe in this document starts a model turn.\n\n'

printf '`accepted` therefore means the flag is spelled and positioned correctly, and nothing more. It is not a claim about the value, and not a claim that the flag does anything here: claude documents half a dozen options as "only works with --print" and takes them silently without it.\n\n'

printf 'It is also why the `session` rows come back quoting print mode. A probe'"'"'s stdout is a pipe, and claude reads a redirected stdout as print mode whatever else is on the command line -- so what these rows measure is the parser, which is the same parser either way. Whether the *interactive* half of the binary takes the same argv is a question only a pty can answer, and "Interactive mode" below is where it is asked.\n\n'

printf 'This list is the one place in this document that names flags rather than discovering them, and `pkg/agent/claude/surface_capture_test.go` is what keeps it honest: a flag that package passes and this script does not probe fails that test rather than going unmeasured.\n\n'

# The session's own argv, as TerminalCommand returns it, built once: it
# is probed whole below, started on a pty further down, and run without
# one in between, and all three have to be the same command line for any
# of them to be evidence about it.
session_argv=(--tools "" --mcp-config "$(mcp_config)" --strict-mcp-config
              --allowedTools "$PROBE_TOOL" --model "$PROBE_MODEL")

for position in print session; do
  probe_flag "$position" --tools ""
  probe_flag "$position" --mcp-config "$(mcp_config)"
  probe_flag "$position" --strict-mcp-config
  probe_flag "$position" --allowedTools "$PROBE_TOOL"
  probe_flag "$position" --output-format stream-json
  probe_flag "$position" --verbose
  probe_flag "$position" --model "$PROBE_MODEL"
  probe_flag "$position" --max-turns 12
  probe_flag "$position" --resume "$PROBE_SESSION_ID"
  probe_flag "$position" --settings "$(stop_settings)"
done
block "does this claude accept the flag in that argv" 0 "$flag_verdicts"

printf 'And the argv whole, since a flag set that parses one flag at a time can still be refused together -- which matters more here than it did for codex, because three of these options are variadic (`--tools <tools...>`, `--mcp-config <configs...>`, `--allowedTools <tools...>`) and a variadic option swallows everything after it that does not start with a dash.\n\n'

probe_argv "the unattended run: Run's own argv" \
  -p --tools "" --mcp-config "$(mcp_config)" --strict-mcp-config \
  --allowedTools "$PROBE_TOOL" --output-format stream-json --verbose \
  --model "$PROBE_MODEL"
probe_argv "the same, as attemptArgs builds a capped retry that resumes" \
  -p --tools "" --mcp-config "$(mcp_config)" --strict-mcp-config \
  --allowedTools "$PROBE_TOOL" --output-format stream-json --verbose \
  --model "$PROBE_MODEL" --max-turns 12 --resume "$PROBE_SESSION_ID"
probe_argv "the session: TerminalCommand" "${session_argv[@]}"
block "does this claude accept the whole command line" 0 "$argv_verdicts"

printf 'That last row is the one worth reading twice. The prompt of a real run travels on stdin rather than in argv (`Run`, deliberately: untrusted issue content must never be a `ps`-visible argument), and this document is the reason to keep it that way -- a positional argument after `--mcp-config <file>` is not a prompt to claude, it is a second config file:\n\n'

claude_capture "$(fresh_home)" "$(not_a_repo)" \
  -p --mcp-config "$(mcp_config)" "a prompt in argv rather than on stdin"

# ----------------------------------------------------- interactive mode

printf '## Interactive mode\n\n'

printf 'What `claude` does with no `-p` at all, which is what `TerminalCommand` runs. `pkg/console` starts it with no working directory of its own (`terminal.go` sets no `Dir`), so on a deployed host it opens wherever the daemon happens to be -- neither a checkout nor anywhere the run'"'"'s own files are.\n\n'

printf 'First, without a terminal. This is not an interactive claude at all, and that is the finding: with stdout redirected, claude reads its own command line as print mode whatever else is on it.\n\n'

int_home="$(onboarded_home)"
plain_dir="$(not_a_repo)"
repo_dir="$(a_repo)"

claude_run "$int_home" "$plain_dir" "${session_argv[@]}"
block "claude $(show_argv "${session_argv[@]}"), stdout redirected" "$claude_status" "$claude_output"

printf 'The same command inside a git repository, so that the message above can be read for what it is not about -- unlike codex, claude has no repository check to skip and no `--skip-git-repo-check` to skip it with:\n\n'

claude_run "$int_home" "$repo_dir" "${session_argv[@]}"
block "claude $(show_argv "${session_argv[@]}"), in a git repository" "$claude_status" "$claude_output"

printf 'So the interactive half of this binary only exists on a pty. Below is exactly the argv `TerminalCommand` returns, started on one, in a HOME whose first-run questions have been answered (a fresh HOME draws the theme picker and nothing else). No credential is planted: an unauthenticated claude gets as far as the screen this section is about either way. A word list rather than a screen, for the reason `tui_fragments` gives in the script.\n\n'

printf '### The session argv, on a pty\n\n'
session_frags="$(tui_fragments "$int_home" "$plain_dir" "${session_argv[@]}")"
block "claude $(show_argv "${session_argv[@]}"), on a pty" 0 "$session_frags"

printf 'Two things are in that list and both matter. There is no `error:` line, which is this document'"'"'s whole question answered for the half of it a test cannot reach: the session argv is argv the interactive claude parses too. And there is a workspace-trust question -- claude asks whoever opened the session whether it may read the directory it started in, before anything else happens.\n\n'

printf 'Nothing on that command line pre-empts that question, and nothing in `pkg/agent/claude` tries to. `-p` does (claude'"'"'s own `--print` help: "The workspace trust dialog is skipped when Claude is run in non-interactive mode"), which is why an unattended run never sees it and a session always does.\n\n'

# ------------------------------- what it loads out of the config grain writes

printf '## What it loads out of the config grain writes\n\n'

printf 'A session and an unattended run of the same task are handed the same `--mcp-config` file, written by `writeMCPConfig` from `mcpConfigJSON`, and everything a run can reach is in it: `--tools ""` empties claude'"'"'s native roster, `--strict-mcp-config` ignores every other MCP configuration, and `--allowedTools` filters what is left. The file, as that function builds it (copied into this script, and diffable against it by eye):\n\n'
printf '%sjson\n' "$FENCE"
grain_config
printf '%s\n\n' "$FENCE"

printf 'What claude does with a `--mcp-config` it cannot read, which is the failure mode a wrong path in this repository would produce -- loud, and before any turn:\n\n'

claude_capture "$(fresh_home)" "$(not_a_repo)" \
  -p --mcp-config "$probe_root/no-such-config.json" --strict-mcp-config

# ---------------------------------------------------- the string table

printf '## The keys, out of the string table\n\n'

printf 'claude ships as one ~200MB file with its whole JavaScript bundle inside it, so every flag name it accepts, every environment variable it reads and every JSON key it looks for is in there as a literal. These are the names `pkg/agent/claude` depends on and cannot check any other way -- the environment variables especially, since none of them appears in `--help`. A name that stops appearing here is a name a new claude has renamed, and an environment variable set under a renamed name is silently no setting at all.\n\n'

summary=""
for key in MCP_TOOL_TIMEOUT CLAUDE_CODE_OAUTH_TOKEN \
           CLAUDE_CODE_DISABLE_AUTO_MEMORY \
           strict-mcp-config allowedTools mcp-config max-turns \
           output-format stream-json mcpServers; do
  key_verdict="not in the string table"
  if [[ -n "$(in_binary "$key")" ]]; then
    key_verdict="present"
  fi
  summary+="$(printf '%-24s %s' "$key_verdict" "$key")"$'\n'
done
block "each name pkg/agent/claude depends on, in the binary's own strings" 0 "$summary"

printf 'And what claude itself will say about the roster `--tools` empties, which is the one argument in this repository whose behaviour no probe here can demonstrate without a model turn. A window around the match rather than a sentence, since a bundle has no separators between its literals:\n\n'
block "strings -n 4 claude | grep -aoE '.{0,60}disable all tools.{0,60}'" 0 "$(in_binary '.{0,60}disable all tools.{0,60}')"
