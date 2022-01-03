package raft

// The Raft API and utility functions.

// ------------------ Raft Client Interface -----------------------------------

// An ApplyMsg is an input for the state machine.
type ApplyMsg struct {
	CommandIndex int         // The index of the command in the Raft log.
	Command      interface{} // The command to apply.
}

// A LogEntry is a single log entry.
type LogEntry struct {
	Term    int         // The term of the entry.
	Command interface{} // The input to the state machine.
}

// ------------------ Raft Server State ---------------------------------------

// State is the state of a Raft server.
type State int

const (
	follower  State = iota // server is a follower.
	candidate              // server is a candidate.
	leader                 // server is a leader.
)

// Stringify function for state enum.
func (s State) String() string {
	return [...]string{"Follower", "Candidate", "Leader"}[s]
}

// ------------------ Raft RPC Request/Responses ------------------------------

type requestVoteArgs struct {
	Term         int // Term for candidate
	CandidateID  int // Id of candidate
	LastLogTerm  int // Last term in candidate's log
	LastLogIndex int // Index of last element in candidate's log
}

type requestVoteReply struct {
	Term        int  // Term of receiver
	VoteGranted bool // Whether the vote was granted
}

type appendEntriesArgs struct {
	Term         int        // Term of leader
	LeaderID     int        // Id of leader
	PrevLogTerm  int        // Term of log entry at prevlogindex
	PrevLogIndex int        // Log preceding those to append
	Entries      []LogEntry // Log entries to append
	LeaderCommit int        // Leader's commit index
}

type appendEntriesReply struct {
	Term    int  // Term of receiver
	Success bool // Whether the append succeeded

	NextIndex int // Next index at receiver (see section 5.3):
	ConflictTerm  int  // Term of conflicting index
	ConflictIndex int  // First index of conflicting index.
	// "If desired, the protocol can be optimized to reduce the number of
	// rejected AppendEntries RPCs. For example, when rejecting an AppendEntries
	// request, the follower 7 can include the term of the conflicting entry and
	// the first index it stores for that term."
}
