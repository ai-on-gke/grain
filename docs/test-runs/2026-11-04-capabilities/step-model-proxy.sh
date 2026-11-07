# step.sh (host side, run by fake-agy while the run is live): item 2's probe.
# The agent CLI runs on the host; the run's sandbox is a container, so the
# probe looks into it with docker exec, as the agent's run_command would.
echo "== agent config files under \$HOME/.gemini"
find "$HOME/.gemini" -type f | sed "s|$HOME|~|" | head -20
c=$(docker ps --latest --format '{{.Names}}')
echo "== sandbox container: $c ($(docker inspect -f '{{.Config.Image}}' "$c"))"
docker exec "$c" sh -c '
echo "-- ls -l ~/.gemini-proxy-*"; ls -l $HOME/.gemini-proxy-* 2>&1
u=$(cat $HOME/.gemini-proxy-url 2>/dev/null); t=$(cat $HOME/.gemini-proxy-token 2>/dev/null)
echo "url: $u"; echo "token: $(printf %s "$t" | head -c 6)<cut> (${#t} chars)"
echo "-- GET <url>/v1beta/models with the placed token"
curl -sS -m 20 -w "\nHTTP %{http_code}\n" -H "x-goog-api-key: $t" "${u%/}/v1beta/models?pageSize=1" 2>&1 | head -c 1500; echo
echo "-- the same with a wrong token"
curl -sS -m 20 -o /dev/null -w "HTTP %{http_code}\n" -H "x-goog-api-key: wrong" "${u%/}/v1beta/models?pageSize=1" 2>&1
' 2>&1
