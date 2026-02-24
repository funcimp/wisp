//go:build linux

// The wisp init program. It runs as PID 1, configures the system,
// and execs the service binary. This binary runs on any Linux architecture
// (inside a guest VM or on hardware).
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func main() {
	// Step 1: Mount virtual filesystems.
	mustf(unix.Mount("devtmpfs", "/dev", "devtmpfs", 0, ""), "mount /dev")
	mustf(unix.Mount("proc", "/proc", "proc", 0, ""), "mount /proc")
	mustf(unix.Mount("sysfs", "/sys", "sysfs", 0, ""), "mount /sys")

	// Step 2: Read network configuration.
	cfg, err := readConf("/etc/wisp.conf")
	mustf(err, "read config")

	// Step 3: Load kernel modules listed in /etc/modules.
	// Modules are listed one per line (filename only, loaded from /lib/modules/).
	// Dependencies must be listed before dependents.
	loadModules("/etc/modules")

	// Step 4: Bring up loopback.
	mustf(linkUp("lo"), "link up lo")

	// Step 5: Configure primary interface.
	// The kernel probes devices asynchronously — the interface may not exist
	// yet when init runs. Wait up to 5 seconds for it to appear in sysfs.
	iface := cfg["IFACE"]
	mustf(waitForIface(iface, 5*time.Second), "wait for "+iface)
	mustf(linkUp(iface), "link up "+iface)
	mustf(addrAdd(iface, cfg["ADDR"]), "addr add "+cfg["ADDR"])
	mustf(routeAddGw(iface, cfg["GW"]), "route add default via "+cfg["GW"])

	// Step 6: Drop privileges.
	mustf(unix.Setgid(1000), "setgid")
	mustf(unix.Setuid(1000), "setuid")

	// Step 7: Exec the service binary. This replaces the init process.
	// After this line, the service binary is PID 1.
	mustf(unix.Exec("/service/run", []string{"/service/run"}, os.Environ()), "exec /service/run")
}

// mustf prints msg and the error to stderr then exits. Since this is PID 1,
// exit causes a kernel panic — which is the desired failure mode.
func mustf(err error, msg string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "init: %s: %v\n", msg, err)
		os.Exit(1)
	}
}

// readConf reads a key=value config file. Blank lines and lines starting
// with # are ignored. No quoting or escaping.
func readConf(path string) (map[string]string, error) {
	f, err := os.Open(path) //#nosec G304 -- hardcoded path from main(), no user input
	if err != nil {
		return nil, err
	}
	defer f.Close()

	m := make(map[string]string)
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		m[k] = v
	}
	return m, s.Err()
}

// --- Module loading ---

// loadModules reads a file listing kernel module filenames (one per line) and
// loads each from /lib/modules/ using the finit_module syscall. Dependencies
// must be listed before dependents. If the file doesn't exist, this is a no-op
// (not all boards need modules).
func loadModules(listPath string) {
	f, err := os.Open(listPath) //#nosec G304 -- hardcoded path from main(), no user input
	if err != nil {
		return // no modules file — nothing to load
	}
	defer f.Close()

	modRoot, err := os.OpenRoot("/lib/modules")
	if err != nil {
		fmt.Fprintf(os.Stderr, "init: open /lib/modules: %v\n", err) //#nosec G705 -- PID 1 stderr, not web output
		return
	}
	defer modRoot.Close()

	s := bufio.NewScanner(f)
	for s.Scan() {
		name := strings.TrimSpace(s.Text())
		if name == "" || name[0] == '#' {
			continue
		}
		if err := loadModule(modRoot, name); err != nil {
			fmt.Fprintf(os.Stderr, "init: load module %s: %v\n", name, err) //#nosec G705 -- PID 1 stderr, not web output
		}
	}
}

// loadModule loads a single kernel module using the finit_module(2) syscall.
// This takes a file descriptor rather than a memory buffer, avoiding the need
// to read the entire module into memory first. The root scopes access to
// /lib/modules/ preventing path traversal.
func loadModule(root *os.Root, name string) error {
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()

	return unix.FinitModule(int(f.Fd()), "", 0) //#nosec G115 -- fd is a small non-negative int, fits in int
}

// --- Netlink helpers ---
//
// These implement the equivalent of:
//
//	ip link set <iface> up
//	ip addr add <cidr> dev <iface>
//	ip route add default via <gw>
//
// They construct RTNETLINK messages using typed structs from golang.org/x/sys/unix
// and send them over an AF_NETLINK socket.

// waitForIface polls sysfs until the named interface appears or timeout expires.
// Kernel device probing is asynchronous — on fast boot the virtio-net (or USB
// Ethernet) driver may not have created the interface yet when init runs.
func waitForIface(name string, timeout time.Duration) error {
	path := "/sys/class/net/" + name + "/ifindex"
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	// List available interfaces to help debug.
	entries, _ := os.ReadDir("/sys/class/net")
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return fmt.Errorf("interface %s not found after %v (available: %v)", name, timeout, names)
}

// ifIndex reads the kernel interface index from sysfs.
func ifIndex(name string) (int32, error) {
	data, err := os.ReadFile("/sys/class/net/" + name + "/ifindex") //#nosec G304 -- sysfs path, name from embedded board config
	if err != nil {
		return 0, err
	}
	var idx int32
	_, err = fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &idx)
	return idx, err
}

// linkUp brings a network interface up (equivalent to: ip link set <name> up).
func linkUp(name string) error {
	idx, err := ifIndex(name)
	if err != nil {
		return err
	}

	payload := nlSerialize(&unix.IfInfomsg{
		Family: unix.AF_UNSPEC,
		Index:  idx,
		Flags:  unix.IFF_UP,
		Change: unix.IFF_UP,
	})

	msg := nlmsg(unix.RTM_NEWLINK, unix.NLM_F_REQUEST|unix.NLM_F_ACK, payload)
	return nlsend(msg)
}

// addrAdd adds a CIDR address to an interface (equivalent to: ip addr add <cidr> dev <name>).
func addrAdd(name, cidr string) error {
	idx, err := ifIndex(name)
	if err != nil {
		return err
	}

	ip, prefixLen, err := parseAddr(cidr)
	if err != nil {
		return err
	}

	payload := nlSerialize(&unix.IfAddrmsg{
		Family:    unix.AF_INET,
		Prefixlen: uint8(prefixLen),  //#nosec G115 -- prefix length is 0-32
		Scope:     unix.RT_SCOPE_UNIVERSE,
		Index:     uint32(idx), //#nosec G115 -- interface index from kernel, always positive
	})
	payload = append(payload, nlattr(unix.IFA_LOCAL, ip)...)
	payload = append(payload, nlattr(unix.IFA_ADDRESS, ip)...)

	msg := nlmsg(unix.RTM_NEWADDR,
		unix.NLM_F_REQUEST|unix.NLM_F_ACK|unix.NLM_F_CREATE|unix.NLM_F_EXCL,
		payload)
	return nlsend(msg)
}

// routeAddGw adds a default route via a gateway (equivalent to: ip route add default via <gw>).
func routeAddGw(name, gw string) error {
	idx, err := ifIndex(name)
	if err != nil {
		return err
	}

	gwIP := net.ParseIP(gw).To4()
	if gwIP == nil {
		return fmt.Errorf("invalid gateway: %s", gw)
	}

	payload := nlSerialize(&unix.RtMsg{
		Family:   unix.AF_INET,
		Table:    unix.RT_TABLE_MAIN,
		Protocol: unix.RTPROT_STATIC,
		Scope:    unix.RT_SCOPE_UNIVERSE,
		Type:     unix.RTN_UNICAST,
	})
	payload = append(payload, nlattr(unix.RTA_GATEWAY, []byte(gwIP))...)

	oif := make([]byte, 4)
	binary.NativeEndian.PutUint32(oif, uint32(idx)) //#nosec G115 -- interface index from kernel, always positive
	payload = append(payload, nlattr(unix.RTA_OIF, oif)...)

	msg := nlmsg(unix.RTM_NEWROUTE,
		unix.NLM_F_REQUEST|unix.NLM_F_ACK|unix.NLM_F_CREATE|unix.NLM_F_EXCL,
		payload)
	return nlsend(msg)
}

// --- Low-level netlink message construction ---

// nlSerialize encodes a struct to bytes using the native byte order.
func nlSerialize(v any) []byte {
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.NativeEndian, v)
	return buf.Bytes()
}

// nlmsg wraps a payload in a netlink message header.
func nlmsg(typ uint16, flags uint16, payload []byte) []byte {
	total := nlmAlign(unix.SizeofNlMsghdr + len(payload))
	hdr := unix.NlMsghdr{
		Len:   uint32(total), //#nosec G115 -- netlink message, well under uint32 max
		Type:  typ,
		Flags: flags,
		Seq:   1,
	}

	buf := make([]byte, total)
	copy(buf, nlSerialize(&hdr))
	copy(buf[unix.SizeofNlMsghdr:], payload)
	return buf
}

// nlattr builds a netlink route attribute (struct rtattr + data), padded
// to a 4-byte boundary.
func nlattr(typ uint16, data []byte) []byte {
	attrLen := unix.SizeofRtAttr + len(data)
	total := nlmAlign(attrLen)
	buf := make([]byte, total)

	copy(buf, nlSerialize(&unix.RtAttr{
		Len:  uint16(attrLen), //#nosec G115 -- netlink attribute, well under uint16 max
		Type: typ,
	}))
	copy(buf[unix.SizeofRtAttr:], data)
	return buf
}

// nlsend opens a netlink socket, sends msg, reads the ACK, and checks for errors.
func nlsend(msg []byte) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return fmt.Errorf("netlink socket: %w", err)
	}
	defer unix.Close(fd)

	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return fmt.Errorf("netlink bind: %w", err)
	}

	if _, err := unix.Write(fd, msg); err != nil {
		return fmt.Errorf("netlink write: %w", err)
	}

	buf := make([]byte, 4096)
	n, err := unix.Read(fd, buf)
	if err != nil {
		return fmt.Errorf("netlink read: %w", err)
	}

	// Parse the response header and check for errors.
	if n < unix.SizeofNlMsghdr {
		return fmt.Errorf("netlink response too short: %d bytes", n)
	}
	var hdr unix.NlMsghdr
	_ = binary.Read(bytes.NewReader(buf[:unix.SizeofNlMsghdr]), binary.NativeEndian, &hdr)

	if hdr.Type == unix.NLMSG_ERROR {
		// The error payload is a 4-byte errno (negative on failure, 0 on success).
		errOff := unix.SizeofNlMsghdr
		if n >= errOff+4 {
			errno := int32(binary.NativeEndian.Uint32(buf[errOff : errOff+4])) //#nosec G115 -- kernel errno, valid range
			if errno != 0 {
				return unix.Errno(-errno) //#nosec G115 -- negated errno to positive syscall.Errno
			}
		}
	}

	return nil
}

// parseAddr splits "192.168.1.100/24" into a 4-byte IP and prefix length.
func parseAddr(cidr string) (ip []byte, prefixLen int, err error) {
	hostIP, network, err := net.ParseCIDR(cidr)
	if err != nil {
		// Try parsing as a plain address with /32.
		hostIP = net.ParseIP(cidr)
		if hostIP == nil {
			return nil, 0, fmt.Errorf("invalid address: %s", cidr)
		}
		return hostIP.To4(), 32, nil
	}
	ones, _ := network.Mask.Size()
	v4 := hostIP.To4()
	if v4 == nil {
		return nil, 0, fmt.Errorf("not an IPv4 address: %s", cidr)
	}
	return v4, ones, nil
}

// nlmAlign rounds up to the nearest 4-byte boundary (NLMSG_ALIGN).
func nlmAlign(l int) int { return (l + 3) &^ 3 }
