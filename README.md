# wisp

A minimal image builder for running a single static binary on single-board computers. No shell. No SSH. No package manager. Just a kernel, an initrd, and your binary.

wisp produces bootable SD card images where the entire userspace runs from RAM. The only way to update is to reflash. The only thing running is your service.

## What it does

You give wisp a static binary and a target board. It produces an SD card image containing:

- An off-the-shelf Linux kernel (no custom builds)
- A minimal initrd that configures networking and runs your binary as a non-privileged user
- Board-specific firmware and device tree blobs

That's it. There is no filesystem on disk. The SD card is read once at boot and never touched again.

## Supported targets

- Raspberry Pi 5
- QEMU (aarch64/virt) — for development and testing without hardware

## Supported binary formats

wisp works with any static binary compiled for the target architecture. The binary must be self-contained — no dynamic linking, no shared libraries, no runtime dependencies.

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
dd if=wisp-output/image.img of=/dev/sdX bs=4M status=progress
```

Boot the Pi. Your service is running.

Run in QEMU without hardware:

```sh
wisp run --target qemu --binary ./myservice --ip 192.168.1.100/24 --gateway 192.168.1.1
```

## Design principles

- **Single binary, single purpose.** wisp images run one binary. If you need two services, make two images on two boards.
- **Immutable by default.** The rootfs lives in RAM. Nothing is writable on disk. No state survives reboot unless your binary manages it over the network.
- **No shell, no escape hatch.** There is no way to log into a wisp image. If something is wrong, you reflash. This is a feature.
- **Off-the-shelf kernels.** wisp does not compile kernels. It pulls pre-built kernels from distribution packages. Kernel configuration is somebody else's problem.
- **Deterministic output.** Same binary + same config = same image. Every time.

## How it works

wisp assembles a boot image from three layers:

1. **Board firmware** — Pi-specific boot files (`start4.elf`, `fixup4.dat`, device tree blobs). Sourced from the Raspberry Pi firmware repository.
2. **Kernel** — A pre-built `Image` file extracted from Alpine Linux or Raspberry Pi OS packages. Includes necessary kernel modules bundled into the initrd.
3. **Initrd** — A minimal cpio archive containing:
   - A static busybox (used only during init, not accessible after boot)
   - An init script that mounts virtual filesystems, configures networking, and execs the target binary
   - The target binary itself
   - A minimal `/etc/passwd` defining the non-privileged service user

At boot, the Pi firmware loads the kernel and initrd into RAM. The kernel unpacks the initrd as its root filesystem. The init script runs, brings up the network, drops privileges, and `exec`s your binary as PID 1. From that point forward, your binary is the only process running on the system.

## Configuration

```yaml
# wisp.yaml
target: pi5

binary: ./build/myservice

network:
  interface: eth0
  address: 192.168.1.100/24
  gateway: 192.168.1.1
  dns: 1.1.1.1
  # or: dhcp: true

service:
  user: service
  uid: 1000
```

## Project status

Early development. Expect breaking changes.

## License

MIT
