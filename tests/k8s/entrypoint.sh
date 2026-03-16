#!/bin/bash
# Entrypoint for the k8s test VM container.
# Boots an FCOS VM with a single-node Kubernetes cluster and either
# drops the user into an SSH shell or runs a test script.
set -euo pipefail

FCOS_DIR=/usr/share/fcos
SSH_ARGS=(-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR)
SSH_PORT=2222
MEMORY_MIB=4096
DISK_SIZE=20G

# --- Public functions (lifecycle order) ---

main() {
    local cmd="${1:-}"
    if [[ -z "${cmd}" ]]; then
        usage
        exit 1
    fi
    shift

    case "${cmd}" in
        shell) cmd_shell "$@" ;;
        run)   cmd_run "$@" ;;
        *)
            echo "ERROR: Unknown command: ${cmd}" >&2
            usage
            exit 1
            ;;
    esac
}

cmd_shell() {
    startup
    echo "Kubernetes cluster is ready. Dropping into VM shell."
    exec ssh "${SSH_ARGS[@]}" -i "${WORKDIR}/id_ed25519" -p "${SSH_PORT}" core@localhost
}

cmd_run() {
    local script="${1:-}"
    if [[ -z "${script}" ]]; then
        echo "ERROR: run requires a path to the test script (e.g. /mnt/test-smoke.sh)" >&2
        exit 1
    fi

    if [[ ! -f "${script}" ]]; then
        echo "ERROR: Test script not found: ${script}" >&2
        exit 1
    fi

    startup
    install_autopkgtest_reboot
    run_test_with_reboot_support "${script}"
}

# --- Private functions (depth-first call order) ---

usage() {
    cat <<EOF
Usage: $(basename "$0") <command> [args...]

Commands:
    shell               Boot VM and drop into an SSH shell
    run <script>        Boot VM and run a test script (e.g. /mnt/test-smoke.sh)

The container must be run with:
    --privileged                for KVM and virtiofsd capabilities
    -v /path/to/dir:/mnt        to share files with the VM (optional for shell)
EOF
}

startup() {
    setup_workdir
    generate_ssh_key
    build_ignition
    create_disk
    start_virtiofsd
    launch_qemu
    wait_for_ssh
    wait_for_k8s
}

setup_workdir() {
    WORKDIR=$(mktemp -d)
    trap cleanup EXIT
}

generate_ssh_key() {
    ssh-keygen -t ed25519 -f "${WORKDIR}/id_ed25519" -N "" -q
}

build_ignition() {
    # Append passwd section with our generated SSH key to the base config.
    local pubkey
    pubkey=$(cat "${WORKDIR}/id_ed25519.pub")
    cat "${FCOS_DIR}/config.bu" > "${WORKDIR}/config.bu"
    cat >> "${WORKDIR}/config.bu" <<EOF
passwd:
  users:
    - name: core
      ssh_authorized_keys:
        - ${pubkey}
EOF
    butane --strict "${WORKDIR}/config.bu" > "${WORKDIR}/config.ign"
}

create_disk() {
    qemu-img create -f qcow2 \
        -b "${FCOS_DIR}/image.qcow2" -F qcow2 \
        "${WORKDIR}/disk.qcow2" "${DISK_SIZE}" >/dev/null
}

start_virtiofsd() {
    /usr/libexec/virtiofsd \
        --socket-path "${WORKDIR}/virtiofsd.sock" \
        --shared-dir /mnt \
        --sandbox none --seccomp none &>/dev/null &
    echo $! > "${WORKDIR}/virtiofsd.pid"
    # Wait for socket to appear
    for _ in {1..20}; do
        [[ -S "${WORKDIR}/virtiofsd.sock" ]] && return
        sleep 0.1
    done
    echo "ERROR: virtiofsd socket did not appear" >&2
    exit 1
}

launch_qemu() {
    local ncpus
    ncpus=$(nproc)
    if [[ ${ncpus} -gt 16 ]]; then
        ncpus=16
    fi

    local qemu_args=(
        -machine "accel=kvm,memory-backend=mem"
        -object "memory-backend-memfd,id=mem,size=${MEMORY_MIB}M,share=on"
        -m "${MEMORY_MIB}"
        -smp "${ncpus}"
        -nographic
        -drive "if=virtio,file=${WORKDIR}/disk.qcow2"
        -fw_cfg "name=opt/com.coreos/config,file=${WORKDIR}/config.ign"
        -netdev "user,id=net0,hostfwd=tcp::${SSH_PORT}-:22"
        -device "virtio-net-pci,netdev=net0"
        -pidfile "${WORKDIR}/qemu.pid"
    )

    qemu_args+=(
        -chardev "socket,id=char0,path=${WORKDIR}/virtiofsd.sock"
        -device "vhost-user-fs-pci,queue-size=1024,chardev=char0,tag=hostmnt"
    )

    qemu-system-x86_64 "${qemu_args[@]}" &>"${WORKDIR}/qemu.log" &
}

wait_for_ssh() {
    echo "Waiting for SSH..."
    local attempt
    for attempt in {1..60}; do
        if vm_ssh true 2>/dev/null; then
            echo "SSH ready after ~$((attempt * 5))s"
            return
        fi
        sleep 5
    done
    echo "ERROR: Timed out waiting for SSH (5 minutes)" >&2
    exit 1
}

wait_for_k8s() {
    echo "Waiting for Kubernetes bootstrap..."
    local attempt
    for attempt in {1..60}; do
        local status
        status=$(vm_ssh "systemctl is-active kubeadm-bootstrap.service" 2>/dev/null || true)
        case "${status}" in
            active)
                echo "kubeadm-bootstrap.service completed"
                break
                ;;
            failed)
                echo "ERROR: kubeadm-bootstrap.service failed" >&2
                vm_ssh "systemctl status kubeadm-bootstrap.service --no-pager" 2>/dev/null || true
                exit 1
                ;;
        esac
        sleep 10
    done
    if [[ "${attempt}" -ge 60 ]]; then
        echo "ERROR: Timed out waiting for kubeadm-bootstrap.service (10 minutes)" >&2
        exit 1
    fi

    echo "Waiting for node to be Ready and all pods Running..."
    for attempt in {1..60}; do
        local node_ready
        node_ready=$(vm_ssh "kubectl get nodes --no-headers 2>/dev/null | awk '{print \$2}'" 2>/dev/null || true)
        if [[ "${node_ready}" != "Ready" ]]; then
            sleep 5
            continue
        fi
        local not_running
        not_running=$(vm_ssh "kubectl get pods -A --no-headers 2>/dev/null | grep -v Running" 2>/dev/null || true)
        if [[ -z "${not_running}" ]]; then
            echo "Node is Ready, all pods are Running"
            return
        fi
        sleep 5
    done
    echo "ERROR: Timed out waiting for cluster readiness (5 minutes)" >&2
    vm_ssh "kubectl get nodes; kubectl get pods -A" 2>/dev/null || true
    exit 1
}

vm_ssh() {
    ssh "${SSH_ARGS[@]}" -i "${WORKDIR}/id_ed25519" -p "${SSH_PORT}" core@localhost "$@"
}

install_autopkgtest_reboot() {
    # Write the reboot mark to the virtiofs shared mount so the
    # entrypoint can read it locally without SSH.
    vm_ssh "sudo tee /tmp/autopkgtest-reboot > /dev/null" <<'REBOOT_SCRIPT'
#!/bin/bash
echo "$1" > /mnt/.reboot-mark
systemctl reboot
REBOOT_SCRIPT
    vm_ssh "sudo chmod +x /tmp/autopkgtest-reboot"
}

get_boot_id() {
    vm_ssh "cat /proc/sys/kernel/random/boot_id" 2>/dev/null
}

run_test_with_reboot_support() {
    local script="$1"
    local mark=""
    local rc=0

    # Clean up any stale reboot mark
    rm -f /mnt/.reboot-mark

    while true; do
        echo "Running test ${script}${mark:+ (mark: ${mark})}..."

        # Capture boot ID before running the test so we can detect
        # whether a real reboot happened (vs. SSH flaking out).
        local boot_id
        boot_id=$(get_boot_id)

        local setenv_args=()
        if [[ -n "${mark}" ]]; then
            setenv_args=(--setenv="AUTOPKGTEST_REBOOT_MARK=${mark}")
        fi

        rc=0
        vm_ssh "sudo systemd-run --wait --collect --pipe --quiet \
            --setenv=KUBECONFIG=/etc/kubernetes/admin.conf \
            ${setenv_args[*]:+${setenv_args[*]}} \
            bash ${script} 2>&1" || rc=$?

        if [[ ${rc} -eq 0 ]]; then
            echo "Test ${script} PASSED"
            break
        fi

        if [[ ${rc} -ne 255 ]]; then
            echo "Test ${script} FAILED (exit code ${rc})"
            break
        fi

        # SSH connection lost (exit 255). Check the virtiofs shared
        # mount for a reboot mark (no SSH needed).
        if [[ ! -f /mnt/.reboot-mark ]]; then
            echo "ERROR: SSH connection lost but no reboot mark found" >&2
            rc=1
            break
        fi

        mark=$(cat /mnt/.reboot-mark)
        rm -f /mnt/.reboot-mark
        echo "Planned reboot detected (mark: ${mark}), waiting for new boot..."

        # Wait for SSH to come back with a new boot ID, confirming
        # the reboot actually happened.
        if ! wait_for_new_boot "${boot_id}"; then
            echo "ERROR: VM did not reboot (boot ID unchanged)" >&2
            rc=1
            break
        fi
    done

    exit "${rc}"
}

wait_for_new_boot() {
    local old_boot_id="$1"
    local attempt
    for attempt in {1..60}; do
        local new_boot_id
        new_boot_id=$(get_boot_id 2>/dev/null || true)
        if [[ -n "${new_boot_id}" && "${new_boot_id}" != "${old_boot_id}" ]]; then
            echo "New boot confirmed after ~$((attempt * 5))s"
            return 0
        fi
        sleep 5
    done
    return 1
}

cleanup() {
    local pid
    if [[ -f "${WORKDIR}/qemu.pid" ]]; then
        pid=$(cat "${WORKDIR}/qemu.pid" 2>/dev/null || true)
        if [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null; then
            kill "${pid}" 2>/dev/null || true
        fi
    fi
    if [[ -f "${WORKDIR}/virtiofsd.pid" ]]; then
        pid=$(cat "${WORKDIR}/virtiofsd.pid" 2>/dev/null || true)
        if [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null; then
            kill "${pid}" 2>/dev/null || true
        fi
    fi
    rm -rf "${WORKDIR}"
}

main "$@"
