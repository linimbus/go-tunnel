package opentun

import (
	"bytes"
	"fmt"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"syscall"
	"unsafe"
)

const (
	tunDevice  = "/dev/net/tun"
	ifnameSize = 16
	tunifaceName = "opentun%d"
)

type ifreqFlags struct {
	IfrnName  [ifnameSize]byte
	IfruFlags uint16
}

type tunLinux struct {
	tunf *os.File
	mtu int
	ifname string
}

func ioctl(fd int, request, argp uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, argp)
	if errno != 0 {
		return fmt.Errorf("ioctl failed with '%s'", errno)
	}
	return nil
}

func fromZeroTerm(s []byte) string {
	return string(bytes.TrimRight(s, "\000"))
}

func (tun *tunLinux)Write(p []byte) error {
	cnt, err := tun.tunf.Write(p)
	if err != nil {
		return fmt.Errorf("tun write fail, %s", err.Error())
	}
	if cnt != len(p) {
		return fmt.Errorf("tun send %d out of %d bytes", cnt, len(p))
	}
	return nil
}

func (tun *tunLinux)Read(p []byte) (int, error) {
	return tun.tunf.Read(p)
}

func (tun *tunLinux)Close() error {
	tun.tunf.Close()
	return nil
}

func OpenTun(ifname string, ip net.IP, ipnet net.IPNet) (TunApi, error) {
	iface, err := InterfaceByName(ifname)
	if err != nil {
		return nil, err
	}

	if iface.MTU < encapOverhead {
		return nil, fmt.Errorf("interface %s mtu is too small", ifname)
	}

	tunfd, err := unix.Open(tunDevice, os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}

	tuns := new(tunLinux)
	tuns.tunf = os.NewFile(uintptr(tunfd), "opentun")

	var ifr ifreqFlags
	copy(ifr.IfrnName[:len(ifr.IfrnName)-1], []byte(tunifaceName+"\000"))
	ifr.IfruFlags = syscall.IFF_TUN | syscall.IFF_NO_PI

	err = ioctl(int(tuns.tunf.Fd()), syscall.TUNSETIFF, uintptr(unsafe.Pointer(&ifr)))
	if err != nil {
		return nil, err
	}

	tuns.ifname = fromZeroTerm(ifr.IfrnName[:ifnameSize])
	err = configureIface(tuns.ifname, ip, ipnet, iface.MTU - encapOverhead)
	if err != nil {
		return nil, err
	}

	return tuns, nil
}

func configureIface(ifname string, ip net.IP, ipnet net.IPNet, mtu int) error {
	iface, err := netlink.LinkByName(ifname)
	if err != nil {
		return fmt.Errorf("failed to lookup interface %v", ifname)
	}

	// Ensure that the device has a /32 address so that no broadcast routes are created.
	// This IP is just used as a source address for host to workload traffic (so
	// the return path for the traffic has an address on the flannel network to use as the destination)
	_, ipnLocal, _ := net.ParseCIDR(fmt.Sprintf("%s/32", ip))

	err = netlink.AddrAdd(iface, &netlink.Addr{IPNet: ipnLocal, Label: ""})
	if err != nil {
		return fmt.Errorf("failed to add IP address %v to %v: %v", ipnet.String(), ifname, err)
	}

	err = netlink.LinkSetMTU(iface, mtu)
	if err != nil {
		return fmt.Errorf("failed to set MTU for %v: %v", ifname, err)
	}

	err = netlink.LinkSetUp(iface)
	if err != nil {
		return fmt.Errorf("failed to set interface %v to UP state: %v", ifname, err)
	}

	// explicitly add a route since there might be a route for a subnet already
	// installed by Docker and then it won't get auto added
	err = netlink.RouteAdd(&netlink.Route{
		LinkIndex: iface.Attrs().Index,
		Scope:     netlink.SCOPE_UNIVERSE,
		Dst:       &ipnet,
	})

	if err != nil && err != syscall.EEXIST {
		return fmt.Errorf("failed to add route (%v -> %v): %v", ipnet.String(), ifname, err)
	}

	return nil
}
