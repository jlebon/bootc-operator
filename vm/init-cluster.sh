#!/bin/bash

# Source common functions
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

VM_NAME="${1:-node1}"

if [ -z "$VM_NAME" ]; then
    echo "Usage: $0 <vm-name>"
    exit 1
fi

echo "=== Setting up kubeadm config on $VM_NAME ==="

# Create the kubeadm config file in the cluster container first
podman exec cluster-cluster bash -c 'cat > /tmp/kubeadm-config.yaml << "KUBEADM"
apiVersion: kubeadm.k8s.io/v1beta3
kind: InitConfiguration
nodeRegistration:
  criSocket: "unix:///var/run/crio/crio.sock"
---
apiVersion: kubeadm.k8s.io/v1beta3
kind: ClusterConfiguration
apiServer:
  certSANs:
  - "localhost"
  - "127.0.0.1"
controllerManager:
  extraArgs:
    flex-volume-plugin-dir: "/var/lib/kubelet/volumeplugins"
  extraVolumes:
  - name: flexvolume-dir
    hostPath: "/var/lib/kubelet/volumeplugins"
    mountPath: "/var/lib/kubelet/volumeplugins"
    readOnly: false
---
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
volumePluginDir: "/var/lib/kubelet/volumeplugins"
KUBEADM
'

# Copy the config file to the VM
podman exec cluster-cluster scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i /var/run/cluster/cluster.key \
    /tmp/kubeadm-config.yaml \
    core@${VM_NAME}.k8s.local:/tmp/kubeadm-config.yaml

# Move it to the proper location
podman exec cluster-cluster ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i /var/run/cluster/cluster.key core@${VM_NAME}.k8s.local \
    'sudo mkdir -p /etc/kubernetes && sudo mv /tmp/kubeadm-config.yaml /etc/kubernetes/kubeadm-config.yaml'

echo ""
echo "✓ kubeadm config created at /etc/kubernetes/kubeadm-config.yaml"
echo ""
echo "=== Initializing Kubernetes cluster on $VM_NAME ==="
echo ""

podman exec cluster-cluster ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i /var/run/cluster/cluster.key core@${VM_NAME}.k8s.local \
    'sudo kubeadm init --config /etc/kubernetes/kubeadm-config.yaml'

echo ""
echo "=== Setting up kubectl for core user ==="
podman exec cluster-cluster ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i /var/run/cluster/cluster.key core@${VM_NAME}.k8s.local \
    'mkdir -p $HOME/.kube && sudo cp -i /etc/kubernetes/admin.conf $HOME/.kube/config && sudo chown $(id -u):$(id -g) $HOME/.kube/config'

echo ""
echo "=== Installing Calico CNI plugin ==="
podman exec cluster-cluster ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i /var/run/cluster/cluster.key core@${VM_NAME}.k8s.local \
    'kubectl apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.27.0/manifests/calico.yaml'

echo ""
echo "✅ Cluster initialized on $VM_NAME with Calico CNI"

echo ""
echo "=== Exposing API server to localhost:6443 ==="

# Get VM IP
VM_IP=$(get_vm_ip "$VM_NAME")

if [ -n "$VM_IP" ]; then
    # Check if SSH tunnel is already running
    if podman exec cluster-cluster ss -tln 2>/dev/null | grep -q ':6443'; then
        echo "Port 6443 is already forwarded"
    else
        echo "Starting SSH port forwarding: container:6443 -> $VM_IP:6443"

        # Use SSH to forward port 6443 from container to VM
        podman exec -d cluster-cluster ssh -N -L 0.0.0.0:6443:${VM_IP}:6443 \
            -o StrictHostKeyChecking=no \
            -o UserKnownHostsFile=/dev/null \
            -o ServerAliveInterval=60 \
            -i /var/run/cluster/cluster.key \
            core@${VM_NAME}.k8s.local

        sleep 3
    fi

    # Verify the port is listening
    if podman exec cluster-cluster ss -tln 2>/dev/null | grep -q ':6443'; then
        echo "✅ API server exposed: localhost:6443 -> $VM_IP:6443"

        # Generate kubeconfig
        KUBECONFIG_PATH="./vm/kubeconfig"
        echo "Generating kubeconfig at $KUBECONFIG_PATH..."
        podman exec cluster-cluster ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
            -i /var/run/cluster/cluster.key core@${VM_NAME}.k8s.local \
            'cat ~/.kube/config' > "$KUBECONFIG_PATH"

        # Replace server address with localhost
        sed -i 's|server: https://.*:6443|server: https://localhost:6443|g' "$KUBECONFIG_PATH"

        echo "✅ Kubeconfig ready at $KUBECONFIG_PATH"
        echo ""
        echo "Usage:"
        echo "  export KUBECONFIG=$KUBECONFIG_PATH"
        echo "  kubectl get nodes"
    else
        echo "⚠ Warning: Failed to start SSH tunnel for API server"
        echo "   You can manually run: ./vm/expose-api.sh $VM_NAME"
    fi
else
    echo "⚠ Warning: Could not get VM IP to expose API server"
    echo "   You can manually run: ./vm/expose-api.sh $VM_NAME"
fi
