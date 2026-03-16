#!/bin/bash
# Smoke test: verify the Kubernetes cluster is functional and survives a reboot.
set -xeuo pipefail

check_cluster() {
    echo "Checking node status..."
    kubectl get nodes
    node_status=$(kubectl get nodes --no-headers | awk '{print $2}')
    if [[ "${node_status}" != "Ready" ]]; then
        echo "ERROR: Node is not Ready: ${node_status}"
        exit 1
    fi

    echo "Checking all pods are Running..."
    not_running=$(kubectl get pods -A --no-headers | grep -v Running || true)
    if [[ -n "${not_running}" ]]; then
        echo "ERROR: Some pods are not Running:"
        echo "${not_running}"
        exit 1
    fi
}

case "${AUTOPKGTEST_REBOOT_MARK:-}" in
    "")
        check_cluster
        echo "First boot checks passed, rebooting..."
        /tmp/autopkgtest-reboot rebooted
        ;;
    rebooted)
        # Give the cluster a moment to stabilize after reboot
        sleep 10
        check_cluster
        echo "Second boot checks passed!"
        ;;
    *)
        echo "ERROR: unexpected mark: ${AUTOPKGTEST_REBOOT_MARK}" >&2
        exit 1
        ;;
esac
