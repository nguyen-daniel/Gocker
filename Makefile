.PHONY: build test setup run clean bench test-unprivileged lint demo

BINARY_NAME=gocker
ROOTFS_DIR=rootfs
ALPINE_IMAGE=alpine:latest

# Build compiles the Go binary
# This creates the gocker executable that will be used for container operations
build:
	@echo "Building $(BINARY_NAME)..."
	@go build -o $(BINARY_NAME) ./cmd/gocker
	@chmod +x $(BINARY_NAME)
	@echo "Build complete: $(BINARY_NAME)"

# Format and vet. Used by CI; no extra linters (keep the zero-dependency bar).
lint:
	@echo "Checking gofmt..."
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	@echo "Running go vet..."
	@go vet ./...

# Setup downloads and extracts a mini-Alpine rootfs using docker export
# This is the shared OverlayFS lower layer; each container gets its own upper/work
setup: $(ROOTFS_DIR)
	@echo "Rootfs is already set up at $(ROOTFS_DIR)/"

$(ROOTFS_DIR):
	@echo "Rootfs directory not found. Setting up Alpine Linux rootfs using Docker..."
	@echo "Note: Rootfs is the shared OverlayFS lower layer for filesystem isolation"
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

# Unprivileged unit tests (user-namespace clone flags; no sudo).
test-unprivileged: build
	@echo "Running namespace unit tests without sudo..."
	@GOCKER_ALLOW_UNPRIVILEGED=1 go test -v ./internal/ns ./internal/cgroup ./internal/net ./internal/overlay ./cmd/gocker -run 'TestNamespaceConfig|TestCloneUserNamespace|TestParseCPULimit|TestParseMemoryLimit|TestFindFreeIP|TestMountPoint|TestDropTeachingCaps|TestParseRunFlags|TestParseIDAndBoolFlag|TestGenerateContainerID|TestShortID|TestHelpRequest|TestIsHelpArg|TestHelpExitsZeroWithoutRoot'

# Startup benchmark vs docker (Linux + sudo). Writes docs/BENCHMARKS.md.
# Invoke via bash so a missing execute bit (git 100644, Windows checkouts) cannot fail CI.
bench: build setup
	@N=$${N:-20} bash ./scripts/bench_startup.sh

# Recruiter walkthrough (Linux + root). Re-execs sudo if needed.
# Shows hostname/UTS, OverlayFS isolation, pids.max=20, two detached IPs + ps.
demo: build setup
	@echo "Running demo (sudo/root required for namespaces, cgroups, overlay, bridge)..."
	@bash ./scripts/demo.sh

clean:
	@echo "Cleaning up..."
	@rm -f $(BINARY_NAME)
	@echo "Clean complete"

