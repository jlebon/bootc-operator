#!/bin/bash

cd images
RUST_LOG=debug bcvk to-disk -K \
	--karg 'console=tty0' \
	--karg 'console=ttyS0,115200n8'  \
	--filesystem ext4  \
	--format qcow2 \
	--disk-size 10G \
	localhost/fedora-bootc-k8s:latest fedora-bootc-k8s.qcow2
