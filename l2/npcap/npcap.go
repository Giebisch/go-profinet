// SPDX-License-Identifier: MIT

//go:build windows

// Package npcap is the Windows backend of l2.Port, on the Npcap driver
// (https://npcap.com), which must be installed separately (its licence
// does not allow redistributing it). The package loads wpcap.dll at run
// time; no cgo is needed.
//
// Npcap is free for personal use on up to five systems; commercial use and
// redistribution need an Npcap OEM licence. Install it with "WinPcap API-
// compatible mode" or add %SystemRoot%\System32\Npcap to the DLL search
// path.
package npcap

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/otfabric/go-profinet"
	"github.com/otfabric/go-profinet/ethernet"
)

var (
	dll           = syscall.NewLazyDLL("wpcap.dll")
	pOpenLive     = dll.NewProc("pcap_open_live")
	pNextEx       = dll.NewProc("pcap_next_ex")
	pSendPacket   = dll.NewProc("pcap_sendpacket")
	pClose        = dll.NewProc("pcap_close")
	pFindAllDevs  = dll.NewProc("pcap_findalldevs")
	pFreeAllDevs  = dll.NewProc("pcap_freealldevs")
	pSetMinToCopy = dll.NewProc("pcap_setmintocopy")
	pGetErr       = dll.NewProc("pcap_geterr")
)

// pcapPkthdr is struct pcap_pkthdr on Windows (32-bit long).
type pcapPkthdr struct {
	Sec, Usec int32
	Caplen    uint32
	Len       uint32
}

// pcapIf is struct pcap_if.
type pcapIf struct {
	Next        *pcapIf
	Name        *byte
	Description *byte
	Addresses   unsafe.Pointer
	Flags       uint32
}

// Device is an Npcap capture device.
type Device struct {
	// Name is the device name, \Device\NPF_{GUID}.
	Name        string
	Description string
}

func goString(p *byte) string {
	if p == nil {
		return ""
	}
	var b []byte
	for i := uintptr(0); ; i++ {
		c := *(*byte)(unsafe.Add(unsafe.Pointer(p), i))
		if c == 0 {
			return string(b)
		}
		b = append(b, c)
	}
}

// Available reports whether wpcap.dll can be loaded.
func Available() error {
	if err := dll.Load(); err != nil {
		return fmt.Errorf("npcap: %w: wpcap.dll: %v (install Npcap)", profinet.ErrUnsupported, err)
	}
	return nil
}

// Devices lists the capture devices.
func Devices() ([]Device, error) {
	if err := Available(); err != nil {
		return nil, err
	}
	var list *pcapIf
	errbuf := make([]byte, 256)
	r, _, _ := pFindAllDevs.Call(uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&errbuf[0])))
	if int32(r) != 0 {
		return nil, fmt.Errorf("npcap: find devices: %s", goString(&errbuf[0]))
	}
	defer pFreeAllDevs.Call(uintptr(unsafe.Pointer(list)))
	var out []Device
	for d := list; d != nil; d = d.Next {
		out = append(out, Device{Name: goString(d.Name), Description: goString(d.Description)})
	}
	return out, nil
}

// Options configure a port.
type Options struct {
	// Promiscuous receives unicast frames for other addresses too.
	Promiscuous bool
	// MAC is the interface's MAC address; required when the name is an
	// Npcap device name that net.Interfaces does not know.
	MAC ethernet.MAC
}

// Port is an Npcap handle. It implements l2.Port.
type Port struct {
	handle  uintptr
	name    string
	mac     ethernet.MAC
	mtu     int
	readMu  sync.Mutex
	writeMu sync.Mutex
	closed  chan struct{}
	once    sync.Once
}

// resolve maps a Windows interface name ("Ethernet") to its Npcap device
// through the GUID the device name carries.
func resolve(name string, opts Options) (dev string, mac ethernet.MAC, mtu int, err error) {
	mtu = ethernet.DefaultMTU
	if strings.HasPrefix(name, `\Device\NPF_`) {
		return name, opts.MAC, mtu, nil
	}
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return "", mac, 0, fmt.Errorf("npcap: %w: interface %q: %v", profinet.ErrNotFound, name, err)
	}
	devs, err := Devices()
	if err != nil {
		return "", mac, 0, err
	}
	for _, d := range devs {
		if strings.EqualFold(d.Description, name) || strings.Contains(strings.ToLower(d.Description), strings.ToLower(name)) {
			return d.Name, ethernet.MAC(ifi.HardwareAddr), ifi.MTU, nil
		}
	}
	return "", mac, 0, fmt.Errorf("npcap: %w: no Npcap device for %q; pass the \\Device\\NPF_{GUID} name", profinet.ErrNotFound, name)
}

// Open opens a port on an interface name or Npcap device name.
func Open(name string, opts Options) (*Port, error) {
	if err := Available(); err != nil {
		return nil, err
	}
	dev, mac, mtu, err := resolve(name, opts)
	if err != nil {
		return nil, err
	}
	if mac.IsZero() {
		return nil, fmt.Errorf("npcap: %w: MAC address of %s unknown", profinet.ErrInvalidConfig, name)
	}
	cname := append([]byte(dev), 0)
	errbuf := make([]byte, 256)
	promisc := 0
	if opts.Promiscuous {
		promisc = 1
	}
	h, _, _ := pOpenLive.Call(uintptr(unsafe.Pointer(&cname[0])), 65535, uintptr(promisc), 100, uintptr(unsafe.Pointer(&errbuf[0])))
	if h == 0 {
		return nil, fmt.Errorf("npcap: open %s: %s", dev, goString(&errbuf[0]))
	}
	pSetMinToCopy.Call(h, 0)
	return &Port{handle: h, name: dev, mac: mac, mtu: mtu, closed: make(chan struct{})}, nil
}

// HardwareAddr implements l2.Port.
func (p *Port) HardwareAddr() ethernet.MAC { return p.mac }

// MTU implements l2.Port.
func (p *Port) MTU() int { return p.mtu }

func (p *Port) lastError() string {
	r, _, _ := pGetErr.Call(p.handle)
	// r is a C pointer the DLL owns; reinterpret it without converting a
	// uintptr expression to unsafe.Pointer.
	return goString(*(**byte)(unsafe.Pointer(&r)))
}

// ReadFrame implements l2.Port.
func (p *Port) ReadFrame(ctx context.Context, b []byte) (int, error) {
	p.readMu.Lock()
	defer p.readMu.Unlock()
	for {
		select {
		case <-p.closed:
			return 0, fmt.Errorf("npcap: %w", profinet.ErrClosed)
		default:
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		var hdr *pcapPkthdr
		var data *byte
		r, _, _ := pNextEx.Call(p.handle, uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&data)))
		switch int32(r) {
		case 1:
			if hdr == nil || data == nil || hdr.Caplen < hdr.Len {
				continue
			}
			return copy(b, unsafe.Slice(data, hdr.Caplen)), nil
		case 0: // timeout
			continue
		default:
			select {
			case <-p.closed:
				return 0, fmt.Errorf("npcap: %w", profinet.ErrClosed)
			default:
			}
			return 0, fmt.Errorf("npcap: read %s: %s", p.name, p.lastError())
		}
	}
}

// WriteFrame implements l2.Port.
func (p *Port) WriteFrame(ctx context.Context, b []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	frame := b
	if len(frame) < ethernet.MinFrameLen {
		frame = ethernet.Pad(append(make([]byte, 0, ethernet.MinFrameLen), b...), 0)
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	select {
	case <-p.closed:
		return fmt.Errorf("npcap: %w", profinet.ErrClosed)
	default:
	}
	r, _, _ := pSendPacket.Call(p.handle, uintptr(unsafe.Pointer(&frame[0])), uintptr(len(frame)))
	if int32(r) != 0 {
		return fmt.Errorf("npcap: write %s: %s", p.name, p.lastError())
	}
	return nil
}

// Close implements l2.Port.
func (p *Port) Close() error {
	p.once.Do(func() {
		close(p.closed)
		p.writeMu.Lock()
		p.readMu.Lock()
		pClose.Call(p.handle)
		p.readMu.Unlock()
		p.writeMu.Unlock()
	})
	return nil
}
