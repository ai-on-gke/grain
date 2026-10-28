// What an agent commits as when its git credential names nobody: the
// server's mcp.DefaultGitIdentityName and DefaultGitIdentityEmail
// (pkg/mcp/git_credentials.go). There is no deployment-wide identity
// between the two any more (f386abe49), and GET /api/settings reports
// none, so the UI names grain's own rather than reading it from there.
//
// That is only the fallback, though: a run commits as the account its
// git credential acts as -- a PAT's own GitHub login, a GitHub App's bot
// account, an ambient connection's host login -- and each connection's
// status carries that identity as commitName/commitEmail
// (components/settings/GitConnections.jsx, commitIdentities). Only a credential that names nobody (a plain git
// host, an unpaired SSH key) commits as this.
export const DEFAULT_GIT_IDENTITY_NAME = "grain agent";
export const DEFAULT_GIT_IDENTITY_EMAIL = "grain-agent@localhost";
