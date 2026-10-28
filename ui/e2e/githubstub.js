// A stand-in for the GitHub REST API, for walkthroughs that start
// `grain demo -github-api 127.0.0.1:PORT`: the only boundary those
// walkthroughs stub. It answers the calls a team credential's Test button
// (githubtoken.Probe), the reach listing (gitproxy.Reach) and the New task
// pickers make -- /rate_limit, /user, /user/repos and /repos/{owner}/{name}
// -- per token, from a table the test owns and may change mid-walk (to
// grant or revoke access the way a GitHub admin would).
//
// Behaviours a token can have:
//   - { repos: { "acme/app": "push", "acme/lib": "read" }, login, scopes }
//   - { sso: true }        403 with X-GitHub-SSO, as an org enforcing SAML SSO
//   - { rateLimited: true } 403 with X-RateLimit-Remaining: 0 and a reset
//   - absent from the table: 401 Bad credentials, as a revoked token is.
//
// Every request is recorded with its Authorization header redacted, so a
// spec can count calls without holding a token anywhere it might print.
import http from "node:http";

export async function startGitHubStub(tokens) {
  const calls = [];
  const server = http.createServer((req, res) => {
    const url = new URL(req.url, "http://stub");
    const token = (req.headers.authorization || "").replace(
      /^(token|Bearer) /,
      "",
    );
    calls.push({ method: req.method, path: url.pathname });
    const send = (status, body, headers = {}) => {
      res.writeHead(status, {
        "Content-Type": "application/json",
        ...headers,
      });
      res.end(JSON.stringify(body));
    };
    const who = tokens[token];
    if (!who) {
      send(401, {
        message: "Bad credentials",
        documentation_url: "https://docs.github.com/rest",
      });
      return;
    }
    if (who.sso) {
      send(
        403,
        {
          message:
            "Resource protected by organization SAML enforcement. You must grant your Personal Access token access to this organization.",
        },
        {
          "X-GitHub-SSO":
            "required; url=https://github.com/orgs/acme/sso?authorization_request=stub",
        },
      );
      return;
    }
    if (who.rateLimited) {
      send(
        403,
        { message: "API rate limit exceeded for user ID 1." },
        {
          "X-RateLimit-Limit": "5000",
          "X-RateLimit-Remaining": "0",
          "X-RateLimit-Reset": String(Math.floor(Date.now() / 1000) + 3600),
        },
      );
      return;
    }
    const repos = who.repos || {};
    if (url.pathname === "/rate_limit") {
      send(200, {
        resources: { core: { limit: 5000, remaining: 4990, reset: 0 } },
      });
      return;
    }
    if (url.pathname === "/user") {
      send(
        200,
        { login: who.login || "octo-dev" },
        {
          "X-OAuth-Scopes": (who.scopes || ["repo", "workflow"]).join(", "),
          "GitHub-Authentication-Token-Expiration": "2026-12-31 00:00:00 UTC",
        },
      );
      return;
    }
    if (url.pathname === "/user/repos") {
      const page = Number(url.searchParams.get("page") || "1");
      const list =
        page > 1
          ? []
          : Object.entries(repos).map(([fullName, access]) => ({
              full_name: fullName,
              permissions: { pull: true, push: access === "push" },
            }));
      send(200, list);
      return;
    }
    const repo = url.pathname.match(/^\/repos\/([^/]+)\/([^/]+)$/);
    if (repo) {
      const access = repos[`${repo[1]}/${repo[2]}`];
      if (!access) {
        send(404, { message: "Not Found" });
        return;
      }
      send(200, {
        full_name: `${repo[1]}/${repo[2]}`,
        permissions: { pull: true, push: access === "push" },
      });
      return;
    }
    send(404, { message: `githubstub: no answer for ${url.pathname}` });
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  return {
    hostPort: `127.0.0.1:${server.address().port}`,
    tokens,
    calls,
    close: () => new Promise((resolve) => server.close(resolve)),
  };
}
