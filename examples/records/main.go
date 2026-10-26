// SPDX-License-Identifier: MIT

// Command records shows the acyclic services an engineering tool and a
// controller use: ReadImplicit without an AR (I&M0, the real
// configuration), a device access AR to write I&M1 and a parameter record,
// and, within a controller's AR, WriteMultiple and the diagnosis.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/examples/internal/vnetenv"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/rpc"
	"github.com/otfabric/go-profinet/vnet"
)

func main() {
	network := vnet.New(4)
	defer func() { _ = network.Close() }()
	dn, err := vnetenv.NewNode(network, 10, "192.168.0.10")
	if err != nil {
		log.Fatal(err)
	}
	defer dn.Close()
	dev, err := device.New(device.Config{Model: vnetenv.Model(), Mux: dn.Mux, StationName: "io-1", IP: vnetenv.Suite("192.168.0.10"),
		ListenPacket: dn.Stack.ListenPacket, IM: device.IMData{IM1: pnio.IM1{TagFunction: "conveyor", TagLocation: "hall 1"}}})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = dev.Close() }()
	cn, err := vnetenv.NewNode(network, 1, "192.168.0.1")
	if err != nil {
		log.Fatal(err)
	}
	defer cn.Close()
	ctrl, err := controller.New(controller.Config{Mux: cn.Mux, ListenPacket: cn.Stack.ListenPacket, StationName: "tool"})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = ctrl.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// An engineering tool: no AR, just the device's RPC endpoint and
	// object UUID (from DCP).
	id, err := ctrl.DCP().IdentifyName(ctx, "io-1", time.Second)
	if err != nil {
		log.Fatal(err)
	}
	rc := controller.NewRecordClient(ctrl.RPC(), &net.UDPAddr{IP: id.IP.Addr.AsSlice(), Port: rpc.Port},
		pnio.ObjectUUID(max(id.Instance, 1), id.DeviceID, id.VendorID))
	im0, err := rc.ReadIM0(ctx, rpc.NilUUID, 0, 1)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("ReadImplicit I&M0: vendor 0x%04X order %q serial %q software %s\n", im0.VendorID, im0.OrderID, im0.SerialNumber, im0.SoftwareRevision)
	b, err := rc.Read(ctx, rpc.NilUUID, controller.Record{Slot: 0, Subslot: 1, Index: pnio.IndexRealIdentAPI}, 0)
	if err != nil {
		log.Fatal(err)
	}
	blk, _, _ := pnio.ReadBlock(b)
	real, err := pnio.DecodeIdentificationData(blk)
	if err != nil {
		log.Fatal(err)
	}
	for _, api := range real.APIs {
		for _, s := range api.Slots {
			fmt.Printf("real configuration: slot %d module 0x%08X, %d submodule(s)\n", s.Slot, s.IdentNumber, len(s.Submodules))
		}
	}

	// A device access AR for writes, released afterwards.
	sar, err := rc.OpenSupervisor(ctx, controller.SupervisorConfig{InitiatorMAC: cn.Mux.HardwareAddr(), InitiatorObject: ctrl.ObjectUUID()})
	if err != nil {
		log.Fatal(err)
	}
	im1 := pnio.IM1{TagFunction: "conveyor", TagLocation: "hall 2"}
	if err := sar.Write(ctx, controller.Record{Slot: 0, Subslot: 1, Index: pnio.IndexIM1}, im1.Append(nil)); err != nil {
		log.Fatal(err)
	}
	if err := sar.Write(ctx, controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, []byte{0, 5}); err != nil {
		log.Fatal(err)
	}
	if err := sar.Release(ctx); err != nil {
		log.Fatal(err)
	}
	b, _ = rc.Read(ctx, rpc.NilUUID, controller.Record{Slot: 0, Subslot: 1, Index: pnio.IndexIM1}, 0)
	blk, _, _ = pnio.ReadBlock(b)
	got, _ := pnio.DecodeIM1(blk)
	fmt.Printf("device access AR: wrote I&M1 (now %q / %q) and record 0x0010 = 00 05, released\n", got.TagFunction, got.TagLocation)

	// A controller's AR: WriteMultiple and diagnosis.
	io1, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "io-1", Model: vnetenv.Model()})
	if err != nil {
		log.Fatal(err)
	}
	if err := io1.WaitState(ctx, controller.StateRunning); err != nil {
		log.Fatal(err)
	}
	sts, err := io1.WriteMultiple(ctx, []controller.WriteItem{
		{Record: controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, Data: []byte{0, 7}},
		{Record: controller.Record{Slot: 2, Subslot: 1, Index: 0x10}, Data: []byte{1}}, // wrong length
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("WriteMultiple: item 1 %s, item 2 %s\n", sts[0], sts[1])
	for dev.ARs()[0].State != device.ARStateData {
		time.Sleep(time.Millisecond)
	}
	_ = dev.AddDiagnosis(ctx, 1, 1, pnio.DiagnosisItem{ChannelNumber: 3, ChannelProperties: pnio.ChanDirInput, ErrorType: pnio.ChannelErrShortCircuit})
	diags, err := io1.ReadDiagnosis(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, d := range diags {
		for _, it := range d.Items {
			fmt.Printf("diagnosis: slot %d subslot %d channel %d error type 0x%04X\n", d.Slot, d.Subslot, it.ChannelNumber, it.ErrorType)
		}
	}
	_, err = io1.ReadRecord(ctx, controller.Record{Slot: 9, Subslot: 1, Index: 0x10}, 0)
	fmt.Printf("read of a missing slot: %v\n", err)
}
