package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"gospr/gateway"
	"gospr/node"
)

// counterDSL is the simplest meaningful counter: each node accumulates its own
// slot (`local (+ k)`), slots merge per-node-id by max (`zip max`), and the
// query sums every slot (`reduce + 0`). A `collection` line is required so the
// cluster has something to instantiate and so HTTP can address it by name.
const counterDSL = `type T = vector real0+
merge T = zip max
query T.Value = reduce + 0
update T.Add k::real0+ = local (+ k)
collection Counter = T
`

// TestClusterHTTP_addPropagatesAcrossNodes is a true blackbox test: it stands up
// a real 3-node cluster (nodes + gossip loops + HTTP gateways) and drives it ONLY
// through the HTTP API. It deploys a counter to one node, Adds on that node, then
// waits for the value to gossip to a DIFFERENT node. Every async stage (deploy
// propagation + random-peer gossip) is absorbed by polling until converged, so
// there are no fixed sleeps and no timing assertions to flake on.
func TestClusterHTTP_addPropagatesAcrossNodes(t *testing.T) {
	urls := startCluster(t, 20*time.Millisecond)

	// Deploy to node 0; the plan propagates to nodes 1 and 2 over the peer channels.
	deploy(t, urls[0], counterDSL)

	// Add on node 0 only.
	add(t, urls[0], "Counter", 5)

	// Sanity: node 0 reads its own slot immediately-ish.
	require.Eventually(t, func() bool {
		v, status := queryValue(t, urls[0], "Counter")
		return status == http.StatusOK && v == 5
	}, 5*time.Second, 25*time.Millisecond, "node 0 should reflect its own Add")

	// Core assertion: a different node (node 2) eventually converges to the same
	// value via gossip. The poller tolerates the pre-init window (non-200) until
	// the deploy has propagated and gossip has carried node 0's slot across.
	require.Eventually(t, func() bool {
		v, status := queryValue(t, urls[2], "Counter")
		return status == http.StatusOK && v == 5
	}, 5*time.Second, 25*time.Millisecond, "value should propagate to a peer node")
}

// startCluster wires n=3 nodes exactly like main.go (each peers with the other
// two), starts their loops, and fronts each with a real HTTP server on an
// ephemeral port. Returns the three base URLs. All servers are torn down via
// t.Cleanup.
func startCluster(t *testing.T, gossip time.Duration) []string {
	t.Helper()
	nodes := []*node.Node{
		node.New("node1", node.WithGossipInterval(gossip)),
		node.New("node2", node.WithGossipInterval(gossip)),
		node.New("node3", node.WithGossipInterval(gossip)),
	}
	for i, n := range nodes {
		for j, peer := range nodes {
			if i != j {
				n.AddPeer(peer.Inbox())
			}
		}
	}
	urls := make([]string, len(nodes))
	for i, n := range nodes {
		n.Start()
		srv := httptest.NewServer(gateway.New(n, "").Handler())
		t.Cleanup(srv.Close)
		urls[i] = srv.URL
	}
	return urls
}

func deploy(t *testing.T, base, dsl string) {
	t.Helper()
	resp, err := http.Post(base+"/api/cluster/deploy", "text/plain", bytes.NewBufferString(dsl))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "deploy should succeed")
}

func add(t *testing.T, base, collection string, k float64) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"params": []any{k}})
	require.NoError(t, err)
	resp, err := http.Post(
		fmt.Sprintf("%s/api/collections/%s/Add", base, collection),
		"application/json", bytes.NewReader(body),
	)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "Add should succeed")
}

// queryValue returns the decoded numeric result and the HTTP status. A non-200
// status (e.g. the brief window before a peer is initialized) is reported to the
// caller rather than failing, so pollers can keep waiting for convergence.
func queryValue(t *testing.T, base, collection string) (float64, int) {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("%s/api/collections/%s/Value", base, collection))
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	if resp.StatusCode != http.StatusOK {
		return 0, resp.StatusCode
	}
	var v float64
	require.NoError(t, json.Unmarshal(data, &v))
	return v, resp.StatusCode
}
