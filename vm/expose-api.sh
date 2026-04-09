#!/bin/bash

# Source common functions
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

VM_NAME="${1:-node1}"
KUBECONFIG_PATH="${2:-./vm/kubeconfig}"

echo "=== Exposing API server to localhost:6443 ==="

# Get VM IP
VM_IP=$(get_vm_ip "$VM_NAME")

if [ -z "$VM_IP" ]; then
    echo "Error: Could not get IP for $VM_NAME"
    exit 1
fi

echo "VM IP: $VM_IP"

# Check if SSH tunnel is already running
if podman exec cluster-cluster ss -tln 2>/dev/null | grep -q ':6443'; then
    echo "Port 6443 is already forwarded"
else
    echo "Starting SSH port forwarding: container:6443 -> $VM_IP:6443"

    # Use SSH to forward port 6443 from container to VM
    # -N = no command, -L = local forward
    podman exec -d cluster-cluster ssh -N -L 0.0.0.0:6443:${VM_IP}:6443 \
        -o StrictHostKeyChecking=no \
        -o UserKnownHostsFile=/dev/null \
        -o ServerAliveInterval=60 \
        -i /var/run/cluster/cluster.key \
        core@${VM_NAME}.k8s.local

    sleep 3
fi

# Verify the port is listening
if ! podman exec cluster-cluster ss -tln 2>/dev/null | grep -q ':6443'; then
    echo "❌ Failed to start SSH tunnel"
    exit 1
fi

echo "✅ API server exposed: localhost:6443 -> $VM_IP:6443"
echo ""

# Generate kubeconfig with localhost:6443
echo "Generating kubeconfig at $KUBECONFIG_PATH..."
podman exec cluster-cluster ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i /var/run/cluster/cluster.key core@${VM_NAME}.k8s.local \
    'cat ~/.kube/config' > "$KUBECONFIG_PATH"

# Replace server address with localhost
sed -i 's|server: https://.*:6443|server: https://localhost:6443|g' "$KUBECONFIG_PATH"

echo "✅ Kubeconfig generated at $KUBECONFIG_PATH"
echo ""
echo "Usage:"
echo "  export KUBECONFIG=$KUBECONFIG_PATH"
echo "  kubectl get nodes"
