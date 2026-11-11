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
