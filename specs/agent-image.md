---
name: agent-image
deps:
---

# Agent Image

## Overview

The container image containing `gemini-cli` and the necessary environment to execute work as an agent inside a secure Kubernetes Pod.

## Goals

- Provide a consistent, minimal execution environment.
- Package `gemini-cli` alongside necessary code manipulation and testing tools.
- Ensure compatibility with strict network and runtime constraints.

## Non-Goals

- Packaging unnecessary tools or generic utilities not required for agent tasks.
- Managing its own credentials.

## Key Requirements

- Assume execution on GKE within a `gvisor` runtime as the "Runtime" container in a multi-container Pod.
- Image will have all external traffic intercepted by a local reverse proxy sidecar via iptables rules set up by an init container.
- Must trust the custom CA certificates provided by the reverse proxy.
- Must lack direct access to credentials; all privileged operations must rely on the reverse proxy or MCP tools.
- Receive configuration, prompts, context, and tools via KRM resources delivered on disk.

## Design

1. **Base Image**:
   - Use a secure, minimal base image (e.g., Alpine or a minimal Debian-based image) to reduce attack surface.
2. **Tooling Integration**:
   - Install the `gemini-cli` binary and its prerequisites.
   - Include standard utilities needed for codebase interaction (e.g., `git`, language runtimes like Go, Python, or Node.js depending on the factory's target projects).
3. **Execution Environment**:
   - The image runs as a standard runtime container with `restartPolicy: Always`.
   - Uses the auxiliary volume mounted at a designated path (e.g., `/workspace`) for executing tasks.
   - Trusts the reverse proxy's custom CA certificate, ensuring seamless TLS connections to intercepted endpoints.

## Configuration Example

An example of configuration for the Agent Runtime delivered to disk:

```yaml
apiVersion: factory.gke.io/v1alpha1
kind: AgentRuntimeConfig
metadata:
  name: gemini-cli-runtime
spec:
  prompt: |
    Execute the following task against the codebase in /workspace.
  tools:
    - name: mcp-github
      endpoint: "http://localhost:8080/mcp"
```

## Tests

- **Image Build Tests**: Validate the Dockerfile builds successfully and passes vulnerability scans.
- **Execution Tests**: Verify that `gemini-cli` and standard tools (like `git`) are executable and in the `PATH`.
- **Certificate Loading**: Tests confirming that if a CA certificate is injected (e.g., via volume mount in the Pod), it is correctly loaded into the trust store by the image's initialization sequence.
