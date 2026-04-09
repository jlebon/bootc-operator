#!/bin/bash

# Source common functions
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

VM_NAME="${1}"
CONTAINER_NAME="cluster-cluster"

if [ -z "$VM_NAME" ]; then
    echo "Usage: $0 <vm-name>"
    exit 1
fi

# Get VM IP
IP=$(get_vm_ip "$VM_NAME")

if [ -z "$IP" ]; then
    echo "Error: Could not find VM $VM_NAME"
    exit 1
fi

echo "Connecting to $VM_NAME at $IP as user core"
podman exec -ti $CONTAINER_NAME ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -i /var/run/cluster/cluster.key core@$IP
