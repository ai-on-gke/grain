# CLI release assets

Container images for Grain deployments and sandboxes are published to the
container registry (`docker.io/grainai/*`), which `scripts/setup.sh`,
`scripts/install-grain.sh`, and `pkg/upgrade` pull from directly.

## The grain CLI

`vX.Y.Z` and `vX.Y.Z-rc.N` GitHub releases carry the standalone `grain` CLI
archives that the daemon's `GET /cli/install.sh` (`pkg/ui/cli_install.sh`)
downloads onto operator workstations:

- `grain-linux-amd64.tar.gz`, `grain-linux-arm64.tar.gz`,
  `grain-darwin-amd64.tar.gz`, `grain-darwin-arm64.tar.gz`, each holding one
  file, `grain`, built by `scripts/build-cli.sh` from the release's tag and
  stamped with the release (`pkg/version.Release`), as its daemon is;
- `checksums.txt`, one `sha256sum` line (`<64 hex>  <archive>`) per archive,
  naming exactly those four. The install script checks the archive it
  downloads against `checksums.txt` before extracting.

`scripts/release-assets.sh cli-validate DIR` checks that a build directory
holds those four archives and `checksums.txt`, and `scripts/release-assets.sh
cli-publish <release> DIR` uploads the archives and then `checksums.txt` to an
existing GitHub release. A release carrying `checksums.txt` is not overwritten;
partial archive uploads left without `checksums.txt` are replaced.

## `grain-agentd`

`vX.Y.Z` and `vX.Y.Z-rc.N` GitHub releases also carry per-platform static
(`CGO_ENABLED=0`) `grain-agentd` binaries and their signed manifests
([agentd-release-signing.md](agentd-release-signing.md)):

- `grain-agentd-linux-amd64` and `grain-agentd-linux-amd64.manifest.json`
- `grain-agentd-linux-arm64` and `grain-agentd-linux-arm64.manifest.json`

`scripts/build-agentd.sh` builds both binaries stamped with the release and
writes their manifests (`version`, `platform`, `sha256`, `size`,
`min_contract_features`, `features`). In the release workflows, the `agentd`
job builds them with `contents: read` and no secrets, and `agentd-assets` signs
the manifests with `AGENTD_RELEASE_SIGNING_KEY` (`scripts/release-assets.sh
agentd-sign`), checks them (`agentd-validate`), and uploads the binaries
followed by the manifests (`agentd-publish`). The controller image also bundles
`/usr/local/share/grain/agentd` and serves `/api/hostd/agentd/linux/<arch>` and
`/api/hostd/agentd/linux/<arch>/manifest.json` for hosts that cannot reach
GitHub.
