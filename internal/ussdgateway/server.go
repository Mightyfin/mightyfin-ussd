// Package ussdgateway implements Mighty Finance's proposed operator callback v1.
// It is independent of the demo and never authorizes a customer by phone number.
package ussdgateway

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

var sessionID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
var phoneNumber = regexp.MustCompile(`^\+?260[79][0-9]{8}$`)
var serviceCode = regexp.MustCompile(`^[0-9*#]{1,32}$`)
var menuInput = regexp.MustCompile(`^(?:[0-9](?:\*[0-9])*)?$`)

type Reader interface {
	Read(context.Context, string, string) ([]byte, error)
}

type session struct {
	gate      chan struct{}
	binding   [32]byte
	expires   time.Time
	last      string
	ended     bool
	responses map[string]string
}

type Server struct {
	Key      string
	Reader   Reader
	mu       sync.Mutex
	sessions map[string]*session
}

func output(w http.ResponseWriter, status int, text string) {
	w.WriteHeader(status)
	_, _ = io.WriteString(w, text)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.Method == "GET" && r.URL.Path == "/healthz" {
		w.Header().Set("Content-Type", "application/json")
		output(w, 200, `{"status":"ok","service":"ussd-gateway","callback_ready":true,"contract":"mightyfin-v1-proposed","mode":"read-only-pilot","customer_onboarding_ready":false,"financial_actions_enabled":false}`)
		return
	}
	if r.URL.Path != "/ussd" {
		output(w, 404, "END Not found.")
		return
	}
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		output(w, 405, "END Use POST.")
		return
	}
	if len(r.Header.Values("Authorization")) != 1 || len(s.Key) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.Key)) != 1 {
		output(w, 401, "END Operator authentication required.")
		return
	}
	media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if len(r.Header.Values("Content-Type")) != 1 || err != nil || media != "application/x-www-form-urlencoded" || (params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8")) || r.Header.Get("Content-Encoding") != "" {
		output(w, 415, "END Use form-encoded requests.")
		return
	}
	if r.URL.RawQuery != "" {
		output(w, 400, "END Query parameters are not accepted.")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8193))
	if err != nil || len(body) > 8192 {
		output(w, 413, "END Request too large.")
		return
	}
	form, err := url.ParseQuery(string(body))
	if err != nil || len(form) != 4 {
		output(w, 400, "END Invalid callback fields.")
		return
	}
	for _, name := range []string{"sessionId", "phoneNumber", "serviceCode", "text"} {
		if len(form[name]) != 1 {
			output(w, 400, "END Invalid callback fields.")
			return
		}
	}
	id, phone, code, text := form.Get("sessionId"), form.Get("phoneNumber"), form.Get("serviceCode"), form.Get("text")
	token := r.Header.Get("X-LMS-Customer-Token")
	if !sessionID.MatchString(id) || !phoneNumber.MatchString(phone) || !serviceCode.MatchString(code) || len(text) > 128 || !menuInput.MatchString(text) || len(token) > 4096 || !printableToken(token) || len(r.Header.Values("X-LMS-Customer-Token")) > 1 {
		output(w, 400, "END Invalid callback fields.")
		return
	}
	// Do not store numbers or bearer tokens. Session identity is immutable.
	binding := sha256.Sum256([]byte(strings.TrimPrefix(phone, "+") + "\x00" + code + "\x00" + token))
	// Include time queued behind another callback in the request deadline.
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	now := time.Now()
	s.mu.Lock()
	if s.sessions == nil {
		s.sessions = make(map[string]*session)
	}
	for key, old := range s.sessions {
		if now.After(old.expires) {
			delete(s.sessions, key)
		}
	}
	current := s.sessions[id]
	if current == nil {
		if text != "" || len(s.sessions) >= 2000 {
			s.mu.Unlock()
			output(w, 200, "END Session expired or unavailable. Please dial again.")
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
	// A supplied bearer token must belong to this subscriber. Revalidate on
	// status retries too; a cached screen must not bypass token revocation.
	if !current.ended && stateFor(text) == "status" && token != "" {
		if !s.customerMatches(ctx, token, phone) {
			if ctx.Err() != nil {
				output(w, 503, "END Service unavailable. Please try again.")
				return
			}
			response := "END Unable to verify your application status. Please contact your loan officer."
			current.ended = true
			// Remove previously authenticated screens after a failed recheck.
			current.responses = map[string]string{text: response}
			output(w, 200, response)
			return
		}
	}
	if cached, ok := current.responses[text]; ok {
		if current.ended && strings.HasPrefix(cached, "CON ") {
			output(w, 409, "END Session ended. Please dial again.")
			return
		}
		output(w, 200, cached)
		return
	}
	if current.ended || len(current.responses) >= 32 || (current.last != "" && !strings.HasPrefix(text, current.last+"*")) {
		output(w, 409, "END Session sequence changed. Please dial again.")
		return
	}
	// Only one new selection per callback; retries use the cached full path.
	prefix := current.last
	if prefix != "" {
		prefix += "*"
	}
	if text != "" && strings.Contains(strings.TrimPrefix(text, prefix), "*") {
		output(w, 409, "END Send each selection in order.")
		return
	}
	response := s.render(ctx, text, token)
	if ctx.Err() != nil {
		// A timed-out or disconnected read must not consume the selection. The
		// operator can retry the same input after the upstream recovers.
		output(w, 503, "END Service unavailable. Please try again.")
		return
	}
	current.last = text
	current.ended = strings.HasPrefix(response, "END ")
	current.responses[text] = response
	output(w, 200, response)
}

func (s *Server) customerMatches(ctx context.Context, token, subscriber string) bool {
	data, err := s.Reader.Read(ctx, "/v1/me", token)
	if err != nil {
		return false
	}
	var profile struct {
		Data struct {
			ID    string `json:"id"`
			Phone string `json:"phone"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &profile) != nil || !strings.HasPrefix(profile.Data.ID, "user-") {
		return false
	}
	phone := strings.TrimPrefix(strings.TrimSpace(profile.Data.Phone), "+")
	if len(phone) == 10 && phone[0] == '0' {
		phone = "260" + phone[1:]
	}
	if len(phone) == 9 {
		phone = "260" + phone
	}
	return phoneNumber.MatchString(phone) && phone == strings.TrimPrefix(subscriber, "+")
}

func stateFor(text string) string {
	state := "home"
	if text == "" {
		return state
	}
	for _, input := range strings.Split(text, "*") {
		if input == "0" {
			return "exit"
		}
		switch state {
		case "home":
			switch input {
			case "1":
				state = "products"
			case "2":
				state = "status"
			default:
				return "invalid"
			}
		case "products":
			if input != "9" {
				return "invalid"
			}
			state = "home"
		case "status":
			if input != "9" {
				return "invalid"
			}
		default:
			return "invalid"
		}
	}
	return state
}

func (s *Server) render(ctx context.Context, text, token string) string {
	switch stateFor(text) {
	case "home":
		return "CON Mighty Finance\n1 Loan products\n2 Application status\n0 Exit"
	case "exit":
		return "END Thank you for choosing Mighty Finance."
	case "products":
		data, err := s.Reader.Read(ctx, "/v1/products", "")
		if err != nil {
			return "END Loan products are unavailable. Please try again later."
		}
		names, err := productNames(data)
		if err != nil || len(names) == 0 {
			return "END Please contact your loan officer for available products."
		}
		// An informational catalogue only, not an eligibility decision or offer.
		message := "CON Loan products"
		for i, name := range names {
			line := "\n- " + name
			if phoneUnits(message)+phoneUnits(line) > 98 {
				message += "\nMore via your officer"
				break
			}
			message += line
			if i == 2 {
				if len(names) > 3 {
					message += "\nMore via your officer"
				}
				break
			}
		}
		return message + "\nSubject to eligibility.\n9 Back\n0 Exit"
	case "status":
		if token == "" {
			return "END Secure customer sign-in is required. Contact your loan officer for application status."
		}
		data, err := s.Reader.Read(ctx, "/v1/journey/screen", token)
		if err != nil {
			return "END Unable to verify your application status. Please contact your loan officer."
		}
		var screen struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(data, &screen) != nil || screen.Text == "" || phoneUnits(screen.Text) > 156 || !safeScreen(screen.Text) {
			return "END Application status is unavailable. Please try again later."
		}
		return "CON " + screen.Text
	default:
		return "END Invalid selection. Please dial again."
	}
}

func printableToken(token string) bool {
	for _, c := range token {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

func safeScreen(text string) bool {
	for _, c := range text {
		if c != '\n' && (c < 32 || c > 126 || c == '`') {
			return false
		}
	}
	return true
}

// GSM extension-table characters occupy two septets even though they are ASCII.
func phoneUnits(text string) int {
	n := 0
	for _, c := range text {
		n++
		if strings.ContainsRune("^{}\\[~]|", c) {
			n++
		}
	}
	return n
}

func productNames(data []byte) ([]string, error) {
	var catalogue struct {
		Data []struct {
			Categories []struct {
				Products []struct {
					Name string `json:"name"`
				} `json:"products"`
			} `json:"categories"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &catalogue); err != nil {
		return nil, err
	}
	names := []string{}
	for _, group := range catalogue.Data {
		for _, category := range group.Categories {
			for _, product := range category.Products {
				var b strings.Builder
				for _, c := range product.Name {
					if c >= 32 && c <= 126 && c != '`' {
						b.WriteRune(c)
					}
				}
				name := strings.TrimSpace(b.String())
				if len(name) > 32 {
					name = name[:29] + "..."
				}
				if name != "" {
					names = append(names, name)
				}
			}
		}
	}
	return names, nil
}

// BridgeReader only permits the read-only resources used by this pilot.
type BridgeReader struct {
	Key  string
	HTTP *http.Client
}

func (b *BridgeReader) Read(ctx context.Context, path, token string) ([]byte, error) {
	if path != "/v1/products" && path != "/v1/journey/screen" && path != "/v1/me" {
		return nil, fmt.Errorf("unsupported resource")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:28091"+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-USSD-Service-Key", b.Key)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := b.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("adapter unavailable")
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 2*1024*1024+1))
	if err != nil || res.StatusCode != 200 || len(data) > 2*1024*1024 {
		return nil, fmt.Errorf("adapter unavailable")
	}
	return data, nil
}
