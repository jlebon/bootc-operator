#!/bin/bash

# Common library functions for VM management

# Get VM IP address by name
# Usage: get_vm_ip <vm_name>
# Returns: IP address or empty string if not found
get_vm_ip() {
    local VM_NAME="$1"
    local IP=""

    if [ -z "$VM_NAME" ]; then
        echo "Error: VM name is required" >&2
        return 1
    fi

    # Try libvirt DNS first (more reliable)
    IP=$(podman exec cluster-cluster nslookup "${VM_NAME}.k8s.local" 192.168.122.1 2>/dev/null | awk '/^Address: / && NR>1 {print $2}' | head -1)

    # Fallback to DHCP leases if DNS lookup fails
    if [ -z "$IP" ]; then
        IP=$(podman exec cluster-cluster virsh -c qemu:///session net-dhcp-leases default 2>/dev/null | grep "$VM_NAME" | awk '{print $5}' | cut -d'/' -f1)
    fi

    echo "$IP"
}
