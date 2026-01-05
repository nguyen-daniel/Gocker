.PHONY: build test setup run clean

BINARY_NAME=gocker
ROOTFS_DIR=rootfs
ALPINE_IMAGE=alpine:latest

# Build compiles the Go binary
# This creates the gocker executable that will be used for container operations
build:
	@echo "Building $(BINARY_NAME)..."
	@go build -o $(BINARY_NAME) .
	@echo "Build complete: $(BINARY_NAME)"

# Setup downloads and extracts a mini-Alpine rootfs using docker export
# This is necessary because Gocker uses chroot to create filesystem isolation
# The rootfs provides a minimal Linux environment inside the container
setup: $(ROOTFS_DIR)
	@echo "Rootfs is already set up at $(ROOTFS_DIR)/"

$(ROOTFS_DIR):
	@echo "Rootfs directory not found. Setting up Alpine Linux rootfs using Docker..."
	@echo "Note: Rootfs is required for chroot-based filesystem isolation"
	@mkdir -p $(ROOTFS_DIR)
	@echo "Pulling Alpine image..."
	@docker pull $(ALPINE_IMAGE) > /dev/null 2>&1 || true
	@echo "Cleaning up any existing temporary container..."
	@docker rm -f gocker-temp > /dev/null 2>&1 || true
	@echo "Creating temporary container..."
	@docker create --name gocker-temp $(ALPINE_IMAGE) > /dev/null
	@echo "Exporting container filesystem..."
	@docker export gocker-temp | tar -xC $(ROOTFS_DIR)
	@echo "Cleaning up temporary container..."
	@docker rm gocker-temp > /dev/null 2>&1 || true
	@echo "Alpine rootfs extracted successfully to $(ROOTFS_DIR)/"

# Test runs the integration tests with sudo privileges
# Sudo is required because Linux namespaces (CLONE_NEWUTS, CLONE_NEWPID, CLONE_NEWNS)
# and cgroups operations require root privileges for container isolation
test: build setup
	@echo "Running tests with sudo (required for namespace operations)..."
	@echo "Note: Sudo is necessary because creating namespaces requires root privileges"
	@sudo go test -v ./...

run: build $(ROOTFS_DIR)
	@echo "Running $(BINARY_NAME)..."
	@sudo ./$(BINARY_NAME) run /bin/sh

clean:
	@echo "Cleaning up..."
	@rm -f $(BINARY_NAME)
	@echo "Clean complete"

