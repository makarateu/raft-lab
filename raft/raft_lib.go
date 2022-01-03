package raft

import (
	"github.com/golang/glog"
)

// ----------------------------------------------------------------------------
// Physical RPC implementations. On success, returns true.

// Calls RequestVote on a remote server, given the server's id and args. reply
// is populated with by remote server.
func (r *Raft) callRequestVote(server int, args requestVoteArgs, reply *requestVoteReply) bool {
	// When there are no peers, return a test response, if any.
	if len(r.peers) == 0 {
		// Under test, return injected reply.
		glog.V(2).Infof("Under test, returning injected reply %v", reply)
		if r.testRequestvotesuccess {
			*reply = *r.testRequestvotereply
		}
		return r.testRequestvotesuccess
	}
	ok := r.peers[server].Call("Raft.RequestVote", args, reply)
	return ok
}

// Calls RequestVote on a remote server, given the server's id and args. reply
// is populated with by remote server.
func (r *Raft) callAppendEntries(server int, args appendEntriesArgs, reply *appendEntriesReply) bool {
	// When there are no peers, return a test response, if any.
	if len(r.peers) == 0 {
		// Under test, return injected reply.
		glog.V(2).Infof("Under test, returning injected reply %v", reply)
		if r.testAppendentriessuccess {
			*reply = *r.testAppendentriesreply
		}
		return r.testAppendentriessuccess
	}
	ok := r.peers[server].Call("Raft.AppendEntries", args, reply)
	return ok
}
