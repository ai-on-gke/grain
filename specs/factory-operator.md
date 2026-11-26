---
name: factory-operator
deps:
  - reverse-proxy
  - agent-image
---

# Factory Operator

## Overview

A Kubernetes controller that implements a CRD-based interface for controlling the factory. It launches Pods running an agent harness to execute work.

## Goals

- Implement a pure Go Kubernetes controller.
- Use a single multidoc YAML configuration file loaded locally.
- Launch secure, isolated agent Pods using the `gvisor` runtime.
- Provision and manage storage for agent work using PVCs.

## Non-Goals

- Implementation of the reverse proxy itself.
- Implementation of the agent container image itself.
- Applying configuration resources directly to a Kubernetes API server.

## Key Requirements

- Everything must be written in pure Go as much as possible, following excellent Go style.
- The command `factory operator` should be implemented under a new root-level `factory/` directory, similar to the `tool/` directory layout.
- The operator accepts a single flag `--config` pointing to a multidoc YAML file containing Kubernetes Resource Model resources (e.g., `FactoryConfig`, `OperatorConfig`).
- Launched Pods must use the `gvisor` runtime class and strict network isolation.
- Pods must run the agent with intercepted traffic via a local reverse proxy, configured entirely via files on disk to prevent K8s API access.

## Design

The `factory operator` will act as a standalone binary that watches or processes local configuration to orchestrate agent jobs as Kubernetes Pods.

1. **Command Structure**: 
   A new command hierarchy under `factory/` will be created (e.g., `factory/cmd/factory/factory.go` or `factory/operator`).
2. **Configuration Loader**: 
   Parse the file specified by `--config` as a stream of YAML documents, decoding them into strongly typed Go structs representing resources like `FactoryConfig` and `OperatorConfig`. No other command-line flags will be supported for the operator.
3. **Pod Provisioning**:
   The operator generates Pod manifests representing the agent harness. This includes:
   - `runtimeClassName: gvisor`
   - **Setup Container**: An init container setting up iptables rules to intercept all inbound/outbound traffic from the runtime container and route it to the reverse proxy.
   - **Reverse Proxy Container**: A Go reverse proxy sidecar (init container with `restartPolicy: Always`) to enforce local network policy and inject credentials on the wire.
   - **Runtime Container**: An arbitrary agent runtime (e.g., `gemini-cli`) container, with `restartPolicy: Always`.
   - **Auxiliary Volume**: A mounted volume (e.g., PVC with GCE PD, default ~100GB disk) used as the working directory, managed by the operator.
4. **Configuration Delivery**:
   The operator will deliver standard configuration to each component using Kubernetes Resource Model (KRM) formatted YAML files on disk, ensuring the Pod needs zero access to the Kubernetes API.
5. **Network Security**:
   The operator will ensure a strict `NetworkPolicy` is applied to the Pod, blocking all access to the K8s API, lateral Pod-to-Pod and Pod-to-Node communication, and cluster DNS. Egress is permitted exclusively to the external internet and 8.8.8.8 for DNS, compatible with GCP Cloud NAT.

## Configuration Example

An example of configuration for the Factory Operator delivered to disk:

```yaml
apiVersion: factory.gke.io/v1alpha1
kind: OperatorConfig
metadata:
  name: factory-operator-config
spec:
  podNamespace: "agents"
  storage:
    storageClassName: "standard-rwo"
    defaultCapacity: "100Gi"
```

## Tests

- **Unit Tests**: Full unit test coverage for the configuration parser, ensuring valid and invalid multidoc YAML files are handled correctly.
- **Manifest Generation Tests**: Verify that the generated Pod manifests include the `gvisor` runtime class, the proper init containers, and the correct volume mounts.
- **Style and Standard**: Tests must be as rigorous and well-structured as those in the Go standard library.
