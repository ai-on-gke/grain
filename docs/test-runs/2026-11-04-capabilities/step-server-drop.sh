# step.sh (host side) for item 4: hold the run open while the capability
# server is stopped, then make one grain MCP tool call and record its result.
cfg="$HOME/.gemini/config/mcp_config.json"
echo "== mcp_config.json (secrets cut)"
sed -E 's/("[^"]*([Tt]oken|[Aa]uthorization|KEY|SECRET)[^"]*" *: *")[^"]{0,8}[^"]*"/\1<cut>"/g' "$cfg"
cp "$cfg" /home/debian/walk/mcp-live.json
touch /home/debian/walk/ready
echo "== $(date -u +%T) waiting for /home/debian/walk/go"
for i in $(seq 240); do [ -f /home/debian/walk/go ] && break; sleep 1; done
echo "== $(date -u +%T) go: calling a tool"
[ -f /home/debian/walk/call.sh ] && sh /home/debian/walk/call.sh 2>&1
rm -f /home/debian/walk/go /home/debian/walk/ready
