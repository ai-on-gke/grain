package cli

// This deployment's model proxies: one more listener per vendor beside
// the git proxy, serving pkg/modelproxy to whatever is running inside a
// sandbox.
//
// They are published the way startGitProxy is, through the one Fabric
// every sandbox-facing service goes through (sandboxnet.go), and
// deliberately no more cleverly than that: one port each, wherever that
// fabric puts them. A guest can reach its own subnet -- that is how a
// clone works at all (see -granule-git-proxy-host's own doc comment) --
// so more ports on the same address need no route, no DNAT and no change
// to any sandbox image.
//
// Going through the fabric rather than reading an address is what fixed
// the bug startModelProxy's own comment describes: these three were the
// one service that asked cfg.granuleGitProxyHost instead of
// sandboxAdvertiseHost, so on a container or Kubernetes deployment they
// were the only listeners a sandbox could not reach.
//
// A port per vendor rather than one listener multiplexing them: the
// whole promise of this proxy is that what is behind it is the vendor's
// own API at the vendor's own paths, and a shared listener would have to
// carve a prefix out of that path space and hope no vendor ever ships an
// endpoint under it. A listening socket is cheap; a base URL that is not
// quite the vendor's is a bug in every client that meets it.
