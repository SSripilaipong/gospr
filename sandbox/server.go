package sandbox

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gospr/builder"
	"gospr/crdt"
	"gospr/node"
	"gospr/parser"
)

//go:embed all:web/dist
var webDist embed.FS

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/sandbox/state", s.handleState)
	mux.HandleFunc("GET /api/sandbox/events", s.handleEvents)
	mux.HandleFunc("POST /api/sandbox/deploy", s.handleDeploy)
	mux.HandleFunc("POST /api/sandbox/reset", s.handleReset)
	mux.HandleFunc("POST /api/sandbox/links", s.handleLinks)
	mux.HandleFunc("POST /api/sandbox/speed", s.handleSpeed)
	mux.HandleFunc("POST /api/sandbox/nodes/{id}/collections/{collection}/{action}", s.handleApply)
	mux.HandleFunc("GET /api/sandbox/nodes/{id}/collections/{collection}/{query}", s.handleQuery)

	// Serve the embedded SPA, falling back to a friendly message if the frontend
	// hasn't been built yet (npm run build populates web/dist).
	if sub, err := fs.Sub(webDist, "web/dist"); err == nil {
		if _, statErr := fs.Stat(sub, "index.html"); statErr == nil {
			mux.Handle("/", http.FileServerFS(sub))
		} else {
			mux.HandleFunc("/", notBuilt)
		}
	} else {
		mux.HandleFunc("/", notBuilt)
	}
	return mux
}

func notBuilt(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, `<!DOCTYPE html><html><body style="font-family:sans-serif;padding:2rem">
<h1>gospr sandbox</h1>
<p>The web UI has not been built yet. Run:</p>
<pre>cd sandbox/web &amp;&amp; npm install &amp;&amp; npm run build</pre>
<p>then restart <code>gospr sandbox run</code>. The API at <code>/api/sandbox/*</code> is live.</p>
</body></html>`)
}

// ---- state ---------------------------------------------------------------

type stateResponse struct {
	Nodes   []nodeState                 `json:"nodes"`
	Schema  map[string]collectionSchema `json:"schema"`
	Links   map[string]bool             `json:"links"` // "a|b" -> connected
	DelayMs int64                       `json:"delayMs"`
}

type nodeState struct {
	ID          string                    `json:"id"`
	Initialized bool                      `json:"initialized"`
	Collections map[string]map[string]any `json:"collections"` // collection -> slot -> (ratString | struct object)
}

type collectionSchema struct {
	Updates map[string][]paramSchema `json:"updates"`
	Queries map[string][]paramSchema `json:"queries"`
}

type paramSchema struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	var resp stateResponse
	_ = s.withCluster(func(c *Cluster) error {
		ids := c.sortedIDs()
		resp.Nodes = make([]nodeState, 0, len(ids))
		for _, id := range ids {
			n, _ := c.node(id)
			ns := nodeState{
				ID:          id,
				Initialized: n.Initialized(),
				Collections: wireToState(n.SnapshotWireAll()),
			}
			resp.Nodes = append(resp.Nodes, ns)
		}
		resp.Schema = planSchema(c.deployed)

		// Links over all unordered pairs.
		resp.Links = map[string]bool{}
		for i := 0; i < len(ids); i++ {
			for j := i + 1; j < len(ids); j++ {
				resp.Links[linkKey(ids[i], ids[j])] = s.net.Connected(ids[i], ids[j])
			}
		}
		return nil
	})
	resp.DelayMs = s.net.Delay().Milliseconds()

	writeJSON(w, resp)
}

// wireToState converts each collection's WireSnapshot into collection -> slot ->
// JSON value for the SPA: a scalar slot is an exact-rational string, a struct slot
// is a (possibly nested) object of field -> value.
func wireToState(snap map[string]crdt.WireSnapshot) map[string]map[string]any {
	out := map[string]map[string]any{}
	for name, ws := range snap {
		slots := make(map[string]any, len(ws.Slots))
		for slot, sw := range ws.Slots {
			slots[slot] = slotWireToAny(sw)
		}
		out[name] = slots
	}
	return out
}

// slotWireToAny renders a SlotWire as a JSON value: scalar -> its rational string,
// struct -> an object of field -> value (recursing).
func slotWireToAny(sw crdt.SlotWire) any {
	if sw.Struct != nil {
		obj := make(map[string]any, len(sw.Struct))
		for k, f := range sw.Struct {
			obj[k] = slotWireToAny(f)
		}
		return obj
	}
	return sw.Num
}

// planSchema describes the deployed plan's collections so the UI can render one
// labeled input per declared param. Empty when nothing is deployed.
func planSchema(plan *builder.BuiltPlan) map[string]collectionSchema {
	out := map[string]collectionSchema{}
	if plan == nil {
		return out
	}
	for _, bc := range plan.Collections {
		m, ok := bc.Spec.(*builder.Model)
		if !ok {
			continue
		}
		cs := collectionSchema{
			Updates: map[string][]paramSchema{},
			Queries: map[string][]paramSchema{},
		}
		for name, method := range m.Updates {
			cs.Updates[name] = paramsOf(method.Params)
		}
		for name, method := range m.Queries {
			cs.Queries[name] = paramsOf(method.Params)
		}
		out[bc.Name] = cs
	}
	return out
}

func paramsOf(ps []parser.ParamSpec) []paramSchema {
	out := make([]paramSchema, len(ps))
	for i, p := range ps {
		out[i] = paramSchema{Name: p.Name, Type: p.Type}
	}
	return out
}

// ---- events (SSE) --------------------------------------------------------

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := s.hub.Subscribe()
	defer s.hub.Unsubscribe(ch)

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// ---- deploy --------------------------------------------------------------

type deployRequest struct {
	Target string `json:"target"`
	Code   string `json:"code"`
}

func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request) {
	var req deployRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Heavy work (parse + build, the latter runs z3 via the prover) happens
	// outside any lock so it never blocks Reset or another request.
	plan, err := parser.Parse(req.Code)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	built, err := builder.Build(plan)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Mirror the gateway's zero-collection guard: Initialize is one-way, so a
	// no-collection deploy would permanently mark the node initialized with nothing.
	if len(built.Collections) == 0 {
		http.Error(w, "deploy has no collections", http.StatusBadRequest)
		return
	}

	status := http.StatusOK
	var msg string
	_ = s.withClusterW(func(c *Cluster) error {
		// One-shot per cluster (pin-the-plan): once a plan is deployed the schema
		// is fixed, so the advertised methods always match what nodes installed.
		if c.deployed != nil {
			status = http.StatusConflict
			msg = "a plan is already deployed — press Reset to deploy different code"
			return nil
		}
		target, ok := c.node(req.Target)
		if !ok {
			status = http.StatusBadRequest
			msg = fmt.Sprintf("unknown node %q", req.Target)
			return nil
		}
		if err := target.Initialize(built); err != nil {
			status = http.StatusInternalServerError
			msg = err.Error()
			return nil
		}
		target.PropagatePlan(built)
		c.deployed = &built
		return nil
	})

	if status != http.StatusOK {
		http.Error(w, msg, status)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ---- reset ---------------------------------------------------------------

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	s.reset()
	w.WriteHeader(http.StatusOK)
}

// ---- apply / query -------------------------------------------------------

type applyRequest struct {
	Params []string `json:"params"`
}

// parseLinearize reads the two consistency headers; see the gateway's copy for
// the rationale (NaN/Inf/out-of-range are rejected so a bad ratio is a fast 400,
// not a quorum timeout 503).
func parseLinearize(r *http.Request) (on bool, ratio float64, err error) {
	on = r.Header.Get("X-Gospr-Linearize") == "true"
	ratio = 0.5
	if raw := r.Header.Get("X-Gospr-Sync-Ratio"); raw != "" {
		ratio, err = strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil || math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 || ratio > 1 {
			return on, 0, fmt.Errorf("invalid X-Gospr-Sync-Ratio %q (want a fraction in [0,1])", raw)
		}
	}
	return on, ratio, nil
}

// writeOpError maps ErrQuorumUnreached → 503, anything else → 400.
func writeOpError(w http.ResponseWriter, err error) {
	if errors.Is(err, node.ErrQuorumUnreached) {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	http.Error(w, err.Error(), http.StatusBadRequest)
}

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	collection := r.PathValue("collection")
	action := r.PathValue("action")

	linearize, ratio, lerr := parseLinearize(r)
	if lerr != nil {
		http.Error(w, lerr.Error(), http.StatusBadRequest)
		return
	}

	var req applyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON (numeric params must be strings, e.g. \"0.1\")", http.StatusBadRequest)
		return
	}
	// String-only params, widened to []any — never decode into []any directly, or
	// JSON numbers arrive as float64 and take the lossy crdt.toRat path.
	params := make([]any, len(req.Params))
	for i, p := range req.Params {
		params[i] = p
	}

	// Hold the cluster RLock for the WHOLE op (acks are serviced by each node's
	// own message loop, not the cluster lock, so there's no deadlock), so a 200
	// always refers to the current cluster.
	err := s.withCluster(func(c *Cluster) error {
		n, ok := c.node(id)
		if !ok {
			return fmt.Errorf("unknown node %q", id)
		}
		if linearize {
			return n.ApplyLinearizable(r.Context(), collection, action, params, ratio)
		}
		return n.Apply(collection, action, params)
	})
	if err != nil {
		writeOpError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	collection := r.PathValue("collection")
	query := r.PathValue("query")

	linearize, ratio, lerr := parseLinearize(r)
	if lerr != nil {
		http.Error(w, lerr.Error(), http.StatusBadRequest)
		return
	}

	var params []any
	if raw := r.URL.Query().Get("params"); raw != "" {
		for _, p := range strings.Split(raw, ",") {
			params = append(params, strings.TrimSpace(p))
		}
	}

	var result any
	err := s.withCluster(func(c *Cluster) error {
		n, ok := c.node(id)
		if !ok {
			return fmt.Errorf("unknown node %q", id)
		}
		var v any
		var err error
		if linearize {
			v, err = n.QueryLinearizable(r.Context(), collection, query, params, ratio)
		} else {
			v, err = n.Query(collection, query, params)
		}
		result = v
		return err
	})
	if err != nil {
		writeOpError(w, err)
		return
	}
	writeJSON(w, result)
}

// ---- links / speed -------------------------------------------------------

type linksRequest struct {
	A         string `json:"a"`
	B         string `json:"b"`
	Connected bool   `json:"connected"`
}

func (s *Server) handleLinks(w http.ResponseWriter, r *http.Request) {
	var req linksRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	s.net.SetLink(req.A, req.B, req.Connected)
	w.WriteHeader(http.StatusOK)
}

type speedRequest struct {
	DelayMs int64 `json:"delayMs"`
}

func (s *Server) handleSpeed(w http.ResponseWriter, r *http.Request) {
	var req speedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.DelayMs < 0 {
		req.DelayMs = 0
	}
	s.net.SetDelay(time.Duration(req.DelayMs) * time.Millisecond)
	w.WriteHeader(http.StatusOK)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
