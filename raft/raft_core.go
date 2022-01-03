package raft

// The Raft process client interface and definition.

// CreateRaft: Clients create and start a Raft server by calling CreateRaft.
// 	Clients supply the peer Raft RPC endpoints, the id of the Raft server being
// 	created, a persistant state interface, and a channel through which Raft
// 	should send inputs (applies) to the underlying state machine.

// Start: Clients call Start in order to supply an input to the underlying state
// 	machine. Start is only successful when called on the leader. This starts the
// 	process of consensus, where the leader will attempt to replicate the
// 	suuplied input to a quorum of the Raft servers.

// Stop: Clients call Stop to stop a Raft server. This can be used to perorm
// 	any necessary cleanup.

import (
	//"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/golang/glog"

	"raft/rpc"
)

// A Raft server.
type Raft struct {
	peers     []*rpc.Endpoint  // RPC endpoints of all peers
	persister *PersistentState // Object to hold this peer's persisted state
	me        int              // This process's index into peers[]
	apply     chan ApplyMsg    // Apply channel to state machine

	state State // Server state, initially Follower

	// Ephemeral state
	commitIndex int // Last log entry committed; entries less may be applied (initially 0)
	lastApplied int // Last log entry applied so far (initially 0)
	votes       int // Current vote count, if candidate

	// Ephemeral state of the leader
	nextIndex  []int // For every server, the index of the next log entry to send
	matchIndex []int // For every server, the index of the highest log entry known to be replicated

	// Persistent state
	log         []LogEntry // The log itself
	currentTerm int        // This process's current view of the term
	votedFor    int        // candidateId that received vote in this term

	// Whether the peer is stopping; signals any asynchronous goroutines to shut
	// down.
	stopping bool

	// Heartbeat or election deadline. If Leader, heartbeat deadline. If Follower,
	// election deadline.
	deadline time.Time

	// Mutex guarding shared state. Coarse-grained; for this lab, hold the mutex
	// for any reads/writes of Raft state.
	mu sync.Mutex

	// Injected RPC replies; may be used in unit tests.
	testAppendentriessuccess bool
	testAppendentriesreply   *appendEntriesReply
	testRequestvotesuccess   bool
	testRequestvotereply     *requestVoteReply
}

// Start is called by the client in order to start the process of consensus for
// a given input to the underlying state machine. If called on a process that is
// not the leader, isLeader will be false. Otherwise, index will be the index at
// which the command will be applied to the state machine and term will be the
// current term. Note that there is no guarantee that the command _will_ be
// committed.  The leader may lose reelection before replication or may be
// paritioned.
//
// This function has a slightly different interface than the paper (clients
// don't really need to know the index or term); these are exposed for testing
// purposes and a client could easily wrap this function with one that did not
// expose these details.
func (r *Raft) Start(command interface{}) (index int, term int, isLeader bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	index = -1
	term = r.currentTerm
	isLeader = r.state == leader
	if isLeader {
		glog.V(1).Infof("%v attempting start on %v", r.me, command)
		index = len(r.log)
		r.log = append(r.log, LogEntry{Command: command, Term: term})
		r.persist()
	}
	return index, term, isLeader
}

// Stop is called by clients to stop the Raft server.
func (r *Raft) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopping = true
}

// GetState returns the state of the Raft server (term and whether it believes
// itself to be leader).
func (r *Raft) GetState() (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.getStateLocked()
}

// CreateRaft creates a Raft server. Peers is a list of RPC endpoints that can
// be used to communicate with other Raft servers. me is the id of the Raft
// server being created (peers[me] is this server's RPC endpoint). persister can
// be used to persist Raft state. applyCh is a channel that can be used to
// commit/apply inputs to the underlying state machine.
//
// Once CreateRaft returns, the Raft server is running. A pair of background
// threads perorm leader election and manage replication.
func CreateRaft(peers []*rpc.Endpoint, me int,
	persister *PersistentState, applyCh chan ApplyMsg) *Raft {
	r := &Raft{}
	r.peers = peers
	r.persister = persister
	r.me = me
	r.apply = applyCh

	// Initialize ephemeral state.
	r.state = follower

	// Initialize persistent state.
	r.votedFor = -1
	r.currentTerm = 0
	r.recoverState(persister.ReadPersistentState())

	// Not stopping.
	r.stopping = false

	// Starting; set the timer.
	r.resetTimer()
	rand.Seed(time.Now().UnixNano())

	glog.V(1).Infof("Starting Raft server %d", me)

	// Start periodic threads.
	PeriodicThread(r.electOnce, RaftHeartbeatInterval)
	PeriodicThread(r.leadOnce, RaftHeartbeatInterval)

	return r
}
