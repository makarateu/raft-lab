package raft

// Raft implementation.

// This file is divided into the following sections, each containing functions
// that implement the roles described in the Raft paper.

// Background Election Thread -------------------------------------------------
// Perioidic thread that checks whether this server should start an election.
// Also attempts to commit any outstanding entries, if necessary (this could be
// a separate periodic thread but is included here).

// Background Leader Thread ---------------------------------------------------
// Periodic thread that performs leadership duties if leader. The leader must
// 1) call the AppendEntries RPC of its followers (either with empty messages or
// with entries to append) and 2) check whether the commit index has advanced.

// Start an Election ----------------------------------------------------------
// Functions to start an election by sending RequestVote RPCs to followers.

// Send AppendEntries RPCs to Followers ---------------------------------------
// Send followers AppendEntries, either as heartbeat or with state machine
// changes.

// Incoming RPC Handlers ------------------------------------------------------
// Handlers for incoming AppendEntries and RequestVote requests. Should populate
// reply based on the current state.

// Commit-Related Functions ---------------------------------------------------
// Functions to update the leader commit index based on the match indices of all
// followers, as well as to apply all outstanding commits.

// Persistence Functions ------------------------------------------------------
// Functions to persist and to read from persistent state. recoverState should
// only be called upon recovery (in CreateRaft). persist should be called
// whenever the durable state changes.

// Outgoing RPC Handlers ------------------------------------------------------
// Call these to invoke a given RPC on the given client.

import (
	// "raft/common" If want to use DieIfError.

	// "bytes" Part 4
	// "encoding/gob" Part 4
	"math/rand"
	"time"
	"bytes"
	"encoding/gob"
	"fmt"
	"github.com/golang/glog"
)

// Resets the election timer. Randomizes timeout to between 0.5 * election
// timeout and 1.5 * election timeout.
// REQUIRES r.mu
func (r *Raft) resetTimer() {
	to := int64(RaftElectionTimeout) / 2
	d := time.Duration(rand.Int63()%(2*to) + to)
	r.deadline = time.Now().Add(d)
}

// getStateLocked returns the current state while locked.
// REQUIRES r.mu
func (r *Raft) getStateLocked() (int, bool) {
	return r.currentTerm, r.state == leader
}

// Background Election Thread -------------------------------------------------
// Perioidic thread that checks whether this server should start an election.
// Also attempts to commit any outstanding entries, if necessary (this could be
// a separate periodic thread but is included here).

// Called once every n ms, where n is < election timeout. Checks whether the
// election has timed out. If so, starts an election. If not, returns
// immediately.
// EXCLUDES r.mu
func (r *Raft) electOnce() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.stopping {
		// Stopping, quit.
		return false
	}

	if time.Now().Before(r.deadline) {
		// Election deadline hasn't passed. Try later.
		// See if we can commit.
		go r.commit()
		return true
	}

	// Deadline has passed. Convert to candidate and start an election.
	glog.V(1).Infof("%d starting election for term %d", r.me, r.currentTerm)

	// TODO: Implement becoming a candidate (Rules for Servers: Candidates).
	// Reset election timer.
	r.resetTimer()

	//Update state
	r.state = candidate

	// Increment currentTerm.
	r.currentTerm++

	// Votes for self.
	r.votes = 1
	r.votedFor = r.me // I voted for myself.

	r.persist() // Persists.
	// Send RequestVote RPCs to followers.
	r.sendBallots()

	return true
}

// Background Leader Thread ---------------------------------------------------
// Periodic thread that performs leadership duties if leader. The leader must:
// call AppendEntries on its followers (either with empty messages or with
// entries to append) and check whether the commit index has advanced.

// Called once every n ms, where n is much less than election timeout. If the
// process is a leader, performs leadership duties.
// EXCLUDES r.mu
func (r *Raft) leadOnce() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.stopping {
		// Stopping, quit.
		return false
	}

	if r.state != leader {
		// Not leader, try later.
		return true
	}

	// Reset deadline timer.
	r.resetTimer()
	// Send heartbeat messages.
	r.sendAppendEntries()
	// Update commitIndex, if possible.
	r.updateCommitIndex()

	return true
}

// Start an Election ----------------------------------------------------------
// Functions to start an election by sending RequestVote RPCs to followers.

// Send RequestVote RPCs to all peers.
// REQUIRES r.mu
func (r *Raft) sendBallots() {
	for p := range r.peers {
		if p == r.me {
			continue
		}
		// TODO: Create RequestVoteArgs for peer p
		args := requestVoteArgs{r.currentTerm,
														r.me,
														r.log[len(r.log)-1].Term,
														len(r.log) - 1, }
		go r.sendBallot(p, args)
	}
}

// Send an individual ballot to a peer; upon completion, check if we are now the
// leader. If so, become leader.
// EXCLUDES r.mu
func (r *Raft) sendBallot(peer int, args requestVoteArgs) {

	glog.V(6).Infof("%d calling rv on %d", r.me, peer)
	// Call RequestVote on peer.
	var reply requestVoteReply
	if ok := r.callRequestVote(peer, args, &reply); !ok {
		glog.V(6).Infof("RPC to %d failed.", peer)
		return
	}

	// TODO: Handle reply; implement Candidate rules for Servers.
	// After Part 1, this code should be relatively stable, presuming Part 1 is
	// implemented correctly. If accessing Raft state, make sure a Mutex is held.
	r.mu.Lock()
	if reply.VoteGranted {
		r.votes++
		glog.V(6).Infof("Vote granted!")
		// If votes received from majority of servers: become leader.
		if float64(r.votes) > float64(len(r.peers))/float64(2) {
			r.state = leader
			glog.V(3).Infof("%v became the leader", r.me)
			r.nextIndex = make([]int, len(r.peers))  // Initialize nextIndex.
			r.matchIndex = make([]int, len(r.peers)) // Initialize matchIndex.
			for i := range r.nextIndex {
				r.nextIndex[i] = len(r.log) // Initialize nextIndex of each server to prevLogIndex of leader + 1
			}
		}
	} else {
		// If the candidate's term is less than another server's term, convert back to follower.
		glog.V(3).Infof("%v reverted back to a follower", r.me)
		r.state = follower         // Reverts back to follower.
		r.currentTerm = reply.Term // Updates to higher term.
		r.persist()                // Persists.
	}

	r.mu.Unlock()

	return
}

// Send AppendEntries RPCs to Followers ---------------------------------------
// Send followers AppendEntries, either as heartbeat or with state machine
// changes.

// Send AppendEntries RPCs to all followers.
// REQUIRES r.mu
func (r *Raft) sendAppendEntries() {
	for p := range r.peers {
		if p == r.me {
			continue
		}

		PrevLogIndex := len(r.log) - 1
		Entries := make([]LogEntry, 0)
		if r.nextIndex[p]-1 < PrevLogIndex {
			PrevLogIndex = r.nextIndex[p] - 1
			Entries = r.log[r.nextIndex[p]:]
		}

		// TODO: Create AppendEntriesArgs for peer p.
		args := appendEntriesArgs{r.currentTerm,
															r.me,
															r.log[PrevLogIndex].Term,
															PrevLogIndex,
															Entries,
															r.commitIndex, }
		go r.sendAppendEntry(p, args)
	}
}

// Send a single AppendEntries RPC to a follower. After it returns, update state
// based on the response.
// EXCLUDES r.mu
func (r *Raft) sendAppendEntry(peer int, args appendEntriesArgs) {
	// Call AppendEntries on peer.
	glog.V(6).Infof("%d calling ae on %d", r.me, peer)
	var reply appendEntriesReply
	if ok := r.callAppendEntries(peer, args, &reply); !ok {
		glog.V(6).Infof("RPC to %d failed.", peer)
		return
	}

	// TODO: Handle reply; implement Leader rules for Servers.
	// Implement the leader portion of AppendEntries from the paper.

	// For part 1, you don't have to worry about properly updating nextIndex and
	// matchIndex.

	// For part 2, you will have to handle updating these values in response to
	// successful and failed RPCs.

	r.mu.Lock()
	r.resetTimer()
	if reply.Success {
		if len(args.Entries) != 0 {
			r.matchIndex[peer] += len(args.Entries) // Peer's matchIndex is now up-to-date.
		}

		r.nextIndex[peer] = r.matchIndex[peer] + 1 // Update peer's nextIndex.

	} else {
		// If reply is unsuccessful, if reply's term is higher, revert to follower. Else update peer's nextIndex
		// if there's discrepancies.
		if reply.Term > r.currentTerm {
			r.state = follower
			r.currentTerm = reply.Term
			r.persist() // Persists.
		} else {
			r.nextIndex[peer] = reply.NextIndex
		}
	}


	r.mu.Unlock()
	return
}

// Incoming RPC Handlers ------------------------------------------------------
// Handlers for incoming AppendEntries and RequestVote requests. Should populate
// reply based on the current state.

// RequestVote RPC handler.
// EXCLUDES r.mu
func (r *Raft) RequestVote(args requestVoteArgs, reply *requestVoteReply) {
	// TODO: Implement the RequestVote receiver behavior from the paper.

	// For part 1, you don't have to worry about validating the "up to date"
	// property of logs.

	// For part 2, you will have to validate that the calling process is a
	// candidate for leader based on the length and terms of its logs.
	// 	 Section 5.4: "If the logs have last entries with different terms, then the
	// 	 log with the later term is more up-to-date. If the logs end with the same
	// 	 term, then whichever log is longer is more up-to-date."

	r.mu.Lock()
	defer r.mu.Unlock()

	if (args.Term == r.currentTerm && (r.votedFor == args.CandidateID || r.votedFor == -1)) || args.Term > r.currentTerm {
		if args.LastLogTerm > r.log[len(r.log)-1].Term || (args.LastLogTerm == r.log[len(r.log)-1].Term && len(r.log) <= args.LastLogIndex+1) {
			r.resetTimer()                // Reset election timeout.
			r.state = follower            // Convert to follower.
			r.currentTerm = args.Term     // Update current term with the proper term.
			r.votedFor = args.CandidateID // I voted for this candidate.
			reply.VoteGranted = true      // Vote granted :)
			r.persist()                   // Persists.
		}
	}

	reply.Term = r.currentTerm

	return
}

// AppendEntries RPC handler.
// EXCLUDES r.mu
func (r *Raft) AppendEntries(args appendEntriesArgs, reply *appendEntriesReply) {
	// TODO: Implement the AppendEntries receiver behavior from the paper.

	// For part 1, you do not have to handle appending log entries or detecting
	// term conflicts in the log. The deadline timer will need to be reset for
	// valid leader heartbeats.

	// For parts 2-4, you will have to handle log appends and conflict management.
	// You will also need to implement the optimization described in section 5.3
	// (also described in raft_api.go) in order for certain integration tests to
	// pass. If you don't implement it right away, you may need to modify unit
	// tests to ignore it (for now).
	r.mu.Lock()
	defer r.mu.Unlock()

	glog.V(3).Infof("%v's log is %v", r.me, r.log)
	glog.V(3).Infof("Args full attributes are %v", args)

	// If leader's term is less than our term, ignore request.
	if args.Term < r.currentTerm {
		reply.Term = r.currentTerm
		return // Cannot retry.
	}

	// Reset the election timer.
	r.resetTimer()

	// For other servers with lower terms to acknowledge the new leader.
	if args.Term > r.currentTerm || r.state != follower {
		r.state = follower
		r.currentTerm = args.Term
		r.persist() // Persists.
	}

	if args.PrevLogIndex > 0 {
		// If the leader is requesting an index that doesn't exist in our log.
		if args.PrevLogIndex >= len(r.log) {
			reply.NextIndex = len(r.log)
			reply.Term = r.currentTerm
			return // Unsuccessful RPC but can retry.
		} else if args.PrevLogTerm != r.log[args.PrevLogIndex].Term {
			// If the terms are mismatched, we go backwards.
			// We keep going back through all the entries with the same wrong term.
			// For some reason this fits the optimization description :)
			reply.NextIndex = args.PrevLogIndex
			for i := args.PrevLogIndex - 1; i >= 0; i-- {
				if r.log[i].Term != r.log[args.PrevLogIndex].Term {
					break
				}
				reply.NextIndex-- // Decrement along the way.
			}
			reply.Term = r.currentTerm
			return // Unsuccessful RPC but can retry.
		}
	}

	var Entries []LogEntry
	if len(args.Entries) > 0 {
		// If an existing entry conflicts with a new one (same index
		// but different terms), delete the existing entry and all that
		// follow it. Append any new entries not already in the log.
		for i := range args.Entries {
			if i+args.PrevLogIndex+1 > len(r.log)-1 {
				Entries = args.Entries[i:]
				break
			}
			if args.Entries[i].Term != r.log[i+args.PrevLogIndex+1].Term {
				r.log = r.log[:i+args.PrevLogIndex+1]
				Entries = args.Entries[i:]
				break
			}
		}

		// Append to log.
		r.log = append(r.log, Entries...)
		r.persist() // Persists.
	}

	// If leaderCommit > commitIndex, set commitIndex = min(leaderCommit, index of last new entry).
	if args.LeaderCommit > 0 && args.LeaderCommit > r.commitIndex {
		// r.commitIndex = int(math.Min(float64(args.LeaderCommit), float64(len(r.log)-1)))
		r.commitIndex = args.LeaderCommit // We update the commitIndex instead, which I assume is for us to catch up gradually.
	}

	reply.NextIndex = len(r.log) // Reply with nextIndex regardless.
	reply.Term = r.currentTerm   // Reply with currentTerm regardless.
	reply.Success = true         // No disruption.

	return
}

// Commit-Related Functions ---------------------------------------------------
// Functions to update the leader commit index based on the match indices of all
// followers, as well as to apply all outstanding commits.

// Update commitIndex for the leader based on the match indices of followers.
// REQUIRES r.mu
func (r *Raft) updateCommitIndex() {
	// TODO: Update commit index; implement Leaders part of Rules for Servers.

	// From the paper:
	// "If there exists an N such that N > commitIndex, a majority of
	// matchIndex[i] ≥ N, and log[N].term == currentTerm: set commitIndex = N"
	majorities := make([][]int, 0)
	for N := r.commitIndex + 1; N < len(r.log); N++ {
		if r.log[N].Term == r.currentTerm {
			ctr := 0
			for i := range r.matchIndex {
				if r.matchIndex[i] >= N {
					ctr++
				}
			}
			// Increment 1 to account for leader too. Might change if discrepancies arise.
			if float64(ctr)+1 > float64(len(r.matchIndex))/float64(2) {
				majorities = append(majorities, []int{N, ctr})
			}
		}
	}

	// Since the tests require the BIGGEST majority and not just a majority. We have to check for
	// the N with the most accepted occurrences.
	if len(majorities) != 0 {
		maximum := majorities[0][1]
		newCommitIndex := majorities[0][0]
		for _, majority := range majorities {
			if majority[1] >= maximum && majority[0] > newCommitIndex {
				newCommitIndex = majority[0]
				maximum = majority[1]
			}
		}
		r.commitIndex = newCommitIndex
		glog.V(3).Infof("%v's commitIndex is now %v", r.me, r.commitIndex)
	}

	return
}

// Commit any outstanding committable indices.
// EXCLUDES r.mu
func (r *Raft) commit() {
	// TODO: Commit any entries between lastApplied and commitIndex.
	r.mu.Lock()
	defer r.mu.Unlock()

	// If commitIndex > lastApplied: increment lastApplied, apply log[lastApplied]
	// to state machine.
	if r.commitIndex > r.lastApplied {
		r.lastApplied++
		glog.V(5).Infof("%v applied index %v", r.me, r.lastApplied)
		r.apply <- ApplyMsg{
			CommandIndex: r.lastApplied,
			Command:      r.log[r.lastApplied].Command,
		}
	}

	return
}

// Persistence Functions ------------------------------------------------------
// Functions to persist and to read from persistent state. recoverState should
// only be called upon recovery (in CreateRaft). persist should be called
// whenever the durable state changes.

// Save persistent state so that it can be recovered from the persistence layer
// after process recovery.
// REQUIRES r.mu
func (r *Raft) persist() {
	if r.persister == nil {
		// Null persister; called in test.
		return
	}

	// TODO: Encode data and persist it using r.persister.WritePersistentState.
	buff := new(bytes.Buffer)
	encoder := gob.NewEncoder(buff)

	// Encode term.
	err := encoder.Encode(r.currentTerm)
	if err != nil {
		return
	}

	// Encode votedFor.
	err = encoder.Encode(r.votedFor)
	if err != nil {
		return
	}

	// Encode the log.
	err = encoder.Encode(r.log)
	if err != nil {
		return
	}

	d := buff.Bytes()
	r.persister.WritePersistentState(d)

	return
}

// Called during recovery with previously-persisted state.
// REQUIRES r.mu
func (r *Raft) recoverState(data []byte) {
	if data == nil || len(data) < 1 { // Bootstrap.
		r.log = append(r.log, LogEntry{0, nil})
		r.persist()
		return
	}

	if r.persister == nil {
		// Null persister; called in test.
		return
	}

	// TODO: Recover from persisted data.
	buff := bytes.NewBuffer(data)
	d := gob.NewDecoder(buff)

	// Decode the term.
	err := d.Decode(&r.currentTerm)
	if err != nil {
		return
	}

	// Decode votedFor.
	err = d.Decode(&r.votedFor)
	if err != nil {
		return
	}

	// Decode the log.
	err = d.Decode(&r.log)
	if err != nil {
		return
	}

	glog.V(3).Infof("Recovering state for %v, state: %v, log: %v, votedFor: %v, nextIndex: %v", r.me, r.state, r.log, r.votedFor, r.nextIndex)

	return
}

// Outgoing RPC Handlers ------------------------------------------------------
// Call these to invoke a given RPC on the given client.

// Call RequestVote on the given server. reply will be populated by the
// receiver server.
func (r *Raft) requestVote(server int, args requestVoteArgs, reply *requestVoteReply) {
	if ok := r.callRequestVote(server, args, reply); !ok {
		glog.V(6).Infof("RPC to %d failed.", server)
		return
	}
}

// Call AppendEntries on the given server. reply will be populated by the
// receiver server.
func (r *Raft) appendEntries(server int, args appendEntriesArgs, reply *appendEntriesReply) {
	if ok := r.callAppendEntries(server, args, reply); !ok {
		glog.V(6).Infof("RPC to %d failed.", server)
		return
	}
}
