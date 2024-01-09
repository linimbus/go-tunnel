package opentun

import (
	"fmt"
	"net"
)

type TunApi interface {
	Write (p []byte ) error
	Read  (p []byte) (n int, err error)
	Close() error
}

const (
	encapOverhead = 28 // 20 bytes IP hdr + 8 bytes UDP hdr
)

func InterfaceByName(ifname string) (*net.Interface, error) {
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
