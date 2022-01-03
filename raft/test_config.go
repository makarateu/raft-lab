package raft

import (
	"fmt"
	"math/rand"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/golang/glog"

	"raft/rpc"
)

// Utility configuration for running Raft tests. A configuration supports
// creating a network with a given number of Raft servers. Once a configuration
// has been created, a config supports:
// - Running a full round of consensus.
// - Waiting for and getting the current leader.
// - Validating there is no leader.
// - Getting the current number of committed applies.
// - Waiting until the current number of committed applies reaches some
//   threshold.
// - Connecting and disconnecting raft servers from the network.
// - Crashing raft servers and restarting them.
//
// The test configuration monitors all raft servers in the background and
// ensures that all applies are valid and consistent with other servers.
// This assumes that all commands given to the leader are ints.
type testConfig struct {
	sync.Mutex
	t *testing.T

	// configuration state
	n         int                // number of rafts
	net       *rpc.Network       // underlying network
	rafts     []*Raft            // array of raft instances
	connected []bool             // whether a raft instance is connected
	states    []*PersistentState // state of raft servers
	logs      []map[int]int      // each server's committed applies
	errors    []string           // last error for each raft instance
	topo      [][]rpc.EndpointID // list of endpoint names for each server

	// telemetry
	tstart        time.Time // time when begin() was called
	rpcstart      int       // total rpcs when begin() was called
	applies       int       // number of applies
	maxIndexStart int       // max index when begin() was called
	maxIndex      int       // max index
}

var once sync.Once

// Make a test configuration, given a testing interface, the number of raft
// instances to create, and whether the network shall be reliable.
func makeConfig(t *testing.T, n int, reliable bool) (c *testConfig) {
	once.Do(func() {
		if runtime.NumCPU() == 1 {
			glog.Infof("Warning: Testing on a unicore system may hide locking bugs (numcpu = %v).", runtime.NumCPU())
		}
		rand.Seed(time.Now().UnixNano())
	})

	c = &testConfig{}
	c.t = t
	c.n = n

	c.net = rpc.MakeNetwork()
	c.rafts = make([]*Raft, n)
	c.connected = make([]bool, n)
	c.states = make([]*PersistentState, n)
	c.logs = make([]map[int]int, n)
	c.errors = make([]string, n)

	c.topo = make([][]rpc.EndpointID, n)

	c.net.Reliable(reliable)
	c.net.LongTimeout(true) // enable long timeouts by default

	// Create/initialize config state.
	for i := 0; i < n; i++ {
		c.logs[i] = make(map[int]int)
		c.startServer(i)
	}

	for i := 0; i < n; i++ {
		c.connectServer(i)
	}

	return
}

// Begins a test. Prints description and records telemetry at test start.
func (c *testConfig) begin(description string) {
	fmt.Printf(" %s ...\n", description)
	c.tstart = time.Now()
	c.rpcstart = c.net.GetTotalRPCs()
	c.maxIndexStart = c.maxIndex
}

// Ends a test. Prints telemetry and validates completion time.
func (c *testConfig) end() {
	c.checkTimeout()
	if !c.t.Failed() {
		c.Lock()
		defer c.Unlock()
		dur := time.Since(c.tstart).Seconds()
		fmt.Printf(" ... Passed --")
		fmt.Printf("  %4.1f (%d servers, %d rpcs, %d cmds)\n",
			dur, c.n, c.net.GetTotalRPCs()-c.rpcstart, c.maxIndex-c.maxIndexStart)
	}
}

// Validate that there is a leader among connected servers, and there is at most
// one. Waits for at most 5 seconds for a leader to emerge.
func (c *testConfig) getLeader() (leader int) {
	t := time.Now()
	for i := 0; i < 10; i++ {
		// Wait ~500ms and check for a leader.
		time.Sleep(time.Duration(450+rand.Intn(100)) * time.Millisecond)

		termLeaders := make(map[int][]int)
		for ii := 0; ii < c.n; ii++ {
			if c.connected[ii] {
				if t, l := c.rafts[ii].GetState(); l {
					termLeaders[t] = append(termLeaders[t], ii)
				}
			}
		}

		lastTermLed := -1
		for t, l := range termLeaders {
			if len(l) > 1 {
				c.t.Fatalf("Term %d had %d leaders", t, len(l))
			}
			if t > lastTermLed {
				lastTermLed = t
			}
		}

		if len(termLeaders) > 0 {
			return termLeaders[lastTermLed][0]
		}
	}
	c.t.Fatalf("No leaders after waiting %v.", time.Since(t))
	return -1
}

// Validate that all connected servers agree on the current term.
func (c *testConfig) validateTerms() (term int) {
	term = -1
	for i := 0; i < c.n; i++ {
		if c.connected[i] {
			nt, _ := c.rafts[i].GetState()
			if term == -1 {
				term = nt
			} else if term != nt {
				c.t.Fatalf("Server %d disagrees with reported term %d: %d", i, term, nt)
			}
		}
	}
	return term
}

// Validate that there is no leader among the connected servers.
func (c *testConfig) validateNoLeader() {
	for i := 0; i < c.n; i++ {
		if c.connected[i] {
			_, l := c.rafts[i].GetState()
			if l {
				c.t.Fatalf("Server %d claims to be leader although there should be none.", i)
			}
		}
	}
}

// Count how many servers have committed the indexed log entry. Returns the
// count and the command at that index.
func (c *testConfig) countCommitsFor(index int) (n int, cmd interface{}) {
	n = 0
	for i := 0; i < c.n; i++ {
		if c.errors[i] != "" {
			// Fail the test even if log.Fatal is turned off above.
			c.t.Fatal(c.errors[i])
		}

		c.Lock()
		cm, ok := c.logs[i][index]
		c.Unlock()

		if ok {
			if n > 0 && cm != cmd {
				c.t.Fatalf("Server %d has %v committed for index %d, while another has %v committed.",
					i, cm, index, cmd)
			}
			cmd = cm
			n++
		}
	}
	return
}

// Waits for at least n servers to commit for the given index. Times out after
// backing off to >1s. If the term changes while waiting, gives up but does not
// fail. A term of -1 always waits.
func (c *testConfig) waitFor(index int, n int, term int) (cmd interface{}) {
	sleep := 10 * time.Millisecond // start with short wait, then back off
	t := time.Now()
	for i := 0; i < 30; i++ {
		// Count commits; may be done.
		nd, _ := c.countCommitsFor(index)
		if nd >= n {
			break
		}

		// Not done. Wait.
		time.Sleep(sleep)
		if sleep < time.Second {
			sleep *= 2 // backoff
		}

		// Check if we have moved on. If so return.
		if term > -1 {
			for _, r := range c.rafts {
				if t, _ := r.GetState(); t > term {
					// r has moved on from the term. Quit waiting.
					return
				}
			}
		}
	}
	nd, cmd := c.countCommitsFor(index)
	if nd < n {
		c.t.Fatalf("Only %d (< %d) committed index %d after %v.",
			nd, n, cmd, time.Since(t))
	}
	return
}

// Runs a single round of consensus. Publishes the given command to the leader
// and waits for the given number of servers to commit. If there is no consensus
// after 10 seconds, gives up. If retry is true, the command may be repeatedly
// attempted even after a failed agreement (i.e., the leader gave a commit
// index, but no consensus was reached). Returns the index the command was
// committed to.
func (c *testConfig) agree(cmd int, n int, retry bool) (index int) {
	tstart := time.Now()
	cand := 0
	maxnd := 0
	for time.Since(tstart).Seconds() < 10 {
		index = -1
		for i := 0; i < c.n; i, cand = i+1, (cand+1)%c.n {
			var r *Raft
			c.Lock()
			if c.connected[cand] {
				r = c.rafts[cand]
			}
			c.Unlock()
			if r != nil {
				i, _, ok := r.Start(cmd)
				if ok {
					// cand claimed to be the leader and gave an index.
					index = i
					break
				}
			}
		}

		if index != -1 {
			// There was a leader that returned ok. Wait until there is consensus or
			// for a timeout of 2s.
			tcstart := time.Now()
			for time.Since(tcstart).Seconds() < 2 {
				nd, cm := c.countCommitsFor(index)
				if nd > maxnd {
					maxnd = nd
				}
				if nd >= n {
					// Committed. Check if it's the one we wanted.
					if cp, ok := cm.(int); ok && cp == cmd {
						return index
					}
				}
				// Sleep for a bit and check again.
				time.Sleep(20 * time.Millisecond)
			}

			// Consensus was not reached within the timeout. Try again if retry is
			// true.
			if !retry {
				c.t.Fatalf("agree(%v) failed to reach consensus (retries? %v). Max agreement was %d.", cmd, retry, maxnd)
			}
		} else {
			// No index yet; wait a bit.
			time.Sleep(50 * time.Millisecond)
		}
	}
	c.t.Fatalf("agree(%v) failed to reach consensus (retries? %v). Max agreement was %d.", cmd, retry, maxnd)
	return
}

// Creates and starts raft server r. If the server already exists, it will be
// crashed and restarted with its previous persistent state.
func (c *testConfig) startServer(r int) {
	c.crashServer(r)

	// create a new set of endpoints for the server.
	c.topo[r] = make([]rpc.EndpointID, c.n)
	ends := make([]*rpc.Endpoint, c.n)
	for i := 0; i < c.n; i++ {
		c.topo[r][i] = fmt.Sprintf("%08X", rand.Int31())
		ends[i] = c.net.NewEndpoint(c.topo[r][i])
		c.net.Connect(c.topo[r][i], i)
	}

	c.Lock()

	// Create new persistent state.
	if c.states[r] != nil {
		c.states[r] = c.states[r].Copy()
	} else {
		c.states[r] = MakePersistentState()
	}

	c.Unlock()

	// Start background thread to watch applies from r.
	applyCh := make(chan ApplyMsg)
	go c.watchApplies(r, applyCh)

	// Create raft server and add to config.
	rf := CreateRaft(ends, r, c.states[r], applyCh)
	c.Lock()
	c.rafts[r] = rf
	c.Unlock()

	// Register server with the network.
	svc := rpc.MakeService(rf)
	srv := rpc.MakeServer()
	srv.Register(svc)
	c.net.AddServer(r, srv)
}

// Each server has a background thread to listen to its applies and update the
// config's view of the server's log. This monitor ensures that any applies that
// are made by this server are consistent with other applies made by other
// servers (i.e., that the logs match). The monitor stops when the apply channel
// is closed.
func (c *testConfig) watchApplies(r int, applyCh chan ApplyMsg) {
	for m := range applyCh {
		// Parse command
		v, ok := (m.Command).(int)
		if !ok {
			glog.Fatalf("%v is not a valid command (int): %T", m.Command, m.Command)
		}

		// Log the command and validate that the command is consistent with other
		// servers' logs.
		c.Lock()
		err := ""
		for i := 0; i < c.n; i++ {
			// Check if any log conflicts.
			if p, e := c.logs[i][m.CommandIndex]; e && p != v {
				err = fmt.Sprintf("Server %v attempting to commit %v at index %v, but server %v already has %v commited!",
					r, m.Command, m.CommandIndex, i, p)
			}
		}

		_, ok = c.logs[r][m.CommandIndex-1]
		if !ok && m.CommandIndex > 1 {
			err = fmt.Sprintf("Server %v attempting out of order commit at index %v when index %v does not exist.",
				r, m.CommandIndex, m.CommandIndex-1)
		}

		c.logs[r][m.CommandIndex] = v
		if m.CommandIndex > c.maxIndex {
			c.maxIndex = m.CommandIndex
		}
		c.Unlock()

		if err != "" {
			c.errors[r] = err
			glog.Fatalln(err)
		}
	}
}

// Crashes a server by destroying it. Keeps its persistent state.
func (c *testConfig) crashServer(r int) {
	c.disconnectServer(r)
	c.net.DeleteServer(r)

	c.Lock()
	defer c.Unlock()

	// copy the persistence layer, so that the old Raft doesn't have a reference
	// to the same persistenec as the new one.
	if c.states[r] != nil {
		c.states[r] = c.states[r].Copy()
	}

	rf := c.rafts[r]
	if rf != nil {
		// Crash and delete the server.
		c.Unlock()
		rf.Stop()
		c.Lock()
		c.rafts[r] = nil
	}

	if c.states[r] != nil {
		// TODO rm
		c.states[r] = c.states[r].Copy()
	}
}

// Connects the server to the network.
func (c *testConfig) connectServer(r int) {
	c.connected[r] = true

	// Enable all outgoing endpoints
	for i := 0; i < c.n; i++ {
		if c.connected[i] {
			eid := c.topo[r][i]
			c.net.Enable(eid, true)
		}
	}

	// Enable all incoming endpoints.
	for i := 0; i < c.n; i++ {
		if c.connected[i] {
			eid := c.topo[i][r]
			c.net.Enable(eid, true)
		}
	}
}

// Disconnects the server from the network.
func (c *testConfig) disconnectServer(r int) {
	c.connected[r] = false

	// Disable all outgoing endpoints.
	if c.topo[r] != nil { // May be called before topo is created.
		for i := 0; i < c.n; i++ {
			eid := c.topo[r][i]
			c.net.Enable(eid, false)
		}
	}

	// Disable all incoming endpoints.
	for i := 0; i < c.n; i++ {
		if c.topo[i] != nil {
			eid := c.topo[i][r]
			c.net.Enable(eid, false)
		}
	}
}

// Enforces a 2 minute timeout on all tests.
func (c *testConfig) checkTimeout() {
	if !c.t.Failed() && time.Since(c.tstart) > 120*time.Second {
		c.t.Fatal("Test took longer than 120 seconds.")
	}
}

// Shuts down and cleans up a configuration.
func (c *testConfig) cleanup() {
	for i := 0; i < c.n; i++ {
		if c.rafts[i] != nil {
			c.rafts[i].Stop()
		}
	}
	c.net.Shutdown()
	c.checkTimeout()
}

// Gets rpc count for server.
func (c *testConfig) rpcCount(s int) int {
	return c.net.GetServerRPCs(s)
}

// Get total commands committed.
func (c *testConfig) commits() int {
	return c.maxIndex - c.maxIndexStart
}
