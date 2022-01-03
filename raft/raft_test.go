package raft

// Lab 3 test suite.
// This file contains a large amount of tests of two flavors:
// - Integration tests. These tests create several Raft processes and validate
// 	 that the system can or cannot reach consensus when the network is perturbed
// 	 in various ways. They also validate leader election and log consistency
// 	 between Raft processes. Note that these tests can sometimes run for a long
// 	 period of time.
// - Unit tests. These tests test individual functions. Currently, only part 2
//   has a set of unit tests. You should examine them before you begin part 1,
//   as if you get stuck on part 1 it will be helpful to introduce your own
//   tests.
// Each suite of tests has a suffix: Part1, P2Unit, Part2, Part3, and Part4. Use
// the -run flag to select a particular suite. If you add unit tests for part 1,
// you may want to use P1Unit as a suffix.

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"raft/rpc"
)

// Use this vlevel flag to set the verbose level for logs.
var (
	vlevel = flag.String("vlevel", "", "verbosity level for glog in tests.")
)

func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Verbose() || *vlevel != "" {
		flag.Set("alsologtostderr", "true")
		if *vlevel != "" {
			flag.Set("v", *vlevel)
		}
	}
	runtime.GOMAXPROCS(4)
	os.Exit(m.Run())
}

// Appends n log entries to a log with the given term.
func addToLog(n int, term int, s *[]LogEntry) {
	for i := 0; i < n; i++ {
		*s = append(*s, LogEntry{Term: term, Command: term})
	}
}

// ------------------ Part 1 Integration tests --------------------------------
// Part 1 introduces leader election. These tests validate that a leader can be
// elected, even when there is network paritioning.

// NOTE: It may be helpful to introduce unit tests for your RequestVote RPC
// handler and the sendBallot function. If you find yourself in a place where
// you can't discover a bug, it will be helpful to write tests as with the
// AppendEntries and sendAppendEntries functions.

// Validate that three servers can elect an initial leader.
// 1. Start 3 servers.
// 2. Check that a leader is elected (getLeader).
// 3. Wait for some period of time and validate that all servers agree on the
// current term (validateTerms).
// 4. Wait for some time and validate that the term hasn't moved on, since there
// has been no failure.
// 5. Validate the leader hasn't changed..
func TestElectionNoFailurePart1(t *testing.T) {
	servers := 3
	cfg := makeConfig(t, servers, true)
	defer cfg.cleanup()

	cfg.begin("Election with no failure")

	// 2. Check leader was elected.
	l1 := cfg.getLeader()

	// 3. Wait and validate that all servers agree on the term.
	time.Sleep(50 * time.Millisecond)
	t1 := cfg.validateTerms()

	// 4. Validate that the term is stable.
	time.Sleep(2 * RaftElectionTimeout)
	t2 := cfg.validateTerms()
	if t1 != t2 {
		t.Fatalf("Term changed from %v to %v even though the network was stable.", t1, t2)
	}

	// 5. Make sure there's still a leader.
	l2 := cfg.getLeader()
	if l1 != l2 {
		t.Fatalf("Leader changed even though the term stayed the same (went from %v to %v).", l1, l2)
	}

	cfg.end()
}

// Validate that if a leader is disconnected, a new leader is elected (in a new
// term).
// With three servers:
// 1. Wait for a leader to be elected.
// 2. Disconnect the leader.
// 3. Wait for a new leader to be elected.
// 4. Rejoin the old leader.
// 5. Validate that the new leader remains the leader.
// 6. Remove the new leader and another process (now only one server in network).
// 7. Validate there is no leader.
// 8. Add back a single process.
// 9. Validate that a new leader is elected.
// 10. Rejoin the last remaining process.
// 11. Validate that the leader remains the same.
func TestElectionWithFailurePart1(t *testing.T) {
	servers := 3
	cfg := makeConfig(t, servers, true)
	defer cfg.cleanup()

	cfg.begin("Election with network failure")

	// 1. Wait for a leader to be elected.
	l1 := cfg.getLeader()

	// 2-3. Disconnect the leader and wait for a new election to finish.
	cfg.disconnectServer(l1)
	l2 := cfg.getLeader()

	// 4-5. Rejoin old leader and validate new leader doesn't change.
	cfg.connectServer(l1)
	l3 := cfg.getLeader()
	if l2 != l3 {
		t.Errorf("Leader changed from %v to %v unneccessarily.", l2, l3)
	}

	// 6-7. Disconnect leader and another server; validate there is no leader.
	cfg.disconnectServer(l2)
	cfg.disconnectServer((l2 + 1) % servers)
	time.Sleep(2 * RaftElectionTimeout)
	cfg.validateNoLeader()

	// 8-9. Reconnect a server, validate there is a new leader.
	cfg.connectServer((l2 + 1) % servers)
	l3 = cfg.getLeader()

	// 10-11. Reconnect last disconnected server; validate the leader stays the
	// same.
	cfg.connectServer(l2)
	l4 := cfg.getLeader()
	if l3 != l4 {
		t.Errorf("Leader changed from %v to %v unneccessarily.", l3, l4)
	}

	cfg.end()
}

// ------------------ Part 2 Unit tests ---------------------------------------
// Part 2 adds log agreement and network failure. The following unit tests
// validate behavior for individual functions; integration tests follow.

// Unit tests for part 2. Each test describes its intent.

func TestAppendEntriesOldTermP2Unit(t *testing.T) {
	r := &Raft{me: 1, currentTerm: 22, log: []LogEntry{LogEntry{Term: 0}}, state: follower}
	args := appendEntriesArgs{
		Term: 20, // Old term should be ignored.
	}
	var reply appendEntriesReply
	r.AppendEntries(args, &reply)
	if reply.Term != 22 || reply.Success {
		t.Fatalf("Invalid reply: %+v", reply)
	}
}

func TestAppendEntriesNewTermP2Unit(t *testing.T) {
	r := &Raft{me: 1, currentTerm: 22, log: []LogEntry{LogEntry{Term: 0}}, state: follower}
	args := appendEntriesArgs{
		Term: 30, // New term should force Raft to advance term.
	}
	var reply appendEntriesReply
	r.AppendEntries(args, &reply)
	if reply.Term != 30 {
		t.Fatalf("Invalid reply: %+v", reply)
	}
}

func TestAppendEntriesEmptyLogP2Unit(t *testing.T) {
	r := &Raft{me: 1, currentTerm: 22, log: []LogEntry{LogEntry{Term: 0}}, state: follower}
	args := appendEntriesArgs{
		Term:         22, // Same term.
		LeaderCommit: 10, // Should advance commit index.
	}
	var reply appendEntriesReply
	r.AppendEntries(args, &reply)
	if reply.Term != 22 || !reply.Success || reply.NextIndex != 1 {
		t.Fatalf("Invalid reply: %+v", reply)
	}
	if r.commitIndex != 10 {
		t.Fatal("Didn't advance commit index: ", r.commitIndex)
	}
}

func TestAppendEntriesBasicP2Unit(t *testing.T) {
	r := &Raft{me: 1, currentTerm: 22, log: []LogEntry{LogEntry{Term: 0}}, state: follower}
	args := appendEntriesArgs{
		Term:         22,
		LeaderCommit: 2,                                         // Should advance commit index.
		Entries:      []LogEntry{LogEntry{Term: 1, Command: 1}}, // Append a single entry.
		PrevLogTerm:  0,
		PrevLogIndex: 0,
	}
	var reply appendEntriesReply
	r.AppendEntries(args, &reply)
	if reply.Term != 22 || !reply.Success || reply.NextIndex != 2 {
		t.Fatalf("Invalid reply: %+v", reply)
	}
	// Entry should be in the log.
	if len(r.log) < 2 || r.log[1].Term != 1 || r.log[1].Command != 1 {
		t.Fatalf("Invalid log: %+v", r.log)
	}
}

func TestAppendEntriesLongPrevIndexP2Unit(t *testing.T) {
	r := &Raft{me: 1, currentTerm: 22, log: []LogEntry{LogEntry{Term: 0}}, state: follower}
	args := appendEntriesArgs{
		Term:         22,
		LeaderCommit: 2,
		Entries:      []LogEntry{LogEntry{Term: 1, Command: 1}},
		PrevLogTerm:  1,
		PrevLogIndex: 5, // Want to append to a spot that doesn't exist.
	}
	var reply appendEntriesReply
	r.AppendEntries(args, &reply)
	// Should fail with NextIndex == 1. If not using NextIndex, should fail.
	if reply.Term != 22 || reply.Success || reply.NextIndex != 1 {
		t.Fatalf("Invalid reply: %+v", reply)
	}
}

func TestAppendEntriesLongerP2Unit(t *testing.T) {
	r := &Raft{me: 1, currentTerm: 22, log: []LogEntry{LogEntry{Term: 0}}, state: follower}
	addToLog(3, 2, &r.log) // Extend log on raft.
	newEntries := make([]LogEntry, 0)
	addToLog(2, 3, &newEntries) // Two new entries.

	args := appendEntriesArgs{
		Term:         22,
		LeaderCommit: 2,
		Entries:      newEntries, // Append a longer set of entries.
		PrevLogTerm:  2,
		PrevLogIndex: 3,
	}

	// New log should be [0, 2, 2, 2, 3, 3]

	var reply appendEntriesReply
	r.AppendEntries(args, &reply)
	if reply.Term != 22 || !reply.Success || reply.NextIndex != 6 {
		t.Fatalf("Invalid reply: %+v", reply)
	}
	if len(r.log) < 6 || r.log[5].Term != 3 {
		t.Fatalf("Invalid log: %+v", r.log)
	}
}

// Series of three tests that test leader erasing a long incorrect follower log.
// If not using NextIndex, this should just not return Success.
func TestAppendEntriesConflictOneP2Unit(t *testing.T) {
	r := &Raft{me: 1, currentTerm: 22, log: []LogEntry{LogEntry{Term: 0}}, state: follower}
	addToLog(3, 2, &r.log)
	addToLog(2, 3, &r.log)
	addToLog(7, 4, &r.log)

	// indices:    0  1  2  3  4  5  6  7  8  9  0
	// Old log is [0, 2, 2, 2, 3, 3, 4, 4, 4, 4, 4, 4, 4]
	// Leader log [0, 2, 2, 2, 5, 5, 5, 6, 6, 6, 6]
	// Conflict starts here:   ^     ^ <- should be NextIndex

	// First call will be to append the last entry (index 10)
	// Should get conflict back to index 6 (backing up through 4s).
	args := appendEntriesArgs{
		Term:         22,
		LeaderCommit: 6,
		Entries:      []LogEntry{LogEntry{Term: 6}},
		PrevLogTerm:  6,
		PrevLogIndex: 9,
	}

	var reply appendEntriesReply
	r.AppendEntries(args, &reply)
	if reply.Term != 22 || reply.Success || reply.NextIndex != 6 {
		t.Fatalf("Invalid reply: %+v", reply)
	}
}

func TestAppendEntriesConflictTwoP2Unit(t *testing.T) {
	r := &Raft{me: 1, currentTerm: 22, log: []LogEntry{LogEntry{Term: 0}}, state: follower}
	addToLog(3, 2, &r.log)
	addToLog(2, 3, &r.log)
	addToLog(7, 4, &r.log)

	// indices:    0 1 2 3 4 5 6 7 8 9 0
	// Old log is [0 2 2 2 3 3 4 4 4 4 4 4 4]
	// Leader log [0 2 2 2 5 5 5 6 6 6 6]
	//                     ^ <- should be NextIndex

	// Second call will be to append at index 6.
	// Should get conflict back to index 3 (backing up through 3s).
	newEntries := []LogEntry{LogEntry{Term: 5}}
	addToLog(4, 6, &newEntries)

	args := appendEntriesArgs{
		Term:         22,
		LeaderCommit: 6,
		Entries:      newEntries,
		PrevLogTerm:  5,
		PrevLogIndex: 5,
	}

	var reply appendEntriesReply
	r.AppendEntries(args, &reply)
	// If not using NextIndex, this should just not return Success.
	if reply.Term != 22 || reply.Success || reply.NextIndex != 4 {
		t.Fatalf("Invalid reply: %+v", reply)
	}
}

func TestAppendEntriesConflictThreeP2Unit(t *testing.T) {
	r := &Raft{me: 1, currentTerm: 22, log: []LogEntry{LogEntry{Term: 0}}, state: follower}
	addToLog(3, 2, &r.log)
	addToLog(2, 3, &r.log)
	addToLog(7, 4, &r.log)

	// indices:    0 1 2 3 4 5 6 7 8 9 0
	// Old log is [0 2 2 2 3 3 4 4 4 4 4 4 4]
	// Leader log [0 2 2 2 5 5 5 6 6 6 6]
	//                   ^ <- prev index here will succeed.

	// Final call will succeed by appending at index 4 as now the terms match.
	newEntries := make([]LogEntry, 0)
	addToLog(3, 5, &newEntries)
	addToLog(4, 6, &newEntries)

	args := appendEntriesArgs{
		Term:         22,
		LeaderCommit: 6,
		Entries:      newEntries,
		PrevLogTerm:  2,
		PrevLogIndex: 3,
	}

	var reply appendEntriesReply
	r.AppendEntries(args, &reply)
	// Ignore NextIndex if not using.
	if reply.Term != 22 || !reply.Success || reply.NextIndex != 11 {
		t.Fatalf("Invalid reply: %+v", reply)
	}
	// Validate that log matches leader.
	for i := 1; i < 11; i++ {
		w := -1
		if i < 4 {
			w = 2
		} else if i < 7 {
			w = 5
		} else {
			w = 6
		}
		if r.log[i].Term != w {
			t.Fatalf("Invalid log: %+v", r.log)
		}
	}
	if r.commitIndex != 6 {
		t.Fatal("Invalid commit: ", r.commitIndex)
	}
}

func TestSendAppendEntriesNoOpP2Unit(t *testing.T) {
	r := &Raft{me: 1, currentTerm: 22, log: []LogEntry{LogEntry{Term: 0}}, state: leader}
	r.nextIndex = []int{1, 1}
	r.matchIndex = []int{0, 0}

	// Heartbeat AppendEntries.
	args := appendEntriesArgs{
		Term:     22,
		LeaderID: r.me,
	}

	// Injected response.
	reply := appendEntriesReply{
		Term:      22,
		Success:   true,
		NextIndex: 1,
	}

	// Inject response.
	r.testAppendentriessuccess = true
	r.testAppendentriesreply = &reply

	r.sendAppendEntry(0, args)

	// sendAppendEntries shouldn't change the matchIndex or nextIndex.
	if r.state != leader || r.matchIndex[0] != 0 || r.nextIndex[0] != 1 {
		t.Fatalf("Invalid state %+v", r)
	}
}

func TestSendAppendEntriesStopsLeadingP2Unit(t *testing.T) {
	r := &Raft{me: 1, currentTerm: 22, log: []LogEntry{LogEntry{Term: 0}}, state: leader}
	r.nextIndex = []int{1, 1}
	r.matchIndex = []int{0, 0}

	// Heartbeat AppendEntries.
	args := appendEntriesArgs{
		Term:     22,
		LeaderID: r.me,
	}

	// Reply with higher term.
	reply := appendEntriesReply{
		Term:      25,
		Success:   false,
		NextIndex: 1,
	}

	// Inject reply.
	r.testAppendentriessuccess = true
	r.testAppendentriesreply = &reply

	r.sendAppendEntry(0, args)

	// sendAppendEntries should convert to follower and advance current term.
	if r.state != follower || r.currentTerm != 25 {
		t.Fatal("Invalid state, should be follower: ", r.state)
	}
}

func TestSendAppendEntriesSuccessUpdateIndicesP2Unit(t *testing.T) {
	r := &Raft{me: 1, currentTerm: 22, log: []LogEntry{LogEntry{Term: 0}}, state: leader}
	r.nextIndex = []int{2, 5}  // Follower log with two entries: [0 1]
	r.matchIndex = []int{1, 0} // Match up to index 1.

	// AppendEntries to add at nextIndex of 2.
	args := appendEntriesArgs{
		Term:         22,
		LeaderID:     r.me,
		PrevLogTerm:  22,
		PrevLogIndex: 1,
		Entries:      make([]LogEntry, 3),
	}

	// Successful response.
	reply := appendEntriesReply{
		Term:      22,
		Success:   true,
		NextIndex: 3,
	}

	r.testAppendentriessuccess = true
	r.testAppendentriesreply = &reply

	r.sendAppendEntry(0, args)

	if r.state != leader || r.matchIndex[0] != 4 || r.nextIndex[0] != 5 {
		t.Fatalf("Invalid state %+v", r)
	}
}

func TestSendAppendEntriesFailureUpdateIndicesP2Unit(t *testing.T) {
	r := &Raft{me: 1, currentTerm: 22, log: []LogEntry{LogEntry{Term: 0}}, state: leader}
	r.nextIndex = []int{6, 6}
	r.matchIndex = []int{1, 0}

	args := appendEntriesArgs{
		Term:         22,
		LeaderID:     r.me,
		PrevLogTerm:  22,
		PrevLogIndex: 5,
		Entries:      make([]LogEntry, 3),
	}

	reply := appendEntriesReply{
		Term:      22,
		Success:   false,
		NextIndex: 3,
	}

	r.testAppendentriessuccess = true
	r.testAppendentriesreply = &reply

	r.sendAppendEntry(0, args)

	// If not using NextIndex in replies, nextIndex should be 5.
	if r.state != leader || r.matchIndex[0] != 1 || r.nextIndex[0] != 3 {
		t.Fatalf("Invalid state %+v", r)
	}
}

// Single unit test for updateCommitIndex, with 7 cases.
func TestUpdateCommitIndexP2Unit(t *testing.T) {
	r := &Raft{me: 0, state: leader, commitIndex: 2, currentTerm: 3}
	r.apply = make(chan ApplyMsg, 100)
	r.log = make([]LogEntry, 1)
	addToLog(3, 1, &r.log)
	addToLog(2, 2, &r.log)
	addToLog(4, 3, &r.log)

	fmt.Println(r.log)

	// idx [0 1 2 3 4 5 6 7 8 9]
	// log [0 1 1 1 2 2 3 3 3 3]

	// Majority greater, all equal to 9 (current last entry).
	r.matchIndex = []int{0, 9, 1, 9}
	r.peers = make([]*rpc.Endpoint, 4)
	r.updateCommitIndex()
	if r.commitIndex != 9 {
		t.Errorf("a) Got %d, expected %d", r.commitIndex, 9)
	}
	r.commitIndex = 2

	// Majority greater, least in term is 8.
	r.matchIndex = []int{0, 8, 1, 8}
	r.peers = make([]*rpc.Endpoint, 4)
	r.updateCommitIndex()
	if r.commitIndex != 8 {
		t.Errorf("b) Got %d, expected %d", r.commitIndex, 8)
	}
	r.commitIndex = 2

	// Majority greater, least in term is 6.
	r.matchIndex = []int{0, 6, 8, 1, 9}
	r.peers = make([]*rpc.Endpoint, 5)
	r.updateCommitIndex()
	if r.commitIndex != 6 {
		t.Errorf("c) Got %d, expected %d", r.commitIndex, 6)
	}
	r.commitIndex = 2

	// Majority greater, none in term.
	r.matchIndex = []int{0, 4, 5, 1, 5}
	r.peers = make([]*rpc.Endpoint, 5)
	r.updateCommitIndex()
	if r.commitIndex != 2 {
		t.Errorf("d) Got %d, expected %d", r.commitIndex, 2)
	}
	r.commitIndex = 2

	// Majority equal.
	r.matchIndex = []int{0, 2, 1, 2, 2}
	r.peers = make([]*rpc.Endpoint, 5)
	r.updateCommitIndex()
	if r.commitIndex != 2 {
		t.Errorf("e) Got %d, expected %d", r.commitIndex, 2)
	}
	r.commitIndex = 2

	// Majority less.
	r.matchIndex = []int{0, 1, 1, 7, 2}
	r.peers = make([]*rpc.Endpoint, 5)
	r.updateCommitIndex()
	if r.commitIndex != 2 {
		t.Errorf("f) Got %d, expected %d", r.commitIndex, 2)
	}
	r.commitIndex = 2

	// Majority greater, some in term.
	r.matchIndex = []int{0, 4, 8, 1, 5}
	r.peers = make([]*rpc.Endpoint, 5)
	r.updateCommitIndex()
	if r.commitIndex != 2 {
		t.Errorf("g) Got %d, expected %d", r.commitIndex, 2)
	}
	r.commitIndex = 2
}

// ------------------ Part 2 Integration tests --------------------------------
// Part 2 adds log agreement and network failure. The following integration
// tests validate the system's behavior when processes join and leave the
// network, and as the network becomes paritioned.

// Validate that servers can run consensus in the absence of failures
// With 5 servers, repeat 3 times:
// 1. Validate that logs are initially empty at the index.
// 2. Run one round of consensus and validate that the index is the expected
// initial index.
// 3. Validate that all 5 servers agree on the index.
func TestConsensusNoFailurePart2(t *testing.T) {
	servers := 5
	cfg := makeConfig(t, servers, true)
	defer cfg.cleanup()

	cfg.begin("Consensus with no failure")

	for index := 1; index < 4; index++ {
		// 1. Validate that logs are initially empty at the index.
		nd, _ := cfg.countCommitsFor(index)
		if nd > 0 {
			t.Fatalf("%v servers have committed at %v before consensus was initiated.", nd, index)
		}

		// 2. Validate that the index returned is expected.
		i := cfg.agree(index*100, servers, false)
		if i != index {
			t.Fatalf("got index %v but expected %v", i, index)
		}
		time.Sleep(500 * time.Millisecond)
		nd, cmd := cfg.countCommitsFor(i)
		if nd != 5 || cmd != index*100 {
			t.Fatalf("%v servers agree on command %v", nd, cmd)
		}
	}

	cfg.end()
}

// Validate that consensus can be reached with a quorum of servers.
// With three servers:
// 1. Run consensus once with all servers.
// 2. Disconnect a follower.
// 3. Run consensus with 2 servers a few times.
// 4. Reconnect the follower.
// 5. Validate that consensus can be reached with all servers.
//
// Note that the agreement watch thread does log validation, so the reconnected
// server's logs are validated in the last consensus run.
func TestConsensusWithQuorumPart2(t *testing.T) {
	servers := 3
	cfg := makeConfig(t, servers, true)
	defer cfg.cleanup()

	cfg.begin("Consensus with a quorum of servers")

	// 1. Run consensus once.
	cfg.agree(101, servers, false)

	// 2. Disconnect a follower.
	leader := cfg.getLeader()
	failedServer := (leader + 1) % servers
	fmt.Println("			-- Disconnecting server", failedServer, "--")
	cfg.disconnectServer(failedServer)

	// 3. Run consensus with 2 servers a few times.
	cfg.agree(102, servers-1, false)
	cfg.agree(103, servers-1, false)
	time.Sleep(RaftElectionTimeout)
	cfg.agree(104, servers-1, false)
	cfg.agree(105, servers-1, false)

	// 4. Reconnect the follower.
	fmt.Println("			-- Reconnecting server", failedServer, "--")
	cfg.connectServer(failedServer)

	// 5. Validate that consensus can be reached with all servers.
	cfg.agree(106, servers, true)
	time.Sleep(RaftElectionTimeout)
	cfg.agree(107, servers, true)

	cfg.end()
}

// Validate that consensus can't be run when there isn't a quorum of servers.
// 1. Run an initial consensus.
// 2. Disconnect 3/5 servers.
// 3. Attempt to run consensus.
// 4. Validate that it doesn't succeed.
// 5. Reconnect servers.
// 6. Validate that consensus can now be reached.
func TestConsensusWithoutQuorumPart2(t *testing.T) {
	servers := 5
	cfg := makeConfig(t, servers, true)
	defer cfg.cleanup()

	cfg.begin("Conensus without a quorum of servers")

	// 1. Run an initial consensus.
	cfg.agree(10, servers, false)

	// 2. Disconnect 3/5 servers.
	leader := cfg.getLeader()
	fmt.Println("			-- Disconnecting",
		(leader+1)%servers, (leader+2)%servers, (leader+3)%servers, "--")
	cfg.disconnectServer((leader + 1) % servers)
	cfg.disconnectServer((leader + 2) % servers)
	cfg.disconnectServer((leader + 3) % servers)

	// 3. Attempt to run consensus.
	fmt.Println("			-- Attempting Start() on Leader", leader, "--")
	index, _, ok := cfg.rafts[leader].Start(20)
	if !ok {
		t.Fatalf("leader rejected Start()")
	}
	if index != 2 {
		t.Fatalf("expected index 2 from Start(), got %v", index)
	}

	// Wait a bit.
	time.Sleep(2 * RaftElectionTimeout)

	// 4. Validate that consensus didn't succeed.
	n, _ := cfg.countCommitsFor(index)
	if n > 0 {
		t.Fatalf("%v committed but no majority", n)
	}
	fmt.Println("			-- Done --")

	// 5. Reconnect servers.
	fmt.Println("			-- Reconnecting",
		(leader+1)%servers, (leader+2)%servers, (leader+3)%servers, "--")
	cfg.connectServer((leader + 1) % servers)
	cfg.connectServer((leader + 2) % servers)
	cfg.connectServer((leader + 3) % servers)

	// 6. Validate that consensus can now be reached.
	// Note that in this case there are two options. Either:
	// - leader managed to reach consensus with the new servers on log entry 2. A
	// new round of consensus will be put in log entry 3.
	// - a new leader was elected from the disconnected peers. In this case, the
	// new round of consensus will be put in log entry 2.
	leader2 := cfg.getLeader()
	index2, _, ok2 := cfg.rafts[leader2].Start(30)
	if !ok2 {
		t.Fatalf("leader2 rejected Start()")
	}
	if index2 < 2 || index2 > 3 {
		t.Fatalf("unexpected index %v", index2)
	}

	cfg.agree(1000, servers, true)

	cfg.end()
}

// Validate that multiple commands can be committed by the same leader, in
// parallel. With 3 servers, the test tries 5 times to commit many commands in
// parallel in the same term.
// 1. Get the current leader.
// 2. Start 5 threads that call Start() on the leader in parallel.
// 3. Validate that all commands were committed in the same term.
func TestConcurrentConsensusPart2(t *testing.T) {
	servers := 3
	cfg := makeConfig(t, servers, true)
	defer cfg.cleanup()

	cfg.begin("Concurrent consensus")

	var success bool
	// Try 5 times to get parallel commits.
loop:
	for try := 0; try < 5; try++ {
		if try > 0 {
			time.Sleep(3 * time.Second)
		}

		// 1. Get the current leader.
		leader := cfg.getLeader()
		term, _ := cfg.rafts[leader].GetState()

		// 2. Start 5 threads that call Start() on the leader in parallel.
		iters := 5
		var wg sync.WaitGroup
		idxCh := make(chan int, iters)
		for i := 0; i < iters; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				idx, t, ok := cfg.rafts[leader].Start(100 + i)
				if t != term {
					// Term has moved on.
					return
				}
				if ok != true {
					// No longer leader.
					return
				}
				idxCh <- idx
			}(i)
		}

		wg.Wait()
		close(idxCh)

		// Check still in the starting term.
		for j := 0; j < servers; j++ {
			if t, _ := cfg.rafts[j].GetState(); t != term {
				continue loop
			}
		}

		// 3. Validate that all commands were committed in the same term.
		failed := false
		cmds := make(map[int]int)
		for index := range idxCh {
			cmd := cfg.waitFor(index, servers, term)
			if cv, ok := cmd.(int); ok {
				if cv == -1 {
					// Still not in correct term.
					// Don't break/continue; drain channel.
					failed = true
				}
				cmds[cv] = index
			} else {
				t.Fatalf("value %v is not an int: %T", cmd, cmd)
			}
		}
		if failed {
			continue
		}

		for i := 0; i < iters; i++ {
			x := 100 + i
			if _, ok := cmds[x]; !ok {
				t.Fatalf("Missing command %v from %v", x, cmds)
				continue loop
			}
		}

		// All commands received, can quit retry loop.
		success = true
		break
	}

	// If the loop terminated due to iterations expiring, the test has failed.
	if !success {
		t.Fatalf("term changed too often")
	}

	cfg.end()
}

// Validate that a paritioned leader abandons failed log entries.
// With three servers:
// 1. Get one round of consensus.
// 2. Disconnect the leader.
// 3. Start rounds of consensus on the old, disconnected leader. These should
// later be discarded.
// 4. Run a round with the new leader of the connected majority.
// 5. Disconnect the new leader, and reconnect the old leader.
// 6. Run one round of consensus.
// 7. Reconnect all.
// 8. Run one round of consensus.
// 9. Validate that the committed entries is the correct number.
//
// The final commits should be the commits from (1), (4), (6), and (8).
func TestPartitionReconnectPart2(t *testing.T) {
	servers := 3
	cfg := makeConfig(t, servers, true)
	defer cfg.cleanup()

	cfg.begin("Partition and reconnect")

	// 1. Get one round of consensus.
	cfg.agree(101, servers, true)

	// 2. Disconnect the leader.
	leader1 := cfg.getLeader()
	cfg.disconnectServer(leader1)

	// 3. Start rounds of consensus on the old, disconnected leader. These should
	// later be discarded.
	cfg.rafts[leader1].Start(102)
	cfg.rafts[leader1].Start(103)
	cfg.rafts[leader1].Start(104)

	// 4. Run a round with the new leader of the connected majority.
	cfg.agree(103, 2, true)

	// 5. Disconnect the new leader, and reconnect the old leader.
	leader2 := cfg.getLeader()
	cfg.disconnectServer(leader2)
	cfg.connectServer(leader1)

	// 6. Run one round of consensus.
	cfg.agree(104, 2, true)

	// 7. Reconnect all.
	cfg.connectServer(leader2)

	// 8. Run one round of consensus.
	cfg.agree(105, servers, true)

	// 9. Validate that the committed entries is the correct number.
	if cfg.commits() != 4 {
		t.Fatalf("Expected 4 commits, got %v.", cfg.commits())
	}

	cfg.end()
}

// Validate that a process with long tail of inconsistent log entries is able to
// catch up once it rejoins the system. Without loss of generality number the
// servers 0-4.
// 1. Get an initial agreement with all servers.
// 2. Partition the leader, say 0,  and another process, say 1, by disconnecting
// 2, 3, and 4. Send a lot of commands to 0 that can't be committed by only two
// processes.
// 3. Disconnect 0 and 1.
// 4. Rejoin 2-4, and allow them to commit a lot of successful commands. Now,
// 0 and 1 have a long tail of uncommitted entries, while the current quorum
// have a long tail of committed entries.
// 5. Disconnect 2 and submit a lot of new entries to 3 and 4. Now 3 and 4 have
// a long tail of uncommitted messages.
// 6. Disconnect 3 and 4 and bring 0, 1, and 2 back. Send a lot of successful
// commands to this group. 0 and 1 will have to catch up to 2's successful
// entries.
// 7. Bring all up. 3 and 4 have will have to catch up.
// 8. Run one last round with all servers. At this point they all must have
// caught up and discarded uncommitted entries.
func TestReconcileLongLogsPart2(t *testing.T) {
	servers := 5
	cfg := makeConfig(t, servers, true)
	defer cfg.cleanup()

	cfg.begin("Reconcile long log inconsistences")

	// 1. Get an initial agreement.
	cfg.agree(rand.Int(), servers, true)

	// 2. Keep 0 and 1...
	leader1 := cfg.getLeader()
	fmt.Println("			-- Partitioning", leader1, (leader1+1)%servers, "--")
	cfg.disconnectServer((leader1 + 2) % servers)
	cfg.disconnectServer((leader1 + 3) % servers)
	cfg.disconnectServer((leader1 + 4) % servers)

	// ...and send a lot of commands that can't commit.
	for i := 0; i < 50; i++ {
		cfg.rafts[leader1].Start(rand.Int())
	}

	time.Sleep(RaftElectionTimeout / 2)

	// 3. Disconnect 0 and 1.
	cfg.disconnectServer((leader1 + 0) % servers)
	cfg.disconnectServer((leader1 + 1) % servers)

	fmt.Println("			-- Connecting", (leader1+2)%servers, (leader1+3)%servers, (leader1+4)%servers, "--")
	// 4. Rejoin 2-4 and...
	cfg.connectServer((leader1 + 2) % servers)
	cfg.connectServer((leader1 + 3) % servers)
	cfg.connectServer((leader1 + 4) % servers)

	// ...issue lots of successful commands.
	for i := 0; i < 50; i++ {
		cfg.agree(rand.Int(), 3, true)
	}

	// 5. Disconnect 2 and...
	leader2 := cfg.getLeader()
	other := (leader1 + 2) % servers
	if leader2 == other {
		other = (leader2 + 1) % servers
	}
	fmt.Println("			-- Disconnecting", other, "--")
	cfg.disconnectServer(other)

	// ...send lots more commands that can't commit.
	for i := 0; i < 50; i++ {
		cfg.rafts[leader2].Start(rand.Int())
	}

	time.Sleep(RaftElectionTimeout / 2)

	// 6. Disconnect all and bring 0, 1, and 2 back. ...
	fmt.Println("			-- Disconnecting all --")
	for i := 0; i < servers; i++ {
		cfg.disconnectServer(i)
	}
	fmt.Println("			-- Connecting", leader1, (leader1+1)%servers, other, "--")
	cfg.connectServer((leader1 + 0) % servers)
	cfg.connectServer((leader1 + 1) % servers)
	cfg.connectServer(other)

	// ...and send lots of successful commands. 0 and 1 will have to catch up.
	for i := 0; i < 50; i++ {
		cfg.agree(rand.Int(), 3, true)
	}

	fmt.Println("			-- Connecting all --")
	// 7. Bring all up. 3 and 4 will have to catch up.
	for i := 0; i < servers; i++ {
		cfg.connectServer(i)
	}

	// 8. Run one last round of agreement. This will ensure that logs are
	// consistent between all servers (as part of the apply check in the
	// background watch thread in the config).
	cfg.agree(rand.Int(), servers, true)

	cfg.end()
}

// Validate that the solution is RPC-efficient; that is, it doesn't send
// unnecessarily many RPCs.
// With 3 servers:
// 1. Validate that it doesn't require >~30 RPCs to elect an initial leader.
// 2. Try to get a stable count of RPCs required to reach agreement a few times.
// 3. With each try, get the count of RPCs sent at the start. Then, run
// consensus a few times.
// 4. Wait for consensus to finish and validate the total RPC count. If the term
// moved on at any point, try again to get a stable count, since an election
// would skew the RPC count.
// 5. If this passes, validate that RPCs in an idle steady-state are not too
// frequent.
func TestRpcEfficiencyPart2(t *testing.T) {
	servers := 3
	cfg := makeConfig(t, servers, true)
	defer cfg.cleanup()

	cfg.begin("Excessive RPCs")

	rpcs := func() (n int) {
		for j := 0; j < servers; j++ {
			n += cfg.rpcCount(j)
		}
		return
	}

	leader := cfg.getLeader()

	countStart := rpcs()

	// 1. Validate count to elect an initial leader.
	if countStart > 30 || countStart < 1 {
		t.Fatalf("too many or few RPCs (%v) to elect initial leader\n", countStart)
	}

	var countConsensus int
	var success bool
	// 2. Try to get a stable count of RPCs required to run consensus a few times.
loop:
	for try := 0; try < 5; try++ {
		if try > 0 {
			// Wait before starting instrumentation.
			time.Sleep(3 * time.Second)
		}

		// 3. Get current count...
		leader = cfg.getLeader()
		countStart = rpcs()

		// Get the current term and start sending commands.
		iters := 10
		startIndex, term, ok := cfg.rafts[leader].Start(1)
		if !ok {
			// Try again if the leader moved on too quickly.
			continue
		}
		// Run consensus a few times...
		for i := 1; i < iters+2; i++ {
			x := int(rand.Int31())
			_, curTerm, ok := cfg.rafts[leader].Start(x)
			if curTerm != term {
				// Term changed while starting.
				continue loop
			}
			if !ok {
				// No longer the leader, so term has changed.
				continue loop
			}
		}

		// Wait for all to finish.
		for i := 1; i < iters+1; i++ {
			cmd := cfg.waitFor(startIndex+i, servers, term)
			pcmd, _ := cmd.(int)
			if pcmd == -1 {
				// The term changed; need to retry.
				continue loop
			}
		}

		// 4. Validate total exchanged wasn't too excessive.
		countConsensus = rpcs()
		total := countConsensus - countStart
		if total > (iters+1+3)*3 {
			t.Fatalf("Too many RPCs for %v entries: %v", iters, total)
		}

		success = true
		break
	}

	if !success {
		t.Fatalf("The term changed too often to get a stable count.")
	}

	// Wait for an election timeout--in an idle steady-state the followers
	// shouldn't have too many heartbeats sent to them.
	time.Sleep(RaftElectionTimeout)

	// 5. Validate that the RPCs in the idle steady-state isn't too many.
	idleCount := rpcs()
	total := idleCount - countConsensus
	if total > 3*20 {
		t.Fatalf("Too many RPCs (%v) for 1 second of idleness, 50ms heartbeat is too fast\n", total)
	}

	cfg.end()
}

// ------------------ Part 3 Integration tests --------------------------------
// Part 3 introduces server failures and validates persistence of state.

// Validates that crashing and restarting servers does not affect the system's
// ability to reach consensus with consistent logs. Tries different permutations
// of crashing servers.
// With three servers:
// 1. Get agreement (log length 1).
// 2. Crash all servers.
// 3. Bring them up and get another agreement (log length 2).
// 4. Crash the leader and bring it back up.
// 5. Get agreement (log length 3).
// 6. Crash the leader.
// 7. Get agreement w/ 2 servers (log length 4).
// 8. Bring old leader back up and wait for it to catch up (validating log
// indices).
// 9. Crash a follower.
// 10. Get agreement (log length 5).
// 11. Bring follower back up.
// 12. Get agreement (log length 6).
// Note that the log consistency checks that are part of the testConfig.agree
// function ensure that the crashed and recovered followers have consistent
// logs.
func TestBasicPersistencePart3(t *testing.T) {
	servers := 3
	cfg := makeConfig(t, servers, true)
	defer cfg.cleanup()

	cfg.begin("Test (2C): basic persistence")

	// 1. Get agreement (log length 1).
	cfg.agree(11, servers, true)

	// 2. Crash all servers.
	for i := 0; i < servers; i++ {
		cfg.disconnectServer(i)
		cfg.startServer(i)
		cfg.connectServer(i)
	}

	// 3. Bring them up and get another agreement (log length 2).
	cfg.agree(12, servers, true)

	// 4. Crash the leader and bring it back up.
	leader := cfg.getLeader()
	cfg.disconnectServer(leader)
	cfg.startServer(leader)
	cfg.connectServer(leader)

	// 5. Get agreement (log length 3).
	cfg.agree(13, servers, true)

	// 6. Crash the leader.
	leader = cfg.getLeader()
	cfg.disconnectServer(leader)

	// 7. Get agreement w/ 2 servers (log length 4).
	cfg.agree(14, servers-1, true)

	// 8. Bring old leader back up and wait for it to catch up.
	cfg.startServer(leader)
	cfg.connectServer(leader)
	cfg.waitFor(4, servers, -1)

	// 9. Crash a follower.
	follower := (cfg.getLeader() + 1) % servers
	cfg.disconnectServer(follower)

	// 10. Get agreement (log length 5).
	cfg.agree(15, servers-1, true)

	// 11. Bring follower back up.
	cfg.startServer(follower)
	cfg.connectServer(follower)

	// 12. Get agreement (log length 6).
	cfg.agree(16, servers, true)

	cfg.end()
}

// Validate that processes that lag catch up when an ahead follower recovers and
// rejoins.
// With 5 servers, repeat the following five times. Without loss of generality
// assume the servers are numbered 0-4.
// 1. Get agreement with all, assume 0 is the leader in this round.
// 2. Crash 1 and 2.
// 3. Get agreement with 0, 3, and 4.
// 4. Crash 0, 3, and 4.
// 5. Recover 1 and 2.
// 6. Wait for 1 and 2 to potentially establish a leader.
// 7. Recover 3 (which is ahead of 1 and 2).
// 8. Get agreement with 3, 1, and 2 (requiring 1 and 2 to catch up).
// 9. Recover 0 and 4, which will have to catch up on the next round of
// consensus.
// 10. After this has repeated 5 times, get one last agreement to ensure 0 and 4
// are caught up.
func TestRecoveryWithLaggingFollowersPart3(t *testing.T) {
	servers := 5
	cfg := makeConfig(t, servers, true)
	defer cfg.cleanup()

	cfg.begin("Recovering an 'ahead' server catches up lagging followers")

	index := 1
	for iters := 0; iters < 5; iters++ {
		fmt.Println("			-- Round", iters+1, "--")
		// 1. All agree.
		cfg.agree(10+index, servers, true)
		index++

		leader := cfg.getLeader()

		// 2. Crash 1 and 2.
		cfg.disconnectServer((leader + 1) % servers)
		cfg.disconnectServer((leader + 2) % servers)

		// 3. Get agreement with 0, 3, and 4.
		cfg.agree(10+index, servers-2, true)
		index++

		// 4. Crash 0, 3, and 4.
		cfg.disconnectServer((leader + 0) % servers)
		cfg.disconnectServer((leader + 3) % servers)
		cfg.disconnectServer((leader + 4) % servers)

		// 5. Restart 1 and 2.
		cfg.startServer((leader + 1) % servers)
		cfg.startServer((leader + 2) % servers)
		cfg.connectServer((leader + 1) % servers)
		cfg.connectServer((leader + 2) % servers)

		// 6. Wait for a leader to be elected.
		time.Sleep(RaftElectionTimeout)

		// 7. Start 3.
		cfg.startServer((leader + 3) % servers)
		cfg.connectServer((leader + 3) % servers)

		// 8. Get agreement with 3, 1, and 2 (requiring 1 and 2 to catch up if 3
		// persisted correctly).
		cfg.agree(10+index, servers-2, true)
		index++

		// 9. Restart 0 and 4, which will have to catch up on the next agreement
		// round.
		cfg.connectServer((leader + 4) % servers)
		cfg.connectServer((leader + 0) % servers)
	}

	// 10. Get one last agreement to ensure 0 and 4 are caught up.
	cfg.agree(1000, servers, true)

	cfg.end()
}

// Validate the scenarios described in Figure 8 of the Raft paper. Test
// specifically the difference between 8 (d) and 8 (e), where the leader is not
// able to replicate a log entry from its term before failing vs. being able to
// replicate a log entry from its term.
//
// With 5 servers, we will repeat the following many times:
// 1. Each iteration calls Start() on a leader, if there is one.
// 2. Crash it quickly with probability 90% (which may result in it not
// committing the command).
// 3. Or crash it after a while with probability 10% (which most likely results
// in the command being committed).
// 4. If the number of alive servers isn't enough to form a majority, start a
// new server.
//
// Note that if reliable is false, the test will be run with unreliable mode
// turned on, allowing for dropped RPCs and long reorderings.
func RunFigure8(reliable bool, t *testing.T) {
	servers := 5
	cfg := makeConfig(t, servers, reliable)
	defer cfg.cleanup()

	cfg.begin("Figure 8")

	// Everyone agrees on something.
	cfg.agree(rand.Int(), 1, true)

	numUp := servers
	for iters := 0; iters < 1000; iters++ {
		if iters%100 == 0 {
			fmt.Println("			-- Iteration", iters, "--")
		}

		// After a few hundred iterations, turn on reorderings.
		if !reliable && iters == 200 {
			cfg.net.LongReordering(true)
		}

		// 1. Try to find a leader and call Start().
		leader := -1
		for i := 0; i < servers; i++ {
			if cfg.rafts[i] != nil {
				_, _, ok := cfg.rafts[i].Start(rand.Int())
				if ok && cfg.connected[i] {
					leader = i
				}
			}
		}

		var ms int64
		if rand.Int()%1000 > 100 {
			// 2. With probability 90% crash it quickly.
			ms = rand.Int63() % 15
		} else {
			// 3. With probability 10% crash is after a bit (in this case ~1/4 an
			// exlection timeout).
			ms = rand.Int63() % (int64(RaftElectionTimeout/time.Millisecond) / 2)
		}
		// Note that we *always* sleep, even if there wasn't a leader; we need to
		// give some time for a new leader to be elected.
		time.Sleep(time.Duration(ms) * time.Millisecond)

		// If there was a leader...
		if leader != -1 {
			// Crash the leader.
			// If using reorderings, only do this with some probability.
			if reliable || (rand.Int()%1000 < 500) {
				cfg.crashServer(leader)
				numUp--
			}
		}

		// 4. If there aren't enough up to form a majority, start a new server.
		if numUp < 3 {
			// Pick a server to restart, at random.
			// We may not have a leader on the next iteration, but the sleep should
			// give us enough time to elect one within a few iterations.
			s := rand.Int() % servers
			for cfg.rafts[s] != nil {
				s = rand.Int() % servers
			}
			cfg.startServer(s)
			cfg.connectServer(s)
			numUp++
		}
	}

	// Restart everyone.
	for i := 0; i < servers; i++ {
		if cfg.rafts[i] == nil {
			cfg.startServer(i)
			cfg.connectServer(i)
		}
	}

	// Get one last agreement to validate log consistency.
	cfg.agree(rand.Int(), servers, true)

	cfg.end()
}

func TestFigure8Part3(t *testing.T) {
	RunFigure8( /*reliable=*/ true, t)
}

// ------------------ Part 4 Integration tests --------------------------------
// Part 4 increases the unreliability of the network, including out of order
// messages and message loss.

// Validate that consensus proceeds with a faulty network.
// With 5 servers:
// 1. Run consensus many times (250 times), with many threads running
// concurrently.
// 2. Wait for all threads to finish.
// 3. Do one more round for a consistency check.
func TestUnreliablePart4(t *testing.T) {
	servers := 5
	cfg := makeConfig(t, servers, false)
	defer cfg.cleanup()

	cfg.begin("Unreliable consensus")

	var wg sync.WaitGroup

	// 1. Run consensus many times...
	for iters := 1; iters < 50; iters++ {
		// ... with many threads running concurrently.
		for j := 0; j < 4; j++ {
			wg.Add(1)
			go func(iters, j int) {
				defer wg.Done()
				cfg.agree((100*iters)+j, 1, true)
			}(iters, j)
		}
		cfg.agree(iters, 1, true)
	}

	// 2. Wait for all threads to finish.
	wg.Wait()

	cfg.net.Reliable(true)

	// 3. Do one more consistency check.
	cfg.agree(100, servers, true)

	cfg.end()
}

func TestUnreliableFigure8Part4(t *testing.T) {
	RunFigure8( /*reliable=*/ false, t)
}
