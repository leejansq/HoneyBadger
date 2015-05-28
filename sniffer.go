/*
 *    HoneyBadger core library
 *
 *    Copyright (C) 2014  David Stainton
 *
 *    This program is free software: you can redistribute it and/or modify
 *    it under the terms of the GNU General Public License as published by
 *    the Free Software Foundation, either version 3 of the License, or
 *    (at your option) any later version.
 *
 *    This program is distributed in the hope that it will be useful,
 *    but WITHOUT ANY WARRANTY; without even the implied warranty of
 *    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 *    GNU General Public License for more details.
 *
 *    You should have received a copy of the GNU General Public License
 *    along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

package HoneyBadger

import (
	"io"
	"log"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/afpacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcap"

	"github.com/david415/HoneyBadger/types"
)

// SnifferOptions are user set parameters for specifying how to receive packets.
type SnifferOptions struct {
	Interface    string
	Filename     string
	WireDuration time.Duration
	Filter       string
	Snaplen      int32
	Dispatcher   PacketDispatcher
	Supervisor   types.Supervisor
	UseAfPacket  bool
}

// Sniffer sets up the connection pool and is an abstraction layer for dealing
// with incoming packets weather they be from a pcap file or directly off the wire.
type Sniffer struct {
	options          SnifferOptions
	stopCaptureChan  chan bool
	decodePacketChan chan TimedRawPacket
	stopDecodeChan   chan bool
	packetDataSource gopacket.PacketDataSource
	pcapHandle       *pcap.Handle
	tpacketHandle    *afpacket.TPacket
	supervisor       types.Supervisor
}

// NewSniffer creates a new Sniffer struct
func NewSniffer(options SnifferOptions) *Sniffer {
	i := Sniffer{
		options:          options,
		stopCaptureChan:  make(chan bool),
		decodePacketChan: make(chan TimedRawPacket),
		stopDecodeChan:   make(chan bool),
	}
	return &i
}

func (i *Sniffer) SetSupervisor(supervisor types.Supervisor) {
	i.supervisor = supervisor
}
func (i *Sniffer) GetStartedChan() chan bool {
	return make(chan bool)
}

// Start... starts the TCP attack inquisition!
func (i *Sniffer) Start() {
	if i.pcapHandle == nil && i.tpacketHandle == nil {
		i.setupHandle()
	}
	go i.capturePackets()
	go i.decodePackets()
}

func (i *Sniffer) Stop() {
	i.stopCaptureChan <- true
	i.stopDecodeChan <- true
	if i.pcapHandle != nil {
		i.pcapHandle.Close()
	} else {
		i.tpacketHandle.Close()
	}
}

func (i *Sniffer) setupHandle() {
	var err error

	if i.options.UseAfPacket { // sniff AF_PACKET interface
		log.Printf("sniffing with AF_PACKET in-kernel socket from interface %q", i.options.Interface)
		if i.tpacketHandle, err = afpacket.NewTPacket(afpacket.OptInterface(i.options.Interface)); err != nil {
			log.Fatal(err)
		}
		i.packetDataSource = i.tpacketHandle
	} else if i.options.Filename != "" { // sniff pcap file
		log.Printf("sniffing with libpcap from file %q", i.options.Filename)
		i.pcapHandle, err = pcap.OpenOffline(i.options.Filename)
		i.packetDataSource = i.pcapHandle
	} else { // sniff pcap wire interface
		log.Printf("sniffing with libpcap from interface %q", i.options.Interface)
		i.pcapHandle, err = pcap.OpenLive(i.options.Interface, i.options.Snaplen, true, i.options.WireDuration)
		if err != nil {
			log.Fatal(err)
		}
		if err = i.pcapHandle.SetBPFFilter(i.options.Filter); err != nil {
			log.Fatal(err)
		}
		i.packetDataSource = i.pcapHandle
	}
}

func (i *Sniffer) capturePackets() {

	tchan := make(chan TimedRawPacket, 0)
	// XXX does this need a shutdown code path?
	go func() {
		for {
			rawPacket, captureInfo, err := i.packetDataSource.ReadPacketData()
			if err == io.EOF {
				log.Print("ReadPacketData got EOF\n")
				i.Stop()
				close(tchan)
				i.supervisor.Stopped()
				return
			}
			if err != nil {
				continue
			}
			tchan <- TimedRawPacket{
				Timestamp: captureInfo.Timestamp,
				RawPacket: rawPacket,
			}
		}
	}()

	for {
		select {
		case <-i.stopCaptureChan:
			return
		case t := <-tchan:
			i.decodePacketChan <- t
		}
	}
}

func (i *Sniffer) decodePackets() {
	var eth layers.Ethernet
	var ip layers.IPv4
	var tcp layers.TCP
	var payload gopacket.Payload

	parser := gopacket.NewDecodingLayerParser(layers.LayerTypeEthernet, &eth, &ip, &tcp, &payload)
	decoded := make([]gopacket.LayerType, 0, 4)

	for {
		select {
		case <-i.stopDecodeChan:
			return
		case timedRawPacket := <-i.decodePacketChan:
			newPayload := new(gopacket.Payload)
			payload = *newPayload
			err := parser.DecodeLayers(timedRawPacket.RawPacket, &decoded)
			if err != nil {
				continue
			}
			flow := types.NewTcpIpFlowFromFlows(ip.NetworkFlow(), tcp.TransportFlow())
			//log.Printf("MEEEEEOW %s", *flow)

			packetManifest := types.PacketManifest{
				Timestamp: timedRawPacket.Timestamp,
				Flow:      flow,
				RawPacket: timedRawPacket.RawPacket,
				IP:        ip,
				TCP:       tcp,
				Payload:   payload,
			}
			i.options.Dispatcher.ReceivePacket(&packetManifest)
		}
	}
}
