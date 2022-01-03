package rpc

// Package used for local RPCs. Simulates RPCs over a network that permits lost,
// delayed, and out-of-order messages. Allows clients to connect/disconnect
// servers to/from the network and to simulate partitioning, network failure,
// etc.

import (
	"bytes"
	"encoding/gob"
	"math/rand"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang/glog"

	"raft/common"
)

// Base vlevel for the network; set very high so that lab code typically doesn't see network logs.
const vlevel = 11

// Raft will use an Endpoint for each peer connection. RPCs will be sent to
// peers via Endpoint.Call.

// Endpoint represents a client endpoint. Clients can use an Endpoint to call
// RPCs via the Call() function, which behaves the same as net/rpc's
// Client.Call: https://golang.org/pkg/net/rpc/#Client.Call
type Endpoint struct {
	name interface{} // endpoint's name
	ch   chan rpcReq // used to queue requests on the network
	done chan bool   // close to shutdown
}

// Call sends an RPC and waits for it to complete. Returns true iff the RPC
// succeeded. This is a replacement for Client.Call in net/rpc:
// https://golang.org/pkg/net/rpc/#Client.Call
// The third parameter (reply) must be a pointer to a reply type struct.
func (e *Endpoint) Call(method string, req interface{}, reply interface{}) bool {
	// Create rpc.
	r := rpcReq{
		sender:  e.name,
		method:  method,
		reqType: reflect.TypeOf(req),
		replyCh: make(chan rpcReply)}

	// Encode request.
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	common.DieIfError(enc.Encode(req))

	r.req = buf.Bytes()

	// Try to send RPC on network. Network may have already shut down, so don't
	// block forever trying to send.
	select {
	case e.ch <- r:
		// sent
	case <-e.done:
		// network is gone.
		return false
	}

	select {
	case <-e.done:
		// network is gone.
		return false
	case rep := <-r.replyCh:
		// got reply.
		if !rep.ok {
			return false
		}
		dec := gob.NewDecoder(bytes.NewBuffer(rep.reply))
		common.DieIfError(dec.Decode(reply))
		return true
	}
}

// Represents an RPC with encoded request.
type rpcReq struct {
	sender  interface{}   // name of sender
	method  string        // method to call
	reqType reflect.Type  // type of request
	req     []byte        // encoded request
	replyCh chan rpcReply // reply channel
}

// Represents an RPC reply.
type rpcReply struct {
	ok    bool   // whether RPC succeeded
	reply []byte // encoded reply
}

// ServerID is a label for a server.
type ServerID interface{}

// EndpointID is a label for an endpoint.
type EndpointID interface{}

// The remaining types in this file are used in tests to simulate the network.
// A Service is an RPC service ala a gRPC service: https://grpc.io/docs/guides/concepts/
// A Server is a server that may host a variety of Services.
// A Network is a collection of Servers, each with an associated endpoint.

// A Network is a simulated network for a collection of Servers. Networks allow
// clients to create an Endpoint associated with a Server that can be used to
// call RPCs on that server.
//
// Clients may manipulate the network in a variety of ways, including connecting
// and disconnecting Servers and Endpoints.
//
// Other options include:
// - The network may be made to be unreliable: messages may be lost, either
//   before or after delivery of the message. Introduces an average message
//   delay of ~15ms.
// - The network timeouts can be tuned; errors can either be delivered
//   after a short (avg 50ms) or long (avg 3500ms) delay.
// - Likewise, message reordering can be tuned; individual messages can be
//   specified to be delivered with a log (avg 1200ms) delay, causing
//   them to be delayed subsequent messages.
type Network struct {
	sync.Mutex // guards network topology and telemetry

	// network topology
	servers     map[ServerID]*Server     // servers indexed by id
	endpoints   map[EndpointID]*Endpoint // endpoints indexed by id
	connections map[EndpointID]ServerID  // endpoint to server map
	enabled     map[EndpointID]bool      // whether endpoints are enabled

	// network config
	reliable       bool // if false, messages are dropped on the wire with probability 0.1
	longTimeout    bool // if true, the average message delay is 3500ms
	longReordering bool // if true, messages are delayed with probability 2/3.

	// delivery queue given to endpoints to queue RPCs on
	deliveryCh chan rpcReq

	nRPC int32     // telemetry, unprotected (access using atomic)
	done chan bool // done channel
}

// MakeNetwork makes an empty network with default options.
func MakeNetwork() (n *Network) {
	n = &Network{}

	n.servers = make(map[ServerID]*Server)
	n.endpoints = make(map[EndpointID]*Endpoint)
	n.connections = make(map[EndpointID]ServerID)
	n.enabled = make(map[EndpointID]bool)

	n.deliveryCh = make(chan rpcReq)
	n.done = make(chan bool)

	n.reliable = true
	n.longTimeout = false
	n.longReordering = false

	// Start watch thread.
	go n.watch()

	return
}

// Reliable sets whether the network is reliable.
func (n *Network) Reliable(b bool) {
	n.Lock()
	defer n.Unlock()
	n.reliable = b
}

// LongTimeout sets whether the networker has a long timeout.
func (n *Network) LongTimeout(b bool) {
	n.Lock()
	defer n.Unlock()
	n.longTimeout = b
}

// LongReordering sets whether packets can be delivered well out of order.
func (n *Network) LongReordering(b bool) {
	n.Lock()
	defer n.Unlock()
	n.longReordering = b
}

// AddServer adds a Server to the network. Replaces any existing server with the
// same id.
func (n *Network) AddServer(id ServerID, s *Server) {
	n.Lock()
	defer n.Unlock()
	n.servers[id] = s
}

// DeleteServer removes a Server from the network. Does not affect any related
// endpoints.
func (n *Network) DeleteServer(id ServerID) {
	n.Lock()
	defer n.Unlock()
	n.servers[id] = nil
}

// Connect connects an Endpoint to a Server. Once an Endpoint is connected, it
// may not be reconnected, or connected to another Server.
func (n *Network) Connect(eid EndpointID, sid ServerID) {
	n.Lock()
	defer n.Unlock()
	n.connections[eid] = sid
}

// Enable or disable an Endpoint. When disabled, messages sent on the endpoint
// are not delivered.
func (n *Network) Enable(eid EndpointID, e bool) {
	n.Lock()
	defer n.Unlock()
	n.enabled[eid] = e
}

// GetServerRPCs returns the number of RPCs sent to a given server.
func (n *Network) GetServerRPCs(sid ServerID) int {
	n.Lock()
	defer n.Unlock()
	s := n.servers[sid]
	return s.totalRPC
}

// GetTotalRPCs gets total RPCs sent on network.
func (n *Network) GetTotalRPCs() int {
	i := atomic.LoadInt32(&n.nRPC)
	return int(i)
}

// NewEndpoint creates an unconnected and disabled endpoint that can be used by
// clients. The client must call Connect() to connect the endpoint to a server
// and call Enabled to enable the endpoint. Endpoints may be connected only once
// during their lifetime.
func (n *Network) NewEndpoint(eid EndpointID) (e *Endpoint) {
	n.Lock()
	defer n.Unlock()

	e = &Endpoint{name: eid, ch: n.deliveryCh, done: n.done}
	n.endpoints[eid] = e
	n.enabled[eid] = false
	n.connections[eid] = nil
	return
}

// Shutdown shuts down the Network, draining any inflight RPCs.
func (n *Network) Shutdown() {
	close(n.done)
}

// Watch delivery queue and dispatch messages. Run as background thread.
func (n *Network) watch() {
	for {
		select {
		case <-n.done:
			// Shutting down.
			return
		case rpc := <-n.deliveryCh:
			atomic.AddInt32(&n.nRPC, 1)
			go n.dispatch(rpc)
		}
	}
}

// Helper function to get everything we need to deliver a message. Acquires the
// mutex, so that the caller can do avoid holding it.
func (n *Network) endpointInfo(eid EndpointID) (enabled bool, sid ServerID, srv *Server, reliable bool, reorder bool, timeout bool) {
	n.Lock()
	defer n.Unlock()
	enabled = n.enabled[eid]
	sid = n.connections[eid]
	srv = n.servers[sid]
	reliable = n.reliable
	reorder = n.longReordering
	timeout = n.longTimeout
	return
}

// Helper function to determine if a server is available. Acquires the mutex
// so that the caller doesn't need to hold it.
func (n *Network) isAvailable(eid EndpointID, sid ServerID, srv *Server) bool {
	n.Lock()
	defer n.Unlock()
	return n.enabled[eid] && n.servers[sid] == srv
}

// Dispatches an RPC to a Server. Uses network topology to route messages and
// uses config to introduce delay, drop messages, etc.
func (n *Network) dispatch(rpc rpcReq) {
	enabled, sid, srv, reliable, reorder, longTimeout := n.endpointInfo(rpc.sender)

	if !enabled || sid == nil || srv == nil {
		// Endpoint is not connected, fail the RPC with a timeout.
		timeout := rand.Int() % 100
		if longTimeout {
			timeout = rand.Int() % 7000
		}
		// Send reply after timeout duration.
		time.AfterFunc(time.Duration(timeout)*time.Millisecond,
			func() {
				rpc.replyCh <- rpcReply{ok: false, reply: nil}
			})
		return
	}

	// Decide whether to delay or drop message.
	if !reliable {
		delay := rand.Int() % 30
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}
	if !reliable && rand.Float32() < 0.1 {
		glog.V(vlevel+2).Infof("Dropping RPC to %v from %v", sid, rpc.sender)
		// TODO add delay?
		rpc.replyCh <- rpcReply{ok: false, reply: nil}
	}

	// Dispatch to server asynchronously.
	ch := make(chan rpcReply)
	go func() {
		r := srv.call(rpc)
		ch <- r
	}()

	// Wait for RPC to complete or detect that the server is no longer available.
	var reply rpcReply
	ok := false
	alive := true
loop:
	for {
		select {
		case reply = <-ch:
			// Got reply.
			ok = true
			break loop
		case <-time.After(100 * time.Millisecond):
			// Still no reply after 100ms.
			alive = n.isAvailable(rpc.sender, sid, srv)
			if !alive {
				go func() {
					// Avoid leaking the dispatch goroutine blocked on ch.
					<-ch
				}()
				break loop
			}
			glog.V(vlevel+1).Infof("Long RPC to %v...", sid)
		}
	}

	// RPC either failed with unavailability or succeeded.
	if !ok || !alive {
		// RPC failed.
		// TODO Add delay?
		rpc.replyCh <- rpcReply{ok: false, reply: nil}
	} else if !reliable && rand.Float32() < 0.1 {
		// Drop RPC on the wire.
		rpc.replyCh <- rpcReply{ok: false, reply: nil}
	} else if reorder && rand.Float32() < 0.66 {
		// Delay reply to reorder replies.
		delay := 200 + rand.Intn(2000)
		time.AfterFunc(time.Duration(delay)*time.Millisecond,
			func() {
				rpc.replyCh <- reply
			})
	} else {
		// Reply immediately.
		rpc.replyCh <- reply
	}
}

// A Server is a server that hosts a set of Services. A Server dispatches RPCs
// to its hosted services, based on the service name.
type Server struct {
	sync.Mutex                     // guard service map and telemetry
	services   map[string]*Service // set of services indexed by name
	totalRPC   int                 // number of RPCs serviced
	nRPC       map[string]int      // number of RPCs serviced by service
}

// MakeServer creates a new RPC server.
func MakeServer() *Server {
	return &Server{services: make(map[string]*Service), nRPC: make(map[string]int)}
}

// Register a Service with the Server.
func (s *Server) Register(svc *Service) {
	s.Lock()
	defer s.Unlock()
	glog.V(vlevel).Infof("Registering server %v", svc)
	s.services[svc.name] = svc
	s.nRPC[svc.name] = 0
}

// Call an RPC on a Server. RPC names should be of the form
// ServiceName.MethodName.
func (s *Server) call(rpc rpcReq) rpcReply {
	// Parse service name.
	splits := strings.SplitN(rpc.method, ".", 2)
	if len(splits) != 2 {
		glog.Fatalf("Could not parse %s, result: %v", rpc.method, splits)
	}
	sname := splits[0]
	method := splits[1]

	// Get service.
	s.Lock()
	service, ok := s.services[sname]
	s.totalRPC++
	if ok {
		s.nRPC[sname]++
	}
	s.Unlock()

	if !ok {
		// Service isn't served by this Server.
		s.Lock()
		avail := make([]string, len(s.services))
		for k := range s.services {
			avail = append(avail, k)
		}
		s.Unlock()
		glog.Errorf("Server.call(): Unexpected service %v, options: %v", sname, avail)
		return rpcReply{ok: false, reply: nil}
	}

	// Dispatch to service.
	return service.call(method, rpc)
}

// A Service is an RPC service ala a gRPC service: https://grpc.io/docs/guides/concepts/
// A Service is backed by a type that exports functions with the following
// signature:
//     func (o *Object) Foo(v0 ReqType, v1 *ReplyType)
// This differs slightly from the net/rpc package, where these functions must
// also return an error.
type Service struct {
	name    string                    // service name
	object  interface{}               // underlying object
	methods map[string]reflect.Method // object's exported methods indexed by name
}

// MakeService makes a service given a pointer to an object.
func MakeService(o interface{}) (s *Service) {
	s = &Service{object: o}
	v := reflect.ValueOf(o)
	if v.Kind() != reflect.Ptr {
		glog.Fatalf("Error: non-pointer type %v", o)
	}
	t := reflect.TypeOf(o)
	s.name = reflect.Indirect(v).Type().Name()
	s.methods = make(map[string]reflect.Method)

	for i := 0; i < t.NumMethod(); i++ {
		method := t.Method(i)
		mt := method.Type
		mn := method.Name

		glog.V(vlevel).Infof("method %v w/ in %v, %v out.", mn, mt.NumIn(), mt.NumOut())

		if method.PkgPath != "" || // not an exported method
			mt.NumIn() != 3 || // not enough parameters
			mt.In(2).Kind() != reflect.Ptr || // second parameter not a pointer
			mt.NumOut() != 0 { // has a return
			glog.V(vlevel).Infof("skipping %v", mn)
			continue
		}

		glog.V(vlevel).Infof("registered method %v w/ in %v: (%v, %v), %v out.", mn, mt.NumIn(), mt.In(1), mt.In(2), mt.NumOut())
		s.methods[mn] = method
	}

	return
}

// Call a method on a service, given its name.
func (s *Service) call(name string, rpc rpcReq) rpcReply {
	m, ok := s.methods[name]
	if !ok {
		meths := make([]string, len(s.methods))
		for k := range s.methods {
			meths = append(meths, k)
		}
		glog.Fatalf("Service.call(): method %v not known for %v, registered methods %v",
			name, s.name, meths)
	}

	// Construct request; decode request bytes.
	req := reflect.New(rpc.reqType)
	dec := gob.NewDecoder(bytes.NewBuffer(rpc.req))
	common.DieIfError(dec.Decode(req.Interface()))

	// Construct reply.
	rt := m.Type.In(2).Elem()
	rv := reflect.New(rt)

	// Call function.
	f := m.Func
	f.Call([]reflect.Value{reflect.ValueOf(s.object), req.Elem(), rv})

	// Encode reply.
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	common.DieIfError(enc.Encode(rv.Interface()))

	return rpcReply{ok: true, reply: buf.Bytes()}
}
