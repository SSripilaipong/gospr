package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"gospr/builder"
	"gospr/node"
	"gospr/parser"
	"gospr/swagger"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

type Gateway struct {
	node        *node.Node
	addr        string
	swaggerJSON []byte
	swaggerMu   sync.RWMutex
}

func New(n *node.Node, addr string) *Gateway {
	g := &Gateway{node: n, addr: addr}
	if data, err := swagger.Generate(builder.BuiltPlan{}); err == nil {
		g.swaggerJSON = data
	}
	return g
}

// Handler builds the HTTP routing for this gateway. Exposed so tests can wrap
// it in httptest.NewServer; Start uses it for the real listener.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/cluster/deploy", g.handleDeploy)
	mux.HandleFunc("POST /api/collections/{collection}/{action}", g.handleApply)
	mux.HandleFunc("GET /api/collections/{collection}/{query}", g.handleQuery)
	mux.HandleFunc("GET /api/swagger.json", g.handleSwagger)
	mux.HandleFunc("GET /api/docs", g.handleDocs)
	return mux
}

func (g *Gateway) Start() {
	log.Printf("[%s] listening on %s", g.node.ID(), g.addr)
	if err := http.ListenAndServe(g.addr, g.Handler()); err != nil {
		log.Fatalf("gateway %s: %v", g.addr, err)
	}
}

func (g *Gateway) handleDeploy(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	plan, err := parser.Parse(string(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	built, err := builder.Build(plan)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Node initialization is one-way/idempotent: an empty cluster can never
	// be populated later, so a deploy with no collections is rejected here.
	if len(built.Collections) == 0 {
		http.Error(w, "deploy has no collections", http.StatusBadRequest)
		return
	}
	if err := g.node.Initialize(built); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	g.node.PropagatePlan(built)
	if data, err := swagger.Generate(built); err == nil {
		g.swaggerMu.Lock()
		g.swaggerJSON = data
		g.swaggerMu.Unlock()
	}
	w.WriteHeader(http.StatusOK)
}

// applyRequest carries action params. Numbers must be exact-rational strings
// (e.g. "0.1", "1/3") — a bare JSON number fails to decode here and is rejected,
// so no value is silently routed through float64 at the boundary.
type applyRequest struct {
	Params []string `json:"params"`
}

// parseSync reads the X-Gospr-Sync-Ratio consistency header. It is presence-based:
// an absent header means async (local-only, today's default); a present header
// turns synchronous quorum mode on. The value is either a fraction in [0,1) — the
// quorum is holders/N > ratio, so 0.5 is a true majority and >=0.5 is linearizable
// — or the literal "all" for every node. A present-but-empty value, a numeric
// value >= 1 (use "all"), and NaN/±Inf/out-of-range/malformed ratios are all
// rejected with a 400: note strconv.ParseFloat("NaN") succeeds and a NaN ratio
// makes the quorum predicate always false, so an unrejected NaN would turn a fast
// 400 into a timeout 503.
func parseSync(r *http.Request) (on bool, ratio float64, err error) {
	vals := r.Header.Values("X-Gospr-Sync-Ratio")
	if len(vals) == 0 {
		return false, 0, nil // header absent ⇒ async, local-only
	}
	raw := strings.TrimSpace(vals[0])
	if raw == "" {
		return false, 0, fmt.Errorf("X-Gospr-Sync-Ratio present but empty (want a fraction in [0,1), or \"all\")")
	}
	if strings.EqualFold(raw, "all") {
		return true, node.AllRatio, nil
	}
	ratio, err = strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 || ratio >= 1 {
		return false, 0, fmt.Errorf("invalid X-Gospr-Sync-Ratio %q (want a fraction in [0,1), or \"all\")", raw)
	}
	return true, ratio, nil
}

// writeOpError maps a node op error to a status: ErrQuorumUnreached → 503,
// anything else → 400 (today's behavior).
func writeOpError(w http.ResponseWriter, err error) {
	if errors.Is(err, node.ErrQuorumUnreached) {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	http.Error(w, err.Error(), http.StatusBadRequest)
}

func (g *Gateway) handleApply(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	action := r.PathValue("action")
	syncOn, ratio, lerr := parseSync(r)
	if lerr != nil {
		http.Error(w, lerr.Error(), http.StatusBadRequest)
		return
	}
	var req applyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON (numeric params must be strings, e.g. \"0.1\")", http.StatusBadRequest)
		return
	}
	params := make([]any, len(req.Params))
	for i, s := range req.Params {
		params[i] = s
	}
	if syncOn {
		if err := g.node.ApplySync(r.Context(), collection, action, params, ratio); err != nil {
			writeOpError(w, err)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := g.node.Apply(collection, action, params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (g *Gateway) handleQuery(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	query := r.PathValue("query")
	syncOn, ratio, lerr := parseSync(r)
	if lerr != nil {
		http.Error(w, lerr.Error(), http.StatusBadRequest)
		return
	}
	var params []any
	if raw := r.URL.Query().Get("params"); raw != "" {
		// Pass each param through as a string; the CRDT parses it into an exact
		// rational (so "0.1" is 1/10), and rejects malformed/out-of-domain values.
		for _, s := range strings.Split(raw, ",") {
			params = append(params, strings.TrimSpace(s))
		}
	}
	var val any
	var err error
	if syncOn {
		val, err = g.node.QuerySync(r.Context(), collection, query, params, ratio)
	} else {
		val, err = g.node.Query(collection, query, params)
	}
	if err != nil {
		writeOpError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(val)
}

func (g *Gateway) handleSwagger(w http.ResponseWriter, r *http.Request) {
	g.swaggerMu.RLock()
	data := g.swaggerJSON
	g.swaggerMu.RUnlock()
	if data == nil {
		http.Error(w, "no plan deployed yet", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (g *Gateway) handleDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, swaggerUIHTML)
}

const swaggerUIHTML = `<!DOCTYPE html>
<html>
<head>
  <title>gospr API</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist/swagger-ui-bundle.js"></script>
  <script>
    SwaggerUIBundle({ url: "/api/swagger.json", dom_id: "#swagger-ui" })
  </script>
</body>
</html>`
