package ussdgateway

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// hobbitonSourceAllowed trusts only the configured loopback tunnel ingress.
// The Caddy and app listeners MUST remain loopback-only. Cloudflare supplies
// CF-Connecting-IP; X-Forwarded-For and caller-supplied alternatives are ignored.
// Local processes and administrators of this Cloudflare zone are trusted.
func (s *Server) hobbitonSourceAllowed(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	peer, peerErr := netip.ParseAddr(host)
	allowed, allowedErr := netip.ParseAddr(s.HobbitonIP)
	if err != nil || peerErr != nil || !peer.IsLoopback() || allowedErr != nil || len(r.Header.Values("CF-Connecting-IP")) != 1 || len(r.Header.Values("CF-Worker")) != 0 {
		return false
	}
	client, err := netip.ParseAddr(r.Header.Get("CF-Connecting-IP"))
	return err == nil && client == allowed
}

// Hobbiton's GET flow is an isolated public-browsing pilot. No customer reads
// or payment calls are made through this handler.
func (s *Server) serveHobbiton(w http.ResponseWriter, r *http.Request) {
	keyOK := len(r.Header.Values("Authorization")) == 1 && len(s.Key) >= 32 && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.Key)) == 1
	if !keyOK && !s.hobbitonSourceAllowed(r) {
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
	if !sessionID.MatchString(id) || !phoneNumber.MatchString(phone) || (kind != "1" && kind != "2") || len(input) > 1 || (input != "" && (input[0] < '0' || input[0] > '9')) {
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
		if kind != "1" {
			s.mu.Unlock()
			output(w, 200, "END Session expired or unavailable. Please dial again.")
			return
		}
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
	if kind == "1" && current.last != "" {
		output(w, 409, "END Session already started. Please dial again.")
		return
	}
	if current.ended {
		output(w, 409, "END Session ended. Please dial again.")
		return
	}
	var response string
	home := "CON Mighty Finance\n1 Loan products\n0 Exit"
	if current.last == "" {
		// requestType=1 opens the menu; initial input may contain the entry selection.
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
