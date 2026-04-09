# Kubernetes Cluster Setup Guide

## Prerequisites

- Podman installed
- `bcvk` (bootc-virt-konverter) for converting bootc images to qcow2
- 8GB RAM per VM minimum

## Quick Start

```bash
# Build images and create cluster
make all cluster-start

# Use kubectl from host
export KUBECONFIG=./vm/kubeconfig
kubectl get nodes

# Add worker nodes
./vm/join-node.sh node2
./vm/join-node.sh node3
```

## Creating a Cluster

Build and start a single-node cluster (takes 3-5 minutes):

```bash
make all          # Build cluster container and VM image
make cluster-start  # Initialize cluster on node1
```

The cluster is accessible from your host at `localhost:6443`. API server certificate includes `localhost` as a SAN, so TLS verification works without `--insecure-skip-tls-verify`.

**Verify cluster:**
```bash
export KUBECONFIG=./vm/kubeconfig
kubectl get nodes
kubectl get pods -A
```

## Adding Worker Nodes

```bash
./vm/join-node.sh node2
./vm/join-node.sh node3
```

The script creates a VM, generates a join token from node1, and executes `kubeadm join` automatically.

## VM Configuration

- **Resources**: 8GB RAM, 4 vCPUs (configurable in create-vm.sh)
- **User**: core (with passwordless sudo)
- **Network**: 192.168.122.0/24, DNS at `<vm-name>.k8s.local`
- **Storage**: qcow2 overlay disks (copy-on-write)
- **Cloud-init**: SSH key injection, swap disabled, CRI-O/kubelet enabled

## Common Operations

**SSH into VM:**
```bash
./vm/ssh-vm.sh <vm-name>
```

**Create VM without joining cluster:**
```bash
./vm/create-vm.sh -n <vm-name>
```

**Re-expose API server:**
```bash
./vm/expose-api.sh node1
```

**Stop cluster:**
```bash
make cluster-stop  # VM disks persist in vm/images/
```

## Troubleshooting

| Issue | Solution |
|-------|----------|
| VM won't boot | `podman exec -ti cluster-cluster virsh console <vm-name>` (Ctrl+] to exit) |
| SSH refused | Wait 30-60s for cloud-init: `./vm/ssh-vm.sh <vm-name>; cloud-init status` |
| Kubelet failing | `./vm/ssh-vm.sh <vm-name>; sudo journalctl -u kubelet -f` |
| Pods pending | Check Calico: `kubectl get pods -n kube-system \| grep calico` |
| kubectl hangs | Re-expose API: `./vm/expose-api.sh node1` |

## Architecture Notes

- **Bootc**: Fedora bootc with read-only `/usr`, kubelet uses `/var/lib/kubelet/volumeplugins`
- **API access**: SSH tunnel (container:6443 → VM:6443) + hostPort (localhost:6443 → container:6443)
- **Storage**: qcow2 overlay disks with shared base image
