# CLAUDE.md

## Project

wisp — a minimal image builder for running a single static binary on
single-board computers.

## What wisp does

Takes a static binary + a target board name → produces a bootable image where
the binary runs as the only process on the system. No shell, no SSH, no package
manager, no persistent filesystem.

## Architecture

The output image has one FAT32 partition containing: board firmware, kernel
(pre-built, not compiled), and an initrd. The initrd is a gzipped cpio archive
containing a compiled Go init binary, a generated network config, and the
user's binary. At boot, the init binary mounts virtual filesystems, loads
kernel modules, configures networking via netlink, drops to a non-root user,
and `exec`s the target binary as PID 1. There is no shell anywhere.

For QEMU targets, no disk image is produced — wisp outputs kernel + initrd
files and launches QEMU directly.

Read `DESIGN.md` for full architecture details and rationale.

## Language and style

wisp is written in Go. The only external dependency is `golang.org/x/sys/unix`
(used in the init binary for portable Linux syscalls). Follow these conventions:

- Idiomatic Go per Rob Pike's proverbs.
- Small interfaces, useful zero values, explicit error handling.
- Table-driven tests.
- Comments on exported functions describe behavior and usage.
- Prefer deep modules with simple interfaces over shallow ones (Ousterhout's
  philosophy).
- If something feels complex, it probably needs a better abstraction. Flag it.

## Project layout

```text
wisp/
├── cmd/
│   ├── wisp/              # CLI entrypoint (thin wiring layer)
│   │   └── main.go
│   └── init/              # Init binary (cross-compiled, runs on target)
├── internal/
│   ├── board/             # Board profile types, loader, and embedded profiles
│   │   └── boards/        # Board profile JSONs (embedded via go:embed)
│   │       ├── pi5.json
│   │       ├── qemu.json
│   │       └── raspi3b.json
│   ├── initrd/            # Initrd assembly, init binary embedding, build logic
│   │   ├── initrd.go      # Low-level cpio archive writer
│   │   ├── initbin.go     # Embedded init binaries + arch lookup
│   │   ├── build.go       # High-level Build() assembling complete initrd
│   │   └── embed/         # Pre-built init binaries (cross-compiled by Makefile)
│   │       ├── init-arm64
│   │       ├── init-riscv64
│   │       └── init-amd64
│   ├── image/             # Disk image assembly (FAT32 boot partition)
│   ├── fetch/             # Asset fetching and caching (kernels, firmware)
│   └── validate/          # Binary validation (static ELF, correct arch)
├── testdata/
│   └── helloworld/        # Test HTTP server for QEMU integration tests
├── Makefile               # Two-step build: init binaries → wisp CLI
├── DESIGN.md              # Architecture and design decisions
├── README.md
├── CLAUDE.md              # This file
├── go.mod
└── go.sum
```

## Key commands

```sh
# Build an image
wisp build --target pi5 --binary ./myservice --ip 192.168.1.100/24 --gateway 192.168.1.1

# Build and run in QEMU (no hardware required)
wisp run --target qemu --binary ./myservice --ip 10.0.2.15/24 --gateway 10.0.2.2

# Build from config file
wisp build -f wisp.json

# List supported targets
wisp targets

# Validate a binary for a target
wisp validate --target pi5 --binary ./myservice
```

## Development workflow

```sh
# Build everything (cross-compile init, build wisp CLI, build test binary)
make

# Quick QEMU test
wisp run --target qemu --binary ./build/helloworld --ip 10.0.2.15/24 --gateway 10.0.2.2

# Run all tests
go test ./...

# Clean build artifacts
make clean
```

The Makefile performs a two-step build:

1. Cross-compile `cmd/init` → `internal/initrd/embed/init-{arm64,riscv64,amd64}`
2. Build `cmd/wisp` (which embeds the init binaries and board profiles via
   `go:embed`)

## Key constraints

- wisp does NOT compile kernels. It downloads pre-built ones from Alpine Linux.
- wisp does NOT support dynamically linked binaries.
- The initrd is the entire rootfs. There is no second partition.
- All configuration (IP, gateway, DNS) is baked into the image at build time.
- The target binary becomes PID 1 after init. If it exits, kernel panics. This
  is intentional.
- Do NOT use Unified Kernel Images (UKI). Pi boards don't do EFI boot natively.
- Pi 5 uses 16KB page size. Binaries must be 16KB-aligned.
- The init binary (`cmd/init`) is architecture-portable — build-tagged
  `linux` only, no `arm64` constraint. Cross-compile for any Linux
  architecture.

## Board profiles

Board profiles are JSON files in `internal/board/boards/` that define everything
board-specific: firmware URLs, DTB filename, kernel package source, required
modules, network interface name, architecture. Embedded into the wisp binary
via `go:embed` and parsed with `board.Parse()`.

Adding a new board = adding a new JSON file. No code changes required (ideally).

## Testing

- Unit tests for initrd assembly, cpio creation, ELF validation, FAT32 image
  writing, asset fetching and caching.
- Integration tests use the `qemu` target: build the kernel + initrd, boot in
  QEMU, and verify the binary is reachable over a forwarded port. No hardware
  required.
- No tests that require actual hardware. Hardware testing is manual.
