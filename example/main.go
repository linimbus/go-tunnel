package main

import (
	"math/rand"
	"flag"
	"fmt"
	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/lixiangyun/opentun"
	"io"
	"log"
	"net"
	"time"
)

func OpenUdp(bindAddr string) (*net.UDPConn, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", bindAddr)
	if err != nil {
		return nil, err
	}
	udpHander, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, err
	}
	return udpHander, nil
}

func UdpWrite(conn *net.UDPConn, dstAddr *net.UDPAddr, body []byte ) error {
	cnt, err := conn.WriteToUDP(body, dstAddr)
	if err != nil {
		return fmt.Errorf("udp write fail, %s", err.Error())
	}
	if cnt != len(body) {
		return fmt.Errorf("udp send %d out of %d bytes", cnt, len(body))
	}
	return nil
}

var tunRead chan []byte
var tunWrite chan []byte

func IPHeader(buff byte) int {
	switch (buff >> 4) {
	case 4:return 4
	case 6:return 6
	default:
		return 0
	}
}

func DecrementTTL(buff []byte) []byte {
	if IPHeader(buff[0]) != 4 {
		return nil
	}
	eth := gopacket.NewPacket(buff, layers.LayerTypeIPv4, gopacket.NoCopy)
	if layer := eth.Layer(layers.LayerTypeIPv4); layer != nil {
		ip4Packet, _ := layer.(*layers.IPv4)
		ip4Packet.TTL = ip4Packet.TTL - 5

		options := gopacket.SerializeOptions{ComputeChecksums: true}
		buffer := gopacket.NewSerializeBuffer()
		gopacket.SerializeLayers(buffer, options, ip4Packet, gopacket.Payload(ip4Packet.Payload))

		return buffer.Bytes()
	}
	return nil
}

func Display(prefix string, buff []byte) {
	eth := gopacket.NewPacket(buff, layers.LayerTypeIPv4, gopacket.NoCopy)
	if layer := eth.Layer(layers.LayerTypeIPv4); layer != nil {
		ip4Packet, _ := layer.(*layers.IPv4)
		log.Printf("%s DstIP:%s SrcIP:%s", prefix, ip4Packet.DstIP, ip4Packet.SrcIP)
	}
}

func TunRecv(tun opentun.TunApi)  {
	for  {
		buff := make([]byte, 1500)
		cnt, err := tun.Read(buff[:])
		if err != nil {
			log.Printf("tun read fail, %s\n", err.Error())
			continue
		}
		//Display("tun read", buff)
		tunRead <- buff[:cnt]
	}
}

func TunSend(tun opentun.TunApi)  {
	for  {
		buff := <- tunWrite
		err := tun.Write(buff)
		if err != nil {
			log.Printf("tun write fail, %s\n", err.Error())
		}
		//Display("tun write",buff)
	}
}

func (c *UdpChannel)UdpRecv()  {
	for  {
		buff := make([]byte, 1500)
		cnt, addr, err := c.conn.ReadFromUDP(buff[:])
		if err != nil {
			if err != io.EOF {
				log.Println(err.Error())
			}
			return
		}
		if c.remote == nil {
			log.Printf("connect from: %s\n", addr.String())
			c.remote = addr
			continue
		}

		tunWrite <- buff[:cnt]
		//Display("udp recv", buff)

		c.statPkg++
		c.statSize += int64(cnt)
	}
}

func (c *UdpChannel)UdpSend()  {
	for  {
		buff := <- tunRead
		//log.Printf("udp send to: %v\n", c.remote)
		if c.remote != nil {
			_, err := c.conn.WriteToUDP(buff, c.remote)
			if err != nil {
				log.Println(err.Error())
			} else {
				c.statPkg++
				c.statSize += int64(len(buff))
			}
			//Display("udp send", buff)
		}
	}
}

func (c *UdpChannel)Display()  {
	for  {
		time.Sleep(time.Second * 2)
		log.Printf("%d %d %d/mb", c.port, c.statPkg, c.statSize/(1024*1024))
		c.statPkg = 0
		c.statSize = 0
	}
}

type UdpChannel struct {
	port    int
	conn   *net.UDPConn
	remote *net.UDPAddr
	statPkg  int64
	statSize int64
}

var channelList []*UdpChannel

func NewUdpChannel(port int, remote string) (error) {
	conn, err := OpenUdp(fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		log.Println(err.Error())
		return err
	}
	var remoteAddr *net.UDPAddr

	if remote != "" {
		remoteAddr, err = net.ResolveUDPAddr("udp", remote)
		if err != nil {
			log.Println(err.Error())
			return err
		}
	}

	channel := new(UdpChannel)
	channel.conn = conn
	channel.port = port
	channel.remote = remoteAddr

	if remote != "" {
		conn.WriteToUDP(make([]byte, 10), remoteAddr)
	}

	go channel.UdpRecv()
	go channel.UdpSend()
	go channel.Display()

	channelList = append(channelList, channel)

	log.Printf("udp channel init success: %d -> %s\n", port, remoteAddr.String())
	return nil
}

func IsUsedPort(port int) error {
	list, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return err
	}
	list.Close()
	return nil
}

func UnusedPort() int {
	begin := 10000
	end := 50000
	for  {
		port := (rand.Int() % (end - begin)) + begin
		if IsUsedPort(port) == nil {
			return port
		}
	}
}

var (
	help   bool

	IPnet  string
	Port   int
	Number int
	Server string
)

func init()  {
	flag.StringVar(&IPnet, "ipnet", "172.168.1.1/16", "ip + network")
	flag.StringVar(&Server, "server", "", "bind or connect")
	flag.IntVar(&Port, "port", 8000, "bind port connect")
	flag.IntVar(&Number, "number", 10, "channels to connect")
	flag.BoolVar(&help, "help", false, "help usage")
}

func main()  {
	flag.Parse()
	if help {
		flag.Usage()
		return
	}

	tunWrite = make(chan []byte, 1024)
	tunRead  = make(chan []byte, 1024)

	var err error
	for i := 0; i < Number; i++ {
		if Server != "" {
			err = NewUdpChannel(UnusedPort(), fmt.Sprintf("%s:%d", Server, Port + i))
		} else {
			err = NewUdpChannel(Port + i, "")
		}

		if err != nil {
			log.Println(err.Error())
			return
		}
	}

	ip, ipnet, err := net.ParseCIDR(IPnet)
	if err != nil {
		log.Println(err.Error())
		return
	}

	var tun opentun.TunApi
	tun, err = opentun.OpenTun("eth0", ip, *ipnet)
	if err != nil {
		log.Println(err.Error())
		return
	}

	go TunRecv(tun)
	go TunSend(tun)

	log.Println("tun init success")

	for {
		time.Sleep(time.Hour)
	}
}