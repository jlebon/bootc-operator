#!/bin/bash
# E2E test: deploy the bootc operator and daemon, create a BootcNodePool
# targeting the single-node cluster, and verify the full reconciliation flow.
#
# This test runs inside an FCOS VM with a single-node kubeadm cluster.
# The repo root is available at /var/mnt via virtiofs, including pre-built
# container images and the install.yaml manifest.
set -xeuo pipefail

ARTIFACTS=/var/mnt/tests/.artifacts

# --- Helper functions ---

wait_for() {
    local desc="$1" timeout="$2"
    shift 2
    echo "Waiting for ${desc} (timeout: ${timeout}s)..."
    local deadline=$((SECONDS + timeout))
    while [[ ${SECONDS} -lt ${deadline} ]]; do
        if "$@" 2>/dev/null; then
            echo "${desc}: OK"
            return 0
        fi
        sleep 5
    done
    echo "ERROR: Timed out waiting for ${desc}" >&2
    return 1
}

# --- Phase 1: Import container images into containerd ---

# Copy images to local storage first (virtiofs can be slow for large
# sequential reads and ctr import may time out).
echo "Copying images to local storage..."
cp "${ARTIFACTS}/operator.tar" /tmp/operator.tar
cp "${ARTIFACTS}/daemon.tar" /tmp/daemon.tar

echo "Importing operator image into containerd..."
ctr -n k8s.io images import --all-platforms /tmp/operator.tar

echo "Importing daemon image into containerd..."
ctr -n k8s.io images import --all-platforms /tmp/daemon.tar

rm -f /tmp/operator.tar /tmp/daemon.tar

echo "Verifying images are available..."
ctr -n k8s.io images ls | grep -E 'bootc-(operator|daemon)'

# --- Phase 2: Deploy the operator ---

echo "Applying install.yaml..."
kubectl apply -f "${ARTIFACTS}/install.yaml"

# Wait for the operator deployment to be available.
wait_for "operator deployment" 120 \
    kubectl -n bootc-operator-system rollout status deployment/bootc-operator-controller-manager --timeout=5s

echo "Operator is running:"
kubectl -n bootc-operator-system get pods

# --- Phase 3: Wait for daemon DaemonSet and pod ---

# The operator creates the bootc-daemon DaemonSet at startup via
# EnsureDaemonResources(). Wait for it to appear and be ready.
wait_for "daemon DaemonSet" 120 \
    kubectl -n bootc-operator-system rollout status daemonset/bootc-daemon --timeout=5s

echo "Daemon DaemonSet is ready:"
kubectl -n bootc-operator-system get daemonset bootc-daemon

# Wait for the daemon pod to be Running.
wait_for "daemon pod running" 60 bash -c '
    kubectl -n bootc-operator-system get pods -l app.kubernetes.io/name=bootc-daemon \
        --no-headers | grep -q Running
'

echo "Daemon pod is running:"
kubectl -n bootc-operator-system get pods -l app.kubernetes.io/name=bootc-daemon

# --- Phase 4: Verify BootcNode creation ---

# Show all pods for debugging.
echo "All pods in bootc-operator-system:"
kubectl -n bootc-operator-system get pods -o wide

# Show daemon logs for debugging if BootcNode creation is slow.
echo "Daemon pod logs:"
kubectl -n bootc-operator-system logs -l app.kubernetes.io/name=bootc-daemon --tail=50 || true

# The daemon creates a BootcNode CRD named after the Kubernetes node.
node_name=$(kubectl get nodes --no-headers -o custom-columns=':metadata.name')

wait_for "BootcNode ${node_name}" 120 \
    kubectl get bootcnode "${node_name}"

echo "BootcNode created:"
kubectl get bootcnode "${node_name}" -o yaml

# Wait for the daemon to populate the status with booted image.
# The initial create may not include status (API server ignores it),
# so we wait for the first poll cycle to update the status subresource.
# Verify the BootcNode has phase=Ready (no pool assigned yet).
bn_phase=$(kubectl get bootcnode "${node_name}" -o jsonpath='{.status.phase}')
if [[ "${bn_phase}" != "Ready" ]]; then
    echo "ERROR: BootcNode phase is '${bn_phase}', expected 'Ready'" >&2
    exit 1
fi

# Verify the BootcNode has an ownerReference to the Node.
owner_kind=$(kubectl get bootcnode "${node_name}" -o jsonpath='{.metadata.ownerReferences[0].kind}')
if [[ "${owner_kind}" != "Node" ]]; then
    echo "ERROR: BootcNode ownerReference kind is '${owner_kind}', expected 'Node'" >&2
    exit 1
fi

# Get the booted image from the BootcNode status (may be empty when
# bootc status reports null booted via nsenter -- a known limitation
# in containerized environments).
booted_image=$(kubectl get bootcnode "${node_name}" -o jsonpath='{.status.booted.image}')
echo "BootcNode booted image: '${booted_image}'"

# --- Phase 5: Create a BootcNodePool targeting the node ---

# Get the actual booted image directly from the host for use in the
# BootcNodePool (the daemon may report empty booted via nsenter).
host_booted_image=$(bootc status --json | jq -r '
    .status.booted.image | "\(.image.image | split(":")[0])@\(.imageDigest)"
')
echo "Host booted image: ${host_booted_image}"

cat <<EOF | kubectl apply -f -
apiVersion: bootc.dev/v1alpha1
kind: BootcNodePool
metadata:
  name: test-pool
spec:
  image: "${host_booted_image}"
  nodeSelector:
    matchLabels:
      kubernetes.io/os: linux
  rollout:
    maxUnavailable: 1
  disruption:
    rebootPolicy: Auto
  healthCheck:
    timeout: 5m
EOF

echo "BootcNodePool created:"
kubectl get bootcnodepool test-pool

# --- Phase 6: Verify pool claiming and convergence ---

# Wait for the BootcNode to be claimed by the pool (pool label set).
wait_for "BootcNode claimed by pool" 60 bash -c "
    kubectl get bootcnode '${node_name}' -o jsonpath='{.metadata.labels.bootc\\.dev/pool}' | grep -q test-pool
"

echo "BootcNode claimed by pool:"
kubectl get bootcnode "${node_name}" -o jsonpath='{.metadata.labels}' | jq .

# Verify the BootcNode spec has the desired image set.
desired_image=$(kubectl get bootcnode "${node_name}" -o jsonpath='{.spec.desiredImage}')
if [[ "${desired_image}" != "${host_booted_image}" ]]; then
    echo "ERROR: BootcNode desiredImage is '${desired_image}', expected '${host_booted_image}'" >&2
    exit 1
fi

# Verify status counters: the pool should show 1 target node.
wait_for "pool targetNodes" 30 bash -c "
    target=\$(kubectl get bootcnodepool test-pool -o jsonpath='{.status.targetNodes}')
    [[ \"\${target}\" == '1' ]]
"

echo "BootcNodePool status:"
kubectl get bootcnodepool test-pool -o yaml

# --- Phase 7: Verify pool deletion cleanup ---

echo "Deleting BootcNodePool..."
kubectl delete bootcnodepool test-pool --timeout=30s

# Verify the BootcNode is released (pool label removed, spec cleared).
wait_for "BootcNode released" 30 bash -c "
    label=\$(kubectl get bootcnode '${node_name}' -o jsonpath='{.metadata.labels.bootc\\.dev/pool}' 2>/dev/null)
    [[ -z \"\${label}\" ]]
"

desired_after=$(kubectl get bootcnode "${node_name}" -o jsonpath='{.spec.desiredImage}')
if [[ -n "${desired_after}" ]]; then
    echo "ERROR: BootcNode spec.desiredImage not cleared after pool deletion: '${desired_after}'" >&2
    exit 1
fi

echo ""
echo "=== All E2E checks passed ==="
