#!/bin/bash
# E2E test: deploy bootc-operator and daemon, create a BootcNodePool, verify
# the full lifecycle from daemon startup through pool readiness.
#
# This test uses the pre-built artifacts in /mnt/.artifacts/:
#   - operator.tar: operator container image
#   - daemon.tar: daemon container image
#   - install.yaml: combined CRDs + RBAC + operator Deployment
#
# The test runs on a single-node FCOS Kubernetes cluster where the node is
# itself a bootc host. The test verifies:
#   1. Operator starts and creates daemon DaemonSet
#   2. Daemon pod starts and creates a BootcNode for the node
#   3. BootcNodePool claims the BootcNode and reaches Ready
#   4. Pool deletion releases the BootcNode cleanly
set -xeuo pipefail

ARTIFACTS=/mnt/.artifacts
NAMESPACE=bootc-operator
POOL_NAME=test-pool

# --- Helpers ---

dump_debug() {
    echo "=== DEBUG: cluster state ==="
    echo "--- Nodes ---"
    kubectl get nodes -o wide 2>/dev/null || true
    echo "--- All resources in ${NAMESPACE} ---"
    kubectl -n "${NAMESPACE}" get all -o wide 2>/dev/null || true
    echo "--- Deployment describe ---"
    kubectl -n "${NAMESPACE}" describe deployment bootc-operator-controller-manager 2>/dev/null || true
    echo "--- ReplicaSets ---"
    kubectl -n "${NAMESPACE}" get replicasets -o wide 2>/dev/null || true
    echo "--- Events in ${NAMESPACE} ---"
    kubectl -n "${NAMESPACE}" get events --sort-by=.lastTimestamp 2>/dev/null | tail -30 || true
    echo "--- Operator pod describe ---"
    kubectl -n "${NAMESPACE}" describe pods -l control-plane=controller-manager 2>/dev/null || true
    echo "--- Operator logs ---"
    kubectl -n "${NAMESPACE}" logs -l control-plane=controller-manager --tail=50 2>/dev/null || true
    echo "--- Daemon pods describe ---"
    kubectl -n "${NAMESPACE}" describe pods -l app.kubernetes.io/name=bootc-daemon 2>/dev/null || true
    echo "--- Daemon logs ---"
    kubectl -n "${NAMESPACE}" logs -l app.kubernetes.io/name=bootc-daemon --tail=50 2>/dev/null || true
    echo "--- BootcNodePools ---"
    kubectl get bootcnodepools -o yaml 2>/dev/null || true
    echo "--- BootcNodes ---"
    kubectl get bootcnodes -o yaml 2>/dev/null || true
    echo "--- Container images ---"
    ctr -n k8s.io images list 2>/dev/null | grep -E 'bootc|REF' || true
    echo "=== END DEBUG ==="
}

trap 'if [[ $? -ne 0 ]]; then dump_debug; fi' EXIT

wait_for() {
    local description="$1"
    local check_cmd="$2"
    local timeout="${3:-120}"

    echo "Waiting for ${description}..."
    local elapsed=0
    while ! eval "${check_cmd}" 2>/dev/null; do
        if [[ ${elapsed} -ge ${timeout} ]]; then
            echo "ERROR: Timed out waiting for ${description} (${timeout}s)"
            return 1
        fi
        sleep 5
        elapsed=$((elapsed + 5))
    done
    echo "${description} ready after ~${elapsed}s"
}

kubectl_bootcnode() {
    kubectl get bootcnodes "$@"
}

kubectl_bootcnodepool() {
    kubectl get bootcnodepools "$@"
}

# --- Test ---

# Step 1: Import container images into containerd
echo "=== Importing container images ==="
ctr -n k8s.io images import "${ARTIFACTS}/operator.tar"
ctr -n k8s.io images import "${ARTIFACTS}/daemon.tar"

# Step 2: Deploy the operator
echo "=== Deploying operator ==="
kubectl apply -f "${ARTIFACTS}/install.yaml" 2>&1 | tee /dev/stderr | tail -5

# Step 3: Wait for operator deployment to be ready
wait_for "operator deployment" \
    "kubectl -n ${NAMESPACE} get deployment bootc-operator-controller-manager -o jsonpath='{.status.readyReplicas}' | grep -q '^1$'" \
    180

# Step 4: Wait for the daemon DaemonSet to be created by the operator
wait_for "daemon DaemonSet" \
    "kubectl -n ${NAMESPACE} get daemonset bootc-daemon -o jsonpath='{.status.desiredNumberScheduled}' | grep -qv '^0$'" \
    60

# Step 5: Wait for daemon pod to be Running
wait_for "daemon pod Running" \
    "kubectl -n ${NAMESPACE} get pods -l app.kubernetes.io/name=bootc-daemon --no-headers | grep -q Running" \
    120

# Step 6: Wait for the BootcNode to be created by the daemon
NODE_NAME=$(kubectl get nodes --no-headers -o custom-columns=':metadata.name')
wait_for "BootcNode ${NODE_NAME}" \
    "kubectl_bootcnode ${NODE_NAME}" \
    60

# Step 7: Verify BootcNode has status populated
wait_for "BootcNode status" \
    "[[ -n \$(kubectl_bootcnode ${NODE_NAME} -o jsonpath='{.status.bootedDigest}') ]]" \
    30

echo "=== BootcNode status ==="
kubectl_bootcnode "${NODE_NAME}" -o yaml

# Step 8: Get the booted image info for creating a pool
BOOTED_IMAGE=$(kubectl_bootcnode "${NODE_NAME}" -o jsonpath='{.status.booted.image}')
echo "Node is running: ${BOOTED_IMAGE}"
if [[ -z "${BOOTED_IMAGE}" ]]; then
    echo "ERROR: BootcNode has no booted image"
    exit 1
fi

# Step 9: Create a BootcNodePool targeting this node with the current image.
# Since the node is already running this image, the pool should converge to
# Ready without needing a reboot.
echo "=== Creating BootcNodePool ==="
cat <<EOF | kubectl apply -f -
apiVersion: bootc.dev/v1alpha1
kind: BootcNodePool
metadata:
  name: ${POOL_NAME}
spec:
  image: "${BOOTED_IMAGE}"
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

# Step 10: Wait for the pool to claim the BootcNode
wait_for "BootcNode claimed by pool" \
    "[[ \$(kubectl_bootcnode ${NODE_NAME} -o jsonpath='{.metadata.labels.bootc\\.dev/pool}') == '${POOL_NAME}' ]]" \
    60

# Step 11: Verify BootcNode has desiredImage set
DESIRED_IMAGE=$(kubectl_bootcnode "${NODE_NAME}" -o jsonpath='{.spec.desiredImage}')
echo "BootcNode desiredImage: ${DESIRED_IMAGE}"
if [[ -z "${DESIRED_IMAGE}" ]]; then
    echo "ERROR: BootcNode desiredImage not set after pool claim"
    exit 1
fi

# Step 12: Wait for the pool to reach Ready phase.
# Since the node is already running the target image, this should happen
# after the daemon reports Ready status and the operator detects the match.
wait_for "pool Ready phase" \
    "[[ \$(kubectl_bootcnodepool ${POOL_NAME} -o jsonpath='{.status.phase}') == 'Ready' ]]" \
    120

echo "=== Pool status ==="
kubectl_bootcnodepool "${POOL_NAME}" -o yaml

# Step 13: Verify pool status counters
TARGET_NODES=$(kubectl_bootcnodepool "${POOL_NAME}" -o jsonpath='{.status.targetNodes}')
READY_NODES=$(kubectl_bootcnodepool "${POOL_NAME}" -o jsonpath='{.status.readyNodes}')
echo "Pool: targetNodes=${TARGET_NODES}, readyNodes=${READY_NODES}"
if [[ "${TARGET_NODES}" != "1" ]]; then
    echo "ERROR: Expected targetNodes=1, got ${TARGET_NODES}"
    exit 1
fi
if [[ "${READY_NODES}" != "1" ]]; then
    echo "ERROR: Expected readyNodes=1, got ${READY_NODES}"
    exit 1
fi

# Step 14: Verify pool has resolvedDigest set
RESOLVED_DIGEST=$(kubectl_bootcnodepool "${POOL_NAME}" -o jsonpath='{.status.resolvedDigest}')
echo "Pool resolvedDigest: ${RESOLVED_DIGEST}"
if [[ -z "${RESOLVED_DIGEST}" ]]; then
    echo "ERROR: Pool has no resolvedDigest"
    exit 1
fi

# Step 15: Verify BootcNode phase is Ready
BOOTCNODE_PHASE=$(kubectl_bootcnode "${NODE_NAME}" -o jsonpath='{.status.phase}')
echo "BootcNode phase: ${BOOTCNODE_PHASE}"
if [[ "${BOOTCNODE_PHASE}" != "Ready" ]]; then
    echo "ERROR: Expected BootcNode phase Ready, got ${BOOTCNODE_PHASE}"
    exit 1
fi

# Step 16: Delete the pool and verify cleanup
echo "=== Deleting BootcNodePool ==="
kubectl delete bootcnodepool "${POOL_NAME}"

# Step 17: Verify BootcNode is released (pool label removed)
wait_for "BootcNode released" \
    "[[ -z \$(kubectl_bootcnode ${NODE_NAME} -o jsonpath='{.metadata.labels.bootc\\.dev/pool}') ]]" \
    60

# Step 18: Verify BootcNode spec is cleared
DESIRED_IMAGE_AFTER=$(kubectl_bootcnode "${NODE_NAME}" -o jsonpath='{.spec.desiredImage}')
if [[ -n "${DESIRED_IMAGE_AFTER}" ]]; then
    echo "ERROR: BootcNode desiredImage not cleared after pool deletion: ${DESIRED_IMAGE_AFTER}"
    exit 1
fi

# Step 19: Verify BootcNode still exists (bound to Node, not pool)
if ! kubectl_bootcnode "${NODE_NAME}" &>/dev/null; then
    echo "ERROR: BootcNode was deleted (should survive pool deletion)"
    exit 1
fi

echo "=== All checks passed ==="
