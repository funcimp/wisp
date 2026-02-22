# CLAUDE.md

## Project

wisp — a minimal image builder for running a single static binary on single-board computers.

## What wisp does

Takes a static binary + a target board name → produces a bootable SD card image where the binary runs as the only process on the system. No shell, no SSH, no package manager, no persistent filesystem.

## Architecture

The output image has one FAT32 partition containing: board firmware, kernel (pre-built, not compiled), and an initrd. The initrd is a gzipped cpio archive containing busybox (static), an init script, and the user's binary. At boot, the init script configures networking, drops to a non-root user, and `exec`s the target binary as PID 1.

Read `DESIGN.md` for full architecture details and rationale.

## Language and style

wisp is written in Go. Follow these conventions:

- Idiomatic Go per Rob Pike's proverbs.
- Small interfaces, useful zero values, explicit error handling.
- Table-driven tests.
- Comments on exported functions describe behavior and usage.
- Prefer deep modules with simple interfaces over shallow ones (Ousterhout's philosophy).
- If something feels complex, it probably needs a better abstraction. Flag it.

## Project layout

```
wisp/
├── cmd/wisp/           # CLI entrypoint
├── internal/
│   ├── board/          # Board profiles (pi5, qemu)
│   ├── initrd/         # Initrd assembly (cpio archive creation)
│   ├── image/          # Disk image assembly (FAT32 boot partition)
│   ├── fetch/          # Asset fetching and caching (kernels, firmware)
│   └── validate/       # Binary validation (static ELF, correct arch)
├── assets/
│   └── init.sh         # Init script template
├── boards/
│   ├── pi5.yaml        # Raspberry Pi 5 board profile
│   └── qemu.yaml       # QEMU aarch64/virt board profile
├── DESIGN.md           # Architecture and design decisions
├── README.md
├── CLAUDE.md           # This file
├── go.mod
└── go.sum
```

## Key commands

```sh
# Build an image
wisp build --target pi5 --binary ./myservice --ip 192.168.1.100/24 --gateway 192.168.1.1

# Build and run in QEMU (no hardware required)
wisp run --target qemu --binary ./myservice --ip 192.168.1.100/24 --gateway 192.168.1.1

# Build from config file
wisp build -f wisp.yaml

# List supported targets
wisp targets

# Validate a binary for a target
wisp validate --target pi5 --binary ./myservice
```

## Development workflow

```sh
go build -o wisp ./cmd/wisp
go test ./...
```

## Key constraints

- wisp does NOT compile kernels. It downloads pre-built ones.
- wisp does NOT support dynamically linked binaries.
- The initrd is the entire rootfs. There is no second partition.
- All configuration (IP, gateway, DNS) is baked into the image at build time.
- The target binary becomes PID 1 after init. If it exits, kernel panics. This is intentional.
- Do NOT use Unified Kernel Images (UKI). Pi boards don't do EFI boot natively.
- Pi 5 uses 16KB page size. Binaries must be 16KB-aligned.

## Board profiles

Board profiles are YAML files that define everything board-specific: firmware URLs, DTB filename, kernel package source, required modules, network interface name, architecture.

Adding a new board = adding a new YAML profile. No code changes required (ideally).

## Testing

- Unit tests for initrd assembly, cpio creation, ELF validation.
- Integration tests use the `qemu` target: build the kernel + initrd, boot in QEMU, and
  verify the binary is reachable over a forwarded port. No hardware required.
- No tests that require actual hardware. Hardware testing is manual.
