package lmsbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

type Server struct {
	Client           *Client
	Journal          *Journal
	ServiceKey       string
	Writes, Payments bool
}
type route struct {
	path                     string
	customer, write, payment bool
}

func resolve(method, path string) (route, bool) {
	if method == "GET" {
		switch path {
		case "/v1/products":
			return route{path: "/loans/config"}, true
		case "/v1/me":
			return route{path: "/users/profile", customer: true}, true
		case "/v1/applications/current":
			return route{path: "/loan-applications/current", customer: true}, true
		case "/v1/loans":
			return route{path: "/loans", customer: true}, true
		case "/v1/journey":
			return route{customer: true}, true
		case "/v1/kyc/status":
			return route{path: "/kyc/status", customer: true}, true
		}
	}
	if method == "POST" {
		switch path {
		case "/v1/quote":
			return route{path: "/loans/quote"}, true
		case "/v1/identity/session":
			return route{path: "/didit/session", customer: true, write: true}, true
		case "/v1/applications":
			return route{path: "/loan-applications", customer: true, write: true}, true
		case "/v1/repayments/quote":
			return route{path: "/payments/early-repayment-calculation", customer: true}, true
		case "/v1/repayments":
			return route{customer: true, write: true, payment: true}, true
		}
	}
	parts := strings.Split(strings.TrimPrefix(path, "/v1/"), "/")
	if !strings.HasPrefix(path, "/v1/") || len(parts) < 2 || !identifier.MatchString(parts[1]) {
		return route{}, false
	}
	id := parts[1]
	if parts[0] == "applications" {
		if method == "PATCH" && len(parts) == 2 {
			return route{"/loan-applications/" + id, true, true, false}, true
		}
		if len(parts) == 3 && parts[2] == "submit" && method == "POST" {
			return route{"/loan-applications/" + id + "/submit", true, true, false}, true
		}
		if len(parts) == 3 && parts[2] == "assessment" && method == "GET" {
			return route{"/loan-applications/" + id + "/assessment", true, false, false}, true
		}
	}
	if parts[0] == "loans" && method == "GET" {
		if len(parts) == 2 {
			return route{"/loans/" + id, true, false, false}, true
		}
		if len(parts) == 3 && parts[2] == "schedule" {
			return route{"/loans/" + id + "/repayment-schedule", true, false, false}, true
		}
		if len(parts) == 3 && parts[2] == "payments" {
			return route{"/payments/history/" + id, true, false, false}, true
		}
	}
	return route{}, false
}
func reply(w http.ResponseWriter, status int, body string) {
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == "GET" && r.URL.Path == "/healthz" {
		reply(w, 200, `{"status":"ok","mode":"lms-adapter"}`)
		return
	}
	if len(s.ServiceKey) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-USSD-Service-Key")), []byte(s.ServiceKey)) != 1 {
		reply(w, 401, `{"error":"SERVICE_AUTH_REQUIRED"}`)
		return
	}
	if r.URL.Path == "/v1/kyc/status" {
		reply(w, 503, `{"error":"READ_ONLY_KYC_STATUS_UNAVAILABLE","message":"Use the journey endpoint for document requirements."}`)
		return
	}
	target, ok := resolve(r.Method, r.URL.Path)
	if !ok {
		reply(w, 404, `{"error":"NOT_FOUND"}`)
		return
	}
	if target.write && !s.Writes || target.payment && !s.Payments {
		reply(w, 503, `{"error":"ACTION_DISABLED"}`)
		return
	}
	// Never expose the legacy early-repayment endpoint: it posts a repayment
	// without a verified provider collection. This capability is explicitly absent.
	if target.payment {
		reply(w, 501, `{"error":"PAYMENT_COLLECTION_NOT_INTEGRATED"}`)
		return
	}
	// No caller-supplied user selectors or arbitrary upstream URLs.
	if r.URL.RawQuery != "" {
		q := r.URL.Query()
		if r.Method != "GET" || r.URL.Path != "/v1/loans" {
			reply(w, 400, `{"error":"QUERY_NOT_ALLOWED"}`)
			return
		}
		for k, values := range q {
			n, e := strconv.Atoi(values[0])
			if (k != "page" && k != "perPage") || len(values) != 1 || e != nil || n < 1 || n > 100 {
				reply(w, 400, `{"error":"INVALID_PAGINATION"}`)
				return
			}
		}
		target.path += "?" + q.Encode()
	}
	var body []byte
	if r.Method == "POST" || r.Method == "PATCH" {
		b, e := io.ReadAll(io.LimitReader(r.Body, 32769))
		if e != nil || len(b) > 32768 {
			reply(w, 413, `{"error":"BODY_TOO_LARGE"}`)
			return
		}
		if len(b) == 0 {
			b = []byte(`{}`)
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(b, &fields) != nil || fields == nil {
			reply(w, 400, `{"error":"JSON_OBJECT_REQUIRED"}`)
			return
		}
		for _, k := range []string{"user_id", "customer_id", "account_id", "tenant_id", "approved_by", "status", "complete", "submitted_at"} {
			if _, ok := fields[k]; ok {
				reply(w, 400, `{"error":"IDENTITY_OR_STATE_OVERRIDE_FORBIDDEN"}`)
				return
			}
		}
		body, _ = json.Marshal(fields)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 14*time.Second)
	defer cancel()
	token, user := "", ""
	if target.customer {
		auth := strings.Fields(r.Header.Get("Authorization"))
		if len(auth) != 2 || !strings.EqualFold(auth[0], "Bearer") {
			reply(w, 401, `{"error":"LMS_CUSTOMER_TOKEN_REQUIRED"}`)
			return
		}
		token = auth[1]
		var identity Result
		var e error
		user, identity, e = s.Client.Identity(ctx, token)
		if e != nil {
			reply(w, 503, `{"error":"LMS_UNAVAILABLE"}`)
			return
		}
		if user == "" {
			status := 401
			if identity.Status == 403 {
				status = 403
			}
			reply(w, status, `{"error":"CUSTOMER_AUTH_REJECTED"}`)
			return
		}
		if r.URL.Path == "/v1/me" {
			w.WriteHeader(identity.Status)
			_, _ = w.Write(identity.Body)
			return
		}
	}
	if r.URL.Path == "/v1/journey" {
		result, e := s.Journey(ctx, token)
		if e != nil {
			reply(w, 503, `{"error":"JOURNEY_UNAVAILABLE"}`)
			return
		}
		w.WriteHeader(result.Status)
		_, _ = w.Write(result.Body)
		return
	}
	if r.URL.Path == "/v1/identity/session" && r.Header.Get("X-USSD-Confirmation") != "verify_identity" {
		reply(w, 400, `{"error":"EXPLICIT_IDENTITY_CONFIRMATION_REQUIRED"}`)
		return
	}
	submitting := r.Method == "POST" && strings.HasSuffix(target.path, "/submit")
	if submitting && (r.Header.Get("X-USSD-Confirmation") != "submit" || len(r.Header.Get("X-USSD-Journey-Version")) != 64) {
		reply(w, 400, `{"error":"EXPLICIT_SUBMISSION_CONFIRMATION_REQUIRED"}`)
		return
	}
	send := func() (Result, error) {
		if submitting {
			validation, e := s.confirmSubmission(ctx, token, target.path, r.Header.Get("X-USSD-Journey-Version"))
			if e != nil || validation.Status != 0 {
				return validation, e
			}
		}
		return s.Client.Call(ctx, r.Method, target.path, token, body)
	}
	var result Result
	var err error
	if target.write {
		key := r.Header.Get("Idempotency-Key")
		if !identifier.MatchString(key) {
			reply(w, 400, `{"error":"IDEMPOTENCY_KEY_REQUIRED"}`)
			return
		}
		if s.Journal == nil {
			reply(w, 503, `{"error":"JOURNAL_UNAVAILABLE"}`)
			return
		}
		sum := sha256.Sum256(bytes.Join([][]byte{[]byte(r.Method), []byte(target.path), body, []byte(r.Header.Get("X-USSD-Journey-Version"))}, []byte{0}))
		result, err = s.Journal.Do(user+":"+key, hex.EncodeToString(sum[:]), send)
	} else {
		result, err = send()
	}
	if err != nil {
		reply(w, 503, `{"error":"LMS_UNAVAILABLE","message":"Do not blindly retry a write with a new request key; check its outcome first."}`)
		return
	}
	w.WriteHeader(result.Status)
	_, _ = w.Write(result.Body)
}
