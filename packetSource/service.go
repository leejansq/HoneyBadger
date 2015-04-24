/*
 *    service.go - HoneyBadger core library for detecting TCP attacks
 *    such as handshake-hijack, segment veto and sloppy injection.
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

package packetSource

import (
	"github.com/david415/HoneyBadger"
	"github.com/david415/HoneyBadger/logging"
	"github.com/david415/HoneyBadger/types"
	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcap"
	"io"
	"log"
	"time"
)

type TimedRawPacket struct {
	Timestamp time.Time
	RawPacket []byte
}

// InquisitorOptions are user set parameters for specifying the
// details of how to proceed with honey_bager's TCP connection monitoring.
// More parameters should soon be added here!
type InquisitorOptions struct {
	Interface                string
	Filename                 string
	WireDuration             time.Duration
	BufferedPerConnection    int
	BufferedTotal            int
	Filter                   string
	LogDir                   string
	Snaplen                  int
	LogPackets               bool
	TcpIdleTimeout           time.Duration
	MaxRingPackets           int
	Logger                   types.Logger
	DetectHijack             bool
	DetectInjection          bool
	DetectCoalesceInjection  bool
	MaxConcurrentConnections int
}

// Inquisitor sets up the connection pool and is an abstraction layer for dealing
// with incoming packets weather they be from a pcap file or directly off the wire.
type Inquisitor struct {
	InquisitorOptions
	stopCaptureChan     chan bool
	decodePacketChan    chan TimedRawPacket
	stopDecodeChan      chan bool
	dispatchPacketChan  chan HoneyBadger.PacketManifest
	stopDispatchChan    chan bool
	closeConnectionChan chan *HoneyBadger.Connection
	pool                *HoneyBadger.ConnectionPool
	handle              *pcap.Handle
	pager               *HoneyBadger.Pager
}

// NewInquisitor creates a new Inquisitor struct
func NewInquisitor(options *InquisitorOptions) *Inquisitor {
	i := Inquisitor{
		InquisitorOptions:   *options,
		stopCaptureChan:     make(chan bool),
		decodePacketChan:    make(chan TimedRawPacket),
		stopDecodeChan:      make(chan bool),
		dispatchPacketChan:  make(chan HoneyBadger.PacketManifest),
		stopDispatchChan:    make(chan bool),
		closeConnectionChan: make(chan *HoneyBadger.Connection),
		pager:               HoneyBadger.NewPager(),
		pool:                HoneyBadger.NewConnectionPool(),
	}
	return &i
}

// Start... starts the TCP attack inquisition!
func (i *Inquisitor) Start() {
	if i.handle == nil {
		i.setupHandle()
	}
	go i.capturePackets()
	go i.decodePackets()
	go i.dispatchPackets()
	i.pager.Start()
}

// Stop... stops the TCP attack inquisition!
func (i *Inquisitor) Stop() {
	i.stopDispatchChan <- true
	i.stopDecodeChan <- true
	i.stopCaptureChan <- true
	i.pool.CloseAllConnections()
	i.handle.Close()
	i.pager.Stop()
}

func (i *Inquisitor) setupHandle() {
	var err error
	if i.Filename != "" {
		log.Printf("Reading from pcap dump %q", i.Filename)
		i.handle, err = pcap.OpenOffline(i.Filename)
	} else {
		log.Printf("Starting capture on interface %q", i.Interface)
		i.handle, err = pcap.OpenLive(i.Interface, int32(i.Snaplen), true, i.WireDuration)
	}
	if err != nil {
		log.Fatal(err)
	}
	if err = i.handle.SetBPFFilter(i.Filter); err != nil {
		log.Fatal(err)
	}
}

func (i *Inquisitor) capturePackets() {

	tchan := make(chan TimedRawPacket, 0)
	// XXX does this need a shutdown code path?
	go func() {
		for {
			rawPacket, captureInfo, err := i.handle.ReadPacketData()
			if err == io.EOF {
				log.Print("ReadPacketData got EOF\n")
				i.Stop()
				close(tchan)
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

func (i *Inquisitor) decodePackets() {
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
			packetManifest := HoneyBadger.PacketManifest{
				Timestamp: timedRawPacket.Timestamp,
				Flow:      flow,
				RawPacket: timedRawPacket.RawPacket,
				IP:        ip,
				TCP:       tcp,
				Payload:   payload,
			}
			i.dispatchPacketChan <- packetManifest
		}
	}
}

func (i *Inquisitor) setupNewConnection(flow *types.TcpIpFlow) *HoneyBadger.Connection {
	options := HoneyBadger.ConnectionOptions{
		MaxBufferedPagesTotal:         i.InquisitorOptions.BufferedTotal,
		MaxBufferedPagesPerConnection: i.InquisitorOptions.BufferedPerConnection,
		MaxRingPackets:                i.InquisitorOptions.MaxRingPackets,
		CloseRequestChan:              i.closeConnectionChan,
		Pager:                         i.pager,
		LogDir:                        i.LogDir,
		AttackLogger:                  i.Logger,
		LogPackets:                    i.LogPackets,
		DetectHijack:                  i.DetectHijack,
		DetectInjection:               i.DetectInjection,
		DetectCoalesceInjection:       i.DetectCoalesceInjection,
		Pool: i.pool,
	}
	conn := HoneyBadger.NewConnection(&options)

	if i.LogPackets {
		conn.PacketLogger = logging.NewPcapLogger(i.LogDir, flow)
		conn.PacketLogger.Start()
	}
	i.pool.Put(flow, conn)
	conn.Start()
	return conn
}

func (i *Inquisitor) dispatchPackets() {
	var conn *HoneyBadger.Connection
	var err error
	timeout := i.InquisitorOptions.TcpIdleTimeout
	ticker := time.Tick(timeout)
	for {
		select {
		case conn := <-i.closeConnectionChan:
			log.Print("Close request received.")
			conn.Stop()
		default:
		}
		select {
		case <-ticker:
			closed := i.pool.CloseOlderThan(time.Now().Add(timeout * -1))
			if closed != 0 {
				log.Printf("timeout closed %d connections\n", closed)
			}
		case <-i.stopDispatchChan:
			return
		case packetManifest := <-i.dispatchPacketChan:
			if i.pool.Has(packetManifest.Flow) {
				conn, err = i.pool.Get(packetManifest.Flow)
				if err != nil {
					panic(err) // wtf
				}
			} else {
				if i.MaxConcurrentConnections != 0 {
					if i.pool.Len() >= i.MaxConcurrentConnections {
						continue
					}
				}
				conn = i.setupNewConnection(packetManifest.Flow)
			}

			conn.ReceivePacket(&packetManifest)
		} // end of select {
	} // end of for {
}
