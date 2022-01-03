package raft

import (
	"testing"
	"time"
)

func TestWaitForTimeoutInternal(t *testing.T) {
	i := WaitFor(100)
	if i != -1 {
		t.Fatalf("Timeout not triggered with empty conds.")
	}
}

func TestWaitForTrueCondInternal(t *testing.T) {
	dl := time.Now().Add(100 * time.Millisecond)
	f := func() bool {
		if time.Now().After(dl) {
			return true
		}
		return false
	}
	i := WaitFor(1000, f)
	if i != 0 {
		t.Fatalf("Timed out before cond became true.")
	}
}

func TestTimeoutBeforeTrueCondInternal(t *testing.T) {
	dl := time.Now().Add(1000 * time.Millisecond)
	f := func() bool {
		if time.Now().After(dl) {
			return true
		}
		return false
	}
	i := WaitFor(100, f)
	if i != -1 {
		t.Fatalf("Cond became true before timeout.")
	}
}

func TestFirstCondWinsInternal(t *testing.T) {
	dl0 := time.Now().Add(1000 * time.Millisecond)
	f0 := func() bool {
		if time.Now().After(dl0) {
			return true
		}
		return false
	}

	dl1 := time.Now().Add(100 * time.Millisecond)
	f1 := func() bool {
		if time.Now().After(dl1) {
			return true
		}
		return false
	}

	i := WaitFor(2000, f0, f1)
	if i != 1 {
		t.Fatalf("Incorrect cond %d", i)
	}
}

func TestPeriodicThreadRunsInternal(t *testing.T) {
	counter := 0
	PeriodicThread(
		func() bool {
			counter++
			return true
		}, time.Duration(10)*time.Millisecond)
	time.Sleep(time.Duration(150) * time.Millisecond)
	if counter < 10 {
		t.Fatalf("Ran infrequently: %d", counter)
	}
}

func TestPeriodicThreadStopsInternal(t *testing.T) {
	counter := 0
	PeriodicThread(
		func() bool {
			counter++
			return counter < 10
		}, time.Duration(10)*time.Millisecond)
	time.Sleep(time.Duration(200) * time.Millisecond)
	if counter != 10 {
		t.Fatalf("Ran incorrect number of times: %d", counter)
	}
}
