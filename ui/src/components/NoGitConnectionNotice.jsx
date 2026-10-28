import { Alert, Link } from "@mui/material";
import { buildPath } from "../paths.js";
import { hasAnyGitConnection } from "../gitConnections.js";

// NoGitConnectionNotice is the "add a git connection first" state of a
// place that picks a repository -- the New task dialog's Repos field and
// the Repositories page's Add repo picker. A fresh install (a Cloudtop's
// among them, since it stopped seeding depot) starts with no git
// connection at all, and a picker with nothing to discover and a task
// that only waits read as broken unless somebody says why. It renders
// nothing once the deployment has a connection on any host.
//
// deployment is config.deployment, the deployment's own settings, where
// its git connections are kept. The link opens Settings → Git, where
// Add connection is (Team settings' Git identity tab until stage 3e-ii).
// onOpenSettings, when given, opens it in place; otherwise the link
// navigates there.
export default function NoGitConnectionNotice({
  deployment,
  onOpenSettings,
  sx,
}) {
  if (!deployment || hasAnyGitConnection(deployment)) return null;
  const href = buildPath({ view: "settings", settingsSection: "git" });
  return (
    <Alert severity="info" sx={sx} data-testid="no-git-connection">
      This deployment has no git connection yet, so its tasks cannot clone or
      push a repository and wait until it has one. Add a git connection first,
      in{" "}
      <Link
        href={href}
        onClick={(evt) => {
          if (
            !onOpenSettings ||
            evt.button !== 0 ||
            evt.metaKey ||
            evt.ctrlKey ||
            evt.shiftKey ||
            evt.altKey
          )
            return;
          evt.preventDefault();
          onOpenSettings("git");
        }}
      >
        Settings → Git
      </Link>
      : a GitHub token or GitHub App for github.com, or an ambient login for
      another git host.
    </Alert>
  );
}
