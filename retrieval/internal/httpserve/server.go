// Package httpserve exposes re-search queries over a small JSON HTTP
// endpoint — the retrieval half any future hosted consumer wraps.
package httpserve

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/adiazpar/re-discipline/retrieval/internal/search"
)

// QueryFunc answers one question with ranked hits.
type QueryFunc func(query string, opts search.QueryOptions) ([]search.Hit, error)
type ResultFunc func(query string, opts search.QueryOptions) (any, error)

// SymbolFunc resolves one symbol name to its lookup result.
type SymbolFunc func(name string, limit int) (search.SymbolHits, error)

// Handler serves GET /query?q=...&limit=N&kind=...&grade=... and
// GET /symbol?name=...&limit=N as JSON.
func Handler(query QueryFunc, symbol SymbolFunc) http.Handler {
	return ResultHandler(func(q string, opts search.QueryOptions) (any, error) {
		hits, err := query(q, opts)
		if hits == nil {
			hits = []search.Hit{}
		}
		return map[string]any{"hits": hits}, err
	}, symbol)
}

func ResultHandler(query ResultFunc, symbol SymbolFunc) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /query", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if q == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing q parameter"})
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		hits, err := query(q, search.QueryOptions{
			Sources: r.URL.Query().Get("sources"), Offline: r.URL.Query().Get("offline") == "true",
			Limit: limit,
			Kind:  r.URL.Query().Get("kind"),
			Grade: r.URL.Query().Get("grade"),
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, hits)
	})
	mux.HandleFunc("GET /symbol", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing name parameter"})
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		res, err := symbol(name, limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if res.Symbols == nil {
			res.Symbols = []search.Symbol{}
		}
		writeJSON(w, http.StatusOK, res)
	})
	return mux
}

// ListenAndServe blocks serving the query API on addr.
func ListenAndServe(addr string, query QueryFunc, symbol SymbolFunc) error {
	return http.ListenAndServe(addr, Handler(query, symbol))
}
func ListenAndServeResult(addr string, query ResultFunc, symbol SymbolFunc) error {
	return http.ListenAndServe(addr, ResultHandler(query, symbol))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
