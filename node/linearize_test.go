package node

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"testing"
	"time"

	"gospr/builder"
	"gospr/crdt"
	"gospr/parser"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// counterDSL: each node accumulates its own slot, slots merge per-node-id by max,
// the query sums every slot. The canonical convergent counter.
const counterDSL = `type T = vector rat0+
merge T = zip max
fn total v::T = reduce + 0 v
query T.Value = total
update T.Add k::rat0+ = local (+ k)
collection Counter = T
`

// counterDSLVariant is value-shape-identical to counterDSL but carries an extra
// (unused) function, so it parses+builds to a DIFFERENT plan fingerprint — used
// to prove a same-name/different-plan peer is not miscounted into a quorum.
const counterDSLVariant = `type T = vector rat0+
fn noop x::rat = x
merge T = zip max
fn total v::T = reduce + 0 v
query T.Value = total
update T.Add k::rat0+ = local (+ k)
collection Counter = T
`

// blackholePeer drops every message: a partitioned/unreachable peer that can
// never ack.
type blackholePeer struct{}

func (blackholePeer) Send(any) {}

const testSyncTimeout = 300 * time.Millisecond

func buildPlan(t *testing.T, dsl string) builder.BuiltPlan {
	t.Helper()
	plan, err := parser.Parse(dsl)
	require.NoError(t, err)
	built, err := builder.Build(plan)
	require.NoError(t, err)
	return built
}

// newSyncNodes builds nodes with gossip disabled (so only the sync protocol can
// move state) and a short sync timeout (so a failed quorum 503s quickly).
func newSyncNodes(ids ...string) []*Node {
	ns := make([]*Node, len(ids))
	for i, id := range ids {
		ns[i] = New(id, WithGossipInterval(time.Hour), WithSyncTimeout(testSyncTimeout))
	}
	return ns
}

func wireMesh(ns []*Node) {
	for _, a := range ns {
		for _, b := range ns {
			if a.id != b.id {
				a.AddPeer(b.id, NewChanPeer(b.Inbox(), b.Done()))
			}
		}
	}
}

func startAll(t *testing.T, ns []*Node) {
	t.Helper()
	for _, n := range ns {
		n.Start()
	}
	t.Cleanup(func() {
		for _, n := range ns {
			n.Stop()
		}
	})
}

func deployAll(t *testing.T, built builder.BuiltPlan, ns ...*Node) {
	t.Helper()
	for _, n := range ns {
		require.NoError(t, n.Initialize(built))
	}
}

// Test 1: a linearized write synchronously pushes the new slot to a quorum, so a
// peer's LOCAL read sees it immediately — gossip (disabled here) could not have.
func TestApplySync_syncWrite(t *testing.T) {
	built := buildPlan(t, counterDSL)
	ns := newSyncNodes("node1", "node2", "node3")
	wireMesh(ns)
	startAll(t, ns)
	deployAll(t, built, ns...)

	require.NoError(t, ns[0].ApplySync(context.Background(), "Counter", "Add", []any{"5"}, 1.0))

	v, err := ns[1].Query("Counter", "Value", nil)
	require.NoError(t, err)
	assert.Equal(t, "5", v, "node2 sees the write via the synchronous push")
}

// Test 2: a linearized read is two-phase. Gather pulls+merges a peer's slot (so
// the query returns it); write-back leaves a THIRD node holding the merged value.
func TestQuerySync_twoPhaseRead(t *testing.T) {
	built := buildPlan(t, counterDSL)
	ns := newSyncNodes("node1", "node2", "node3")
	wireMesh(ns)
	startAll(t, ns)
	deployAll(t, built, ns...)

	// Write locally on node2 ONLY (no sync), so node1 and node3 don't have it yet.
	require.NoError(t, ns[1].Apply("Counter", "Add", []any{"7"}))

	v, err := ns[0].QuerySync(context.Background(), "Counter", "Value", nil, 1.0)
	require.NoError(t, err)
	assert.Equal(t, "7", v, "gather pulled+merged node2's slot")

	// Write-back phase pushed the merged state to node3.
	v3, err := ns[2].Query("Counter", "Value", nil)
	require.NoError(t, err)
	assert.Equal(t, "7", v3, "write-back left node3 holding the merged value")
}

// Test 3: a quorum that can't be met returns ErrQuorumUnreached, and a cancelled
// context produces the same error.
func TestApplySync_quorumFailure(t *testing.T) {
	built := buildPlan(t, counterDSL)
	n1 := New("node1", WithGossipInterval(time.Hour), WithSyncTimeout(testSyncTimeout))
	n2 := New("node2", WithGossipInterval(time.Hour), WithSyncTimeout(testSyncTimeout))
	// node1 reaches node2 (real) but node3 is blackholed (partition) ⇒ at most 1 ack.
	n1.AddPeer("node2", NewChanPeer(n2.Inbox(), n2.Done()))
	n1.AddPeer("node3", blackholePeer{})
	n2.AddPeer("node1", NewChanPeer(n1.Inbox(), n1.Done()))
	n1.Start()
	n2.Start()
	t.Cleanup(func() { n1.Stop(); n2.Stop() })
	require.NoError(t, n1.Initialize(built))
	require.NoError(t, n2.Initialize(built))

	// holders = 1 (self) + 1 (node2) = 2; 2/3 >= 1.0 never holds.
	err := n1.ApplySync(context.Background(), "Counter", "Add", []any{"5"}, 1.0)
	require.ErrorIs(t, err, ErrQuorumUnreached)

	// Context-cancel variant: an already-cancelled ctx yields the same error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = n1.ApplySync(ctx, "Counter", "Add", []any{"5"}, 1.0)
	require.ErrorIs(t, err, ErrQuorumUnreached)
}

// Test 4: an un-deployed peer never acks (validity gated on Initialized + plan),
// so it is not miscounted; once deployed, the retry reaches quorum.
func TestApplySync_ackValidity_undeployed(t *testing.T) {
	built := buildPlan(t, counterDSL)
	ns := newSyncNodes("node1", "node2", "node3")
	wireMesh(ns)
	startAll(t, ns)
	// Deploy to node1 + node2 only; leave node3 Uninitialized.
	deployAll(t, built, ns[0], ns[1])

	err := ns[0].ApplySync(context.Background(), "Counter", "Add", []any{"5"}, 1.0)
	require.ErrorIs(t, err, ErrQuorumUnreached, "uninitialized node3 must not be counted")

	// Now deploy node3 and retry ⇒ both peers ack ⇒ success.
	require.NoError(t, ns[2].Initialize(built))
	require.NoError(t, ns[0].ApplySync(context.Background(), "Counter", "Add", []any{"5"}, 1.0))
}

// Test 5: a peer running a DIFFERENT plan (same collection name + value shape,
// different fingerprint) drops the push and is not miscounted.
func TestApplySync_fingerprintMismatch(t *testing.T) {
	planA := buildPlan(t, counterDSL)
	planB := buildPlan(t, counterDSLVariant)
	require.NotEqual(t, planA.Fingerprint, planB.Fingerprint, "variant must hash differently")

	ns := newSyncNodes("node1", "node2", "node3")
	wireMesh(ns)
	startAll(t, ns)
	deployAll(t, planA, ns[0], ns[1])
	require.NoError(t, ns[2].Initialize(planB)) // node3 runs the incompatible plan

	// node3's fingerprint differs ⇒ it drops the push ⇒ only node2 acks ⇒ 2/3 < 1.0.
	err := ns[0].ApplySync(context.Background(), "Counter", "Add", []any{"5"}, 1.0)
	require.ErrorIs(t, err, ErrQuorumUnreached)
}

// Test 6: when self alone satisfies the ratio, the op succeeds without
// broadcasting or waiting — even with every peer blackholed.
func TestApplySync_zeroAckShortCircuit(t *testing.T) {
	built := buildPlan(t, counterDSL)

	// (a) ratio=0 on a 3-node cluster, all peers blackholed.
	n1 := New("node1", WithGossipInterval(time.Hour), WithSyncTimeout(testSyncTimeout))
	n1.AddPeer("node2", blackholePeer{})
	n1.AddPeer("node3", blackholePeer{})
	n1.Start()
	t.Cleanup(n1.Stop)
	require.NoError(t, n1.Initialize(built))

	start := time.Now()
	require.NoError(t, n1.ApplySync(context.Background(), "Counter", "Add", []any{"5"}, 0.0))
	assert.Less(t, time.Since(start), testSyncTimeout, "ratio=0 returns without waiting")

	// (b) 2-node cluster, peer blackholed, sub-majority ratio 0.4 ⇒ holders 1/2 =
	// 0.5 > 0.4, so self alone satisfies it without waiting.
	m1 := New("node1", WithGossipInterval(time.Hour), WithSyncTimeout(testSyncTimeout))
	m1.AddPeer("node2", blackholePeer{})
	m1.Start()
	t.Cleanup(m1.Stop)
	require.NoError(t, m1.Initialize(built))

	start = time.Now()
	require.NoError(t, m1.ApplySync(context.Background(), "Counter", "Add", []any{"5"}, 0.4))
	assert.Less(t, time.Since(start), testSyncTimeout, "self alone satisfies a sub-majority ratio")
}

// Test 6b: the strict-majority rule regression guard. In a 2-node cluster, ratio
// 0.5 is a *strict* majority (holders/N > 0.5), so self alone is NOT enough — the
// (blackholed) peer must ack, so an isolated coordinator 503s. Under the old `>=`
// rule 0.5 short-circuited on self, silently breaking read/write overlap for even
// N; this test fails if that behavior comes back.
func TestApplySync_majorityRequiresPeer(t *testing.T) {
	built := buildPlan(t, counterDSL)

	m1 := New("node1", WithGossipInterval(time.Hour), WithSyncTimeout(testSyncTimeout))
	m1.AddPeer("node2", blackholePeer{})
	m1.Start()
	t.Cleanup(m1.Stop)
	require.NoError(t, m1.Initialize(built))

	err := m1.ApplySync(context.Background(), "Counter", "Add", []any{"5"}, 0.5)
	require.ErrorIs(t, err, ErrQuorumUnreached, "0.5 in a 2-node cluster is a strict majority — self alone must not satisfy it")
}

// Test 7: a linearized read validates BEFORE any quorum work — a bad query is a
// fast validation error, never a quorum timeout; a valid query under the same
// (blackholed) setup instead 503s because it reaches the gather phase.
func TestQuerySync_preflight(t *testing.T) {
	built := buildPlan(t, counterDSL)
	n1 := New("node1", WithGossipInterval(time.Hour), WithSyncTimeout(testSyncTimeout))
	n1.AddPeer("node2", blackholePeer{})
	n1.AddPeer("node3", blackholePeer{})
	n1.Start()
	t.Cleanup(n1.Stop)
	require.NoError(t, n1.Initialize(built))

	start := time.Now()
	_, err := n1.QuerySync(context.Background(), "Counter", "NoSuchQuery", nil, 1.0)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrQuorumUnreached, "validation fails before quorum work")
	assert.Less(t, time.Since(start), testSyncTimeout, "bad query fails fast")

	// A valid query reaches the gather phase and times out (no peer can ack).
	_, err = n1.QuerySync(context.Background(), "Counter", "Value", nil, 1.0)
	require.ErrorIs(t, err, ErrQuorumUnreached)
}

// Test 8: ChanPeer.Send to a stopped peer returns promptly (via the done guard)
// instead of leaking a goroutine blocked forever on a full inbox.
func TestChanPeer_sendBoundByDone(t *testing.T) {
	n2 := New("node2")
	peer := NewChanPeer(n2.Inbox(), n2.Done())
	// Fill the inbox so the inbox-send case would block.
	for i := 0; i < cap(n2.Inbox()); i++ {
		n2.Inbox() <- struct{}{}
	}
	n2.Stop() // close done

	returned := make(chan struct{})
	go func() {
		peer.Send("one more") // inbox full ⇒ must take the done case
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("Send to a stopped, full peer blocked instead of returning")
	}
}

// Test 9: every sync DTO round-trips through both gob and JSON (no unexported
// field dropped, no any/*big.Rat that fails to encode), and reqIDs are monotonic.
func TestSyncMessages_codecRoundTrip(t *testing.T) {
	snap := crdt.WireSnapshot{Slots: map[string]crdt.SlotWire{"n1": {Num: "1/2"}, "n2": {Num: "3"}}}
	push := SyncPushMsg{FromID: "node1", ReqID: "node1-7", Collection: "C", Fingerprint: "abc", Snapshot: snap}
	pull := SyncPullMsg{FromID: "node1", ReqID: "node1-8", Collection: "C", Fingerprint: "abc"}
	ack := SyncAckMsg{FromID: "node2", ReqID: "node1-7", Snapshot: snap}

	assertGobJSON(t, push, &SyncPushMsg{})
	assertGobJSON(t, pull, &SyncPullMsg{})
	assertGobJSON(t, ack, &SyncAckMsg{})

	n := New("node1")
	assert.NotEqual(t, n.nextReqID(), n.nextReqID(), "reqIDs are monotonic/unique")
}

// assertGobJSON gob- and json-round-trips orig into a fresh out (a pointer) and
// asserts the decoded value equals orig.
func assertGobJSON[T any](t *testing.T, orig T, out *T) {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, gob.NewEncoder(&buf).Encode(orig))
	require.NoError(t, gob.NewDecoder(&buf).Decode(out))
	assert.Equal(t, orig, *out, "gob round-trip")

	data, err := json.Marshal(orig)
	require.NoError(t, err)
	var jout T
	require.NoError(t, json.Unmarshal(data, &jout))
	assert.Equal(t, orig, jout, "json round-trip")
}

// Test 9a: quorum counts DISTINCT acking peers, and a duplicate flood is
// suppressed at the demux so it neither inflates the count nor crowds a valid
// distinct ack out of the buffer.
func TestSyncAck_distinctPeerCounting(t *testing.T) {
	t.Run("no inflation from duplicates/unknowns", func(t *testing.T) {
		n := New("node1", WithSyncTimeout(testSyncTimeout))
		n.AddPeer("node2", blackholePeer{})
		n.AddPeer("node3", blackholePeer{}) // present in peerByID but never acks
		rid := n.nextReqID()
		pr, cancel := n.register(rid)
		defer cancel()

		n.handleSyncAck(SyncAckMsg{FromID: "node2", ReqID: rid})
		n.handleSyncAck(SyncAckMsg{FromID: "node2", ReqID: rid}) // duplicate ⇒ dropped
		n.handleSyncAck(SyncAckMsg{FromID: "node9", ReqID: rid}) // not a peer ⇒ dropped

		// Only node2 is a distinct holder ⇒ holders=2 ⇒ 2/3 ≥ 1.0 false ⇒ times out.
		err := n.collectAcks(context.Background(), pr, 1.0, nil)
		require.ErrorIs(t, err, ErrQuorumUnreached)
	})

	t.Run("a pull ack whose merge fails does not count", func(t *testing.T) {
		built := buildPlan(t, counterDSL)
		n := New("node1", WithSyncTimeout(testSyncTimeout))
		require.NoError(t, n.Initialize(built))
		n.AddPeer("node2", blackholePeer{})
		n.AddPeer("node3", blackholePeer{})
		rid := n.nextReqID()
		pr, cancel := n.register(rid)
		defer cancel()

		// A known peer returns a corrupt wire snapshot (invalid rational); another
		// returns a valid one.
		n.handleSyncAck(SyncAckMsg{FromID: "node2", ReqID: rid,
			Snapshot: crdt.WireSnapshot{Slots: map[string]crdt.SlotWire{"node2": {Num: "not-a-rational"}}}})
		n.handleSyncAck(SyncAckMsg{FromID: "node3", ReqID: rid,
			Snapshot: crdt.WireSnapshot{Slots: map[string]crdt.SlotWire{"node3": {Num: "4"}}}})

		onAck := func(ack SyncAckMsg) error { return n.mergeCollection("Counter", ack.Snapshot) }
		// ratio 1.0 needs both peers as holders, but node2's merge fails ⇒ only 1
		// distinct holder counts ⇒ quorum unreached (a corrupt response can't
		// satisfy a read without contributing state).
		require.ErrorIs(t, n.collectAcks(context.Background(), pr, 1.0, onAck), ErrQuorumUnreached)

		// node3's valid slot WAS incorporated.
		v, err := n.Query("Counter", "Value", nil)
		require.NoError(t, err)
		assert.Equal(t, "4", v)
	})

	t.Run("no crowding-out of a valid distinct ack", func(t *testing.T) {
		n := New("node1", WithSyncTimeout(testSyncTimeout))
		n.AddPeer("node2", blackholePeer{})
		n.AddPeer("node3", blackholePeer{})
		rid := n.nextReqID()
		pr, cancel := n.register(rid)
		defer cancel()

		for i := 0; i < 10; i++ { // burst of duplicates from node2
			n.handleSyncAck(SyncAckMsg{FromID: "node2", ReqID: rid})
		}
		n.handleSyncAck(SyncAckMsg{FromID: "node3", ReqID: rid}) // one valid distinct ack

		// node2 + node3 = 2 distinct ⇒ holders=3 ⇒ 3/3 ≥ 1.0 ⇒ success.
		require.NoError(t, n.collectAcks(context.Background(), pr, 1.0, nil))
	})
}
