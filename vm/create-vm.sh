#!/bin/bash

set -xe

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"
VM_NAME="fedora-bootc-k8s"

while getopts "n:" opt; do
  case $opt in
    n)
      VM_NAME="$OPTARG"
      ;;
    \?)
      echo "Usage: $0 [-n vm_name] [base_disk] [overlay_disk] [memory_mb] [vcpus]" >&2
      exit 1
      ;;
  esac
done

shift $((OPTIND-1))

BASE_DISK="${1:-/src/fedora-bootc-k8s.qcow2}"
OVERLAY_DISK="${2:-/src/${VM_NAME}.qcow2}"
MEMORY="${3:-8192}"
VCPUS="${4:-4}"

echo "=== Creating VM with overlay disk ==="
echo "VM Name: $VM_NAME"
echo "Base disk: $BASE_DISK"
echo "Overlay disk: $OVERLAY_DISK"
echo "Memory: ${MEMORY}MB"
echo "VCPUs: $VCPUS"

# Create overlay image with base as backing file
echo ""
echo "Creating overlay image..."
podman exec -ti cluster-cluster \
qemu-img create \
  -f qcow2 \
  -F qcow2 \
  -b "$BASE_DISK" \
  "$OVERLAY_DISK"

echo ""
echo "Setting up SSH key..."

CLUSTER_KEY="/var/run/cluster/cluster.key"
CLUSTER_KEY_PUB="/var/run/cluster/cluster.key.pub"

# Check if cluster key exists
if ! podman exec cluster-cluster test -f "$CLUSTER_KEY"; then
    echo "Generating cluster SSH key..."
    podman exec cluster-cluster mkdir -p /var/run/cluster
    podman exec cluster-cluster ssh-keygen -t rsa -b 4096 -f "$CLUSTER_KEY" -N "" -C "cluster-key"
    echo "Cluster SSH key created at $CLUSTER_KEY"
else
    echo "Using existing cluster SSH key"
fi

echo ""
echo "Creating cloud-init ISO with SSH key..."

# Get the public key
PUB_KEY=$(podman exec cluster-cluster cat "$CLUSTER_KEY_PUB")
ISO_PATH="/src/${VM_NAME}-cloud-init.iso"

# Create cloud-init ISO inside the container
podman exec cluster-cluster bash -c "
TMPDIR=\$(mktemp -d)
cat > \"\$TMPDIR/meta-data\" << 'EOFMETA'
instance-id: $VM_NAME
local-hostname: $VM_NAME
EOFMETA

cat > \"\$TMPDIR/user-data\" << 'EOFUSER'
#cloud-config
hostname: $VM_NAME
users:
  - name: core
    ssh_authorized_keys:
      - $PUB_KEY
    sudo: ALL=(ALL) NOPASSWD:ALL
    groups: wheel
    shell: /bin/bash

write_files:
  - path: /etc/kubernetes/kubelet-config.yaml
    content: |
      apiVersion: kubelet.config.k8s.io/v1beta1
      kind: KubeletConfiguration
      volumePluginDir: /var/lib/kubelet/volumeplugins
  - path: /etc/sysconfig/kubelet
    content: |
      KUBELET_EXTRA_ARGS=--volume-plugin-dir=/var/lib/kubelet/volumeplugins

runcmd:
  - swapoff -a
  - sed -i '/swap/d' /etc/fstab
  - modprobe br_netfilter
  - echo 'br_netfilter' > /etc/modules-load.d/k8s-bridge.conf
  - sysctl -w net.ipv4.ip_forward=1
  - echo 'net.ipv4.ip_forward=1' > /etc/sysctl.d/99-kubernetes.conf
  - mkdir -p /var/lib/kubelet/volumeplugins
  - systemctl enable --now crio
  - systemctl enable kubelet
EOFUSER

genisoimage -output \"$ISO_PATH\" \
    -volid cidata \
    -joliet \
    -rock \
    \"\$TMPDIR/user-data\" \
    \"\$TMPDIR/meta-data\" 2>&1 | grep -v 'Warning: creating filesystem'

rm -rf \"\$TMPDIR\"
"

echo "✓ Cloud-init ISO created at $ISO_PATH"

echo ""
echo "Creating VM with overlay disk..."
podman exec -ti cluster-cluster \
virt-install \
  --connect qemu:///session \
  --name "$VM_NAME" \
  --memory "$MEMORY" \
  --vcpus "$VCPUS" \
  --disk path="$OVERLAY_DISK",format=qcow2,bus=virtio \
  --disk path="$ISO_PATH",device=cdrom \
  --import \
  --os-variant fedora-unknown \
  --network network=default \
  --graphics none \
  --console pty,target_type=serial \
  --noautoconsole

echo ""
echo "Getting VM MAC address..."
MAC=$(podman exec cluster-cluster virsh -c qemu:///session domiflist "$VM_NAME" | awk '/default/ {print $5}' | tr -d '\r\n')

if [ -z "$MAC" ]; then
    echo "Error: Could not get MAC address for VM"
    exit 1
fi

echo "MAC address: $MAC"

echo ""
echo "Adding static DHCP entry to network..."
# Calculate next available IP (simple approach: use hash of VM name)
IP_SUFFIX=$((16#$(echo -n "$VM_NAME" | md5sum | cut -c1-2) % 240 + 10))
VM_IP="192.168.122.$IP_SUFFIX"

podman exec cluster-cluster virsh -c qemu:///session net-update default add ip-dhcp-host \
    "<host mac='$MAC' name='$VM_NAME' ip='$VM_IP'/>" \
    --live --config 2>/dev/null || echo "Note: DHCP entry may already exist"

echo ""
echo "Restarting VM to get new DHCP lease with DNS registration..."
podman exec cluster-cluster virsh -c qemu:///session destroy "$VM_NAME"
podman exec cluster-cluster virsh -c qemu:///session start "$VM_NAME"

echo ""
echo "Waiting for VM to boot, cloud-init to run, and get DHCP lease..."
ACTUAL_IP=""
for i in {1..30}; do
    ACTUAL_IP=$(get_vm_ip "$VM_NAME")
    if [ -n "$ACTUAL_IP" ]; then
        echo "VM got IP: $ACTUAL_IP"
        break
    fi
    sleep 2
done

if [ -z "$ACTUAL_IP" ]; then
    echo "Warning: Could not get IP from DHCP lease, using configured IP: $VM_IP"
    ACTUAL_IP="$VM_IP"
fi

echo "✓ Cloud-init is configuring the VM (SSH key injection, hostname, etc.)"

echo ""
echo "Updating /etc/hosts in container..."
echo "IP: $ACTUAL_IP, VM: $VM_NAME"

# Remove old entry if exists (don't fail if it doesn't exist)
podman exec cluster-cluster sed -i "/$VM_NAME.k8s.local/d" /etc/hosts || true

# Add new entry with actual IP
if [ -n "$ACTUAL_IP" ]; then
    podman exec cluster-cluster bash -c "echo '$ACTUAL_IP $VM_NAME.k8s.local $VM_NAME' >> /etc/hosts"
    echo "Added: $ACTUAL_IP -> $VM_NAME.k8s.local"

    # Verify it was added
    podman exec cluster-cluster grep "$VM_NAME.k8s.local" /etc/hosts || echo "Warning: Entry not found in /etc/hosts"
else
    echo "Error: ACTUAL_IP is empty, cannot update /etc/hosts"
fi

echo ""
echo "✅ VM '$VM_NAME' created successfully!"
echo "   DNS name: $VM_NAME.k8s.local"
echo "   IP address: $ACTUAL_IP"
echo "   MAC address: $MAC"
echo "   Network: default (192.168.122.0/24)"
echo "   User: core (with sudo access)"
echo ""
echo "SSH access: ./vm/ssh-vm.sh $VM_NAME"
echo "   or: podman exec -ti cluster-cluster ssh core@$VM_NAME.k8s.local"
echo "SSH key: $CLUSTER_KEY"
