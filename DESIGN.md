# wisp design

This document captures the architecture, constraints, and technical decisions behind wisp. It serves as context for development.

## Problem

Running a single network service on a single-board computer should not require a general-purpose operating system. Every package, daemon, shell, and login mechanism that ships with a traditional Linux distribution is attack surface you don't need and cognitive load you don't want.

Unikernels solve this for cloud/VM environments but require a hypervisor on SBCs, which defeats the purpose. Buildroot solves this but is a heavyweight build system designed for complex embedded products. gokrazy is close but Go-only and opinionated about updates and monitoring.

wisp is the minimal layer between hardware and a static binary. Nothing more.

## Architecture

### Boot sequence

```
Pi firmware (GPU, closed source)
  → reads config.txt from FAT32 boot partition
  → loads kernel Image and initrd into RAM
  → loads device tree blob (.dtb)
  → jumps to kernel

Kernel
  → unpacks initrd as rootfs (tmpfs, lives in RAM)
  → executes /init

/init (shell script, busybox)
  → mounts /dev (devtmpfs), /proc, /sys
  → configures network interface
  → drops privileges via exec to non-root user
  → execs target binary as PID 1
```

After the final `exec`, the init script and busybox are no longer in memory as running processes. The target binary IS the system.

### Image layout

```
SD card:
└── boot (FAT32, ~60MB)
    ├── config.txt          # Pi firmware config
    ├── cmdline.txt         # Kernel command line
    ├── start4.elf          # Pi firmware (or start_cd.elf for cut-down)
    ├── fixup4.dat          # Pi firmware fixup
    ├── bcm2712-rpi-5-b.dtb # Device tree (board-specific)
    ├── overlays/           # Device tree overlays (if needed)
    ├── Image               # Linux kernel (arm64)
    └── initrd.img          # Everything else
```

There is no second partition. The entire userspace is the initrd, which lives in RAM after boot. The SD card can theoretically be removed after boot completes (though the firmware may need it present).

### Initrd contents

```
initrd.img (gzipped cpio archive):
/
├── init                    # Shell script, PID 1 until exec
├── bin/
│   └── busybox             # Static build, provides sh, ip, mount, etc.
├── etc/
│   ├── passwd              # Defines service user
│   └── resolv.conf         # DNS config, baked in at build time
├── dev/                    # Mount point for devtmpfs
├── proc/                   # Mount point for procfs
├── sys/                    # Mount point for sysfs
└── service/
    └── run                 # The target static binary (always named "run")
```

busybox is sourced from the Alpine Linux `busybox-static` aarch64 package — the same
package registry as the kernel. Version and sha256 are specified in the board profile.

### Init script

The init script is intentionally simple. It should be readable in under a minute.

```sh
#!/bin/busybox sh

# Mount virtual filesystems
/bin/busybox mount -t devtmpfs devtmpfs /dev
/bin/busybox mount -t proc proc /proc
/bin/busybox mount -t sysfs sysfs /sys

# Configure network
/bin/busybox ip link set lo up
/bin/busybox ip link set eth0 up
/bin/busybox ip addr add 192.168.1.100/24 dev eth0
/bin/busybox ip route add default via 192.168.1.1

# Drop privileges and exec the service binary
# After this line, busybox and this script are gone.
# The service binary becomes PID 1.
exec /bin/busybox chpst -u service /service/run
```

The script above shows literal values. The init script is a Go template — `wisp build`
performs string substitution at image build time. The script shipped inside the initrd
contains no template variables, only the literal values from the build configuration.

For DHCP, the init script runs `busybox udhcpc` in the foreground until a lease is
acquired, then proceeds to exec the service binary.

### Privilege model

The target binary runs as a non-root user (`service`, uid 1000). The init script runs as
root only long enough to mount filesystems and configure networking, then permanently drops
privileges via `exec chpst -u`.

Since the binary is PID 1, if it exits, the kernel panics. This is intentional. A crashed
service should not silently restart — it should be observable as a hardware-level failure
(no network response, no heartbeat). If restart behavior is desired, the binary should
implement it internally.

**PID 1 signal semantics**: Linux does not deliver SIGTERM or SIGINT to PID 1 unless the
process explicitly handles them. If your binary needs to respond to shutdown signals (e.g.,
for graceful drain on reboot), it must install signal handlers. Binaries that ignore signals
will not respond to SIGTERM. This is a Linux kernel behavior, not a wisp constraint.

## Design decisions

### Pure Go implementation

wisp has no external tool dependencies. FAT32 image creation and cpio archive assembly are
handled by Go libraries. wisp runs natively on Linux and macOS without Docker or system
tools (`mkdosfs`, `cpio`, etc.). The output is a single static binary.

### Off-the-shelf kernels

wisp does not compile kernels. Rationale:

- Kernel compilation is slow, fragile, and configuration-heavy.
- Pi hardware support requires specific patches that the Raspberry Pi kernel team maintains.
- The kernel is not where attack surface reduction matters — the userspace is.
- Pre-built kernels from Alpine or Raspberry Pi OS are tested, signed, and maintained by people whose job it is to do so.

wisp extracts kernel images and modules from distribution packages. The kernel source is tracked (distro, version, sha256) for reproducibility.

### No kernel modules on disk

Kernel modules needed at boot are bundled into the initrd. This is standard practice for diskless/netboot systems. Modules not needed are excluded. The module set is determined by the target board profile.

### Static binaries only

wisp does not support dynamically linked binaries. Rationale:

- A static binary has zero runtime dependencies on the host system.
- No libc version compatibility concerns.
- No need to include shared libraries in the initrd.
- Go, Rust, and Zig all produce static binaries easily.
- If a binary requires dynamic linking, it is not a good fit for wisp.

### No shell after boot

Busybox is present in the initrd for the init script but is not accessible after the service binary starts. The `exec` replaces the init process entirely. There is no way to get a shell on a running wisp system.

This means:
- No SSH, no serial console login, no debug shell.
- Debugging requires either: (a) adding logging/metrics to the binary, (b) serial console output from the kernel and init script during boot, or (c) reflashing with a debug image that includes a shell.
- This is the correct tradeoff for production. Debug images can be built separately.

### No persistent storage

The rootfs is tmpfs (RAM-backed). Nothing is writable on the SD card after boot. The binary has no filesystem to write to unless it creates tmpfs directories itself.

Persistent state should be managed externally — over the network, in a database, in object storage. The wisp image is stateless and disposable.

### DHCP vs static IP

Both are supported. Static IP is preferred for production (deterministic, no dependency on DHCP server). DHCP is convenient for development and home lab use.

DNS resolution is configured at build time via `/etc/resolv.conf` baked into the initrd.

## Target board profiles

Each supported board has a YAML profile that defines everything board-specific. Adding a
new board means adding a new YAML file — no code changes required (ideally).

Fields in a board profile:

- **`name`** — Board identifier, used as the `--target` value.
- **`arch`** — Target architecture (`aarch64`).
- **`page_size`** — Required binary alignment in bytes (4096 or 16384).
- **`network_interface`** — Primary network interface name (e.g., `eth0`).
- **`kernel`** — Distribution, package name, version, and sha256.
- **`firmware`** — List of firmware files: URL, destination filename, sha256.
- **`dtb`** — Device tree blob filename.
- **`boot_config`** — Content of `config.txt` written to the boot partition.
- **`cmdline`** — Kernel command line written to `cmdline.txt`.
- **`modules`** — Kernel modules to bundle into the initrd.

Example (`boards/pi5.yaml`):

```yaml
name: pi5
arch: aarch64
page_size: 16384
network_interface: eth0

kernel:
  source: alpine
  package: linux-rpi
  version: "6.6.31-r0"
  sha256: "<hash>"

busybox:
  source: alpine
  package: busybox-static
  version: "1.36.1-r2"
  sha256: "<hash>"

firmware:
  - url: "https://github.com/raspberrypi/firmware/raw/<commit>/boot/start4.elf"
    dest: start4.elf
    sha256: "<hash>"
  - url: "https://github.com/raspberrypi/firmware/raw/<commit>/boot/fixup4.dat"
    dest: fixup4.dat
    sha256: "<hash>"

dtb: bcm2712-rpi-5-b.dtb

boot_config: |
  arm_64bit=1
  kernel=Image
  initramfs initrd.img followkernel

cmdline: "console=serial0,115200 console=tty1 rdinit=/init net.ifnames=0"

modules:
  - broadcom/genet.ko
```

### Raspberry Pi 5

- SoC: BCM2712 (Cortex-A76, ARMv8.2)
- Firmware: `start4.elf`, `fixup4.dat` from `raspberrypi/firmware` repo
- DTB: `bcm2712-rpi-5-b.dtb`
- Kernel: `linux-rpi` package from Alpine aarch64
- Network: `eth0` (Gigabit Ethernet via RP1 southbridge)
- Architecture: `aarch64`
- Page size: 16KB (kernel and all binaries must be 16KB-aligned)

### QEMU (aarch64/virt)

- Machine: `virt` (`-machine virt -cpu cortex-a76`)
- Firmware: none — QEMU loads kernel and initrd directly via `-kernel`/`-initrd` flags
- DTB: generated by QEMU at runtime
- Kernel: same Alpine aarch64 kernel as Pi 5
- Network: `virtio-net`, interface `eth0` (via `net.ifnames=0` on kernel command line)
- Architecture: `aarch64`

`wisp run --target qemu` builds the kernel and initrd then launches QEMU with port
forwarding configured. No SD card image is produced. Useful for development, integration
testing, and verifying behavior before flashing hardware.

The `boards/qemu.yaml` profile omits `firmware`, `dtb`, and `boot_config` fields. The
`cmdline` uses `console=ttyAMA0` instead of the serial console entries used on hardware.

## Build pipeline

wisp is a CLI tool. The build process:

1. **Validate binary** — Confirm it is a static ELF binary for the correct architecture.
2. **Fetch board assets** — Download or cache kernel, firmware, DTBs for the target board.
   QEMU target: kernel only, no firmware or DTB.
3. **Build initrd** — Assemble the cpio archive: busybox, init script, passwd, target
   binary, kernel modules.
4. **Package for target**:
   - Hardware targets (`pi5`): assemble a FAT32 disk image containing all boot files.
   - QEMU target: emit `kernel` + `initrd.img` and construct the QEMU invocation.
5. **Output** — Write artifacts to the output directory along with a manifest of all
   components and their checksums.

The core outputs are `kernel` + `initrd.img`. The SD card image is a packaging step that
wraps these for hardware boot. QEMU uses the artifacts directly without packaging.

### Caching

Board assets (kernels, firmware, DTBs) are downloaded once and cached in
`~/.cache/wisp/<board>/<asset-type>/<version>/`. Cache entries are keyed by board name,
asset type, and sha256. A cached entry is used without re-downloading as long as the
version and checksum in the board profile match.

### Split initrd (future)

The kernel initrd spec allows pointing at multiple cpio archives that get merged. This enables:

```
base.cpio.gz      — busybox, init script, modules (stable, rarely changes)
service.cpio.gz   — just the target binary (changes every deploy)
```

This optimization is not required for v1 but should be kept in mind during design. It enables faster iteration because you only rebuild the service archive when deploying a new binary.

## Non-goals

- **Multiple binaries per image.** wisp runs one binary. Use separate boards or a different tool.
- **Runtime package management.** There is no package manager. The image is the deployment artifact.
- **Kernel compilation.** wisp uses pre-built kernels.
- **GUI or display output.** wisp is for headless network services.
- **Container support.** No Docker, no containerd, no OCI. The binary runs directly.
- **Orchestration.** wisp builds images. Fleet management is a separate concern.
- **Unified Kernel Images (UKI).** UKIs are optimized for EFI boot, which Pi boards don't use natively. Avoid this path.

## Future considerations

- **Raspberry Pi Zero 2 W:** WiFi-only support requires firmware blobs for the wireless
  chipset, `wpa_supplicant` in the initrd, WiFi credentials baked into a
  `wpa_supplicant.conf`, and a different network bring-up sequence in the init script
  (associate before exec). This is a meaningful increase in initrd assembly complexity and
  a second code path through the network configuration logic. Deferred to v2.
- **Network boot (PXE/TFTP):** Pi 5 supports network boot. wisp could produce
  TFTP-servable artifacts instead of SD card images. The initrd-based architecture makes
  this natural — the QEMU target already separates kernel + initrd from packaging.
- **A/B partition updates:** An alternative to reflashing where two boot partitions exist
  and a flag selects which to boot. Adds complexity but enables remote updates.
- **Watchdog:** The Pi has a hardware watchdog. The init script could enable it so the
  board reboots if the service binary hangs. Simple, useful, low complexity.
- **Metrics endpoint:** A tiny sidecar that exposes basic health metrics (uptime, memory,
  temperature) without requiring changes to the service binary. Must be careful not to
  violate the "single binary" principle.

## References

- [Build a Linux kernel running only a Go binary](https://remy.io/blog/custom-built-kernel-running-go-binary/) — The initramfs-as-rootfs technique.
- [gokrazy](https://gokrazy.org/) — Go appliance OS for Pi. Reference implementation of "Go binary on minimal Linux."
- [Raspberry Pi firmware](https://github.com/raspberrypi/firmware) — Boot firmware files.
- [Alpine Linux aarch64 packages](https://pkgs.alpinelinux.org/) — Source for pre-built kernels.
- [Buildroot](https://buildroot.org/) — Full embedded Linux build system. Heavier than what wisp needs but good reference for board support.
- [Nanos (NanoVMs)](https://nanos.org/) — Unikernel that inspired wisp's security posture goals.
