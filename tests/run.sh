#!/bin/bash
# Test runner for bootc-operator k8s tests.
# Discovers test-*.sh scripts and runs each in a fresh FCOS VM
# with a single-node Kubernetes cluster.
set -euo pipefail

usage() {
    cat <<EOF
Usage: $(basename "$0") [OPTIONS] [TEST...]

Run Kubernetes e2e tests on FCOS VMs.

Options:
    -v, --verbose        Show test output in real-time (default: only on failure)
    --output-dir DIR     Directory for test artifacts (default: \${SCRIPT_DIR}/results)
    -h, --help           Show this help message

Arguments:
    TEST             Test name(s) to run (without 'test-' prefix and '.sh' suffix).
                     If none specified, all test-*.sh files are run.

Examples:
    $(basename "$0")                    # Run all tests
    $(basename "$0") smoke              # Run only test-smoke.sh
    $(basename "$0") -v smoke           # Run with verbose output
EOF
}

IMAGE_TAG="${IMAGE_TAG:-bootc-operator-test-k8s}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${SCRIPT_DIR}"

ONLY_TESTS=()
VERBOSE=false
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

# Build test image if needed. The cosa stage inside the Containerfile
# needs /dev/kvm for osbuild and --privileged for nested podman builds.
if ! podman image exists "${IMAGE_TAG}"; then
    echo "Building test image ${IMAGE_TAG}..."
    tmpdir=$(mktemp -d)
    trap "rm -rf ${tmpdir}" EXIT
    podman build \
        --device /dev/kvm \
        --device /dev/fuse \
        --security-opt label=disable \
        --cap-add all \
        -v "${tmpdir}":/var/lib/containers \
        -t "${IMAGE_TAG}" "${SCRIPT_DIR}/k8s/"
fi

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

    rc=0
    podman run --rm --privileged \
        -v "${SCRIPT_DIR}:/mnt" \
        "${IMAGE_TAG}" run "/mnt/${test}" >&3 2>&3 || rc=$?

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
