package raft

import (
	"time"
)

// RaftElectionTimeout is the election timeout used for Raft processes.
const RaftElectionTimeout = 1000 * time.Millisecond

// RaftHeartbeatInterval is the heartbeat interval, currently 1/20th the
// election timeout.
const RaftHeartbeatInterval = 50 * time.Millisecond

// PeriodicThread runs a periodic thread; the given function is called every d
// duration. Terminates if the function returns false.
func PeriodicThread(t func() bool, d time.Duration) {
	go func() {
		for {
			if again := t(); !again {
				break
			}
			time.Sleep(d)
		}
	}()
}

// WaitFor waits for a given duration of time, or for a condition to become
// true. Returns -1 if the deadline expired. Otherwise, returns the index
// of the function that returned true.
// Example:
//  c1 := func() bool {
//			return False
//  }
//  c2 := func() bool {
//			return True
//  }
//  i := WaitFor(100, c1, c2) // Returns 1
func WaitFor(ms int, conds ...func() bool) (which int) {
	dl := time.Now().Add(time.Duration(ms) * time.Millisecond)
	for {
		for i, c := range conds {
			if c() {
				return i
			}
		}
		if time.Now().After(dl) {
			return -1
		}
		time.Sleep(10 * time.Millisecond)
	}
}
