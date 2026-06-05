package gateway

import (
	"encoding/json"
	"gospr/builder"
	"gospr/node"
	"gospr/parser"
	"io"
	"log"
	"net/http"
)

type Gateway struct {
	node *node.Node
	addr string
}

func New(n *node.Node, addr string) *Gateway {
	return &Gateway{node: n, addr: addr}
}

func (g *Gateway) Start() {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /deploy", g.handleDeploy)
	mux.HandleFunc("POST /{collection}", g.handleApply)
	mux.HandleFunc("GET /{collection}/{query}", g.handleQuery)
	log.Printf("[%s] listening on %s", g.node.ID(), g.addr)
	if err := http.ListenAndServe(g.addr, mux); err != nil {
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
	if err := g.node.Initialize(built); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	g.node.PropagatePlan(built)
	w.WriteHeader(http.StatusOK)
}

type commandRequest struct {
	Action  string `json:"action"`
	Payload []any  `json:"payload"`
}

func (g *Gateway) handleApply(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	var cmd commandRequest
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := g.node.Apply(collection, cmd.Action, cmd.Payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (g *Gateway) handleQuery(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	query := r.PathValue("query")
	val, err := g.node.Query(collection, query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(val)
}
