# wisp

> **Warning:** wisp is early alpha software. It is under active development and
> has not been thoroughly tested on real hardware. Expect breaking changes,
> missing features, and rough edges. Use at your own risk.

A minimal image builder for running a single static binary on single-board
computers. No shell. No SSH. No package manager. Just a kernel, an initrd,
and your binary.

wisp produces bootable SD card images where the entire userspace runs from
RAM. The only way to update is to reflash. The only thing running is your
service.

## What it does

You give wisp a static binary and a target board. It produces a bootable image
containing:

- An off-the-shelf Linux kernel (no custom builds)
- A minimal initrd that configures networking and runs your binary as a
  non-privileged user
- Board-specific firmware and device tree blobs

There is no filesystem on disk. The SD card is read once at boot and never
touched again.

## Supported targets

- **Raspberry Pi 5** — Produces a FAT32 disk image ready to flash
- **QEMU (aarch64/virt)** — For development and testing without hardware

## Supported binary formats

wisp works with any static binary compiled for the target architecture. The
binary must be self-contained — no dynamic linking, no shared libraries, no
runtime dependencies.

- **Go** — `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build`
- **Rust** — `cargo build --release --target aarch64-unknown-linux-musl`
- **Zig** — `zig build -Dtarget=aarch64-linux-musl -Doptimize=ReleaseSafe`

## Quick start

Build an image for Pi 5:

```sh
wisp build --target pi5 --binary ./myservice --ip 192.168.1.100/24 --gateway 192.168.1.1
```

Flash the output image:

```sh
dd if=wisp-output/pi5.img of=/dev/sdX bs=4M status=progress
```

Boot the Pi. Your service is running.

Run in QEMU without hardware:

```sh
wisp run --target qemu --binary ./myservice --ip 10.0.2.15/24 --gateway 10.0.2.2
```

Test it:

```sh
curl http://localhost:18080/
```

## Commands

```sh
# Build an image
wisp build --target pi5 --binary ./myservice --ip 192.168.1.100/24 --gateway 192.168.1.1

# Build and boot in QEMU
wisp run --target qemu --binary ./myservice --ip 10.0.2.15/24 --gateway 10.0.2.2

# Build from a config file
wisp build -f wisp.json

# List supported targets
wisp targets

# Validate a binary for a target
wisp validate --target pi5 --binary ./myservice
```

## Configuration

All configuration can be provided via flags or a JSON config file:

```json
{
  "target": "pi5",
  "binary": "./build/myservice",
  "ip": "192.168.1.100/24",
  "gateway": "192.168.1.1",
  "dns": "1.1.1.1",
  "output": "wisp-output"
}
```

## Design principles

- **Single binary, single purpose.** wisp images run one binary. If you need
  two services, make two images on two boards.
- **Immutable by default.** The rootfs lives in RAM. Nothing is writable on
  disk. No state survives reboot unless your binary manages it over the network.
- **No shell, no escape hatch.** There is no way to log into a wisp image. If
  something is wrong, you reflash. This is a feature.
- **Off-the-shelf kernels.** wisp does not compile kernels. It pulls pre-built
  kernels from distribution packages.
- **Deterministic output.** Same binary + same config = same image. Every time.

## How it works

wisp assembles a boot image from three layers:

1. **Board firmware** — Pi-specific boot files (`start4.elf`, `fixup4.dat`,
   device tree blobs). Sourced from the Raspberry Pi firmware repository.
   QEMU targets skip this — QEMU provides its own firmware.
2. **Kernel** — A pre-built kernel image extracted from Alpine Linux packages.
   Required kernel modules are bundled into the initrd.
3. **Initrd** — A gzipped cpio archive containing:
   - A compiled Go init binary (pre-built, embedded in the wisp tool)
   - Generated network config (`wisp.conf`, `resolv.conf`) with address,
     gateway, and DNS baked in
   - The target binary itself
   - Kernel modules needed for the target board

At boot, the firmware loads the kernel and initrd into RAM. The kernel unpacks
the initrd and runs `/init`. The init binary mounts virtual filesystems, loads
kernel modules, configures the network interface via netlink, drops privileges,
and `exec`s your binary as PID 1. There is no shell at any point. From that
point forward, your binary is the only process running on the system.

## Development

Build the wisp tool (requires Go 1.26+):

```sh
make
```

This cross-compiles the init binary for the target architecture, then builds
the wisp CLI which embeds it.

Quick QEMU test without the wisp CLI (Makefile workflow):

```sh
make qemu
```

Run tests:

```sh
go test ./...
```

## Project status

Early development. Expect breaking changes.

## License

MIT
