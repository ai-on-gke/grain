// Package protocol implements Grain's side of the capability control-plane
// protocol specified in docs/capabilities-spec.md (the author's guide is
// docs/capabilities.md): the target architecture in which a capability is
// an MCP server configured under mcpServers in grain/config, advertising a
// mint_<method> tool per capability method and a release_<method> tool for
// each stateful one.
//
// What is here is the protocol, independent of any sandbox backend:
//
//   - capability references and their grammar (ParseRef, §2.1) and the
//     single-method shorthand (Server.Resolve, §2.1.4);
//   - the mcpServers entry shape, its validation, $secret:<name> resolution
//     and the stdio server's isolated environment (ServerConfig, §3.1);
//   - discovery of methods from tools/list (Discover, §4.1);
//   - validation of with: arguments against a method's args schema, with
//     defaults applied (ValidateArgs, §4.2.1);
//   - grants: the bare-string and object forms, normalization,
//     deduplication and attempt_id assignment (ParseGrant, Normalize, §2.2,
//     §2.3);
//   - an MCP client over stdio and Streamable HTTP (Dial, §4.0);
//   - the mint response, checked against every limit of §4.3.3 and §5
//     (ParseMintResponse);
//   - the admission pass that mints every grant of an attempt in order,
//     rolls it back on any failure and checks the responses against one
//     another before a sandbox boots (MintPass, §4.2.3, §5.1.2, §5.3.1,
//     §5.5.2), and the attempt's release (Pass.Release, §4.4);
//   - the server side, for capabilities written in Go: Method turns a mint
//     and release function into the mint_/release_ tool pair, idempotent
//     per attempt_id and expiring at deadline (§4.3.5, §4.4.2).
//
// Materializing a pass into a sandbox -- forwarding ports, writing
// placements, running setup commands, wiring mcp_endpoint into an agent
// CLI -- is the orchestrator's and the sandbox backend's job; this package
// hands them a validated, collision-free Pass to do it from.
package protocol
