#!/bin/bash

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NETWORK_XML="$SCRIPT_DIR/default-network.xml"

echo "=== Setting up default network for user session libvirt ==="

podman cp "$NETWORK_XML" cluster-cluster:/tmp/default-network.xml

if podman exec cluster-cluster virsh -c qemu:///session net-info default &>/dev/null; then
    echo "Network 'default' already exists"

    if podman exec cluster-cluster virsh -c qemu:///session net-info default | grep -q "Active:.*yes"; then
        echo "Network 'default' is already active"
    else
        echo "Starting network 'default'..."
        podman exec cluster-cluster virsh -c qemu:///session net-start default
    fi

    if podman exec cluster-cluster virsh -c qemu:///session net-info default | grep -q "Autostart:.*yes"; then
        echo "Network 'default' autostart is already enabled"
    else
        echo "Enabling autostart for network 'default'..."
        podman exec cluster-cluster virsh -c qemu:///session net-autostart default
    fi
else
    echo "Creating network 'default'..."
    podman exec cluster-cluster virsh -c qemu:///session net-define /tmp/default-network.xml

    echo "Starting network 'default'..."
    podman exec cluster-cluster virsh -c qemu:///session net-start default

    echo "Enabling autostart..."
    podman exec cluster-cluster virsh -c qemu:///session net-autostart default
fi

echo ""
echo "=== Network status ==="
podman exec cluster-cluster virsh -c qemu:///session net-list --all

echo ""
echo "=== Network info ==="
podman exec cluster-cluster virsh -c qemu:///session net-info default

echo ""
echo "✅ Network setup complete!"
