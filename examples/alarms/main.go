// SPDX-License-Identifier: MIT

// Command alarms shows diagnosis alarms: the device reports a wire break
// on channel 2 of its input module, the controller receives and
// acknowledges the alarm and reads the diagnosis, then the diagnosis
// disappears.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/otfabric/go-profinet/controller"
	"github.com/otfabric/go-profinet/device"
	"github.com/otfabric/go-profinet/examples/internal/vnetenv"
	"github.com/otfabric/go-profinet/pnio"
	"github.com/otfabric/go-profinet/vnet"
)

func main() {
	network := vnet.New(3)
	defer func() { _ = network.Close() }()
	dn, err := vnetenv.NewNode(network, 10, "192.168.0.10")
	if err != nil {
		log.Fatal(err)
	}
	defer dn.Close()
	dev, err := device.New(device.Config{Model: vnetenv.Model(), Mux: dn.Mux, StationName: "io-1", IP: vnetenv.Suite("192.168.0.10"), ListenPacket: dn.Stack.ListenPacket})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = dev.Close() }()
	cn, err := vnetenv.NewNode(network, 1, "192.168.0.1")
	if err != nil {
		log.Fatal(err)
	}
	defer cn.Close()
	ctrl, err := controller.New(controller.Config{Mux: cn.Mux, ListenPacket: cn.Stack.ListenPacket})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = ctrl.Close() }()
	alarms := make(chan controller.AlarmEvent, 4)
	io1, err := ctrl.AddDevice(controller.DeviceConfig{StationName: "io-1", Model: vnetenv.Model(), OnAlarm: func(a controller.AlarmEvent) { alarms <- a }})
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := io1.WaitState(ctx, controller.StateRunning); err != nil {
		log.Fatal(err)
	}
	// The device enters Data just after the controller is Running.
	for len(dev.ARs()) == 0 || dev.ARs()[0].State != device.ARStateData {
		time.Sleep(time.Millisecond)
	}
	item := pnio.DiagnosisItem{ChannelNumber: 2, ChannelProperties: pnio.ChanDirInput | 4, ErrorType: pnio.ChannelErrLineBreak}
	if err := dev.AddDiagnosis(ctx, 1, 1, item); err != nil {
		log.Fatal(err)
	}
	a := <-alarms
	fmt.Printf("alarm %s from slot %d subslot %d (sequence %d)\n", a.Notification.Type, a.Notification.Slot, a.Notification.Subslot, a.Notification.Sequence())
	diag, err := io1.ReadDiagnosis(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, d := range diag {
		for _, it := range d.Items {
			fmt.Printf("diagnosis slot %d subslot %d channel %d error type 0x%04X\n", d.Slot, d.Subslot, it.ChannelNumber, it.ErrorType)
		}
	}
	if err := dev.RemoveDiagnosis(ctx, 1, 1, 2, pnio.ChannelErrLineBreak); err != nil {
		log.Fatal(err)
	}
	a = <-alarms
	fmt.Printf("alarm %s\n", a.Notification.Type)
}
