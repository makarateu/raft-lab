# COMP385 Lab 3: Raft

### Important: This README and the Raft paper are your lifelines for this lab

This document is __long__, but it contains a huge amount of tips, guidance, and
advice for being succesful with this lab.

Similarly, the starting code is also expansive (~3700 lines) but by reading
through it and understanding how functions are calling one another, you will
have a much clearer picture of how the code fits together.

For what it's worth, my most recent reference solution is ~4000 lines of code,
including logging and comments. That's a delta of ~300 lines of code. Also, the
code complexity is rather low--it does not even contain a nested loop. The
challenge of this lab is not in devising sophisticated algorithms, but rather,
capturing an already-sophisticated algorithm exactly and safely, in a highly
concurrent and nondeterministic environment.

## Getting started

Before you begin, clone this repository to your local computer. Welcome to the
next three weeks of your life.

```
$ git clone https://github.com/[your github repo]
```

__The very first thing you should do is create and switch over to a branch.__

```
git checkout -b [your branch name]
```

## Raft overview

In this lab, you will implement Raft--the consensus and replicated state machine
layer from [this
paper](https://matthewlang.github.io/comp385/readings/raft.pdf).

This replicated state machine protocol can be used as a foundational primitive
for building more sophisticated systems.

Raft is used to ensure that a sequence of inputs (_commands_) to a collection of
replicated state machines is consistent and each state machine agrees upon the
sequence of inputs, and whether or not the inputs have been applied.

Raft does this by organizing client inputs into a sequence (called the _log_).
A designated _leader_ process ensures that all Raft follower processes
agree on the content and order of entries in their logs. When the leader has
determined that all followers have the same view of their log, the leader then
allows followers (and itself) to _apply_/_commit_ the entries in the log to
their underlying state machines.

Since Raft only requires a majority of servers to agree on the contents of the
log before committing it, it can continue to operate even when a minority of
servers are down.

Raft also has a mechanism to elect a new leader if the leader were to fail.

## Outline of lab

Before you begin this lab, you should re-read the Raft paper (up to section 6).
This lab will ask you to implement a Go process that acts as a Raft server and
communicates with Raft peer processes.

The [Raft website](https://raft.github.io/) has an illustration of how Raft
works that allows you to experiment with it. As linked from the Raft site, there
is also [another animation](http://thesecretlivesofdata.com/raft/) that
illustrates how Raft works at a high level.

The lab begins by having you implement the leader election component of Raft
(Part 1). After ensuring that you can reliably elect a leader, you will
implement reliable log replication when processes can be network
partitioned (Part 2), followed by reliable replication when processes can crash
(Part 3).  Part 4 simulates unreliable network conditions.

The Raft implementation is heavily scaffolded; you will be asked to follow a
particular design and implement particular functions. That being said, there is
some freedom in their implementation and exact specifications.

Additionally, the Raft algorithm has some subtleties, so the main challenge will
be in implementing the algorithm _exactly_ as described. You can view this lab
as implementing the boxes from page 5 in the paper. You should refer to that
page often, as it is the source of truth for the Raft implementation.

### Tour of lab code

The code for this lab looks like this:

```
lab3
├── README.md ---------- This document
├── common ------------- Utilities used RPC and Raft code
│   └── common.go ------ "
├── raft --------------- The main Raft package
│   ├── common.go ------ Utilities used in Raft
│   ├── common_test.go - Unit tests for utilities
│   ├── persister.go --- Persistence layer for Raft
│   ├── raft.go -------- Raft implementation (main source file)
│   ├── raft_api.go ---- Raft API
│   ├── raft_core.go --- Raft interface definition
│   ├── raft_lib.go ---- Raft utility functions
│   ├── raft_test.go --- Main tests for lab
│   └── test_config.go - Helper for main tests
└── rpc ---------------- RPC library used in lab
    ├── rpc.go --------- "
    └── rpc_test.go ---- RPC library tests
```

The main files to pay attention to are the ones that begin with `raft`. You
should open and read these files to get a sense of the general outline of code
for the lab.

* `raft_core.go`: This is the interface of Raft used by clients. It defines the
  Raft structure that you will be implementing functions for, included its
  ephemeral and persistent state. _Do not modify this file (other than to add
  logging, if desired)._

* `raft_api.go`: This defines Raft's RPC interface as well as the structs with
  which is communicates with its underlying state machine. _Do not modify this
  file (other than to add logging, if desired)._

* `raft.go`: This is the main implementation file for a Raft server. Inside, you
  will find the functions that you have to implement for the lab, some of which
  are partially implemented. The description of the various parts of the lab
  will illustrate certain parts of this file.

* `raft_test.go`: This is the main set of tests for this lab. 

   This file contains a large amount of tests of two flavors:

   * Integration tests. These tests create several Raft processes and validate
   	 that the system can or cannot reach consensus when the network is perturbed
   	 in various ways. They also validate leader election and log consistency
   	 between Raft processes. Note that these tests can sometimes run for a long
   	 period of time.

   * Unit tests. These tests test individual functions. Currently, only part 2
     has a set of unit tests. You should examine them before you begin part 1,
     as if you get stuck on part 1 it will be helpful to introduce your own
     tests.

   Each suite of tests has a suffix: `Part1`, `P2Unit`, `Part2`, `Part3`, and
   `Part4`. Use the `-run` flag to select a particular suite. If you add unit
   tests for part 1, you may want to use `P1Unit` as a suffix.

* `raft_lib.go`: This file contains the RPC implementations to call RPCs on
  other Raft servers. These allow you to inject responses in tests, if desired.
  _Do not modify this file (other than to add logging, if desired)._

### General outline of this Raft implementation

There are two perspectives from which we can look at this lab's implementation
of Raft: that of the client that wants to use Raft to send commands to a state
machine, and that of the implementer of Raft (you).

* From the client's perspective, the client calls the function `CreateRaft` in
  `raft_core.go`. This creates and starts a Raft server that is then able to
  accept commands to commit to the state machine. Please read the documentation
  for this function (and the others) in the source file. The comments are more
  detailed than this description.

  Then, when the client wants to send a command to the state machine, they call
  `Start` (`raft_core.go`) on the Raft instance with their given command. When
  called on the leader, this function writes the command to the leader's log at
  a particular index with the current term. Then Raft will try to replicate that
  command to a quorum of the other Raft instances. 

  The client can continue to send commands. When the client wants to shut down a
  Raft server, they call `Stop`. This shuts down the server.

* From your perspective as the programmer of the Raft server, the server is
  roughly organized as follows.

  The main `Raft` struct (`raft_core.go`) contains all of the persistent and
  ephemeral (not persisted) state for the Raft server. These are the elements
  described in the __State__ box on page 5 of the paper.

  The `Raft` struct also contains implementation details: the server's id, RPC
  endpoints for its peers, a reference to a tool for persisting its state, a
  channel to send messages to its state machine, etc. A key component of the
  state is its `deadline`. This field is the timer that the Raft servers use for
  leader election timeouts.

  When the Raft process starts, it starts two background threads (using
  `PeriodicThread` in `raft/common.go`).

  One periodic thread is the `election` thread: it checks whether or not it is
  time to start an election (_e.g._, if it hasn't received a heartbeat from the
  leader before its deadline).

  The other periodic thread is the `leader` thread. It performs leadership
  duties: sending heartbeats with empty or new log entries to append, updating
  its current `commitIndex`, etc.

  Both of these are called _periodic_ threads because they periodically invoke
  functions (`electOnce` and `leadOnce` in `raft.go`). These functions perform
  their duties and then exit. The functions are called every $n$ milliseconds
  until the client calls `Stop`.

  These functions perform the main work of the __Rules for Servers__ box on page
  5 of the paper.

  `raft.go` also contains the functions for handling incoming RPCs from other
  instances of Raft (`AppendEntries` and `RequestVote`). These will implement
  the __AppendEntries RPC__ and __RequestVote RPC__ boxes from the paper.

### Tips

* __Tests:__ When you test your implementation, you will use the given test suite.
  Descriptions of integration tests for each part of the lab are given in the
  corresponding part of the writeup. You should also spend some time _reading_
  the documentation for tests in `raft_test.go`.

  You may want to write unit tests to help you if you get stuck with Part 1.
  Part 2 has unit tests you can use as a template.

  You are free to modify the tests to print debug information, however I will
  run your code against the default integration tests, so don't make
  _functional_ changes to them.

* __Logging:__ You should use `glog` for debug logging for the lab. Refer to lab
  2's writeup to refresh your memory of logging. We are using logging vs.
  `printf` in this lab because you will likely want to log _a lot_ when you are
  debugging, and almost nothing when the system is operating normally.

  By default the tests are run with no output. If you want to enable logging to
  the console, you should run your test with the `-test.v` flag. This enables
  output from both the integration tests as well as
  `glog.{Infof,Warningf,Errorf}`.

  __However, use verbose logging!__ You will want to have log statements that you can
  enable/disable when you are debugging particularly subtle or rare race
  conditions. Use `glog.V(level).Infof(...)` to log at a particular level. Then,
  when you run the tests, you can set the maximum level you will log with the
  `-vlevel n` flag.

  For example:
  ```
  go test raft/raft -run Part1 -vlevel 4 -test.v
  ```

  This will run the test enabling all log levels less than or equal to 4.
  Anything logged `glog.V(5).Infof(...)` would _not_ be logged.

  There are already examples of this style of logging in `raft.go`,
  `raft_core.go`, and in `rpc.go`. The latter examples use a very high log
  level, so that that they will probably not be logged by your code, but can be
  seen if you run with `vlevel` above 11 (e.g., with `go test raft/rpc -test.v
  -v 13`--note the different flag, sorry).

* The lab uses a local RPC implementation so that it can be instrumented in the
  integration tests. The performance of this RPC implementation should be fine
  for almost any machine, but it contains a benchmark, should you be concerned
  that your computer is too slow (this affects timing, which Raft relies on).
  You can run the benchmark here:

  ```
  go test raft/rpc -bench Network
  ```

  The source code for `rpc_test.go` contains the timing numbers from the
  computer where I wrote/ran the reference implementation (~15us/RPC).

  Some of the tests in this lab take a long time to run (the _entire_ suite
  should take about 2-5 minutes depending on your computer). If you implement
  Raft inefficiently, they will take longer. Run the benchmark before you blame
  your computer for being slow.

* Since many parts of Raft depending on timing, coordination, and
  synchronization between servers, the tests will necessarily be nondeterministic.
  You will want to run tests many times for each part of the lab (I will do this
  as well). The `-count` flag allows you to specify the number of times to run a
  test. When a run fails, Go moves on to the next run. If you want it to stop
  (so that you can inspect output), use `-failfast`. This stops Go whenever
  there is a failure.

  For example:
  ```
  go test raft/raft -run Part1 -test.v -vlevel 1 -count 10 -failfast
  ```

  This will run Part 1 tests with verbose logging level 1. Each test will be run
  10 times, and the test suite will immediately stop when a test fails.

* The Raft algorithm is subtle. It is __very__ important to follow the algorithm
  described in the paper exactly. For example, in the `AppendEntries` receiver
  implementation, the paper describes:

  > 1. Reply false if term < currentTerm (§5.1)
  >
  > 2. Reply false if log doesn’t contain an entry at prevLogIndex whose term
  >    matches prevLogTerm (§5.3)
  >
  > 3. If an existing entry conflicts with a new one (same index but different
  >    terms), delete the existing entry and all that follow it (§5.3)

  This means that if `term < currentTerm` the receiver should reply false and
  not modify its state (the RPC is stale--from a previous term--and not from the
  current leader).

  It also means that if the entry at prevLogIndex does not match the term of
  prevLogTerm, the receiver should reply false __and not modify its log__. 

  Also, condition 3 __only__ applies when there is a mismatch in the logs that
  the receiver is appending (_i.e._, it is going to return true). And note that
  it truncates __only__ when there is a mismatch!

  If there is no mismatch, all entries that follow the entries that the leader
  sent __must be kept__. This could be an old RPC!

* Another important pitfall is that a heartbeat from the leader (empty `Entries`
  on the RPC) __must also__ perform the checks in the AppendEntries section
  (_i.e._, returning false if necessary)!

* Also pay attention to the section in __Rules for Servers__ that describes
  rules for all servers; these are actions that need to be done on any RPC
  request or response!

* If you get stuck on Part 1 and are having trouble debugging, write unit tests
  for your functions. There are good patterns for how to do this in the unit
  tests for Part 2 (those named `P2Unit`).

* Note that methods have been marked with REQUIRES and EXCLUDES annotations.
  These annotations describe whether the method must be called while r.mu is
  held/locked, or if they cannot be held while r.mu is locked. This is inspired
  by C++ mutex annotations, which can be enforced by the compiler and are a
  standard practice in large C++ codebases (including at Google, which
  [introduced
  them](https://static.googleusercontent.com/media/research.google.com/en//pubs/archive/42958.pdf)).

* Make sure you _read_ the comments and the tests. __I cannot stress this
  enough.__ It is _very_ important that you build a general sense of how
  everything is supposed to fit together and how the tests are exercising the
  system.

* Debug logging will be _very_ helpful for you in finding errors in your code.
  Make sure to log important events. Then, when you find something unexpected in
  your logs, add more logging to inspect the state of your server so you can
  see when things change.

* Make sure to restart the timer `resetTimer` _only_ when you need to: when
  getting an `AppendEntries` RPC _from the current leader_, when you start an
  election, when you grant a vote to another peer, or periodically if the
  process is the leader.

* Try not to treat heartbeat messages as special (either on the follower or on
  the leader). If you write your code so that it handles the heartbeat case (no
  entries to send) and the append case (when there are entries to send) the
  same, your code will be __much__ simpler.

* Along those lines, remember that you can slice Go lists. `list[i:]` returns
  the slice that starts at `i` and goes to the end of the list (empty if `i ==
  len(list)`). `list[:i]` returns the slice that starts at the beginning of the
  list and goes up to (but doesn't include) `i`.

* If you find a particular test is flaky or you want to focus specifically on
  it, you can always use the full name of the test for the `-run` flag to run
  only that test.

* Ask questions! __GET STARTED EARLY AND SEE ME IF YOU GET STUCK.__

## Part 1

In this part of the lab you will implement leader election. Specifically, you
should implement:

* The rules for an election timeout: the follower becomes a candidate for a new
  term ("Candidate" in "Rules for Servers" in the paper). See `leadOnce`.

* Deciding the appropriate arguments for other servers when sending a
  RequestVote RPC (`sendBallots` in `raft.go`), as well as handling the response
  (in `sendBallot`) from the receiver (when the server can become leader).

* Implementing the `RequestVote` handler and the rules for granting votes. The
  handler that will be called is `RequestVote` in `raft.go`.

* Implementing the heartbeat RPCs by deciding the appropriate arguments (in
  `sendAppendEntries`) and handling the responses (`sendAppendEntry`).

* Implementing the `AppendEntries` handler and the rules for extending the
  election timeout. The handler that will be called is `AppendEntries` in
  `raft.go`.

### Tips

* Make sure you read and understand the comments in the `raft.go` file.

* Notice that the actual sending of RPCs and handling the responses are in
  goroutines: `sendBallots` calls `go sendBallot(p, args)`, which starts a
  background thread. Similarly `sendAppendEntries` does the same thing for
  `appendEntries`.

* Likewise the RPC handlers `RequestVote` and `AppendEntries` will be called by
  different threads.

* You will need to make sure that you acquire and release mutexes in these
  functions. Some functions are called already with the mutex held. These are
  marked with a `REQUIRED r.mu` tag. Functions that will acquire the mutex are
  marked with `EXCLUDED r.mu` (adding these annotations is a general practice to
  help write code that doesn't deadlock).

  You _do not need to do any fine-grained locking_. It is fine for most parts of
  the lab to just `Lock` the mutex and `defer r.mu.Unlock()`.

* To run the tests for this part of the lab, run the following. Recall you can
  add `-vlevel` if you use verbose logging.

  ```
  go test raft/raft -run Part1 -test.v
  ```

  Your solution should probably finish in less than 10 seconds (if you are close
  in performance to the reference computer). If not, later parts of the lab may
  be slow or time out.

* This part of the lab is tricky, as it will be the first step in the
  implementation and navigating the lab's code. However, the bulk of the work in
  the lab is in part 2. __Start early.__ If you are stuck and don't know what to
  do next, __come see me__.

### Part 1 tests overview

* `TestElectionNoFailurePart1`: Validate that three servers can elect an initial leader.

  1. Start 3 servers.
  2. Check that a leader is elected (`getLeader`).
  3. Wait for some period of time and validate that all servers agree on the
     current term (`validateTerms`).
  4. Wait for some time and validate that the term hasn't moved on, since there
     has been no failure.
  5. Validate the leader hasn't changed..

* `TestElectionWithFailurePart1`: Validate that if a leader is disconnected, a
  new leader is elected (in a new term).

  With three servers:

  1. Wait for a leader to be elected.
  2. Disconnect the leader.
  3. Wait for a new leader to be elected.
  4. Rejoin the old leader.
  5. Validate that the new leader remains the leader.
  6. Remove the new leader and another process (now only one server in network).
  7. Validate there is no leader.
  8. Add back a single process.
  9. Validate that a new leader is elected.
  10. Rejoin the last remaining process.
  11. Validate that the leader remains the same.

## Part 2

In this section of the lab, you will add log replication. Your leader process
will now have its log appended to by the code in `Start` (`raft_core.go`).

* When the leader sends AppendEntries RPCs, it will now have to populate the
  arguments correctly when there are logs to send.

* Similarly the leader will now need to handle the RPC responses and update its
  `matchIndex` and `nextIndex` lists correctly in `sendAppendEntry`.

* Followers will need to only grant votes in the RequestVote RPC handler if the
  candidate's log is at least as ahead of the follower (section 5.4.1 of the
  paper, last two paragraphs).

* Followers will also need to implement rules for appending to their own logs,
  including how to handle conflict.

* The leader needs to implement `updateCommitIndex`, which implements the last
  step of the "Leaders" section under "Rules for All Servers."

* You need to implement `commit`, which is called periodically to apply
  commands to the state machine.

* Later in this section, you will need to implement the optimization described
  in section 5.3 of the paper and in the `raft_api.go` file.

  This optimization instructs followers to set the `NextIndex` field of the
  AppendEntries RPC response to be the _first index it stores for the
  conflicting term_.

  There is a three-part set of unit tests in `2PUnit` that illustrates the
  correct follower behavior:

  ```
  TestAppendEntriesConflictOneP2Unit
  TestAppendEntriesConflictTwoP2Unit
  TestAppendEntriesConflictThreeP2Unit
  ```

### Tips

* Start by implementing basic behavior. The integration tests for this part are
  increasingly more complex. It may be helpful to only run a test or two at a
  time as you implement more of the protocol. For example, the test
  `TestReconcileLongLogsPart2` requires the `NextIndex` optimization, but
  earlier tests do not (implementing the `nextIndex - 1` behavior described
  earlier in the paper is good enough).

* Take advantage of the unit tests! If the unit tests pass, you should have the
  handlers and RPC requests correct. If they don't, the integration tests are
  unlikely to pass.

* When you are done with this section, all of the part 2 integration tests
  should pass:

  ```
  go test raft/raft -run Part2 -test.v
  ```

  __Note:__ Some of these tests take some time! However, your implementation
  should pass the entire test suite in about a minute or less.

  You should also make sure that your tests for Part 1 continue to pass as well!

* This is by far the most challenging section of the lab. Once you have this
  right, Part 3 and part 4 should be less work. __Make sure you start this
  early and come to me if you are stuck!__

### Part 2 unit tests

There are a handful of unit tests for this part of the lab that will help you
ensure that your implementation has the correct behavior. They use `NextIndex`
in the `AppendEntries` response, however, so you will need to either modify them
or do that optimization.

You should also feel free to add more. Notice that I do not have unit tests for
`RequestVote`, whereas if you are having trouble with elections, you may want to
add some.

To run the unit tests, run the following:
```
go test raft/raft -run P2Unit -test.v
```

The test names are descriptive of their function; they have comments that
describe what they test, as well.
```
TestAppendEntriesOldTermP2Unit
TestAppendEntriesNewTermP2Unit
TestAppendEntriesEmptyLogP2Unit
TestAppendEntriesBasicP2Unit
TestAppendEntriesLongPrevIndexP2Unit
TestAppendEntriesLongerP2Unit
TestAppendEntriesConflictOneP2Unit
TestAppendEntriesConflictTwoP2Unit
TestAppendEntriesConflictThreeP2Unit
TestSendAppendEntriesNoOpP2Unit
TestSendAppendEntriesStopsLeadingP2Unit
TestSendAppendEntriesSuccessUpdateIndicesP2Unit
TestSendAppendEntriesFailureUpdateIndicesP2Unit
TestUpdateCommitIndexP2Unit
```

### Part 2 integration tests overview

To run the integration tests, run:
```
go test raft/raft -run Part2 -test.v
```

* `TestConsensusNoFailurePart2`: Validate that servers can run consensus in the
  absence of failures.

  With 5 servers, repeat 3 times:

  1. Validate that logs are initially empty at the index.
  2. Run one round of consensus and validate that the index is the expected
     initial index.
  3. Validate that all 5 servers agree on the index.

* `TestConsensusWithQuorumPart2`: Validate that consensus can be reached with a
  quorum of servers.

  With three servers:

  1. Run consensus once with all servers.
  2. Disconnect a follower.
  3. Run consensus with 2 servers a few times.
  4. Reconnect the follower.
  5. Validate that consensus can be reached with all servers.
  
  Note that the agreement watch thread does log validation, so the reconnected
  server's logs are validated in the last consensus run.

* `TestConsensusWithoutQuorumPart2`: Validate that consensus can't be run when
  there isn't a quorum of servers.
  
  1. Run an initial consensus.
  2. Disconnect 3/5 servers.
  3. Attempt to run consensus.
  4. Validate that it doesn't succeed.
  5. Reconnect servers.
  6. Validate that consensus can now be reached.

* `TestConcurrentConsensusPart2`: Validate that multiple commands can be
  committed by the same leader, in parallel. With 3 servers, the test tries 5
  times to commit many commands in parallel in the same term.

  1. Get the current leader.
  2. Start 5 threads that call Start() on the leader in parallel.
  3. Validate that all commands were committed in the same term.

* `TestPartitionReconnectPart2`: Validate that a paritioned leader abandons
  failed log entries. With three servers:

  1. Get one round of consensus.
  2. Disconnect the leader.
  3. Start rounds of consensus on the old, disconnected leader. These should
     later be discarded.
  4. Run a round with the new leader of the connected majority.
  5. Disconnect the new leader, and reconnect the old leader.
  6. Run one round of consensus.
  7. Reconnect all.
  8. Run one round of consensus.
  9. Validate that the committed entries is the correct number.
  
  The final commits should be the commits from (1), (4), (6), and (8).


* `TestReconcileLongLogsPart2`: Validate that a process with long tail of
  inconsistent log entries is able to catch up once it rejoins the system.  
  
  Without loss of generality number the servers 0-4.

  1. Get an initial agreement with all servers.
  2. Partition the leader, say 0,  and another process, say 1, by disconnecting
     2, 3, and 4. Send a lot of commands to 0 that can't be committed by only
     two processes.
  3. Disconnect 0 and 1.
  4. Rejoin 2-4, and allow them to commit a lot of successful commands. Now, 0
     and 1 have a long tail of uncommitted entries, while the current quorum
     have a long tail of committed entries.
  5. Disconnect 2 and submit a lot of new entries to 3 and 4. Now 3 and 4 have a
     long tail of uncommitted messages.
  6. Disconnect 3 and 4 and bring 0, 1, and 2 back. Send a lot of successful
     commands to this group. 0 and 1 will have to catch up to 2's successful
     entries.
  7. Bring all up. 3 and 4 have will have to catch up.
  8. Run one last round with all servers. At this point they all must have
     caught up and discarded uncommitted entries.


* `TestRpcEfficiencyPart2`: Validate that the solution is RPC-efficient; that
  is, it doesn't send unnecessarily many RPCs.

  With 3 servers:

  1. Validate that it doesn't require >~30 RPCs to elect an initial leader.
  2. Try to get a stable count of RPCs required to reach agreement a few times.
  3. With each try, get the count of RPCs sent at the start. Then, run consensus
     a few times.
  4. Wait for consensus to finish and validate the total RPC count. If the term
     moved on at any point, try again to get a stable count, since an election
     would skew the RPC count.
  5. If this passes, validate that RPCs in an idle steady-state are not too
     frequent.

## Part 3

This part of the lab ensures that your implementation behaves correctly when
processes can crash and recover.

Notice that `CreateRaft` reads the Raft server's state from disk when creating
it. This means that you have to persist the state whenever the state that must
be persisted is changed. These are the fields `currentTerm`, `votedFor`, and
`log[]` (from page 5 in the paper).

Specifically, you will need to write the functions that persist and recover
data:

* `persist` should use the `encoding/gob` library to encode the Raft state to a
  byte buffer and then write this buffer with `WritePersistentState`.

  ```
  buff := new(bytes.Buffer)
  e := gob.NewEncoder(buff)
  e.Encode(field_to_encode)
  data := buff.Bytes()
  r.persister.WritePersistentState(data)
  ```

* `recoverState` should use a corresponding decoder to decode the Raft state.

  ```
  buff := bytes.NewBuffer(data)
  d := gob.NewDecoder(buff)
  d.Decode(ptr_to_field)
  ```

* Finally, you must make sure that your implementation calls `persist` _whenever
  the state that must be persisted changes_.

### Tips:

* This part of the lab is fairly straightfoward, but it is easy to miss places
  where state changes. Make sure that you are thorough.

* When you are done, the tests for this section should pass. You should also
  make sure that all previous tests pass!

  ```
  go test raft/raft -run Part3 -test.v
  ```

  This should take about a minute.

### Part 3 test overview

* `TestBasicPersistencePart3`: Validates that crashing and restarting servers
  does not affect the system's ability to reach consensus with consistent logs.
  Tries different permutations of crashing servers.

  With three servers:

  1. Get agreement (log length 1).
  2. Crash all servers.
  3. Bring them up and get another agreement (log length 2).
  4. Crash the leader and bring it back up.
  5. Get agreement (log length 3).
  6. Crash the leader.
  7. Get agreement w/ 2 servers (log length 4).
  8. Bring old leader back up and wait for it to catch up (validating log
     indices).
  9. Crash a follower.
  10. Get agreement (log length 5).
  11. Bring follower back up.
  12. Get agreement (log length 6).

  Note that the log consistency checks that are part of the testConfig.agree
  function ensure that the crashed and recovered followers have consistent
  logs.

* `TestRecoveryWithLaggingFollowersPart3`: Validate that processes that lag
  catch up when an ahead follower recovers and rejoins.

  With 5 servers, repeat the following five times. Without loss of generality
  assume the servers are numbered 0-4.
  
  1. Get agreement with all, assume 0 is the leader in this round.
  2. Crash 1 and 2.
  3. Get agreement with 0, 3, and 4.
  4. Crash 0, 3, and 4.
  5. Recover 1 and 2.
  6. Wait for 1 and 2 to potentially establish a leader.
  7. Recover 3 (which is ahead of 1 and 2).
  8. Get agreement with 3, 1, and 2 (requiring 1 and 2 to catch up).
  9. Recover 0 and 4, which will have to catch up on the next round of
     consensus.
  10. After this has repeated 5 times, get one last agreement to ensure 0 and 4
      are caught up.

* `TestFigure8Part3`: Validate the scenarios described in Figure 8 of the Raft
  paper. Test specifically the difference between 8 (d) and 8 (e), where the
  leader is not able to replicate a log entry from its term before failing vs.
  being able to replicate a log entry from its term.

  With 5 servers, we will repeat the following many times:

  1. Each iteration calls Start() on a leader, if there is one.
  2. Crash it quickly with probability 90% (which may result in it not
     committing the command).
  3. Or crash it after a while with probability 10% (which most likely results
     in the command being committed).
  4. If the number of alive servers isn't enough to form a majority, start a new
     server.
  
  Note that if reliable is false, the test will be run with unreliable mode
  turned on, allowing for dropped RPCs and long reorderings.

## Part 4

If, at this point, your implementation has passed previous parts of the lab (and
is correct), it should pass this part as well. This part adds unreliable
networks to the mix: messages will be delayed and some will be lost.

You can see how the unreliable network is simulated in the `dispatch` function
of `rpc.go`, if desired (look at the `unreliable` flag).

The tests for this section should take a minute or less.

```
go test raft/raft -run Part4 -test.v
```

### Part 4 test overview

* `TestUnreliablePart4`: Validate that consensus proceeds with a faulty network.

  With 5 servers:
  
  1. Run consensus many times (250 times), with many threads running
     concurrently.
  2. Wait for all threads to finish.
  3. Do one more round for a consistency check.

* `TestUnreliableFigure8Part4`: This is the same as `TestFigure8Part3` except
  with the introduction of a faulty network.

### Before you turn this in

* __Make sure you are pushing a branch and creating a pull request. Do not push
  to the main branch!__

* Run `gofmt` on your code or have GoLand do this for you. Do not turn in code
  that is not indented correctly.

* I will give 1-2 points of extra credit for clean, well documented code. In
  particular:
  * If code is not being executed (_i.e._, is commented out), it should not be
    committed. 
  * `TODO` comments should be addressed, and therefore they should be removed
    (unless you still have work to do).
  * Comments should be at a high enough level that they are descriptive and
    explain how the code is working, _not explaining what the code is_ (you
    should assume everyone who reads your code understands the programming
    language).
  * Variable names should be descriptive and accurate, even if they are short.
    For example, using `mi` and `ni` for match/next index, respectively, is
    short, but still descriptive.
  * It is covered above, but make sure that you are using the correct amount of
    indentation. Also do not use excessive whitespace or no whitespace. Use
    blank lines to break up "paragraphs" of code within a function.

# Congratulations! You are done!

You've implemented an incredibly subtle distributed algorithm. Give yourself a
high five.

<img src="https://media.giphy.com/media/CW27AW0nlp5u0/giphy.gif"/>

If you're on campus this summer, come see me and I'll give you a high five too.
