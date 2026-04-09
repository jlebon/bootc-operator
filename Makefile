.PHONY: all build-vm-image build-cluster-image build-disk deploy setup-network cluster-start cluster-stop clean help

# Image names and tags
BOOTC_IMAGE := localhost/fedora-bootc-k8s:latest
CLUSTER_IMAGE := localhost/cluster:latest
DISK_IMAGE := fedora-bootc-k8s.qcow2
DISK_SIZE := 10G

# Directories
IMAGES_DIR := containerfiles/images
VM_DIR := containerfiles/vm
OUTPUT_DIR := vm/images
CLUSTER_YAML := vm/cluster.yaml

all: build-cluster-image build-vm-image build-disk

# Build the fedora-bootc-k8s VM image
build-vm-image:
	@echo "=== Building fedora-bootc-k8s VM image ==="
	podman build -t $(BOOTC_IMAGE) -f $(IMAGES_DIR)/Containerfile $(IMAGES_DIR)
	@echo "✅ VM image built: $(BOOTC_IMAGE)"

# Build the cluster container image
build-cluster-image:
	@echo "=== Building cluster container image ==="
	podman build -t $(CLUSTER_IMAGE) -f $(VM_DIR)/Containerfile $(VM_DIR)
	@echo "✅ Cluster image built: $(CLUSTER_IMAGE)"

# Convert bootc image to qcow2 disk
build-disk: build-vm-image
	@echo "=== Converting bootc image to disk ==="
	@mkdir -p $(OUTPUT_DIR)
	cd $(OUTPUT_DIR) && \
	RUST_LOG=debug bcvk to-disk -K \
		--karg 'console=tty0' \
		--karg 'console=ttyS0,115200n8' \
		--filesystem ext4 \
		--format qcow2 \
		--disk-size $(DISK_SIZE) \
		$(BOOTC_IMAGE) $(DISK_IMAGE)
	@echo "✅ Disk image created: $(OUTPUT_DIR)/$(DISK_IMAGE)"

# Clean built images and disk
clean:
	@echo "=== Cleaning up ==="
	podman rmi -f $(BOOTC_IMAGE) $(CLUSTER_IMAGE) 2>/dev/null || true
	rm -f $(OUTPUT_DIR)/$(DISK_IMAGE)
	@echo "✅ Cleaned up images and disk"

# Clean disk image only
clean-disk:
	@echo "=== Cleaning disk image ==="
	rm -f $(OUTPUT_DIR)/$(DISK_IMAGE)
	@echo "✅ Disk image removed"

# Rebuild everything from scratch
rebuild: clean all

# Deploy the cluster container
deploy: build-cluster-image
	@echo "=== Deploying cluster container ==="
	podman kube play $(CLUSTER_YAML)
	@echo "✅ Cluster container deployed"

# Setup libvirt network in the cluster container
setup-network: deploy
	@echo "=== Setting up libvirt network ==="
	@sleep 3
	./vm/setup-network.sh
	@echo "✅ Network configured"

# Start the cluster (deploy + setup network + create node1 + init cluster)
cluster-start: deploy setup-network
	@echo "✅ Cluster is ready!"
	@echo ""
	@echo "=== Creating node1 VM ==="
	./vm/create-vm.sh -n node1
	@echo ""
	@echo "=== Initializing Kubernetes cluster on node1 ==="
	./vm/init-cluster.sh node1
	@echo ""
	@echo "✅ Cluster initialized on node1!"

# Stop and remove the cluster container
cluster-stop:
	@echo "=== Stopping cluster container ==="
	podman kube down $(CLUSTER_YAML) || true
	@echo "✅ Cluster stopped"

help:
	@echo "Makefile for building and deploying cluster and VM images"
	@echo ""
	@echo "Build Targets:"
	@echo "  all                 - Build both cluster and VM images, then create disk (default)"
	@echo "  build-vm-image      - Build the fedora-bootc-k8s VM container image"
	@echo "  build-cluster-image - Build the cluster container image"
	@echo "  build-disk          - Convert bootc image to qcow2 disk image"
	@echo ""
	@echo "Deployment Targets:"
	@echo "  cluster-start       - Deploy cluster and setup network (recommended)"
	@echo "  deploy              - Deploy cluster container with podman kube play"
	@echo "  setup-network       - Setup libvirt network in cluster container"
	@echo "  cluster-stop        - Stop and remove cluster container"
	@echo ""
	@echo "Clean Targets:"
	@echo "  clean               - Remove all built images and disk"
	@echo "  clean-disk          - Remove only the disk image"
	@echo "  rebuild             - Clean and rebuild everything"
	@echo ""
	@echo "Other:"
	@echo "  help                - Show this help message"
	@echo ""
	@echo "Images:"
	@echo "  VM image:      $(BOOTC_IMAGE)"
	@echo "  Cluster image: $(CLUSTER_IMAGE)"
	@echo "  Disk image:    $(OUTPUT_DIR)/$(DISK_IMAGE)"
