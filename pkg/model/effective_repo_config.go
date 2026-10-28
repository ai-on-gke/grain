package model

import "context"

// EffectiveRepoConfig is the resolved configuration for a repository:
// its RepoConfig over the deployment's Config defaults. Until schema 23
// the default team's team_repo_config layered over both; that table is
// folded into repo_config now (docs/single-tenant-reset.md, 6-ii-b3).
type EffectiveRepoConfig struct {
	Repo                  RepoRef  `json:"repo"`
	CloneURL              string   `json:"cloneUrl,omitempty"`
	DefaultCapabilities   []string `json:"defaultCapabilities,omitempty"`
	CICapabilities        []string `json:"ciCapabilities,omitempty"`
	PromptExtension       string   `json:"promptExtension,omitempty"`
	SetupCommand          string   `json:"setupCommand,omitempty"`
	CIMode                CIMode   `json:"ciMode,omitempty"`
	GitCredential         string   `json:"gitCredential,omitempty"`
	RepairTemplateID      string   `json:"repairTemplateId,omitempty"`
	RequireBranchUpToDate bool     `json:"requireBranchUpToDate,omitempty"`
	WatchDefaultBranch    bool     `json:"watchDefaultBranch,omitempty"`
	ReleaseSecrets        []string `json:"releaseSecrets,omitempty"`
}

// Empty returns true if all configuration fields are zero-valued.
func (c EffectiveRepoConfig) Empty() bool {
	return c.CloneURL == "" &&
		len(c.DefaultCapabilities) == 0 &&
		len(c.CICapabilities) == 0 &&
		c.PromptExtension == "" &&
		c.SetupCommand == "" &&
		c.CIMode == "" &&
		c.GitCredential == "" &&
		c.RepairTemplateID == "" &&
		!c.RequireBranchUpToDate &&
		!c.WatchDefaultBranch &&
		len(c.ReleaseSecrets) == 0
}

// EffectiveRepoConfigFor resolves the effective configuration from the
// repository's own configuration and the deployment's settings.
// Fallback hierarchy: RepoConfig -> global Config / defaults.
func EffectiveRepoConfigFor(global Config, repoCfg *RepoConfig) EffectiveRepoConfig {
	var eff EffectiveRepoConfig
	if repoCfg != nil {
		eff.Repo = repoCfg.Repo
		eff.CloneURL = repoCfg.CloneURL
		eff.CICapabilities = append([]string(nil), repoCfg.CICapabilities...)
		eff.SetupCommand = repoCfg.SetupCommand
		eff.GitCredential = repoCfg.GitCredential
		eff.RepairTemplateID = repoCfg.RepairTemplateID
		eff.RequireBranchUpToDate = repoCfg.RequireBranchUpToDate
		eff.WatchDefaultBranch = repoCfg.WatchDefaultBranch
		eff.ReleaseSecrets = append([]string(nil), repoCfg.ReleaseSecrets...)
		eff.CIMode = repoCfg.CIMode
		if eff.CIMode == "" && repoCfg.IgnoreGitHubCI {
			eff.CIMode = CIModeGrain
		}
	}
	if len(eff.CICapabilities) == 0 {
		eff.CICapabilities = nil
	}
	if len(eff.ReleaseSecrets) == 0 {
		eff.ReleaseSecrets = nil
	}

	// DefaultCapabilities
	if repoCfg != nil && len(repoCfg.DefaultCapabilities) > 0 {
		eff.DefaultCapabilities = append([]string(nil), repoCfg.DefaultCapabilities...)
	} else if len(global.DefaultCapabilities) > 0 {
		eff.DefaultCapabilities = append([]string(nil), global.DefaultCapabilities...)
	}

	// PromptExtension
	if repoCfg != nil && repoCfg.PromptExtension != "" {
		eff.PromptExtension = repoCfg.PromptExtension
	} else if global.PromptExtension != "" {
		eff.PromptExtension = global.PromptExtension
	}
	return eff
}

// EffectiveRepoConfig returns the resolved configuration for a
// repository: its RepoConfig over the deployment's Config defaults.
func (s *Store) EffectiveRepoConfig(ctx context.Context, repo RepoRef) (EffectiveRepoConfig, error) {
	var global Config
	if cfg, err := s.GetConfig(ctx); err != nil {
		return EffectiveRepoConfig{}, err
	} else if cfg != nil {
		global = *cfg
	}
	repoCfg, err := s.GetRepoConfig(ctx, repo)
	if err != nil {
		return EffectiveRepoConfig{}, err
	}
	return EffectiveRepoConfigFor(global, repoCfg), nil
}
