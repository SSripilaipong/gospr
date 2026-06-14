package gateway_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gospr/gateway"
	"gospr/node"
)

const counterDSL = `type T = vector rat0+
merge T = zip max
query T.Value = reduce + 0
update T.Add k::rat0+ = local (+ k)
collection Counter = T
`

// A single node behind a gateway, deployed via the real HTTP deploy endpoint.
func newCounterServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(gateway.New(node.New("solo"), "").Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/cluster/deploy", "text/plain", bytes.NewBufferString(counterDSL))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return srv
}

func postAdd(t *testing.T, base, jsonBody string) int {
	t.Helper()
	resp, err := http.Post(base+"/api/collections/Counter/Add", "application/json", bytes.NewBufferString(jsonBody))
	require.NoError(t, err)
	resp.Body.Close()
	return resp.StatusCode
}

// A bare JSON number must be rejected: numeric params cross the boundary only as
// exact-rational strings, so float never enters the pipeline.
func TestGateway_rejectsNumericParam(t *testing.T) {
	srv := newCounterServer(t)
	assert.Equal(t, http.StatusBadRequest, postAdd(t, srv.URL, `{"params":[0.1]}`))
}

// A string param is accepted and parsed exactly: "0.1" is 1/10, and the query
// reports the exact rational string.
func TestGateway_acceptsStringParamExactly(t *testing.T) {
	srv := newCounterServer(t)
	require.Equal(t, http.StatusOK, postAdd(t, srv.URL, `{"params":["0.1"]}`))

	resp, err := http.Get(srv.URL + "/api/collections/Counter/Value")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var v string
	require.NoError(t, json.Unmarshal(data, &v))
	assert.Equal(t, "1/10", v)
}
