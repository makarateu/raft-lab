package rpc

import (
	"bytes"
	"encoding/gob"
	"flag"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/golang/glog"

	"raft/common"
)

func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Verbose() {
		flag.Set("alsologtostderr", "true")
		flag.Set("v", "12")
	}
	runtime.GOMAXPROCS(4)
	os.Exit(m.Run())
}

type TestRequest struct {
	I int
}

type TestReply struct {
	S string
}

type TestServer struct {
	sync.Mutex
	calls1 []string
	calls2 []int
}

func (t *TestServer) M1(req string, reply *int) {
	t.Lock()
	defer t.Unlock()
	t.calls1 = append(t.calls1, req)
	*reply, _ = strconv.Atoi(req)
}

func (t *TestServer) M2(req int, reply *string) {
	t.Lock()
	defer t.Unlock()
	t.calls2 = append(t.calls2, req)
	*reply = "M2:" + strconv.Itoa(req)
}

func (t *TestServer) M3(req TestRequest, reply *TestReply) {
	reply.S = "no pointer"
}

func (t *TestServer) M4(req *TestRequest, reply *TestReply) {
	reply.S = "pointer"
}

func (t *TestServer) M5(req int, reply *int) {
	time.Sleep(20 * time.Second)
	*reply = -req
}

func enc(a interface{}) (t reflect.Type, buf []byte) {
	t = reflect.TypeOf(a)
	var b bytes.Buffer
	e := gob.NewEncoder(&b)
	common.DieIfError(e.Encode(a))
	buf = b.Bytes()
	return
}

func TestServerBasic(t *testing.T) {
	ts := &TestServer{}
	svc := MakeService(ts)

	srv := MakeServer()
	srv.Register(svc)

	s := "42"
	ty, b := enc(s)
	r := rpcReq{method: "TestServer.M1", reqType: ty, req: b}

	reply := srv.call(r)
	glog.Infof("%v", reply)

	i := 42
	ty, b = enc(i)
	r = rpcReq{method: "TestServer.M2", reqType: ty, req: b}
	reply = srv.call(r)
	reply = srv.call(r)
	reply = srv.call(r)
	reply = srv.call(r)
	reply = srv.call(r)
	glog.Infof("%v", reply)

	glog.Infof("m1: %v, m2: %v", ts.calls1, ts.calls2)
	if len(ts.calls1) != 1 && len(ts.calls2) != 5 {
		t.Errorf("Num calls mismatch.")
	}
}

// Simple send/receive test.
func TestNetworkBasic(t *testing.T) {
	n := MakeNetwork()
	defer n.Shutdown()

	e := n.NewEndpoint("end1-2")

	ts := &TestServer{}
	svc := MakeService(ts)

	srv := MakeServer()
	srv.Register(svc)

	n.AddServer("server2", srv)

	n.Connect("end1-2", "server2")
	n.Enable("end1-2", true)

	{
		reply := 0
		b := e.Call("TestServer.M1", "111", &reply)
		if reply != 111 || !b {
			t.Fatalf("Incorrect reply from TestServer.M1: %v (%v)", reply, b)
		}
	}
	{
		reply := ""
		b := e.Call("TestServer.M2", 111, &reply)
		if reply != "M2:111" || !b {
			t.Fatalf("Wrong reply from TestServer.M2: %v (%v)", reply, b)
		}
	}
}

// Ensure types match the RPC handler.
func TestNetworkTypes(t *testing.T) {
	n := MakeNetwork()
	defer n.Shutdown()

	e := n.NewEndpoint("end1-2")

	ts := &TestServer{}
	svc := MakeService(ts)

	srv := MakeServer()
	srv.Register(svc)

	n.AddServer("server2", srv)

	n.Connect("end1-2", "server2")
	n.Enable("end1-2", true)

	{
		var req TestRequest
		var reply TestReply
		b := e.Call("TestServer.M3", req, &reply)
		if reply.S != "no pointer" || !b {
			t.Fatalf("Incorrect reply from TestServer.M3: %v (%v)", reply, b)
		}
	}
	{
		var req TestRequest
		var reply TestReply
		b := e.Call("TestServer.M4", &req, &reply)
		if reply.S != "pointer" || !b {
			t.Fatalf("Wrong reply from TestServer.M4 (%v): %v", reply, b)
		}
	}
}

// Ensure disabling an endpoint delays messages.
func TestNetworkDisconnect(t *testing.T) {
	n := MakeNetwork()
	defer n.Shutdown()

	e := n.NewEndpoint("end1-2")

	ts := &TestServer{}
	svc := MakeService(ts)

	srv := MakeServer()
	srv.Register(svc)

	n.AddServer("server2", srv)

	n.Connect("end1-2", "server2")

	// Endpoint is disabled.
	{
		reply := ""
		b := e.Call("TestServer.M2", 111, &reply)
		if reply != "" || b {
			t.Fatalf("Incorrect reply from TestServer.M2: %v (%v)", reply, b)
		}
	}

	// Now enable endpoint.
	n.Enable("end1-2", true)

	{
		reply := 0
		b := e.Call("TestServer.M1", "111", &reply)
		if reply != 111 || !b {
			t.Fatalf("Wrong reply from TestServer.M2: %v (%v)", reply, b)
		}
	}
}

// Validate RPC telemetry.
func TestNetworkRpcCount(t *testing.T) {
	n := MakeNetwork()
	defer n.Shutdown()

	e := n.NewEndpoint("end1-2")

	ts := &TestServer{}
	svc := MakeService(ts)

	srv := MakeServer()
	srv.Register(svc)

	n.AddServer("server2", srv)

	n.Connect("end1-2", "server2")
	n.Enable("end1-2", true)

	for i := 0; i < 47; i++ {
		reply := 0
		b := e.Call("TestServer.M1", "111", &reply)
		if reply != 111 || !b {
			t.Fatalf("Incorrect reply from TestServer.M1: %v (%v)", reply, b)
		}
	}

	total := n.GetTotalRPCs()
	if total != 47 {
		t.Fatalf("Got wrong RPC count %v", total)
	}
}

// Test with concurrent clients.
func TestNetworkConcurrencySingleReceiver(t *testing.T) {
	n := MakeNetwork()
	defer n.Shutdown()

	ts := &TestServer{}
	svc := MakeService(ts)

	srv := MakeServer()
	srv.Register(svc)

	n.AddServer(1000, srv)

	sent := make(chan int)

	nclients := 27
	nrpcs := 33
	for i := 0; i < nclients; i++ {
		// Start a client in bg thread.
		go func(i int) {
			count := 0
			defer func() { sent <- count }()

			e := n.NewEndpoint(i)
			n.Connect(i, 1000)
			n.Enable(i, true)

			for j := 0; j < nrpcs; j++ {
				arg := i*100 + j
				reply := ""
				b := e.Call("TestServer.M2", arg, &reply)
				expected := "M2:" + strconv.Itoa(arg)
				if !b || reply != expected {
					t.Fatalf("Wrong reply %v, expecting %v", reply, expected)
				}
				count++
			}
		}(i)
	}

	total := 0
	for i := 0; i < nclients; i++ {
		total += <-sent
	}

	if total != nclients*nrpcs {
		t.Fatalf("Wrong number of RPCs completed, got %v, wanted %v", total, nclients*nrpcs)
	}

	s := n.GetServerRPCs(1000)
	all := n.GetTotalRPCs()
	if s != total || all != total {
		t.Fatalf("Wrong total RPCs %v or server RPCs %v", all, s)
	}
}

// Test that a single client may have concurrent requests on a server.
func TestNetworkConcurrentcySingleSender(t *testing.T) {
	n := MakeNetwork()
	defer n.Shutdown()

	ts := &TestServer{}
	svc := MakeService(ts)

	srv := MakeServer()
	srv.Register(svc)

	n.AddServer(2, srv)

	e := n.NewEndpoint(1)
	n.Connect(1, 2)
	n.Enable(1, true)

	sent := make(chan int)

	nrpcs := 100
	for i := 0; i < nrpcs; i++ {
		// Send requests concurrently.
		go func(i int) {
			count := 0
			defer func() { sent <- count }()

			arg := 100 + i
			reply := ""
			b := e.Call("TestServer.M2", arg, &reply)
			expected := "M2:" + strconv.Itoa(arg)
			if !b || reply != expected {
				t.Fatalf("Wrong reply %v, expecting %v", reply, expected)
			}
			count++
		}(i)
	}

	total := 0
	for i := 0; i < nrpcs; i++ {
		total += <-sent
	}

	if total != nrpcs {
		t.Fatalf("Wrong number of RPCs completed, got %v, wanted %v", total, nrpcs)
	}

	s := n.GetServerRPCs(2)
	all := n.GetTotalRPCs()
	if s != total || all != total {
		t.Fatalf("Wrong total RPCs %v or server RPCs %v", all, s)
	}

	ts.Lock()
	defer ts.Unlock()
	if len(ts.calls2) != nrpcs {
		t.Fatalf("Wrong number of RPCs delivered: %v", len(ts.calls2))
	}
}

// Validate that messages are dropped when the server is unreliable.
func TestNetworkUnreliable(t *testing.T) {
	n := MakeNetwork()
	defer n.Shutdown()

	ts := &TestServer{}
	svc := MakeService(ts)

	srv := MakeServer()
	srv.Register(svc)

	n.AddServer(1000, srv)

	n.Reliable(false)

	sent := make(chan int)

	nclients := 27
	nrpcs := 33
	for i := 0; i < nclients; i++ {
		// Start a client in bg thread.
		go func(i int) {
			count := 0
			defer func() { sent <- count }()

			e := n.NewEndpoint(i)
			n.Connect(i, 1000)
			n.Enable(i, true)

			for j := 0; j < nrpcs; j++ {
				arg := i*100 + j
				reply := ""
				b := e.Call("TestServer.M2", arg, &reply)
				expected := "M2:" + strconv.Itoa(arg)
				if b && reply != expected {
					t.Fatalf("Wrong reply %v, expecting %v", reply, expected)
				}
				if b {
					count++
				}
			}
		}(i)
	}

	total := 0
	for i := 0; i < nclients; i++ {
		total += <-sent
	}

	if total == nclients*nrpcs {
		t.Fatalf("Wrong number of RPCs completed, got %v, wanted less than %v.", total, nclients*nrpcs)
	}

	s := n.GetServerRPCs(1000)
	all := n.GetTotalRPCs()
	if s == total || all == total {
		t.Fatalf("Wrong total RPCs %v or server RPCs %v, wanted less than %v", all, s, nclients*nrpcs)
	}
}

// Disabling and re-enabling an endpoint should unblock messages.
func TestNetworkDisabledDelay(t *testing.T) {
	n := MakeNetwork()
	defer n.Shutdown()

	ts := &TestServer{}
	svc := MakeService(ts)

	srv := MakeServer()
	srv.Register(svc)

	n.AddServer(2, srv)

	e := n.NewEndpoint(1)
	n.Connect(1, 2)

	sent := make(chan int)

	nrpcs := 20

	n.Enable(1, false)
	for i := 0; i < nrpcs; i++ {
		// Send requests concurrently.
		go func(i int) {
			arg := 100 + i
			reply := ""
			b := e.Call("TestServer.M2", arg, &reply)
			if b {
				t.Fatalf("Expected failed RPC.")
			}
		}(i)
	}

	time.Sleep(100 * time.Millisecond)

	n.Enable(1, true)
	tstart := time.Now()
	for i := 0; i < nrpcs; i++ {
		// Send requests concurrently.
		go func(i int) {
			count := 0
			defer func() { sent <- count }()

			arg := 100 + i
			reply := ""
			b := e.Call("TestServer.M2", arg, &reply)
			expected := "M2:" + strconv.Itoa(arg)
			if !b || reply != expected {
				t.Fatalf("Wrong reply %v, expecting %v", reply, expected)
			}
			count++
		}(i)
	}
	dur := time.Since(tstart).Seconds()

	if dur > 0.03 {
		t.Fatalf("RPC took too long (%v) to be delvered after Enable.", dur)
	}

	total := 0
	for i := 0; i < nrpcs; i++ {
		total += <-sent
	}

	if total != nrpcs {
		t.Fatalf("Wrong number of RPCs completed, got %v, wanted %v", total, nrpcs)
	}

	s := n.GetServerRPCs(2)
	if s != total {
		t.Fatalf("Wrong server RPCs %v", s)
	}

	ts.Lock()
	defer ts.Unlock()
	if len(ts.calls2) != nrpcs {
		t.Fatalf("Wrong number of RPCs delivered: %v", len(ts.calls2))
	}
}

// Validate that the network drains on shutdown.
func TestNetworkShutdown(t *testing.T) {
	n := MakeNetwork()

	e := n.NewEndpoint("end1-2")

	ts := &TestServer{}
	svc := MakeService(ts)

	srv := MakeServer()
	srv.Register(svc)

	n.AddServer("server2", srv)

	n.Connect("end1-2", "server2")
	n.Enable("end1-2", true)

	done := make(chan bool)
	go func() {
		reply := 0
		ok := e.Call("TestServer.M5", 99, &reply)
		done <- ok
	}()

	time.Sleep(1000 * time.Millisecond)

	select {
	case <-done:
		t.Fatalf("M5 should still be executing.")
	case <-time.After(100 * time.Millisecond):
	}

	n.DeleteServer("server2")

	select {
	case x := <-done:
		if x {
			t.Fatalf("M5 should not have succeeded")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("M5 should return after DeleteServer()")
	}
}

// Determine RPC cost for reliable network.
func BenchmarkNetwork(b *testing.B) {
	n := MakeNetwork()

	e := n.NewEndpoint("end1-2")

	ts := &TestServer{}
	svc := MakeService(ts)

	srv := MakeServer()
	srv.Register(svc)

	n.AddServer("server2", srv)

	n.Connect("end1-2", "server2")
	n.Enable("end1-2", true)

	reply := ""

	for i := 0; i < b.N; i++ {
		e.Call("TestServer.M2", 111, &reply)
	}
	// 2.3Ghz 8-core i9
	// goos: darwin
	// goarch: amd64
	// pkg: raft/rpc
	// BenchmarkNetwork-4         74452             14629 ns/op
	// PASS
	// ok      raft/rpc        3.287s
}
