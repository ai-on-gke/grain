// repoParts reads a repo as grain spells it -- "owner/name", or
// "host/owner/name" for one off the deployment's default host -- into
// its parts, host "" for the default. It is model.ParseRepo's rule for
// the bare spellings: a three-part repo is always host-qualified.
export function repoParts(repo) {
  const parts = String(repo || "")
    .trim()
    .split("/");
  if (parts.length >= 3) {
    return {
      host: parts[0],
      owner: parts[1],
      name: parts.slice(2).join("/"),
    };
  }
  return { host: "", owner: parts[0] || "", name: parts[1] || "" };
}

// storedOwner is model.RepoRef.StoredOwner: the owner, prefixed with the
// host for a repo off the default host. It is what the server's {owner}
// path wildcard holds (model.RepoFromStored reads it back).
export function storedOwner(repo) {
  const { host, owner } = repoParts(repo);
  return host ? `${host}/${owner}` : owner;
}

// repoApiRoot is /api/repos/{owner}/{name} for repo, the root of every
// repo-scoped API route. A host-qualified repo's owner is
// "host/owner" escaped into the one {owner} segment, so
// git.forge.example/experimental/bsalmon-test is
// /api/repos/git.forge.example%2Fexperimental/bsalmon-test rather
// than a path the server reads as owner git.forge.example, name
// experimental.
export function repoApiRoot(repo) {
  const { name } = repoParts(repo);
  return `/api/repos/${encodeURIComponent(storedOwner(repo))}/${encodeURIComponent(name)}`;
}
