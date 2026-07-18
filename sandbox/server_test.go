package sandbox

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gospr/builder"
	"gospr/crdt"
	"gospr/parser"
)

const keyedSandboxDSL = `type T = vector rat0+
merge T = zip max
fn total v::T = reduce + 0 v
query T.Value = total
update T.Add k::rat0+ = local (+ k)
collection Counters[id::rat0+] = T
`

// A string slot leaf renders as its string value in sandbox state — including one
// nested in a struct — not the empty string the numeric fallthrough would yield.
func TestSlotWireToAny_stringLeaf(t *testing.T) {
	hi := "hi"
	assert.Equal(t, "hi", slotWireToAny(crdt.SlotWire{Str: &hi}))

	empty := ""
	assert.Equal(t, "", slotWireToAny(crdt.SlotWire{Str: &empty}))

	// A numeric leaf still renders as its rational string.
	assert.Equal(t, "5", slotWireToAny(crdt.SlotWire{Num: "5"}))

	// An LWW-shaped struct slot: { version (num), value (string) }.
	v := "world"
	got := slotWireToAny(crdt.SlotWire{Struct: map[string]crdt.SlotWire{
		"version": {Num: "3"},
		"value":   {Str: &v},
	}})
	assert.Equal(t, map[string]any{"version": "3", "value": "world"}, got)
}

func TestWireToState_projectsSingletonAndDocuments(t *testing.T) {
	snap := map[string]map[string]crdt.WireSnapshot{
		"Counter": {"": {Slots: map[string]crdt.SlotWire{"n1": {Num: "3"}}}},
		"Users": {
			"alice": {Slots: map[string]crdt.SlotWire{"n1": {Num: "5"}}},
		},
	}
	collections, documents := wireToState(snap)
	assert.Equal(t, map[string]any{"n1": "3"}, collections["Counter"])
	assert.Equal(t, map[string]any{"n1": "5"}, documents["Users"]["alice"])
	assert.NotContains(t, collections, "Users")
}

func TestPlanSchema_includesDocumentKey(t *testing.T) {
	key := parser.ParamSpec{Name: "id", Type: "string"}
	plan := builder.BuiltPlan{Collections: []builder.BuiltCollection{{
		Name: "Users", Key: &key, Spec: &builder.Model{},
	}}}
	schema := planSchema(&plan)
	require.NotNil(t, schema["Users"].Key)
	assert.Equal(t, paramSchema{Name: "id", Type: "string"}, *schema["Users"].Key)
}

func TestSandbox_keyedCollectionAPI(t *testing.T) {
	s := &Server{net: NewNetwork(), hub: NewHub(), nodes: 1}
	s.current = newCluster(1, s.net, s.hub)
	t.Cleanup(s.current.stop)
	h := s.handler()
	request := func(method, target string, body []byte) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	deploy := mustJSON(t, map[string]any{"target": "node1", "code": keyedSandboxDSL})
	require.Equal(t, http.StatusOK, request(http.MethodPost, "/api/sandbox/deploy", deploy).Code)
	add := mustJSON(t, map[string]any{"params": []any{"4"}})
	require.Equal(t, http.StatusOK, request(http.MethodPost, "/api/sandbox/nodes/node1/collections/Counters/2/Add", add).Code)
	query := request(http.MethodGet, "/api/sandbox/nodes/node1/collections/Counters/2/Value", nil)
	require.Equal(t, http.StatusOK, query.Code)
	assert.JSONEq(t, `"4"`, query.Body.String())

	state := request(http.MethodGet, "/api/sandbox/state", nil)
	require.Equal(t, http.StatusOK, state.Code)
	assert.Contains(t, state.Body.String(), `"key":{"name":"id","type":"rat0+"}`)
	assert.Contains(t, state.Body.String(), `"documents":{"Counters":{"2"`)
}
