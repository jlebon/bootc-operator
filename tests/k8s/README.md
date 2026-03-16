# Kubernetes on FCOS Test Harness

This directory contains a self-contained test harness for running
Kubernetes e2e tests on Fedora CoreOS VMs. A container image bundles
QEMU, an FCOS qcow2 image, and a Butane config that bootstraps a
single-node Kubernetes cluster on first boot using kubeadm.

## Why not kola?

This harness is heavily inspired by
[kola](https://coreos.github.io/coreos-assembler/kola/) thus the natural
question is why not use kola?

First, I didn't want to tie this to CoreOS-specific tech. The image uses FCOS
because I like Ignition, but otherwise its role here is just as a stand-in for
any valid bootable container image. We could switch over to a more minimal bootc
image in the future.

Second, testing a Kubernetes operator is very different from testing the OS
itself. There are already some customizations specific to its needs, and I plan
to customize it further as we go.

## Building the image

```bash
podman build -t bootc-operator-test-k8s tests/k8s/
```

The image downloads the latest stable FCOS qemu qcow2 at build time, so
the first build takes a few minutes. Subsequent rebuilds use the cache
unless the Containerfile or config changes.

## Usage

### Interactive shell

Boot a VM with a running Kubernetes cluster and drop into an SSH shell:

```bash
podman run -it --rm --privileged bootc-operator-test-k8s shell
```

The cluster is fully ready when the prompt appears. `kubectl` and the
`k` alias work out of the box. The container's `/mnt` is shared with
the VM via virtiofs, so anything bind-mounted there is immediately
accessible at `/mnt` inside the VM:

```bash
podman run -it --rm --privileged -v $PWD:/mnt \
    bootc-operator-test-k8s shell
# Inside the VM: ls /mnt/test-smoke.sh
```

### Running a single test

(This is usually driven by `tests/run.sh`.)

```bash
podman run --rm --privileged -v ./tests:/mnt \
    bootc-operator-test-k8s run /mnt/test-smoke.sh
```

The test script runs inside the VM via `systemd-run` with
`KUBECONFIG` already set. The exit code is propagated back.

### Running all tests

```bash
./tests/run.sh              # run all test-*.sh scripts
./tests/run.sh -v smoke     # run only test-smoke.sh, verbose output
```

## Writing tests

Test scripts live in `tests/` and are named `test-<name>.sh`. They run
as root inside the FCOS VM with the Kubernetes cluster already up. The
`tests/` directory is mounted at `/mnt/` via virtiofs, so tests can
source shared libraries.

### Rebooting

Tests can reboot the VM using the autopkgtest protocol. Call
`/tmp/autopkgtest-reboot <mark>` to trigger a reboot. After the VM
comes back, the test script re-runs from the top with
`AUTOPKGTEST_REBOOT_MARK` set to the mark string. Use a `case`
statement to branch:

```bash
case "${AUTOPKGTEST_REBOOT_MARK:-}" in
    "")
        # First boot: do initial checks
        /tmp/autopkgtest-reboot rebooted
        ;;
    rebooted)
        # Second boot: verify state survived reboot
        ;;
    *)
        echo "ERROR: unexpected mark: ${AUTOPKGTEST_REBOOT_MARK}" >&2
        exit 1
        ;;
esac
```

## Container requirements

The container needs `--privileged` for KVM acceleration and virtiofsd
capabilities.
