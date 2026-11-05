// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

const SAFE_REV_RE = /^[A-Za-z0-9._/-]+$/;

function parseRepoWebBase(repo, config) {
  if (!repo || typeof repo !== "string") return "";
  const parts = repo.split("/").filter(Boolean);
  if (parts.length === 2) {
    const defaultHost = (config?.githubHost || "github.com").trim();
    if (!defaultHost) return "";
    return `https://${defaultHost}/${parts[0]}/${parts[1]}`;
  }
  if (parts.length === 3 && parts[0].includes(".")) {
    return `https://${parts[0]}/${parts[1]}/${parts[2]}`;
  }
  return "";
}

function encodeRepoPath(relPath) {
  return String(relPath || "")
    .split("/")
    .filter(Boolean)
    .map((seg) => encodeURIComponent(seg))
    .join("/");
}

function contextTreeHref(item, config) {
  if (!item?.repo || !item?.rev || !SAFE_REV_RE.test(item.rev)) return "";
  const base = parseRepoWebBase(item.repo, config);
  if (!base) return "";
  const encodedRev = encodeURIComponent(item.rev);
  const encodedPath = encodeRepoPath(item.path);
  return encodedPath
    ? `${base}/tree/${encodedRev}/${encodedPath}`
    : `${base}/tree/${encodedRev}`;
}

function isSafeHttpUrl(raw) {
  if (!raw || typeof raw !== "string") return false;
  try {
    const parsed = new URL(raw);
    return parsed.protocol === "http:" || parsed.protocol === "https:";
  } catch {
    return false;
  }
}

export default function CommittedContext({ t, config }) {
  const items = Array.isArray(t?.context) ? t.context : [];
  if (items.length === 0) return null;

  return (
    <fieldset
      id="committed-context"
      className="committed-context"
      data-testid="committed-context"
    >
      <legend>Committed context</legend>
      <ul className="committed-context-list">
        {items.map((item, idx) => {
          const isRead = item.placement === "read";
          const displayRef =
            isRead && item.repo ? `${item.repo}:${item.path}` : item.path;
          const shortRev = item.rev ? item.rev.slice(0, 10) : "";
          const href = contextTreeHref(item, config);
          const manifest = item.manifest || null;
          const files = Array.isArray(manifest?.files) ? manifest.files : [];

          return (
            <li
              key={`${item.placement || "target"}:${item.repo || ""}:${item.path}:${item.rev || idx}`}
              className="committed-context-item"
            >
              <div className="committed-context-header">
                <span
                  className={
                    isRead
                      ? "committed-context-badge is-read"
                      : "committed-context-badge is-target"
                  }
                >
                  {isRead ? "read-only repo" : "target repo"}
                </span>
                {shortRev &&
                  (href ? (
                    <a
                      className="committed-context-sha"
                      href={href}
                      target="_blank"
                      rel="noopener noreferrer"
                      title={`Pinned commit ${item.rev}`}
                    >
                      @{shortRev}
                    </a>
                  ) : (
                    <code
                      className="committed-context-sha"
                      title={`Pinned commit ${item.rev}`}
                    >
                      @{shortRev}
                    </code>
                  ))}
              </div>
              <div className="committed-context-path">
                <code>{displayRef}</code>
              </div>
              {manifest &&
                (manifest.title ||
                  manifest.description ||
                  files.length > 0) && (
                  <div className="committed-context-manifest">
                    {manifest.title && (
                      <div className="committed-context-manifest-title">
                        {manifest.title}
                      </div>
                    )}
                    {manifest.description && (
                      <div className="committed-context-manifest-desc">
                        {manifest.description}
                      </div>
                    )}
                    {files.length > 0 && (
                      <ul className="committed-context-files">
                        {files.map((f, fIdx) => (
                          <li
                            key={`${f.path}:${fIdx}`}
                            className="committed-context-file"
                          >
                            <code>{f.path}</code>
                            {f.kind && (
                              <span className="committed-context-file-kind">
                                {f.kind}
                              </span>
                            )}
                            {f.title && (
                              <span className="committed-context-file-title">
                                {f.title}
                              </span>
                            )}
                            {isSafeHttpUrl(f.source_url) && (
                              <a
                                className="committed-context-file-source"
                                href={f.source_url}
                                target="_blank"
                                rel="noopener noreferrer"
                              >
                                source
                              </a>
                            )}
                          </li>
                        ))}
                      </ul>
                    )}
                  </div>
                )}
            </li>
          );
        })}
      </ul>
    </fieldset>
  );
}
