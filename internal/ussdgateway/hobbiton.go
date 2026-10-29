package ussdgateway

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var hobbitonType = regexp.MustCompile(`^[0-9]{1,10}$`)

// Hobbiton's query transport is confirmed; its requestType meanings and retry
// protocol are not. This isolated pilot supports public browsing only. No
// customer reads or payment calls can be made through this handler.
func (s *Server) serveHobbiton(w http.ResponseWriter, r *http.Request) {
	if len(r.Header.Values("Authorization")) != 1 || len(s.Key) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.Key)) != 1 {
		output(w, 401, "END Operator authentication required.")
		return
	}
	if len(r.URL.RawQuery) > 2048 {
		output(w, 414, "END Request too large.")
		return
	}
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.Header.Get("Content-Encoding") != "" || len(r.Header.Values("X-LMS-Customer-Token")) != 0 {
		output(w, 400, "END Invalid callback fields.")
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) != 4 {
		output(w, 400, "END Invalid callback fields.")
		return
	}
	for _, name := range []string{"msisdn", "SESSION_ID", "input", "requestType"} {
		if len(q[name]) != 1 {
			output(w, 400, "END Invalid callback fields.")
			return
		}
	}
	id, phone, input, kind := q.Get("SESSION_ID"), q.Get("msisdn"), q.Get("input"), q.Get("requestType")
	phone = strings.TrimPrefix(phone, "+")
	if len(phone) == 10 && phone[0] == '0' {
		phone = "260" + phone[1:]
	}
	if !sessionID.MatchString(id) || !phoneNumber.MatchString(phone) || !hobbitonType.MatchString(kind) || len(input) > 1 || (input != "" && (input[0] < '0' || input[0] > '9')) {
		output(w, 400, "END Invalid callback fields.")
		return
	}
	// Separate namespace prevents GET requests from reusing a POST session.
	id = "hobbiton:" + id
	binding := sha256.Sum256([]byte(phone))
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	s.mu.Lock()
	if s.sessions == nil {
		s.sessions = make(map[string]*session)
	}
	now := time.Now()
	for key, old := range s.sessions {
		if now.After(old.expires) {
			delete(s.sessions, key)
		}
	}
	current := s.sessions[id]
	if current == nil {
		if len(s.sessions) >= 2000 {
			s.mu.Unlock()
			output(w, 503, "END Service busy. Please dial again.")
			return
		}
		current = &session{gate: make(chan struct{}, 1), binding: binding, expires: now.Add(5 * time.Minute), responses: make(map[string]string)}
		s.sessions[id] = current
	}
	s.mu.Unlock()
	select {
	case current.gate <- struct{}{}:
		defer func() { <-current.gate }()
	case <-ctx.Done():
		output(w, 503, "END Service busy. Please dial again.")
		return
	}
	if ctx.Err() != nil {
		output(w, 503, "END Service busy. Please dial again.")
		return
	}
	if time.Now().After(current.expires) {
		output(w, 200, "END Session expired. Please dial again.")
		return
	}
	if current.binding != binding {
		output(w, 409, "END Session identity changed. Please dial again.")
		return
	}
	// Only an immediately repeated callback is replayed: selecting 1 after Back
	// must not replay an earlier product screen as an old protocol event.
	fingerprint := kind + ":" + input
	if cached, ok := current.responses[fingerprint]; ok {
		output(w, 200, cached)
		return
	}
	if current.ended {
		output(w, 409, "END Session ended. Please dial again.")
		return
	}
	var response string
	home := "CON Mighty Finance\n1 Loan products\n0 Exit"
	if current.last == "" {
		// Initial input may contain the entry-code selection. No requestType meaning
		// is assumed; a previously unseen session always receives the initial menu.
		response = home
	} else if input == "0" {
		response = "END Thank you for choosing Mighty Finance."
	} else if current.last == "home" && input == "1" {
		response = s.render(ctx, "1", "")
	} else if current.last == "products" && input == "9" {
		response = home
	} else {
		response = "END This option is unavailable during connection testing. Please dial again."
	}
	if ctx.Err() != nil {
		output(w, 503, "END Service unavailable. Please try again.")
		return
	}
	current.last = "home"
	if strings.HasPrefix(response, "CON Loan products") {
		current.last = "products"
	}
	current.ended = strings.HasPrefix(response, "END ")
	current.responses = map[string]string{fingerprint: response}
	output(w, 200, response)
}
