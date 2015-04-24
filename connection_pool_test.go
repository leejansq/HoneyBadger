package HoneyBadger

import (
	"github.com/david415/HoneyBadger/types"
	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"log"
	"net"
	"sync"
	"testing"
	"time"
)

func TestConnectionPoolDelete(t *testing.T) {

	connPool := NewConnectionPool()
	options := ConnectionOptions{
		MaxBufferedPagesTotal:         0,
		MaxBufferedPagesPerConnection: 0,
		MaxRingPackets:                40,
		CloseRequestChan:              nil,
		Pager:                         nil,
		LogDir:                        "fake-log-dir",
		Pool:                          connPool,
	}
	conn := NewConnection(&options)
	conn.Start()

	ipFlow, _ := gopacket.FlowFromEndpoints(layers.NewIPEndpoint(net.IPv4(1, 2, 3, 4)), layers.NewIPEndpoint(net.IPv4(2, 3, 4, 5)))
	tcpFlow, _ := gopacket.FlowFromEndpoints(layers.NewTCPPortEndpoint(layers.TCPPort(1)), layers.NewTCPPortEndpoint(layers.TCPPort(2)))
	flow := types.NewTcpIpFlowFromFlows(ipFlow, tcpFlow)

	connPool.Put(flow, conn)

	if len(connPool.connectionMap) != 1 {
		t.Error("failed to add connection to pool")
		t.Fail()
	}

	// delete that flow
	connPool.Delete(flow)
	if len(connPool.connectionMap) != 0 {
		t.Error("failed to delete connection from pool")
		t.Fail()
	}

	// now delete the non-existent flow
	connPool.Delete(flow)
	if len(connPool.connectionMap) != 0 {
		t.Error("failed to delete connection from pool")
		t.Fail()
	}

}

func TestConnectionPoolCloseAll(t *testing.T) {
	pager := NewPager()
	pager.Start()
	connPool := NewConnectionPool()
	options := ConnectionOptions{
		MaxBufferedPagesTotal:         0,
		MaxBufferedPagesPerConnection: 0,
		MaxRingPackets:                40,
		CloseRequestChan:              nil,
		Pager:                         pager,
		LogDir:                        "fake-log-dir",
		Pool:                          connPool,
	}

	ipFlow, _ := gopacket.FlowFromEndpoints(layers.NewIPEndpoint(net.IPv4(1, 2, 3, 4)), layers.NewIPEndpoint(net.IPv4(2, 3, 4, 5)))
	tcpFlow, _ := gopacket.FlowFromEndpoints(layers.NewTCPPortEndpoint(layers.TCPPort(1)), layers.NewTCPPortEndpoint(layers.TCPPort(2)))
	flow := types.NewTcpIpFlowFromFlows(ipFlow, tcpFlow)

	conn := NewConnection(&options)
	conn.Start()
	conn.clientFlow = flow
	conn.serverFlow = flow.Reverse()

	connPool.Put(flow, conn)
	log.Print("added new connection flow to pool")

	time.Sleep(1000 * time.Millisecond)

	var wg sync.WaitGroup
	closed := connPool.CloseAllConnections(&wg)
	wg.Wait()
	if closed != 1 || len(connPool.connectionMap) != 0 {
		t.Errorf("failed to close %d all connections from pool: %d\n", closed, len(connPool.connectionMap))
		t.Fail()
	}

	connPool = NewConnectionPool()
	closed = connPool.CloseAllConnections(&wg)
	wg.Wait()
	if closed != 0 || len(connPool.connectionMap) != 0 {
		t.Errorf("fail %d\n", closed)
		t.Fail()
	}

	// check nil case of Connections method
	conns := connPool._connections()
	if len(conns) != 0 {
		t.Error("connectionsLocked() should failed to return zero")
		t.Fail()
	}

	// test zero case of CloseOlderThan
	count := connPool.CloseOlderThan(time.Now())
	if count != 0 {
		t.Error("1st CloseOlderThan fail")
		t.Fail()
	}

	log.Print("before 2nd CloseOlderThan\n")
	// test close one case of CloseOlderThan
	conn = NewConnection(&options)
	conn.Start()
	conn.clientFlow = flow
	connPool.Put(flow, conn)
	count = connPool.CloseOlderThan(time.Now())
	if count != 1 {
		t.Error("2nd CloseOlderThan fail")
		t.Fail()
	}

	log.Print("after 2nd CloseOlderThan\n")

	timeDuration := time.Minute * 5
	timestamp1 := time.Now()
	timestamp2 := timestamp1.Add(timeDuration)
	conn = NewConnection(&options)
	conn.Start()
	conn.clientFlow = flow
	conn.serverFlow = flow.Reverse()
	conn.clientNextSeq = 3
	connPool.Put(flow, conn)
	conn.state = TCP_DATA_TRANSFER

	ip := layers.IPv4{
		SrcIP:    net.IP{1, 2, 3, 4},
		DstIP:    net.IP{2, 3, 4, 5},
		Version:  4,
		TTL:      64,
		Protocol: layers.IPProtocolTCP,
	}
	tcp := layers.TCP{
		Seq:     3,
		SYN:     false,
		SrcPort: 1,
		DstPort: 2,
	}
	packetManifest := PacketManifest{
		Timestamp: timestamp1,
		Flow:      flow,
		IP:        ip,
		TCP:       tcp,
		Payload:   []byte{1, 2, 3, 4, 5, 6, 7},
	}
	log.Printf("before receivePacket flow %s\n", flow.String())
	conn.ReceivePacket(&packetManifest)
	log.Print("before 3rd CloseOlderThan\n")

	count = connPool.CloseOlderThan(time.Now())
	if count != 1 {
		t.Error("CloseOlderThan fail")
		t.Fail()
	}
	log.Print("after 3rd CloseOlderThan\n")
	conn = NewConnection(&options)
	conn.Start()
	conn.clientFlow = flow
	conn.serverFlow = flow.Reverse()
	conn.clientNextSeq = 3
	connPool.Put(flow, conn)
	conn.state = TCP_DATA_TRANSFER
	packetManifest = PacketManifest{
		Timestamp: timestamp2,
		Flow:      flow,
		IP:        ip,
		TCP:       tcp,
		Payload:   []byte{1, 2, 3, 4, 5, 6, 7},
	}
	conn.ReceivePacket(&packetManifest)
	log.Print("before last CloseOlderThan\n")
	count = connPool.CloseOlderThan(timestamp1)
	if count != 0 {
		t.Error("CloseOlderThan fail")
		t.Fail()
	}
	log.Print("after last CloseOlderThan\n")
	if !connPool.Has(flow) {
		t.Error("Has method fail")
		t.Fail()
	}

	if !connPool.Has(flow.Reverse()) {
		t.Error("Has method fail")
		t.Fail()
	}
	log.Print("before CloseAllConnections\n")
	closed = connPool.CloseAllConnections(&wg)
	wg.Wait()
	log.Print("after CloseAllConnections\n")
	if connPool.Has(flow) {
		t.Error("Has method fail")
		t.Fail()
	}

	log.Print("before NewConn\n")
	conn = NewConnection(&options)
	conn.Start()
	conn2, err := connPool.Get(flow)
	if err == nil {
		t.Error("Get method fail")
		t.Fail()
	}

	conn.clientFlow = flow
	conn.serverFlow = flow.Reverse()
	conn.clientNextSeq = 3
	conn.state = TCP_DATA_TRANSFER

	connPool.Put(flow, conn)
	myPacketManifest := PacketManifest{
		Timestamp: timestamp2,
		Flow:      flow,
		IP:        ip,
		TCP:       tcp,
		Payload:   []byte{1, 2, 3, 4, 5, 6, 7},
	}
	conn.ReceivePacket(&myPacketManifest)
	conn2, err = connPool.Get(flow)
	if conn2 == nil && err != nil {
		t.Error("Get method fail")
		t.Fail()
	}

}
