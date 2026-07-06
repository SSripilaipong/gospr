package sandbox

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

const counterDSL = `type T = vector rat0+
merge T = zip max
query T.Value = reduce + 0
update T.Add k::rat0+ = local (+ k)
collection Counter = T
`

// Test 12: a linearized update under a partition that isolates the coordinator
// from every peer cannot reach quorum, so the sandbox HTTP layer returns 503
// rather than a stale 200 — the CP tradeoff the partition demo is meant to show.
func TestSandbox_linearizedUpdateUnderPartition503(t *testing.T) {
	s := &Server{net: NewNetwork(), hub: NewHub(), nodes: 3}
	s.current = newCluster(3, s.net, s.hub)
	defer s.current.stop()
	s.net.SetDelay(0) // no artificial delay; the partition (not slowness) is what fails the op

	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	// Deploy to node1 (initializes it synchronously so it can apply locally).
	postJSON(t, srv.URL+"/api/sandbox/deploy",
		map[string]any{"target": "node1", "code": counterDSL}, http.StatusOK, nil)

	// Partition node1 from both peers: it can reach no one ⇒ holders stay at 1.
	postJSON(t, srv.URL+"/api/sandbox/links",
		map[string]any{"a": "node1", "b": "node2", "connected": false}, http.StatusOK, nil)
	postJSON(t, srv.URL+"/api/sandbox/links",
		map[string]any{"a": "node1", "b": "node3", "connected": false}, http.StatusOK, nil)

	// A synchronous Add requiring "all" nodes can never reach quorum here (a peer
	// is partitioned) ⇒ 503.
	req, err := http.NewRequest(http.MethodPost,
		srv.URL+"/api/sandbox/nodes/node1/collections/Counter/Add",
		bytes.NewReader(mustJSON(t, map[string]any{"params": []any{"5"}})))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gospr-Sync-Ratio", "all")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode,
		"isolated coordinator must 503, not return a stale 200")
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}

func postJSON(t *testing.T, url string, body any, wantStatus int, out any) {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewReader(mustJSON(t, body)))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, wantStatus, resp.StatusCode)
	if out != nil {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(out))
	}
}
