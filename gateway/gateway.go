package gateway

import (
	"encoding/json"
	"fmt"
	"gospr/builder"
	"gospr/node"
	"gospr/parser"
	"gospr/swagger"
	"io"
	"log"
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

type applyRequest struct {
	Params []any `json:"params"`
}

func (g *Gateway) handleApply(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	action := r.PathValue("action")
	var req applyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := g.node.Apply(collection, action, req.Params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (g *Gateway) handleQuery(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	query := r.PathValue("query")
	var params []any
	if raw := r.URL.Query().Get("params"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if err != nil {
				http.Error(w, fmt.Sprintf("invalid param %q: must be a real number", s), http.StatusBadRequest)
				return
			}
			params = append(params, f)
		}
	}
	val, err := g.node.Query(collection, query, params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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
