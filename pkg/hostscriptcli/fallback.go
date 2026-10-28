package hostscriptcli

import (
	"bytes"
	"encoding/json"
)

// Marshal is d as a grant places it: indented one field to a line, in
// Descriptor's field order, with no HTML escaping, which FallbackScript
// reads with sed.
func Marshal(d Descriptor) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(d); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// FallbackScript is `grain` for a sandbox whose image carries none: a
// POSIX shell stand-in that answers `grain host-script` exactly as Main
// does, with curl, od and printf -- what every grain sandbox image has
// (pkg/sandboximage) -- and nothing else. A grant places it beside its
// descriptor and installs it on PATH only when no other grain is there
// (the host-script capability's setup). Frame bytes are printf formats
// of literal characters and octal escapes only, so printing one can
// neither expand anything nor be read as an option.
const FallbackScript = `#!/bin/sh
# grain, as grain's host-script capability installs it in a sandbox whose
# image has no grain of its own: it runs only ` + "`grain host-script`" + `.
set -u
dir=${` + DirEnv + `:-` + Dir + `}
usage() {
  cat <<'GRAIN_USAGE_END'
` + usage + `GRAIN_USAGE_END
}
if [ "${1-}" != host-script ]; then
  echo "grain: this sandbox's grain only runs host-script" >&2
  usage >&2
  exit 2
fi
shift
# field NAME FILE is one field of a descriptor, still JSON-escaped.
field() {
  sed -n "s/^  \"$1\": \(.*\)\$/\1/p" "$2" | sed 's/,$//; s/^"//; s/"$//'
}
# text NAME FILE is a string field, unescaped.
text() {
  printf '%b' "$(field "$1" "$2" | sed 's/\\"/"/g')"
}
descriptor() {
  case $1 in
    ""|[_-]*|*[!a-z0-9_-]*) echo "grain host-script: \"$1\" is not a host script name" >&2; exit 2 ;;
  esac
  f=$dir/$1.json
  if [ ! -r "$f" ]; then
    echo "grain host-script: no host script \"$1\" is granted to this sandbox (or its grant was released); ` + "`grain host-script --list`" + ` shows the ones that are" >&2
    exit 125
  fi
}
help() {
  descriptor "$1"
  printf 'Usage: grain host-script %s [arguments...]\n\n%s\n' "$1" "$(text description "$f")"
  u=$(text usage "$f")
  if [ -n "$u" ]; then printf '\n%s\n' "$u"; fi
  printf "\nRuns on grain's side, not in this sandbox; arguments are passed through exactly, without a shell, and are logged.\n"
  exit 0
}
case "${1-}" in
  "")
    usage >&2
    exit 2 ;;
  --list|-list|--grain-list)
    n=0
    for f in "$dir"/*.json; do
      [ -r "$f" ] || continue
      printf '%s\t%s\n' "$(text name "$f")" "$(text description "$f")"
      n=1
    done
    if [ "$n" = 0 ]; then echo "grain host-script: no host scripts are granted to this sandbox" >&2; fi
    exit 0 ;;
  --help|-help|-h|--grain-help)
    if [ "$#" -ge 2 ]; then help "$2"; fi
    usage
    exit 0 ;;
esac
name=$1
shift
if [ "$#" -ge 1 ] && [ "$1" = --grain-help ]; then help "$name"; fi
descriptor "$name"
port=$(field port "$f")
attempt=$(field attempt_id "$f")
tok=$(field token "$f")
for a in "$@"; do
  printf '%s' "$a" | od -An -v -tx1 | tr -d ' \n'
  echo
done | curl -sS -N -X POST -H "Authorization: Bearer $tok" -H 'Content-Type: ` + ArgvType + `' \
  --data-binary @- "http://127.0.0.1:$port/runs/$attempt/exec" | {
  while IFS= read -r line; do
    case $line in
      "o "*) printf "${line#o }" ;;
      "e "*) printf "${line#e }" >&2 ;;
      "x "*) exit "${line#x }" ;;
    esac
  done
  echo "grain host-script: $name: no answer from grain's host-script endpoint (the grant was released, or it is unreachable)" >&2
  exit 125
}
`
