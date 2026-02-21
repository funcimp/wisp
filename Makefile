BUILD_DIR := build
MODULES_DIR := $(BUILD_DIR)/modules
CONF_DIR := $(BUILD_DIR)/conf
EMBED_DIR := cmd/wisp/embed

# Cross-compilation for aarch64 Linux (used for init and service binaries)
CROSS_ENV := GOOS=linux GOARCH=arm64 CGO_ENABLED=0

# Alpine kernel for QEMU (v3.23, kernel 6.18 LTS)
KERNEL_PKG := linux-virt-6.18.13-r0.apk
KERNEL_URL := https://dl-cdn.alpinelinux.org/alpine/v3.23/main/aarch64/$(KERNEL_PKG)
KERNEL_VER := 6.18.13-0-virt

# Kernel modules needed for QEMU virtio networking (load order matters).
# virtio and virtio_pci are built-in; these three are modules.
QEMU_MODULES := \
	lib/modules/$(KERNEL_VER)/kernel/net/core/failover.ko.gz \
	lib/modules/$(KERNEL_VER)/kernel/drivers/net/net_failover.ko.gz \
	lib/modules/$(KERNEL_VER)/kernel/drivers/net/virtio_net.ko.gz

# QEMU settings
QEMU_PORT := 18080

.PHONY: all clean initrd kernel qemu kill wisp

# Two-step build: cross-compile init binary, then build wisp CLI (which embeds it).
all: wisp $(BUILD_DIR)/helloworld

# Step 1: Cross-compile the init binary into the embed directory.
$(EMBED_DIR)/init-arm64: cmd/init/main.go
	@mkdir -p $(EMBED_DIR)
	$(CROSS_ENV) go build -o $@ ./cmd/init/

# Step 2: Build the wisp CLI (embeds init binary and board profiles).
wisp: $(BUILD_DIR)/wisp
$(BUILD_DIR)/wisp: $(EMBED_DIR)/init-arm64 cmd/wisp/main.go
	@mkdir -p $(BUILD_DIR)
	go build -o $@ ./cmd/wisp/

# Cross-compile the init binary to build/ (for manual Makefile-based workflow)
$(BUILD_DIR)/init: cmd/init/main.go
	@mkdir -p $(BUILD_DIR)
	$(CROSS_ENV) go build -o $@ ./cmd/init/

# Cross-compile the test helloworld binary for aarch64
$(BUILD_DIR)/helloworld: testdata/helloworld/main.go
	@mkdir -p $(BUILD_DIR)
	$(CROSS_ENV) go build -o $@ ./testdata/helloworld/

# Build mkinitrd for the host (not cross-compiled)
$(BUILD_DIR)/mkinitrd: cmd/mkinitrd/main.go internal/initrd/initrd.go
	@mkdir -p $(BUILD_DIR)
	go build -o $@ ./cmd/mkinitrd/

# Download Alpine virt kernel and extract modules (cached)
kernel: $(BUILD_DIR)/vmlinuz
$(BUILD_DIR)/vmlinuz:
	@mkdir -p $(BUILD_DIR)
	curl -sL -o $(BUILD_DIR)/$(KERNEL_PKG) "$(KERNEL_URL)"
	cd $(BUILD_DIR) && tar -xzf $(KERNEL_PKG) boot/vmlinuz-virt $(QEMU_MODULES) 2>/dev/null
	mv $(BUILD_DIR)/boot/vmlinuz-virt $@
	@mkdir -p $(MODULES_DIR)
	for mod in $(QEMU_MODULES); do \
		gunzip -c $(BUILD_DIR)/$$mod > $(MODULES_DIR)/$$(basename $$mod .gz); \
	done
	rm -rf $(BUILD_DIR)/boot $(BUILD_DIR)/lib $(BUILD_DIR)/$(KERNEL_PKG)
	@echo "kernel: $@"
	@ls -1 $(MODULES_DIR)/

# Generate config files for QEMU
$(CONF_DIR)/wisp.conf:
	@mkdir -p $(CONF_DIR)
	printf 'IFACE=eth0\nADDR=10.0.2.15/24\nGW=10.0.2.2\n' > $@

$(CONF_DIR)/resolv.conf:
	@mkdir -p $(CONF_DIR)
	printf 'nameserver 10.0.2.3\n' > $@

$(CONF_DIR)/modules:
	@mkdir -p $(CONF_DIR)
	printf 'failover.ko\nnet_failover.ko\nvirtio_net.ko\n' > $@

# Assemble the initrd using our Go cpio writer (no shelling out to cpio)
initrd: $(BUILD_DIR)/initrd.img
$(BUILD_DIR)/initrd.img: $(BUILD_DIR)/mkinitrd $(BUILD_DIR)/init $(BUILD_DIR)/helloworld $(BUILD_DIR)/vmlinuz $(CONF_DIR)/wisp.conf $(CONF_DIR)/resolv.conf $(CONF_DIR)/modules
	$(BUILD_DIR)/mkinitrd \
		-o $@ \
		-init $(BUILD_DIR)/init \
		-service $(BUILD_DIR)/helloworld \
		-conf $(CONF_DIR)/wisp.conf \
		-resolv $(CONF_DIR)/resolv.conf \
		-modules-list $(CONF_DIR)/modules \
		-modules-dir $(MODULES_DIR)

# Build everything and boot in QEMU (manual Makefile workflow)
qemu: initrd kernel
	@echo "Booting QEMU (port forward: localhost:$(QEMU_PORT) -> guest:8080)"
	@echo "Test: curl http://localhost:$(QEMU_PORT)/"
	@echo "Quit: Ctrl-A X"
	@echo "---"
	qemu-system-aarch64 \
		-machine virt \
		-accel hvf \
		-cpu host \
		-m 512M \
		-kernel $(BUILD_DIR)/vmlinuz \
		-initrd $(BUILD_DIR)/initrd.img \
		-append "rdinit=/init console=ttyAMA0 net.ifnames=0 quiet" \
		-nographic \
		-netdev user,id=net0,hostfwd=tcp::$(QEMU_PORT)-:8080 \
		-device virtio-net-pci,netdev=net0

# Kill any running QEMU instances (by name and by port)
kill:
	@pkill -f qemu-system-aarch64 2>/dev/null || true
	@lsof -ti :$(QEMU_PORT) 2>/dev/null | xargs kill -9 2>/dev/null || true
	@echo "killed"

clean:
	rm -rf $(BUILD_DIR)
	rm -f $(EMBED_DIR)/init-arm64
