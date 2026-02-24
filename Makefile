BUILD_DIR := build
EMBED_DIR := internal/initrd/embed

.PHONY: all clean wisp kill

# Two-step build: cross-compile init binaries, then build wisp CLI (which embeds them).
all: wisp $(BUILD_DIR)/helloworld

# Step 1: Cross-compile init binaries for all supported architectures.
$(EMBED_DIR)/init-arm64: cmd/init/main.go
	@mkdir -p $(EMBED_DIR)
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o $@ ./cmd/init/

$(EMBED_DIR)/init-riscv64: cmd/init/main.go
	@mkdir -p $(EMBED_DIR)
	GOOS=linux GOARCH=riscv64 CGO_ENABLED=0 go build -o $@ ./cmd/init/

$(EMBED_DIR)/init-amd64: cmd/init/main.go
	@mkdir -p $(EMBED_DIR)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o $@ ./cmd/init/

INIT_BINARIES := $(EMBED_DIR)/init-arm64 $(EMBED_DIR)/init-riscv64 $(EMBED_DIR)/init-amd64

# Step 2: Build the wisp CLI (embeds init binaries and board profiles).
wisp: $(BUILD_DIR)/wisp
$(BUILD_DIR)/wisp: $(INIT_BINARIES) cmd/wisp/main.go
	@mkdir -p $(BUILD_DIR)
	go build -o $@ ./cmd/wisp/

# Cross-compile the test helloworld binary for aarch64
$(BUILD_DIR)/helloworld: testdata/helloworld/main.go
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o $@ ./testdata/helloworld/

# Kill any running QEMU instances
kill:
	@pkill -f qemu-system-aarch64 2>/dev/null || true
	@lsof -ti :18080 2>/dev/null | xargs kill -9 2>/dev/null || true
	@echo "killed"

clean:
	rm -rf $(BUILD_DIR)
	rm -f $(EMBED_DIR)/init-arm64 $(EMBED_DIR)/init-riscv64 $(EMBED_DIR)/init-amd64
