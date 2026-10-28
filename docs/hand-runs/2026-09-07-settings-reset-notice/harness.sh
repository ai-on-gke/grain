#!/bin/sh
# Stand a throwaway grain up with a state repository on github.com, for
# the settings-reset hand run written up one directory above.
#
# Two modes, and which one you want depends entirely on whether you hold
# a GitHub credential that may write somewhere:
#
#   harness.sh sim
#       No credential. The state repository is served by a `git daemon`
#       on this machine under the name github.com, and api.github.com is
#       standin-api.py on 127.0.0.1:443. Everything grain does is real
#       except the half of the conversation where GitHub says yes. This
#       is what the 2026-09-07 run and section 4 of the 2026-09-08 one
#       were done on.
#
#   harness.sh live <owner>/<repo> <token-file>
#       A real throwaway repository and a real fine-grained token. git
#       and the REST API both go to the real github.com, /etc/hosts is
#       left alone, and the merges are real pull requests you merge
#       yourself. This is what the "Still owed" items need.
#
# Both leave a daemon on 127.0.0.1:8420 and a data directory under
# /tmp/hr. `harness.sh stop` puts the machine back.
#
# The trick sim mode rests on, spelled out because it is what lets one
# machine be two GitHubs: remoteHost reads the host out of the settings
# remote, githubRepoFromRemote answers only for github.com, and
# github.NewRealTransport then maps that host to api.github.com
# (github.APIHost). So a remote on github.com served locally gives a
# deployment whose git is entirely local and whose REST calls go wherever
# api.github.com resolves.

set -e

HR=${HR:-/tmp/hr}
GRAIN=${GRAIN:-/tmp/grain}
HERE=$(cd "$(dirname "$0")" && pwd)

case "$1" in
sim)
	mkdir -p "$HR/grain-handrun"
	git init --bare -q "$HR/grain-handrun/state.git"
	git daemon --base-path="$HR" --export-all --enable=receive-pack \
		--reuseaddr --listen=127.0.0.1 --detach
	grep -q ' github.com' /etc/hosts ||
		echo "127.0.0.1 github.com api.github.com" | sudo tee -a /etc/hosts >/dev/null
	openssl req -x509 -newkey rsa:2048 -nodes -days 2 \
		-keyout "$HR/key.pem" -out "$HR/cert.pem" -subj "/CN=api.github.com" \
		-addext "subjectAltName=DNS:api.github.com,DNS:github.com,IP:127.0.0.1" 2>/dev/null
	sudo sh -c "STANDIN_LOG=$HR/standin.log setsid python3 $HERE/standin-api.py \
		$HR/cert.pem $HR/key.pem </dev/null >$HR/standin.out 2>&1 &
		echo \$! >$HR/standin.pid"
	sleep 2
	printf 'ghp_standinstandinstandin' >"$HR/token"
	"$GRAIN" state -data-dir "$HR/data" adopt \
		-remote git://github.com/grain-handrun/state.git -token-file "$HR/token"
	SSL_CERT_FILE=$HR/cert.pem setsid "$GRAIN" daemon -data-dir "$HR/data" \
		-ui-addr 127.0.0.1:8420 -sandbox-dir "$HR/sandboxes" \
		</dev/null >"$HR/daemon.log" 2>&1 &
	echo $! >"$HR/daemon.pid"
	sleep 8
	git clone -q -b main git://github.com/grain-handrun/state.git "$HR/clone"
	echo "daemon on 127.0.0.1:8420, clone in $HR/clone, journal in $HR/daemon.log"
	echo "the comment grain posts lands in $HR/standin.log.comment"
	;;
live)
	repo=$2
	token=$3
	test -n "$repo" -a -n "$token" || {
		echo "usage: harness.sh live <owner>/<repo> <token-file>" >&2
		exit 2
	}
	mkdir -p "$HR"
	"$GRAIN" state -data-dir "$HR/data" adopt \
		-remote "https://github.com/$repo.git" -token-file "$token"
	setsid "$GRAIN" daemon -data-dir "$HR/data" -ui-addr 127.0.0.1:8420 \
		-sandbox-dir "$HR/sandboxes" </dev/null >"$HR/daemon.log" 2>&1 &
	echo $! >"$HR/daemon.pid"
	sleep 8
	# The clone git pushes from is yours, not the deployment's: a merge
	# has to arrive as a pull request somebody merged, so branch here,
	# open one, merge it, and let the daemon's own tick take it up.
	git -c credential.helper= clone -q "https://x-access-token:$(cat "$token")@github.com/$repo.git" "$HR/clone"
	echo "daemon on 127.0.0.1:8420, clone in $HR/clone, journal in $HR/daemon.log"
	;;
stop)
	# /proc rather than pkill or ps: a grain sandbox has neither procps
	# nor psmisc, which is the sort of thing only running this finds out.
	for p in /proc/[0-9]*; do
		c=$(tr '\0' ' ' <"$p/cmdline" 2>/dev/null) || continue
		# Not this script, and not the shell that invoked it: the
		# patterns below are broad enough to match the command line
		# somebody typed to run the teardown, and it kills itself.
		case "$c" in *harness.sh*) continue ;; esac
		case "$c" in
		*grain\ daemon*|*git-daemon*|*standin-api.py*)
			# `sudo kill` is not available either: kill is a shell
			# builtin and /bin/kill is procps', so root has to be given
			# a shell to run the builtin in.
			pid=${p#/proc/}
			kill "$pid" 2>/dev/null || sudo sh -c "kill $pid" 2>/dev/null || true
			;;
		esac
	done
	sudo sed -i '/ github.com api.github.com/d' /etc/hosts || true
	echo "stopped; $HR is still there to read"
	;;
*)
	sed -n '2,32p' "$0"
	exit 2
	;;
esac
