package app

import (
	"crypto/hmac"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

//go:embed static/*
var assets embed.FS

type Request struct {
	SessionID string `json:"session_id"`
	Phone     string `json:"phone"`
	RequestID string `json:"request_id"`
	Sequence  int    `json:"sequence"`
	Input     string `json:"input"`
}
type Response struct {
	InputMode string `json:"input_mode"`
	MaxLength int    `json:"max_length"`
	InputHint string `json:"input_hint"`
	SessionID string `json:"session_id"`
	Sequence  int    `json:"sequence"`
	ScreenID  string `json:"screen_id"`
	Text      string `json:"text"`
	End       bool   `json:"end"`
}
type apiError struct{ Code, Message string }

func (e *apiError) Error() string { return e.Message }
func conflict(msg string) error   { return &apiError{"DUPLICATE_CONFLICT", msg} }
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		b, _ := assets.ReadFile("static/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(strings.ReplaceAll(string(b), "__DEMO_TOKEN__", a.token)))
	})
	mux.HandleFunc("GET /app.js", func(w http.ResponseWriter, r *http.Request) {
		b, _ := assets.ReadFile("static/app.js")
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("GET /style.css", func(w http.ResponseWriter, r *http.Request) {
		b, _ := assets.ReadFile("static/style.css")
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("POST /api/sessions", a.sessions)
	mux.HandleFunc("GET /api/demo/state", a.state)
	mux.HandleFunc("POST /api/demo/actions", a.demo)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok", "mode": "local-demo"})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Host != "127.0.0.1:8090" && r.Host != "localhost:8090" {
			writeJSON(w, 403, map[string]string{"error": "LOCAL_ONLY"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			origin := r.Header.Get("Origin")
			if origin != "" && origin != "http://"+r.Host {
				writeJSON(w, 403, map[string]string{"error": "ORIGIN_REJECTED"})
				return
			}
			if !hmac.Equal([]byte(r.Header.Get("X-Demo-Token")), []byte(a.token)) {
				writeJSON(w, 401, map[string]string{"error": "DEMO_TOKEN_REQUIRED"})
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return errors.New("one JSON object required")
	}
	return nil
}
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, err error) {
	var e *apiError
	if errors.As(err, &e) {
		writeJSON(w, 409, map[string]string{"error": e.Code, "message": e.Message})
		return
	}
	writeJSON(w, 503, map[string]string{"error": "LOCAL_STORAGE_UNAVAILABLE", "message": "State could not be saved. Retry the same request."})
}
func (a *App) sessions(w http.ResponseWriter, r *http.Request) {
	var req Request
	if decode(w, r, &req) != nil {
		writeJSON(w, 400, map[string]string{"error": "VALIDATION_ERROR"})
		return
	}
	if !requestKeyPattern.MatchString(req.RequestID) || (req.SessionID != "" && !requestKeyPattern.MatchString(req.SessionID)) || req.Sequence < 1 {
		writeJSON(w, 400, map[string]string{"error": "VALIDATION_ERROR", "message": "Use valid request/session identifiers and a positive whole-number sequence."})
		return
	}
	var response Response
	err := a.transaction(func() error {
		now := time.Now().UTC()
		s := a.db.Sessions[req.SessionID]
		if s == nil {
			if req.SessionID != "" {
				return &apiError{"SESSION_EXPIRED", "Dial again to start a new session."}
			}
			if req.Sequence != 1 || req.Input != "" {
				return conflict("A new session starts at sequence 1 with empty input.")
			}
			p, e := phone(req.Phone)
			if e != nil {
				return &apiError{"VALIDATION_ERROR", e.Error()}
			}
			// Client request IDs also deduplicate session creation after a lost response.
			for _, prior := range a.db.Sessions {
				if prior.Phone == p && prior.LastRequestID == req.RequestID && prior.LastSequence == 1 && now.Before(prior.ExpiresAt) {
					response = prior.LastResponse
					return nil
				}
			}
			s = &Session{ID: id("SES"), Phone: p, Screen: "main", ExpiresAt: now.Add(5 * time.Minute)}
			a.db.Sessions[s.ID] = s
			for _, customer := range a.db.Customers {
				if customer.Phone == p {
					a.gate(s, "home")
					break
				}
			}
		}
		fp := a.fingerprint(req.RequestID + "\x00" + req.Input)
		if now.After(s.ExpiresAt) {
			return &apiError{"SESSION_EXPIRED", "Session expired. Dial again and confirm your name."}
		}
		if req.Sequence == s.LastSequence {
			if req.RequestID != s.LastRequestID || fp != s.LastFingerprint {
				return conflict("Request key was reused with different input.")
			}
			if !s.ConfirmedUntil.IsZero() && !a.customerConfirmed(s) {
				return &apiError{"SESSION_CONFIRMATION_REQUIRED", "Session confirmation expired. Dial again."}
			}
			response = s.LastResponse
			return nil
		}
		if req.Sequence != s.LastSequence+1 || req.RequestID == s.LastRequestID {
			return conflict("Out-of-order request. Retry the original request or dial again.")
		}
		if s.Ended {
			return &apiError{"SESSION_ENDED", "Dial again for a new session."}
		}
		if req.Sequence > 1 {
			a.advance(s, req.Input)
		}
		response = a.render(s)
		response.Sequence = req.Sequence
		s.LastSequence = req.Sequence
		s.LastRequestID = req.RequestID
		s.LastFingerprint = fp
		s.LastResponse = response
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, response)
}
func (a *App) state(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	type customerView struct {
		ID, Name, Phone string
	}
	customers := []customerView{}
	for _, c := range a.db.Customers {
		customers = append(customers, customerView{c.ID, c.Name, masked(c.Phone)})
	}
	writeJSON(w, 200, map[string]any{"mode": "LOCAL SIMULATION — no money moves", "customers": customers, "applications": a.db.Applications, "offers": a.db.Offers, "loans": a.db.Loans, "transactions": a.db.Operations, "cases": a.db.Cases, "ledger": a.db.Ledger, "audit": a.db.Audit})
}
