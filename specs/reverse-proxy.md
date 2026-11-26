---
name: reverse-proxy
deps:
---

# Reverse Proxy

## Overview

A TLS-terminating MITM reverse proxy that acts as the sole external gateway for agent Pods, intercepting, authenticating, and observing their requests.

## Goals

- Securely host credentials on behalf of the agent, keeping them invisible to the agent Pod.
- Provide a secure, isolated channel for necessary external access, specifically an LLM API.
- Support Model Context Protocol (MCP) tools for operations like creating Pull Requests.
- Allow task-specific or sandbox-specific security policies.

## Non-Goals

- General-purpose HTTP proxying for unrestricted internet access.
- Complex routing outside of predefined upstream targets.

## Key Requirements

- Pure Go implementation, following excellent Go style (e.g., potentially reusing or similar to `gke-labs/service-portals`).
- Must run locally as a "sidecar" container (e.g., an init container with `restartPolicy: Always`) within the agent Pod.
- Must terminate TLS to inspect and intercept egress traffic from the runtime container.
- Must allow direct proxying to LLM APIs (e.g., `https://generativelanguage.googleapis.com`).
- All other operations must go through MCP tools served by the reverse proxy.
- Security policies must be configurable per task/sandbox via KRM resources delivered on disk.
- Must securely host and inject credentials (e.g., GitHub tokens) on the wire without exposing them to the agent.

## Design

1. **Proxy Server Architecture**:
   - A Go-based HTTP/HTTPS server using `net/http` and custom TLS configuration to act as a MITM proxy.
   - Runs as an init container with `restartPolicy: Always` alongside the agent runtime.
   - Generates and serves custom certificates for the agent Pods to trust, enabling TLS termination.
2. **Traffic Routing and Interception**:
   - Transparently receives traffic redirected by the setup container's iptables rules.
   - Maintains an allowlist of external domains (e.g., `generativelanguage.googleapis.com`). Traffic to these domains is logged and forwarded, potentially injecting API keys stored securely in the proxy.
   - For all other requests, particularly those interacting with systems like GitHub, the proxy blocks direct access and instead serves or routes traffic to MCP tools.
3. **Credential Management**:
   - The proxy handles authentication on behalf of the agent. For example, if an MCP tool initiates a GitHub PR, the proxy uses its securely stored tokens to authenticate the request on the wire.
4. **Policy Configuration**:
   - The proxy reads KRM configuration from disk defining the security policy for a given sandbox, restricting the tools and endpoints available to that specific agent session.

## Configuration Example

An example of configuration for the Reverse Proxy delivered to disk:

```yaml
apiVersion: factory.gke.io/v1alpha1
kind: ReverseProxyConfig
metadata:
  name: sandbox-proxy-policy
spec:
  allowlist:
    - "generativelanguage.googleapis.com"
  mcpTools:
    - name: "github-pr-creator"
      enabled: true
  credentials:
    githubTokenSecretRef: "sandbox-github-token"
```

## Tests

- **Unit Tests**: Coverage for request routing, TLS certificate generation, and header injection.
- **Proxy Simulation**: Tests establishing a local TLS connection through the proxy to ensure credentials are injected correctly and unauthorized domains are blocked.
- **Style and Standard**: High-quality, Go-standard-library level testing for all logic.
