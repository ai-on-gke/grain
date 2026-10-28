package model

// DefaultTeamID is the identifier of the built-in default team: the one
// team the API's team shape and an old client's `team` may still name.
// The team table went at schema 24, its settings onto grain_config, and
// the team columns other rows carried at 25 (docs/single-tenant-reset.md,
// 6-ii-b5).
const DefaultTeamID = "default"
