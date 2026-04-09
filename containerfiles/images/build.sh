#!/bin/bash	

podman build \
	--device /dev/vhost-vsock \
	--device /dev/kvm \
	--device /dev/fuse \
	--device /dev/net/tun \
   -v $HOME/.local/share/containers/storage:$HOME/.local/share/containers/storage: \
	--security-opt label=disable \
	-v $XDG_RUNTIME_DIR/podman/podman.sock:/run/podman/podman.sock:Z \
	-v /run/libvirt/libvirt-sock:/run/libvirt/libvirt-sock \
	-t test  -f Containerfile.test .

