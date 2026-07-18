package node

import (
	"context"
	"errors"
	"fmt"
	"gospr/builder"
	"gospr/crdt"
	"gospr/numtype"
	"gospr/parser"
	"log"
	"math/big"
	"math/rand"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

type State int

const (
	Uninitialized State = iota
	Initialized
)

type gossipMsg struct {
	senderID string
	snapshot map[string]map[string]any // collection -> canonical document -> CRDT snapshot
}

type deployMsg struct {
	built builder.BuiltPlan
}

// The three sync-protocol messages are the coordination layer for linearizable
// reads/writes. Unlike gossipMsg/deployMsg (in-process only), they are exported
// plain-data DTOs with exported fields and a concrete transport-safe snapshot
// shape (crdt.WireSnapshot, not any), so a real network codec needs no field
// surgery later — nodes will eventually run on separate machines. Acks route
// back to the coordinator via FromID, and ReqID (nodeID + monotonic counter)
// correlates them to a pending request without collisions across timed-out ops.

// SyncPushMsg asks a peer to merge the coordinator's snapshot of one collection.
type SyncPushMsg struct {
	FromID      string
	ReqID       string
	Collection  string
	Document    string
	Fingerprint string
	Snapshot    crdt.WireSnapshot
}

// SyncPullMsg asks a peer to send back its snapshot of one collection.
type SyncPullMsg struct {
	FromID      string
	ReqID       string
	Collection  string
	Document    string
	Fingerprint string
}

// SyncAckMsg is a peer's reply to a push or pull. For a push-ack Snapshot is the
// zero value (nil Slots); for a pull-response it carries the peer's snapshot.
type SyncAckMsg struct {
	FromID   string
	ReqID    string
	Document string
	Snapshot crdt.WireSnapshot
}

// Peer is the send-side of a node link. The default implementation (ChanPeer)
// delivers straight to a peer's inbox channel; alternative implementations can
// intercept, delay, or drop messages (e.g. the sandbox) without the node code
// knowing the difference. This is the single interception point for both gossip
// and deploy propagation.
type Peer interface{ Send(msg any) }

// ChanPeer is the production Peer: a send to the target inbox, bounded by the
// target's done channel so a Send to a stopped peer returns promptly instead of
// blocking forever on a full inbox (which would leak the broadcasting goroutine).
// A live-but-full inbox still blocks — correct backpressure that resolves when
// the peer drains.
type ChanPeer struct {
	inbox chan any
	done  <-chan struct{}
}

func NewChanPeer(inbox chan any, done <-chan struct{}) ChanPeer {
	return ChanPeer{inbox: inbox, done: done}
}

func (p ChanPeer) Send(msg any) {
	select {
	case p.inbox <- msg:
	case <-p.done:
	}
}

// MessageKind classifies an inter-node message without exposing the unexported
// message types, so non-node packages can label traffic.
func MessageKind(msg any) string {
	switch msg.(type) {
	case gossipMsg:
		return "gossip"
	case deployMsg:
		return "deploy"
	case SyncPushMsg:
		return "sync-push"
	case SyncPullMsg:
		return "sync-pull"
	case SyncAckMsg:
		return "sync-ack"
	default:
		return "unknown"
	}
}

// ErrQuorumUnreached is returned by a synchronous op when the sync-ratio is not
// met before the per-phase timeout or request-context cancellation. The HTTP
// layers map it to 503 via errors.Is.
var ErrQuorumUnreached = errors.New("quorum not reached")

type Node struct {
	id             string
	state          State
	fingerprint    string // of the deployed plan; set in Initialize, gates ack validity
	inbox          chan any
	peers          []Peer          // for gossip's random pick
	peerByID       map[string]Peer // for routing a sync ack back to its coordinator
	collections    map[string]*colState
	gossipInterval time.Duration
	syncTimeout    time.Duration
	quit           chan struct{}
	stopOnce       sync.Once
	mu             sync.RWMutex

	reqSeq    uint64                 // monotonic counter for collision-resistant ReqIDs
	pendingMu sync.Mutex             // guards pending
	pending   map[string]*pendingReq // in-flight sync requests keyed by ReqID
}

// pendingReq is the coordinator-side state for one in-flight sync phase. seen is
// the set of distinct peer IDs that have acked; it is touched ONLY in the single
// message-loop goroutine (the demux), so it needs no extra lock. ch carries
// already-validated, already-distinct acks to the waiting collectAcks.
type pendingReq struct {
	ch       chan SyncAckMsg
	seen     map[string]struct{}
	document string
}

// colState is one deployed collection family. The node mutex guards docs.
// Singleton collections reserve the empty document key and create it eagerly;
// keyed collections start sparse.
type colState struct {
	spec builder.CollectionSpec
	key  *parser.ParamSpec
	docs map[string]crdt.CRDT
}

type documentRef struct {
	collection string
	document   string // canonical; empty only for singleton collections
}

var decimalDocumentKey = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]+)?$`)

// Option configures a Node at construction.
type Option func(*Node)

// WithGossipInterval overrides the default 2s gossip ticker; tests use a short
// interval so propagation converges quickly.
func WithGossipInterval(d time.Duration) Option {
	return func(n *Node) { n.gossipInterval = d }
}

// WithSyncTimeout overrides the default per-phase deadline for a synchronous op.
// It must exceed a full request+ack round trip; tests use a short value so a
// failed quorum 503s quickly.
func WithSyncTimeout(d time.Duration) Option {
	return func(n *Node) { n.syncTimeout = d }
}

func New(id string, opts ...Option) *Node {
	n := &Node{
		id:             id,
		inbox:          make(chan any, 64),
		peerByID:       make(map[string]Peer),
		collections:    make(map[string]*colState),
		gossipInterval: 2 * time.Second,
		syncTimeout:    5 * time.Second,
		quit:           make(chan struct{}),
		pending:        make(map[string]*pendingReq),
	}
	for _, opt := range opts {
		opt(n)
	}
	return n
}

func (n *Node) ID() string { return n.id }

func (n *Node) Inbox() chan any { return n.inbox }

// Done exposes the node's lifetime channel (closed on Stop) so a ChanPeer's Send
// can abort promptly when the target node is torn down.
func (n *Node) Done() <-chan struct{} { return n.quit }

// AddPeer registers a peer addressable both for random-pick gossip (peers) and
// for routing a sync ack back to it by ID (peerByID).
func (n *Node) AddPeer(id string, p Peer) {
	n.peers = append(n.peers, p)
	n.peerByID[id] = p
}

func (n *Node) Start() {
	go n.runMessageLoop()
	go n.runGossip()
}

// Stop gracefully halts the gossip and message-loop goroutines. It is idempotent
// and prod-neutral: `server local` never calls it (behavior identical to before);
// the sandbox calls it on Reset to tear a cluster down without leaking goroutines.
func (n *Node) Stop() {
	n.stopOnce.Do(func() { close(n.quit) })
}

// Initialized reports whether a plan has been deployed to this node.
func (n *Node) Initialized() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.state == Initialized
}

func (n *Node) Initialize(plan builder.BuiltPlan) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.state == Initialized {
		return nil
	}
	for _, bc := range plan.Collections {
		cs := &colState{spec: bc.Spec, key: bc.Key, docs: make(map[string]crdt.CRDT)}
		if bc.Key == nil {
			cs.docs[""] = bc.Spec.New(n.id)
		}
		n.collections[bc.Name] = cs
	}
	n.fingerprint = plan.Fingerprint
	n.state = Initialized
	log.Printf("[%s] initialized with %d collection(s)", n.id, len(n.collections))
	return nil
}

func (n *Node) PropagatePlan(plan builder.BuiltPlan) {
	msg := deployMsg{built: plan}
	for _, peer := range n.peers {
		peer.Send(msg)
	}
}

// resolveDocument is the sole collection/document boundary. It asserts the
// singleton-vs-keyed shape and canonicalizes a keyed URL value before any
// storage, gossip, or sync lookup.
func (n *Node) resolveDocument(collection, raw string) (documentRef, error) {
	n.mu.RLock()
	cs, ok := n.collections[collection]
	n.mu.RUnlock()
	if !ok {
		return documentRef{}, fmt.Errorf("collection %q not found", collection)
	}
	if cs.key == nil {
		if raw != "" {
			return documentRef{}, fmt.Errorf("collection %q is a singleton and does not accept a document key", collection)
		}
		return documentRef{collection: collection}, nil
	}
	if raw == "" {
		return documentRef{}, fmt.Errorf("collection %q requires a document key (use the document route)", collection)
	}
	key, err := normalizeDocumentKey(*cs.key, raw)
	if err != nil {
		return documentRef{}, fmt.Errorf("collection %q document %s: %w", collection, cs.key.Name, err)
	}
	return documentRef{collection: collection, document: key}, nil
}

func normalizeDocumentKey(spec parser.ParamSpec, raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("must not be empty")
	}
	if strings.Contains(raw, "/") {
		return "", fmt.Errorf("must not contain '/'")
	}
	if !utf8.ValidString(raw) {
		return "", fmt.Errorf("must be valid UTF-8")
	}
	if spec.Type == "string" {
		return raw, nil
	}
	if !decimalDocumentKey.MatchString(raw) {
		return "", fmt.Errorf("invalid %s key %q (want a decimal such as 0, -2, or 0.5)", spec.Type, raw)
	}
	q, ok := new(big.Rat).SetString(raw)
	if !ok {
		return "", fmt.Errorf("invalid number %q", raw)
	}
	nt, ok := numtype.Parse(spec.Type)
	if !ok {
		return "", fmt.Errorf("unknown key type %q", spec.Type)
	}
	if !numtype.Allows(nt, q) {
		return "", fmt.Errorf("value %s is not a valid %s", canonicalDecimal(raw), spec.Type)
	}
	return canonicalDecimal(raw), nil
}

// canonicalDecimal normalizes the accepted finite-decimal spelling without
// converting it to RatString (which could introduce a slash such as 1/2).
func canonicalDecimal(raw string) string {
	neg := strings.HasPrefix(raw, "-")
	if neg {
		raw = raw[1:]
	}
	whole, frac, hasFrac := strings.Cut(raw, ".")
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	if hasFrac {
		frac = strings.TrimRight(frac, "0")
	}
	out := whole
	if frac != "" {
		out += "." + frac
	}
	if neg && out != "0" {
		out = "-" + out
	}
	return out
}

// document returns a stored CRDT, or an unregistered zero-state CRDT on miss.
// ref has already passed resolveDocument, so its collection is deployed.
func (n *Node) document(ref documentRef) (crdt.CRDT, bool) {
	n.mu.RLock()
	cs := n.collections[ref.collection]
	c, ok := cs.docs[ref.document]
	n.mu.RUnlock()
	if ok {
		return c, true
	}
	return cs.spec.New(n.id), false
}

func (n *Node) apply(ref documentRef, action string, payload []any) error {
	n.mu.RLock()
	cs := n.collections[ref.collection]
	c, ok := cs.docs[ref.document]
	n.mu.RUnlock()
	if ok {
		return c.Apply(action, payload)
	}

	// An update cannot safely be applied to a temporary document and later
	// merged into a concurrent winner: two local Adds would collapse under max.
	// Serialize only the first touch, and publish the document only on success.
	n.mu.Lock()
	defer n.mu.Unlock()
	if c, ok = cs.docs[ref.document]; ok {
		return c.Apply(action, payload)
	}
	c = cs.spec.New(n.id)
	if err := c.Apply(action, payload); err != nil {
		return err
	}
	cs.docs[ref.document] = c
	return nil
}

func (n *Node) query(ref documentRef, queryName string, params []any) (any, error) {
	c, _ := n.document(ref)
	return c.Query(queryName, params)
}

func (n *Node) Apply(collection, action string, payload []any) error {
	ref, err := n.resolveDocument(collection, "")
	if err != nil {
		return err
	}
	return n.apply(ref, action, payload)
}

func (n *Node) ApplyDocument(collection, document, action string, payload []any) error {
	ref, err := n.resolveDocument(collection, document)
	if err != nil {
		return err
	}
	return n.apply(ref, action, payload)
}

func (n *Node) Query(collection, queryName string, params []any) (any, error) {
	ref, err := n.resolveDocument(collection, "")
	if err != nil {
		return nil, err
	}
	return n.query(ref, queryName, params)
}

func (n *Node) QueryDocument(collection, document, queryName string, params []any) (any, error) {
	ref, err := n.resolveDocument(collection, document)
	if err != nil {
		return nil, err
	}
	return n.query(ref, queryName, params)
}

func (n *Node) Snapshot() map[string]map[string]any {
	n.mu.RLock()
	defer n.mu.RUnlock()
	snap := make(map[string]map[string]any, len(n.collections))
	for name, cs := range n.collections {
		docs := make(map[string]any, len(cs.docs))
		for document, c := range cs.docs {
			docs[document] = c.Snapshot()
		}
		snap[name] = docs
	}
	return snap
}

// SnapshotWireAll returns collection -> canonical document -> transport-safe
// WireSnapshot. Singleton collections use the reserved empty document key.
func (n *Node) SnapshotWireAll() map[string]map[string]crdt.WireSnapshot {
	n.mu.RLock()
	defer n.mu.RUnlock()
	out := make(map[string]map[string]crdt.WireSnapshot, len(n.collections))
	for name, cs := range n.collections {
		docs := make(map[string]crdt.WireSnapshot, len(cs.docs))
		for document, c := range cs.docs {
			docs[document] = c.SnapshotWire()
		}
		out[name] = docs
	}
	return out
}

func (n *Node) MergeSnapshot(snap map[string]map[string]any) {
	for name, docs := range snap {
		for document, s := range docs {
			if snapshotEmpty(s) {
				continue
			}
			ref, err := n.resolveDocument(name, document)
			if err != nil {
				continue
			}
			if err := n.mergeSnapshot(ref, s); err != nil {
				log.Printf("[%s] merge error for %s/%s: %v", n.id, name, document, err)
			}
		}
	}
}

func snapshotEmpty(s any) bool {
	v := reflect.ValueOf(s)
	return v.IsValid() && v.Kind() == reflect.Map && v.Len() == 0
}

func (n *Node) mergeSnapshot(ref documentRef, snap any) error {
	n.mu.RLock()
	cs := n.collections[ref.collection]
	c, ok := cs.docs[ref.document]
	n.mu.RUnlock()
	if ok {
		return c.Merge(snap)
	}

	temp := cs.spec.New(n.id)
	if err := temp.Merge(snap); err != nil {
		return err
	}
	n.mu.Lock()
	winner, exists := cs.docs[ref.document]
	if !exists {
		cs.docs[ref.document] = temp
		n.mu.Unlock()
		return nil
	}
	n.mu.Unlock()
	return winner.Merge(temp.Snapshot())
}

func (n *Node) runGossip() {
	ticker := time.NewTicker(n.gossipInterval)
	defer ticker.Stop()
	for {
		select {
		case <-n.quit:
			return
		case <-ticker.C:
		}
		n.mu.RLock()
		ready := n.state == Initialized
		n.mu.RUnlock()
		if !ready || len(n.peers) == 0 {
			continue
		}
		snap := n.Snapshot()
		peer := n.peers[rand.Intn(len(n.peers))]
		peer.Send(gossipMsg{senderID: n.id, snapshot: snap})
	}
}

func (n *Node) runMessageLoop() {
	for {
		var msg any
		select {
		case <-n.quit:
			return
		case msg = <-n.inbox:
		}
		switch m := msg.(type) {
		case gossipMsg:
			n.mu.RLock()
			ready := n.state == Initialized
			n.mu.RUnlock()
			if ready {
				n.MergeSnapshot(m.snapshot)
			}
		case deployMsg:
			if err := n.Initialize(m.built); err != nil {
				log.Printf("[%s] deploy error: %v", n.id, err)
			}
		case SyncPushMsg:
			n.handleSyncPush(m)
		case SyncPullMsg:
			n.handleSyncPull(m)
		case SyncAckMsg:
			n.handleSyncAck(m)
		}
	}
}

// handleSyncPush merges the coordinator's snapshot and acks — but only if this
// node runs the same deployed plan (fingerprint match) and actually holds the
// target collection with a successful merge. A mismatched/non-holding peer drops
// the request silently, so it can never be counted into a false quorum.
func (n *Node) handleSyncPush(m SyncPushMsg) {
	if !n.fingerprintMatches(m.Fingerprint) {
		return
	}
	ref, err := n.resolveDocument(m.Collection, m.Document)
	if err != nil || ref.document != m.Document {
		return
	}
	if err := n.mergeCollection(ref, m.Snapshot); err != nil {
		return
	}
	n.routeAck(m.FromID, SyncAckMsg{FromID: n.id, ReqID: m.ReqID, Document: ref.document})
}

// handleSyncPull replies with this node's snapshot of the target collection,
// gated identically to handleSyncPush.
func (n *Node) handleSyncPull(m SyncPullMsg) {
	if !n.fingerprintMatches(m.Fingerprint) {
		return
	}
	ref, err := n.resolveDocument(m.Collection, m.Document)
	if err != nil || ref.document != m.Document {
		return
	}
	snap := n.snapshotCollection(ref)
	n.routeAck(m.FromID, SyncAckMsg{FromID: n.id, ReqID: m.ReqID, Document: ref.document, Snapshot: snap})
}

// handleSyncAck demuxes a peer's ack into the pending request. Validation and
// duplicate suppression happen HERE, before enqueueing: an ack from an unknown
// peer or a repeat from an already-seen peer is dropped, so only first-time
// known-peer acks consume the channel buffer. seen is mutated only in this
// single message-loop goroutine, so it needs no lock.
func (n *Node) handleSyncAck(m SyncAckMsg) {
	n.pendingMu.Lock()
	pr, ok := n.pending[m.ReqID]
	n.pendingMu.Unlock()
	if !ok {
		return // late ack for a timed-out/cancelled op, or unknown ReqID
	}
	if _, isPeer := n.peerByID[m.FromID]; !isPeer {
		return // unknown / non-peer sender
	}
	if _, dup := pr.seen[m.FromID]; dup {
		return // duplicate/retried delivery — already counted
	}
	if m.Document != pr.document {
		return // valid peer, wrong document scope
	}
	pr.seen[m.FromID] = struct{}{}
	select {
	case pr.ch <- m: // buffered to len(peers); first-time distinct acks can't overflow it
	default:
	}
}

// routeAck sends an ack back to the coordinator without blocking this node's
// message loop on a back-pressured coordinator inbox.
func (n *Node) routeAck(toID string, ack SyncAckMsg) {
	if p, ok := n.peerByID[toID]; ok {
		go p.Send(ack)
	}
}

func (n *Node) fingerprintMatches(fp string) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.state == Initialized && n.fingerprint == fp
}

// ---- pending-request registry --------------------------------------------

// nextReqID mints a collision-resistant request ID: the node's (cluster-unique)
// ID plus a per-node monotonic counter. A delayed ack from a timed-out op can
// never match a later request — the counter never repeats and the prefix keeps
// two coordinators disjoint.
func (n *Node) nextReqID() string {
	return fmt.Sprintf("%s-%d", n.id, atomic.AddUint64(&n.reqSeq, 1))
}

// register creates pending state for one in-flight sync phase. ch is buffered to
// len(peers) so every distinct peer can ack without blocking the message loop;
// cancel removes the entry (a later ack then finds nothing and is dropped).
func (n *Node) register(reqID, document string) (*pendingReq, func()) {
	pr := &pendingReq{
		ch:       make(chan SyncAckMsg, len(n.peers)),
		seen:     make(map[string]struct{}),
		document: document,
	}
	n.pendingMu.Lock()
	n.pending[reqID] = pr
	n.pendingMu.Unlock()
	return pr, func() {
		n.pendingMu.Lock()
		delete(n.pending, reqID)
		n.pendingMu.Unlock()
	}
}

// ---- collection-scoped helpers -------------------------------------------

func (n *Node) mergeCollection(ref documentRef, snap crdt.WireSnapshot) error {
	n.mu.RLock()
	cs := n.collections[ref.collection]
	c, ok := cs.docs[ref.document]
	n.mu.RUnlock()
	if !ok && len(snap.Slots) == 0 {
		return nil // a globally untouched document still counts as a successful merge
	}
	if ok {
		return c.MergeWire(snap)
	}

	temp := cs.spec.New(n.id)
	if err := temp.MergeWire(snap); err != nil {
		return err
	}
	n.mu.Lock()
	winner, exists := cs.docs[ref.document]
	if !exists {
		cs.docs[ref.document] = temp
		n.mu.Unlock()
		return nil
	}
	n.mu.Unlock()
	return winner.MergeWire(temp.SnapshotWire())
}

func (n *Node) snapshotCollection(ref documentRef) crdt.WireSnapshot {
	n.mu.RLock()
	cs := n.collections[ref.collection]
	c, ok := cs.docs[ref.document]
	n.mu.RUnlock()
	if !ok {
		return crdt.WireSnapshot{Slots: map[string]crdt.SlotWire{}}
	}
	return c.SnapshotWire()
}

// ValidateQuery is the non-evaluating preflight for a synchronous read: it checks
// the collection and query exist and the params bind, without evaluating the
// query body (which is state-dependent).
func (n *Node) ValidateQuery(collection, query string, params []any) error {
	ref, err := n.resolveDocument(collection, "")
	if err != nil {
		return err
	}
	c, _ := n.document(ref)
	return c.ValidateQuery(query, params)
}

func (n *Node) ValidateDocumentQuery(collection, document, query string, params []any) error {
	ref, err := n.resolveDocument(collection, document)
	if err != nil {
		return err
	}
	c, _ := n.document(ref)
	return c.ValidateQuery(query, params)
}

// ---- linearizable coordinator --------------------------------------------

// clusterSize is N = peers + self.
func (n *Node) clusterSize() int { return len(n.peers) + 1 }

// AllRatio is the sync-ratio marker meaning "every node". On the wire it is
// spelled `all`; numeric ratios are restricted to [0,1), so this value is only
// ever produced by the `all` keyword, never by a numeric parse.
const AllRatio = 1.0

// reached reports whether the sync predicate holds given `distinct` acking peers.
// The coordinator counts itself as a holder, so holders = 1 + distinct. An
// explicit ratio r requires a strict majority-style overlap: quorum = the
// smallest holder count with holders/N > r, so r=0.5 is a true majority
// (floor(N/2)+1) — the linearizable knee. r=0 is satisfied by self alone; the
// AllRatio sentinel (>= 1) requires every node.
func (n *Node) reached(distinct int, ratio float64) bool {
	holders := 1 + distinct
	if ratio >= AllRatio {
		return holders >= n.clusterSize()
	}
	return float64(holders)/float64(n.clusterSize()) > ratio
}

// ApplySync applies an update locally, then synchronously pushes the new state to
// a quorum fraction of peers before returning. A 503 (ErrQuorumUnreached) is
// indeterminate: the local slot is already mutated (CRDT merges don't roll back)
// and may still converge via gossip — retrying a non-idempotent Add can
// double-count.
func (n *Node) ApplySync(ctx context.Context, collection, action string, payload []any, ratio float64) error {
	ref, err := n.resolveDocument(collection, "")
	if err != nil {
		return err
	}
	return n.applySync(ctx, ref, action, payload, ratio)
}

func (n *Node) ApplyDocumentSync(ctx context.Context, collection, document, action string, payload []any, ratio float64) error {
	ref, err := n.resolveDocument(collection, document)
	if err != nil {
		return err
	}
	return n.applySync(ctx, ref, action, payload, ratio)
}

func (n *Node) applySync(ctx context.Context, ref documentRef, action string, payload []any, ratio float64) error {
	if err := n.apply(ref, action, payload); err != nil {
		return err
	}
	return n.pushQuorum(ctx, ref, ratio)
}

// QuerySync performs an ABD-style two-phase read: a non-evaluating preflight (so
// a bad query is a fast 400, never a 503 timeout), then gather (pull+merge from a
// quorum), then write-back (push the merged state to a quorum), then the
// authoritative local query. At ratio >= 0.5 (or `all`) the read and write
// quorums overlap, giving linearizability; a weaker ratio only guarantees
// synchronous replication to that fraction.
func (n *Node) QuerySync(ctx context.Context, collection, query string, params []any, ratio float64) (any, error) {
	ref, err := n.resolveDocument(collection, "")
	if err != nil {
		return nil, err
	}
	return n.querySync(ctx, ref, query, params, ratio)
}

func (n *Node) QueryDocumentSync(ctx context.Context, collection, document, query string, params []any, ratio float64) (any, error) {
	ref, err := n.resolveDocument(collection, document)
	if err != nil {
		return nil, err
	}
	return n.querySync(ctx, ref, query, params, ratio)
}

func (n *Node) querySync(ctx context.Context, ref documentRef, query string, params []any, ratio float64) (any, error) {
	c, _ := n.document(ref)
	if err := c.ValidateQuery(query, params); err != nil {
		return nil, err
	}
	if err := n.pullQuorum(ctx, ref, ratio); err != nil {
		return nil, err
	}
	if err := n.pushQuorum(ctx, ref, ratio); err != nil {
		return nil, err
	}
	return n.query(ref, query, params)
}

// pushQuorum broadcasts this node's snapshot of the collection and waits until a
// quorum fraction of peers ack. Used by writes and the read write-back phase.
func (n *Node) pushQuorum(ctx context.Context, ref documentRef, ratio float64) error {
	if n.reached(0, ratio) {
		return nil // self alone satisfies the ratio (e.g. ratio=0 or single-node)
	}
	snap := n.snapshotCollection(ref)
	rid := n.nextReqID()
	pr, cancel := n.register(rid, ref.document)
	defer cancel()
	n.broadcast(SyncPushMsg{
		FromID:      n.id,
		ReqID:       rid,
		Collection:  ref.collection,
		Document:    ref.document,
		Fingerprint: n.fingerprint,
		Snapshot:    snap,
	})
	return n.collectAcks(ctx, pr, ratio, nil)
}

// pullQuorum broadcasts a pull and merges each peer's returned snapshot until a
// quorum fraction has responded. Used by the read gather phase.
func (n *Node) pullQuorum(ctx context.Context, ref documentRef, ratio float64) error {
	if n.reached(0, ratio) {
		return nil
	}
	rid := n.nextReqID()
	pr, cancel := n.register(rid, ref.document)
	defer cancel()
	n.broadcast(SyncPullMsg{
		FromID:      n.id,
		ReqID:       rid,
		Collection:  ref.collection,
		Document:    ref.document,
		Fingerprint: n.fingerprint,
	})
	return n.collectAcks(ctx, pr, ratio, func(ack SyncAckMsg) error {
		if err := n.mergeCollection(ref, ack.Snapshot); err != nil {
			// A known peer whose returned state we couldn't incorporate (e.g. a
			// malformed wire rational) must NOT count toward the gather quorum —
			// otherwise the read could "succeed" without that state and return
			// stale local data.
			log.Printf("[%s] pull merge error for %s/%s: %v", n.id, ref.collection, ref.document, err)
			return err
		}
		return nil
	})
}

// broadcast sends msg to every peer in its own goroutine, so a full/stopped peer
// inbox can't hang the request past the collectAcks deadline.
func (n *Node) broadcast(msg any) {
	for _, p := range n.peers {
		go p.Send(msg)
	}
}

// collectAcks counts already-validated, already-distinct acks (the demux
// enforced known-peer + first-time-FromID before enqueueing) until the ratio is
// reached or the per-phase timeout / request context fires. onAck, if non-nil,
// runs for each ack (the pull merge) and an ack is counted ONLY when it returns
// nil — so a known peer whose returned state could not be incorporated does not
// satisfy the quorum. holders = 1 + count.
func (n *Node) collectAcks(ctx context.Context, pr *pendingReq, ratio float64, onAck func(SyncAckMsg) error) error {
	ctx, cancel := context.WithTimeout(ctx, n.syncTimeout)
	defer cancel()
	count := 0
	for {
		select {
		case ack := <-pr.ch:
			if onAck != nil {
				if err := onAck(ack); err != nil {
					continue // not incorporated ⇒ does not count toward quorum
				}
			}
			count++
			if n.reached(count, ratio) {
				return nil
			}
		case <-ctx.Done():
			return fmt.Errorf("%w: got %d of %d holders", ErrQuorumUnreached, 1+count, n.clusterSize())
		}
	}
}
