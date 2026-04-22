---
name: bink
description: Interact with a running bink Kubernetes cluster. Use this skill whenever the user asks to run kubectl commands, check node/pod status, inspect Kubernetes resources, SSH into a bink VM, run commands inside a bink cluster node, debug cluster issues, or interact with the bink development cluster in any way. Also use when the user mentions "bink cluster", "k8s cluster", "kubectl", or asks about pods, deployments, services, nodes, or any Kubernetes resource in the context of this project.
argument-hint: [kubectl command or shell command to run in the cluster]
allowed-tools: [Bash]
---

# Bink Cluster Interaction

This skill provides instructions for interacting with a running bink Kubernetes cluster.

## Architecture

A bink cluster runs as one or more Podman containers, each hosting a libvirt VM running a bootc-based Fedora image with Kubernetes. The container names follow the pattern `k8s-podman-<nodename>` (e.g., `k8s-podman-node1`). The VM inside each container runs SSH on port 22, which is port-forwarded to port 2222 on the container's localhost via passt.

## How to run commands

### Discovering cluster containers

```bash
podman ps --filter "name=k8s-podman" --format "{{.Names}}"
```

### Running kubectl commands

To run kubectl on a cluster node, SSH into the VM from inside the container. The SSH user is `core` (not root), and sudo is needed for kubectl:

```bash
podman exec <container-name> ssh -i /run/cluster/cluster.key \
  -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
  -p 2222 core@localhost \
  "sudo kubectl --kubeconfig=/etc/kubernetes/admin.conf <kubectl-args>"
```

Example for listing nodes:
```bash
podman exec k8s-podman-node1 ssh -i /run/cluster/cluster.key \
  -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
  -p 2222 core@localhost \
  "sudo kubectl --kubeconfig=/etc/kubernetes/admin.conf get nodes -o wide"
```

### Running arbitrary commands on the VM

```bash
podman exec <container-name> ssh -i /run/cluster/cluster.key \
  -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
  -p 2222 core@localhost \
  "<command>"
```

Use `sudo` for commands requiring root privileges (e.g., `bootc`, `journalctl`, reading files under `/etc/kubernetes/`).

### Running commands inside the container (not the VM)

For libvirt/container-level operations:
```bash
podman exec <container-name> virsh list --all
podman exec <container-name> virsh domifaddr <vm-name> --source agent
```

## Connection details

| Parameter | Value |
|-----------|-------|
| SSH key | `/run/cluster/cluster.key` (inside container) |
| SSH user | `core` |
| SSH port | `2222` (passt-forwarded to VM port 22) |
| SSH host | `localhost` (from inside container) |
| kubeconfig | `/etc/kubernetes/admin.conf` (inside VM, requires sudo) |
| Container name pattern | `k8s-podman-<nodename>` |
| VM name | matches the node name (e.g., `node1`) |

## Handling arguments

If the user provides arguments with `/bink`, treat them as a command to run inside the cluster VM. If the argument starts with `kubectl`, wrap it with the full SSH+sudo pattern above. Otherwise, run it as a shell command via SSH.

If no arguments are provided, list the cluster containers and show node status.

## Tips

- The guest agent (qemu-ga) runs under SELinux context `virt_qemu_ga_t` which blocks access to most files and network. Always use SSH instead.
- For long-running commands, use a timeout (e.g., `--connect-timeout` for curl inside the VM).
- The K8s API server is also exposed on the host via a mapped port (check `podman ps` for the port mapping to 6443), but requires client certificates for authentication. SSH+kubectl is simpler.
- Multi-node clusters will have multiple `k8s-podman-*` containers. Run kubectl on the control-plane node (typically `node1`).
