package raft

// PersistentState interface to be used by Raft implementations for durably
// saving and restoring state.

import (
	"sync"
)

// PersistentState is a persistable buffer.
type PersistentState struct {
	sync.Mutex
	state []byte // State is a byte buffer.
}

// MakePersistentState creates a new PersistentState.
func MakePersistentState() *PersistentState {
	return &PersistentState{}
}

// Copy perform a copy of persistent state, returning a duplicate.
func (ps *PersistentState) Copy() *PersistentState {
	ps.Lock()
	defer ps.Unlock()
	np := MakePersistentState()
	np.state = ps.state
	return np
}

// WritePersistentState writes the given bytes to persistent state.
func (ps *PersistentState) WritePersistentState(state []byte) {
	ps.Lock()
	defer ps.Unlock()
	ps.state = state
}

// ReadPersistentState reads previously persisted state or nil if state is
// empty.
func (ps *PersistentState) ReadPersistentState() []byte {
	ps.Lock()
	defer ps.Unlock()
	return ps.state
}
