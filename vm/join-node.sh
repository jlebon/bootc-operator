#!/bin/bash

VM_NAME="${1}"
CONTROL_PLANE="${2:-node1}"

if [ -z "$VM_NAME" ]; then
    echo "Usage: $0 <new-node-name> [control-plane-node]"
    echo "Example: $0 node2"
    echo "Example: $0 node3 node1"
    exit 1
fi

echo "=== Creating worker node $VM_NAME ==="
echo ""

# Create the new VM
./vm/create-vm.sh -n "$VM_NAME"

if [ $? -ne 0 ]; then
    echo "Error: Failed to create VM $VM_NAME"
    exit 1
fi

echo ""
echo "=== Generating join command from $CONTROL_PLANE ==="

# Generate a fresh join command from the control plane
JOIN_COMMAND=$(podman exec cluster-cluster ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i /var/run/cluster/cluster.key core@${CONTROL_PLANE}.k8s.local \
    'sudo kubeadm token create --print-join-command' 2>/dev/null)

if [ -z "$JOIN_COMMAND" ]; then
    echo "Error: Failed to generate join command from $CONTROL_PLANE"
    exit 1
fi

echo "Join command: $JOIN_COMMAND"

echo ""
echo "=== Waiting for $VM_NAME to be ready ==="
sleep 10

echo ""
echo "=== Joining $VM_NAME to the cluster ==="

# Execute the join command on the new node
podman exec cluster-cluster ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i /var/run/cluster/cluster.key core@${VM_NAME}.k8s.local \
    "sudo $JOIN_COMMAND"

if [ $? -eq 0 ]; then
    echo ""
    echo "✅ Node $VM_NAME successfully joined the cluster!"
    echo ""
    echo "Verify with:"
    echo "  ./vm/ssh-vm.sh $CONTROL_PLANE"
    echo "  kubectl get nodes"
else
    echo ""
    echo "❌ Failed to join $VM_NAME to the cluster"
    exit 1
fi
