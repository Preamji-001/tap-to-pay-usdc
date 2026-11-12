// Package httpapi exposes the checkout API over HTTP.
package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Preamji-001/crypto-pos-base/internal/checkout"
)

type server struct {
	svc        *checkout.Service
	apiKey     []byte
	corsOrigin string
}

// NewServer returns the API handler. Creating checkouts needs apiKey in the
// X-API-Key header; reading one, asking for its routes or posting its
// authorization does not, because its id is unguessable and the signature is
// the customer's proof. Routes reads chain balances, so they are cached.
func NewServer(svc *checkout.Service, apiKey, corsOrigin string) http.Handler {
	s := &server{svc: svc, apiKey: []byte(apiKey), corsOrigin: corsOrigin}

	mux := http.NewServeMux()
	mux.Handle("POST /api/checkouts", s.requireAPIKey(http.HandlerFunc(s.createCheckout)))
	mux.HandleFunc("GET /api/checkouts/{id}", s.getCheckout)
	mux.HandleFunc("POST /api/checkouts/{id}/authorization", s.authorizeCheckout)
	mux.HandleFunc("POST /api/checkouts/{id}/routes", s.checkoutRoutes)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "not found")
	})
	return s.cors(mux)
}

func (s *server) requireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := []byte(r.Header.Get("X-API-Key"))
		if subtle.ConstantTimeCompare(key, s.apiKey) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid API key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// cors allows the configured frontend origin only, and answers preflights.
func (s *server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed := r.Header.Get("Origin") == s.corsOrigin
		if allowed {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", s.corsOrigin)
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type")
			h.Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions && strings.HasPrefix(r.URL.Path, "/api/") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{Error: msg, Code: code})
}

func writeInternal(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("internal error", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("writing response", "err", err)
	}
}
