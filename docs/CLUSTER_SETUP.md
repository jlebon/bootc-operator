# Kubernetes Cluster Setup Guide

This guide explains how to create a Kubernetes cluster and add worker nodes using the VM-based development environment.

## Prerequisites

- Podman installed
- `bcvk` (bootc-virt-konverter) installed for converting bootc images to qcow2
- Sufficient resources (8GB RAM per VM minimum)

## Building Images

Before creating a cluster, build the required images and disk:

```bash
# Build all images and create the bootc disk image
make all
```

This will:
1. Build the cluster container image (includes libvirt, qemu, virt-install)
2. Build the fedora-bootc-k8s VM image (includes Kubernetes, CRI-O, cloud-init)
3. Convert the bootc image to a qcow2 disk

## Creating a New Cluster

To create a fresh Kubernetes cluster with a single control plane node:

```bash
make cluster-start
```

This automated process will:
1. Deploy the cluster container with `podman kube play`
2. Setup the libvirt network with DNS (*.k8s.local domain)
3. Create a VM named `node1` as the control plane
4. Initialize Kubernetes on node1 with `kubeadm init`
5. Install Calico CNI plugin for pod networking
6. Configure kubectl for the core user
7. Expose the API server on localhost:6443 (accessible from your host)
8. Generate kubeconfig at `./vm/kubeconfig`

The entire process takes 3-5 minutes. Once complete, you'll have a working single-node Kubernetes cluster accessible from both inside the VMs and from your host machine.

### Verifying the Cluster

Check that the cluster is running:

```bash
# SSH into the control plane node
./vm/ssh-vm.sh node1

# Inside the VM, check cluster status
kubectl get nodes
kubectl get pods -A
```

You should see:
- node1 in `Ready` status
- Calico pods running in `kube-system` namespace
- Core components (etcd, kube-apiserver, kube-controller-manager, kube-scheduler) running

## Accessing the Cluster from Your Host

After `make cluster-start` completes, the Kubernetes API server is automatically exposed on `localhost:6443` and a kubeconfig file is generated at `./vm/kubeconfig`.

To use `kubectl` from your host machine:

```bash
export KUBECONFIG=./vm/kubeconfig
kubectl get nodes
kubectl get pods -A
```

The API server certificate is configured with `localhost` and `127.0.0.1` as Subject Alternative Names (SANs), so you don't need to use `--insecure-skip-tls-verify`.

### How It Works

The cluster initialization automatically:
1. Configures the API server certificate to include `localhost` as a valid hostname
2. Creates an SSH tunnel inside the cluster container: `container:6443 -> VM:6443`
3. Exposes the container's port 6443 to your host via `hostPort` in `cluster.yaml`
4. Generates a kubeconfig file with `server: https://localhost:6443`

### Manual API Exposure

If the automatic exposure fails or you need to re-establish the connection:

```bash
./vm/expose-api.sh node1
```

This script will:
- Set up the SSH tunnel from the cluster container to the VM
- Generate/update the kubeconfig file at `./vm/kubeconfig`

## Adding Worker Nodes

To add worker nodes to the cluster:

```bash
# Add a worker node named node2
./vm/join-node.sh node2

# Add another worker node named node3
./vm/join-node.sh node3

# And so on...
./vm/join-node.sh node4
```

The `join-node.sh` script will:
1. Create a new VM with the specified name
2. Generate a fresh join token from the control plane (node1)
3. Wait for the VM to boot and cloud-init to complete
4. Execute `kubeadm join` on the new worker node
5. The node joins the cluster automatically

### Verifying Worker Nodes

After adding worker nodes, verify they joined successfully:

```bash
./vm/ssh-vm.sh node1
kubectl get nodes
```

You should see all nodes in `Ready` status:
```
NAME    STATUS   ROLES           AGE   VERSION
node1   Ready    control-plane   10m   v1.35.x
node2   Ready    <none>          2m    v1.35.x
node3   Ready    <none>          1m    v1.35.x
```

## VM Configuration Details

Each VM is created with:
- **Memory**: 8192 MB (8GB) - configurable in create-vm.sh
- **vCPUs**: 4 - configurable in create-vm.sh
- **User**: core (with sudo access)
- **SSH Key**: Shared cluster key at `/var/run/cluster/cluster.key`
- **Network**: 192.168.122.0/24 (libvirt default network)
- **DNS**: VM accessible via `<vm-name>.k8s.local`
- **Storage**: qcow2 overlay disks with copy-on-write

### Cloud-init Configuration

Each VM is automatically configured via cloud-init with:
- SSH key injection for the `core` user
- Swap disabled (required for Kubernetes)
- IP forwarding enabled
- br_netfilter kernel module loaded
- CRI-O and kubelet services enabled
- Kubelet configured to use `/var/lib/kubelet/volumeplugins` (bootc-compatible)

## Manual VM Creation

If you need to create a VM without joining the cluster:

```bash
./vm/create-vm.sh -n <vm-name>
```

This creates a VM ready for Kubernetes but doesn't run `kubeadm init` or `kubeadm join`.

## SSH Access to VMs

Access any VM using the helper script:

```bash
./vm/ssh-vm.sh <vm-name>
```

Or manually from the cluster container:

```bash
podman exec -ti cluster-cluster ssh -i /var/run/cluster/cluster.key core@<vm-name>.k8s.local
```

## Checking VM Health

Check cloud-init status:

```bash
./vm/ssh-vm.sh <vm-name>
cloud-init status --long
```

Check Kubernetes health:

```bash
./vm/ssh-vm.sh <vm-name>
kubectl get nodes
kubectl get pods -A
sudo systemctl status kubelet
sudo systemctl status crio
```

## Stopping the Cluster

To stop the entire cluster environment:

```bash
make cluster-stop
```

This stops and removes the cluster container. VMs are stopped as well since they run inside the container.

**Note**: VM overlay disks persist in `vm/images/`, so you can restart the cluster with existing VMs by running `make deploy setup-network` (without re-creating node1).

## Troubleshooting

### VM won't boot or cloud-init fails

Check the VM console:
```bash
podman exec -ti cluster-cluster virsh -c qemu:///session console <vm-name>
```

Press `Ctrl+]` to exit the console.

### SSH connection refused

Wait for cloud-init to complete (can take 30-60 seconds after VM creation):
```bash
./vm/ssh-vm.sh <vm-name>
cloud-init status
```

### Kubelet not starting

Check kubelet logs:
```bash
./vm/ssh-vm.sh <vm-name>
sudo journalctl -u kubelet -f
```

### Pods stuck in Pending

Check if Calico is running:
```bash
kubectl get pods -n kube-system | grep calico
```

If Calico pods are not running, reinstall:
```bash
kubectl apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.27.0/manifests/calico.yaml
```

### kubectl from host hangs or connection refused

The SSH tunnel may have stopped. Re-establish it:
```bash
./vm/expose-api.sh node1
export KUBECONFIG=./vm/kubeconfig
kubectl get nodes
```

Check if port 6443 is exposed:
```bash
podman ps
# Look for 0.0.0.0:6443->6443/tcp in the PORTS column
```

## Architecture Notes

- **Bootc immutability**: VMs use Fedora bootc with read-only `/usr` filesystem
- **Volume plugin dir**: Kubelet configured to use `/var/lib/kubelet/volumeplugins` instead of `/usr/libexec/kubernetes`
- **CRI-O CNI path**: Modified to use `/var/lib/cni/bin` instead of `/opt/cni/bin`
- **Persistent SSH keys**: Cluster key stored in `/var/run/cluster/` survives container restarts
- **Overlay disks**: Each VM uses qcow2 overlay with base image as backing file for efficient storage
- **API server access**: Exposed via SSH tunnel (container:6443 → VM:6443) and hostPort (localhost:6443 → container:6443)
- **API server certificates**: Configured with `localhost` and `127.0.0.1` as Subject Alternative Names for secure host access

## Quick Reference

```bash
# Create cluster with control plane
make cluster-start

# Use kubectl from host
export KUBECONFIG=./vm/kubeconfig
kubectl get nodes
kubectl get pods -A

# Add worker nodes
./vm/join-node.sh node2
./vm/join-node.sh node3

# SSH to nodes
./vm/ssh-vm.sh node1

# Check cluster status from inside VM
./vm/ssh-vm.sh node1
kubectl get nodes
kubectl get pods -A

# Re-expose API if needed
./vm/expose-api.sh node1

# Stop cluster
make cluster-stop

# Rebuild everything
make clean all
```
