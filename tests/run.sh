#!/bin/bash
# Test runner for bootc-operator k8s tests.
# Discovers test-*.sh scripts and runs each in a fresh FCOS VM
# with a single-node Kubernetes cluster.
#
# The entire repo is mounted into the VM via virtiofs at /var/mnt,
# giving tests access to pre-built container images, kustomize
# manifests, and other project files.
set -euo pipefail

usage() {
    cat <<EOF
Usage: $(basename "$0") [OPTIONS] [TEST...]

Run Kubernetes e2e tests on FCOS VMs.

Options:
    -v, --verbose        Show test output in real-time (default: only on failure)
    --output-dir DIR     Directory for test artifacts (default: \${SCRIPT_DIR}/results)
    --skip-build         Skip building operator/daemon container images
    -h, --help           Show this help message

Arguments:
    TEST             Test name(s) to run (without 'test-' prefix and '.sh' suffix).
                     If none specified, all test-*.sh files are run.

Examples:
    $(basename "$0")                    # Run all tests
    $(basename "$0") smoke              # Run only test-smoke.sh
    $(basename "$0") -v rollout         # Run with verbose output
    $(basename "$0") --skip-build smoke # Skip image build (use cached)
EOF
}

VM_IMAGE_TAG="${VM_IMAGE_TAG:-bootc-operator-test-k8s}"
OPERATOR_IMG="${OPERATOR_IMG:-localhost/bootc-operator:test}"
DAEMON_IMG="${DAEMON_IMG:-localhost/bootc-daemon:test}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${SCRIPT_DIR}"

ONLY_TESTS=()
VERBOSE=false
SKIP_BUILD=false
output_dir="${SCRIPT_DIR}/results"
while [[ $# -gt 0 ]]; do
    case $1 in
        --help|-h)
            usage
            exit 0
            ;;
        --verbose|-v)
            VERBOSE=true
            shift
            ;;
        --output-dir)
            output_dir="$2"
            shift 2
            ;;
        --skip-build)
            SKIP_BUILD=true
            shift
            ;;
        -*)
            echo "Error: Unknown option: $1" >&2
            echo "Try '$(basename "$0") --help' for more information." >&2
            exit 1
            ;;
        *)
            ONLY_TESTS+=("$1")
            shift
            ;;
    esac
done

# Build VM test image if needed
if ! podman image exists "${VM_IMAGE_TAG}"; then
    echo "Building VM test image ${VM_IMAGE_TAG}..."
    podman build -t "${VM_IMAGE_TAG}" "${SCRIPT_DIR}/k8s/"
fi

# Build operator and daemon container images and save as OCI archives
# so tests can import them into the VM's containerd.
artifacts_dir="${SCRIPT_DIR}/.artifacts"
if [[ "${SKIP_BUILD}" == false ]]; then
    echo "Building operator and daemon container images..."
    mkdir -p "${artifacts_dir}"
    podman build -t "${OPERATOR_IMG}" --target operator "${REPO_DIR}"
    podman build -t "${DAEMON_IMG}" --target daemon "${REPO_DIR}"
    podman save --format oci-archive -o "${artifacts_dir}/operator.tar" "${OPERATOR_IMG}"
    podman save --format oci-archive -o "${artifacts_dir}/daemon.tar" "${DAEMON_IMG}"
    echo "Container images saved to ${artifacts_dir}/"
fi

# Generate install.yaml for deploying into the VM cluster.
echo "Generating install.yaml..."
mkdir -p "${artifacts_dir}"
make -C "${REPO_DIR}" manifests generate kustomize 2>&1 | tail -1
(
    cd "${REPO_DIR}/config/manager"
    "${REPO_DIR}/bin/kustomize" edit set image "controller=${OPERATOR_IMG}"
)
"${REPO_DIR}/bin/kustomize" build "${REPO_DIR}/config/default" > "${artifacts_dir}/install.yaml"
# Restore the kustomization.yaml to avoid polluting the repo.
git -C "${REPO_DIR}" checkout config/manager/kustomization.yaml 2>/dev/null || true
# Patch the install.yaml to use the correct daemon image reference.
sed -i "s|value: daemon:latest|value: ${DAEMON_IMG}|g" "${artifacts_dir}/install.yaml"
echo "install.yaml generated at ${artifacts_dir}/install.yaml"

# Find test scripts
if [[ ${#ONLY_TESTS[@]} -gt 0 ]]; then
    tests=()
    for name in "${ONLY_TESTS[@]}"; do
        testfile="test-${name}.sh"
        if [[ ! -f "${testfile}" ]]; then
            echo "Error: Test not found: ${testfile}" >&2
            exit 1
        fi
        tests+=("${testfile}")
    done
else
    tests=( test-*.sh )
    if [[ ${#tests[@]} -eq 0 ]]; then
        echo "No test-*.sh files found in ${SCRIPT_DIR}" >&2
        exit 1
    fi
fi

total=${#tests[@]}
passed=0
failed=0
failed_tests=()

echo ""
echo "Running ${total} test(s)..."
echo ""

for test in "${tests[@]}"; do
    echo "=== Running ${test} ==="
    test_name="${test%.sh}"

    # Create per-test output directory
    test_output_dir="${output_dir}/${test_name}"
    mkdir -p "${test_output_dir}"

    if [[ ${VERBOSE} == true ]]; then
        exec 3>&2
    else
        exec 3>"${test_output_dir}/runner.log"
    fi

    # Mount the entire repo as /mnt so the VM has access to
    # pre-built images, manifests, and test scripts.
    rc=0
    podman run --rm --privileged \
        -v "${REPO_DIR}:/mnt" \
        "${VM_IMAGE_TAG}" run "/mnt/tests/${test}" >&3 2>&3 || rc=$?

    if [[ ${rc} -eq 0 ]]; then
        echo "=== PASSED: ${test} ==="
        passed=$((passed+1))
    else
        echo "=== FAILED: ${test} ==="
        failed=$((failed+1))
        failed_tests+=("${test}")
        if [[ ${VERBOSE} == false ]]; then
            # Show the VM test output (written via virtiofs)
            local_log="${SCRIPT_DIR}/results/${test_name}/output.log"
            if [[ -f "${local_log}" ]]; then
                echo "--- Test output ---"
                cat "${local_log}"
                echo "--- End output ---"
            fi
            # Also show runner log
            if [[ -f "${test_output_dir}/runner.log" ]]; then
                echo "--- Runner output ---"
                cat "${test_output_dir}/runner.log"
                echo "--- End runner output ---"
            fi
        fi
    fi

    exec 3>&-
    echo ""
done

echo "========================================"
echo "Results: ${passed} passed, ${failed} failed"
echo "========================================"

if [[ "${failed}" -gt 0 ]]; then
    echo "Failed tests:"
    for t in "${failed_tests[@]}"; do
        echo "  - ${t}"
    done
    exit 1
fi
