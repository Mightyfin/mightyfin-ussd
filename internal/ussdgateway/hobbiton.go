package ussdgateway

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
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
		hobbitonOutput(w, 401, "END Operator authentication required.")
		return
	}
	if len(r.URL.RawQuery) > 2048 {
		hobbitonOutput(w, 414, "END Request too large.")
		return
	}
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.Header.Get("Content-Encoding") != "" || len(r.Header.Values("X-LMS-Customer-Token")) != 0 {
		hobbitonOutput(w, 400, "END Invalid callback fields.")
		return
	}
	raw, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(raw) != 4 {
		hobbitonOutput(w, 400, "END Invalid callback fields.")
		return
	}
	// Normalize field names while rejecting ambiguous aliases such as input+INPUT.
	q := make(url.Values)
	for name, values := range raw {
		key := strings.ToLower(name)
		if _, exists := q[key]; exists {
			hobbitonOutput(w, 400, "END Invalid callback fields.")
			return
		}
		q[key] = values
	}
	for _, name := range []string{"msisdn", "session_id", "input", "requesttype"} {
		if len(q[name]) != 1 {
			hobbitonOutput(w, 400, "END Invalid callback fields.")
			return
		}
	}
	id, phone, input, kind := q.Get("session_id"), q.Get("msisdn"), q.Get("input"), q.Get("requesttype")
	phone = strings.TrimPrefix(phone, "+")
	if len(phone) == 10 && phone[0] == '0' {
		phone = "260" + phone[1:]
	}
	// Initial input is the gateway's entry selection (e.g. 29), not a menu answer.
	inputLimit := 13
	if kind == "1" {
		inputLimit = 32
	}
	validInput := len(input) <= inputLimit
	navigation := kind == "2" && (input == "*" || input == "#")
	for _, c := range input {
		if !navigation && (c < '0' || c > '9') && c != '.' {
			validInput = false
		}
	}
	if kind == "1" && strings.Contains(input, ".") {
		validInput = false
	}
	if kind == "2" && !navigation && !moneyInput.MatchString(input) && normalizePaymentPhone(input) == "" {
		validInput = false
	}
	if kind == "2" && s.Leads != nil {
		validInput = len(input) > 0 && len(input) <= 160 && utf8.ValidString(input)
		for _, c := range input {
			if unicode.IsControl(c) {
				validInput = false
			}
		}
	}
	if !sessionID.MatchString(id) || !phoneNumber.MatchString(phone) || (kind != "1" && kind != "2") || !validInput {
		hobbitonOutput(w, 400, "END Invalid callback fields.")
		return
	}
	// Operational metadata only: never log phone numbers, input contents, URLs or keys.
	log.Printf("Hobbiton callback: request_type=%s input_length=%d", kind, len(input))
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
			hobbitonOutput(w, 200, "END Session expired or unavailable. Please dial again.")
			return
		}
		if len(s.sessions) >= 2000 {
			s.mu.Unlock()
			hobbitonOutput(w, 503, "END Service busy. Please dial again.")
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
		hobbitonOutput(w, 503, "END Service busy. Please dial again.")
		return
	}
	if ctx.Err() != nil {
		hobbitonOutput(w, 503, "END Service busy. Please dial again.")
		return
	}
	if time.Now().After(current.expires) {
		hobbitonOutput(w, 200, "END Session expired. Please dial again.")
		return
	}
	if current.binding != binding {
		hobbitonOutput(w, 409, "END Session identity changed. Please dial again.")
		return
	}
	// Initial/terminal callbacks replay safely. Active menus must process repeated
	// answers: selecting product menu 1 followed by product 1 is legitimate.
	// With no gateway sequence ID, active repeats cannot be identified as retries.
	fingerprint := kind + ":" + input
	if cached, ok := current.responses[fingerprint]; ok && (kind == "1" || current.ended) {
		hobbitonOutput(w, 200, cached)
		return
	}
	if kind == "1" && current.last != "" {
		hobbitonOutput(w, 409, "END Session already started. Please dial again.")
		return
	}
	if current.ended {
		hobbitonOutput(w, 409, "END Session ended. Please dial again.")
		return
	}
	next := *current
	next.hobbySessionID = id
	response := s.hobbitonMenu(ctx, &next, phone, input)
	if ctx.Err() != nil && !(next.last == "payment" && strings.HasPrefix(response, "END ")) {
		hobbitonOutput(w, 503, "END Service unavailable. Please try again.")
		return
	}
	current.last = next.last
	current.hobbyNames = next.hobbyNames
	current.hobbyPage = next.hobbyPage
	current.hobbyWelcome = next.hobbyWelcome
	current.hobbyPaymentPath = next.hobbyPaymentPath
	current.hobbyHasPayments = next.hobbyHasPayments
	current.leadEnabled = next.leadEnabled
	current.leadRecord = next.leadRecord
	current.leadProducts = next.leadProducts
	current.leadPage = next.leadPage
	current.leadProductName = next.leadProductName
	current.hobbySessionID = next.hobbySessionID
	current.payLoans = next.payLoans
	current.payPage = next.payPage
	current.hobbyPayerPhone = next.hobbyPayerPhone
	current.ended = strings.HasPrefix(response, "END ")
	current.responses = map[string]string{fingerprint: response}
	hobbitonOutput(w, 200, response)
}

// Hobbiton renders the body verbatim. Keep control markers in internal session
// state, and remove them only at this transport boundary (including retries/errors).
func hobbitonOutput(w http.ResponseWriter, status int, text string) {
	// Hobbiton controls the handset session through this header, not body prefixes.
	flow := "FB"
	if status == http.StatusOK && strings.HasPrefix(text, "CON ") {
		flow = "FC"
	}
	w.Header().Set("FreeFlow", flow)
	screen := "other"
	if strings.HasPrefix(text, "CON Welcome") {
		screen = "home"
	}
	if strings.HasPrefix(text, "CON Loan products") {
		screen = "products"
	}
	log.Printf("Hobbiton response: status=%d screen=%s terminal=%t", status, screen, strings.HasPrefix(text, "END "))
	text = strings.TrimPrefix(strings.TrimPrefix(text, "CON "), "END ")
	output(w, status, text)
}
