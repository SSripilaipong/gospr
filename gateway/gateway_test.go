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
fn total v::T = reduce + 0 v
query T.Value = total
update T.Add k::rat0+ = local (+ k)
collection Counter = T
`

const keyedCounterDSL = `type T = vector rat0+
merge T = zip max
fn total v::T = reduce + 0 v
query T.Value = total
update T.Add k::rat0+ = local (+ k)
collection Counters[id::rat0+] = T
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

func TestGateway_keyedCollectionRoutes(t *testing.T) {
	h := gateway.New(node.New("solo"), "").Handler()
	request := func(method, target, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	require.Equal(t, http.StatusOK, request(http.MethodPost, "/api/cluster/deploy", keyedCounterDSL).Code)
	untouched := request(http.MethodGet, "/api/collections/Counters/0.5/Value", "")
	require.Equal(t, http.StatusOK, untouched.Code)
	assert.JSONEq(t, `"0"`, untouched.Body.String())
	require.Equal(t, http.StatusOK, request(http.MethodPost, "/api/collections/Counters/0.50/Add", `{"params":["2"]}`).Code)
	got := request(http.MethodGet, "/api/collections/Counters/0.5/Value", "")
	require.Equal(t, http.StatusOK, got.Code)
	assert.JSONEq(t, `"2"`, got.Body.String())

	assert.Equal(t, http.StatusBadRequest, request(http.MethodGet, "/api/collections/Counters/1%2F2/Value", "").Code)
	assert.Equal(t, http.StatusBadRequest, request(http.MethodGet, "/api/collections/Counters/1e2/Value", "").Code)
	assert.Equal(t, http.StatusBadRequest, request(http.MethodGet, "/api/collections/Counters/Value", "").Code)
}
