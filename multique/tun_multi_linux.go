package multique

import (
	"bytes"
	"fmt"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"os"
	"syscall"
	"unsafe"
)

const (
	tunDevice  = "/dev/net/tun"
	ifnameSize = 16
	tunifaceName = "tunmq%d"
)

type ifreqFlags struct {
	IfrnName  [ifnameSize]byte
	IfruFlags uint16
}

type Tunnel struct {
	FD     []io.ReadWriteCloser
	MTU      int
	Ifname   string
}

const (
	encapOverhead = 28 // 20 bytes IP hdr + 8 bytes UDP hdr
)

func interfaceByName(ifname string) (*net.Interface, error) {
	iface, err := net.InterfaceByName(ifname)
	if err != nil {
		return nil, fmt.Errorf("error looking up interface %s: %s",
			ifname, err.Error())
	}
	if iface.MTU == 0 {
		return nil, fmt.Errorf("failed to determine MTU for %s interface", ifname)
	}
	return iface, nil
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

// #define IFF_MULTI_QUEUE 0x0100
const IFF_MULTI_QUEUE  = 0x0100
// #define IFF_ATTACH_QUEUE 0x0200
const IFF_ATTACH_QUEUE = 0x0200

const TUNSETQUEUE = 0x400454d9

func OpenTun(ifname string, ip net.IP, ipnet net.IPNet, cnt int) (*Tunnel, error) {
	iface, err := interfaceByName(ifname)
	if err != nil {
		return nil, err
	}

	if iface.MTU < encapOverhead {
		return nil, fmt.Errorf("interface %s mtu is too small", ifname)
	}

	tuns := new(Tunnel)

	var ifr ifreqFlags
	copy(ifr.IfrnName[:len(ifr.IfrnName)-1], tunifaceName+"\000")
	ifr.IfruFlags = syscall.IFF_TUN | syscall.IFF_NO_PI | IFF_MULTI_QUEUE

	for i := 0 ; i < cnt ; i++ {
		tunfd, err := unix.Open(tunDevice, os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		fd := os.NewFile(uintptr(tunfd), fmt.Sprintf("tunmq%d",i) )
		err = ioctl(int(fd.Fd()), syscall.TUNSETIFF, uintptr(unsafe.Pointer(&ifr)))
		if err != nil {
			return nil, err
		}
		tuns.FD = append(tuns.FD, fd)
	}

	tuns.MTU = iface.MTU - encapOverhead
	tuns.Ifname = fromZeroTerm(ifr.IfrnName[:ifnameSize])
	err = configureIface(tuns.Ifname, ip, ipnet, tuns.MTU)
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
