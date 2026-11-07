# collector.py PORT DIR: appends each POST body to DIR/<path>.log -- the
# fake agent's records, sent out of its sandbox container -- and answers
# GET /step with DIR/../step.sh, the probe the walk's current item runs.
import http.server, os, sys
port, root = int(sys.argv[1]), sys.argv[2]
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        name = os.path.basename(self.path.strip("/")) or "unknown"
        with open(os.path.join(root, name + ".log"), "ab") as f:
            f.write(self.rfile.read(n))
        self.send_response(204); self.end_headers()
    def do_GET(self):
        try:
            body = open(os.path.join(root, "..", "step.sh"), "rb").read()
        except OSError:
            body = b""
        self.send_response(200); self.send_header("Content-Length", str(len(body))); self.end_headers()
        self.wfile.write(body)
    def log_message(self, *a): pass
http.server.ThreadingHTTPServer(("0.0.0.0", port), H).serve_forever()
