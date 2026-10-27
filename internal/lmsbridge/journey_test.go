package lmsbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJourneyStates(t *testing.T) {
	empty := Result{404, json.RawMessage(`{"status":"empty","data":null}`)}
	noLoans := Result{200, json.RawMessage(`{"data":[],"meta":{"last_page":1},"links":{"next":null}}`)}
	cases := []struct {
		name, app, loans, state, action string
		confirm                         bool
	}{
		{"new", "", ``, "no_active_journey", "view_products", false},
		{"resume", `{"application":{"id":44,"status":"draft"},"application_completion":{"is_complete":false,"missing_profile_fields":["dob"],"missing_documents":["nrc"]}}`, "", "draft", "continue_application", false},
		{"documents", `{"application":{"id":44,"status":"draft"},"application_completion":{"is_complete":false,"missing_profile_fields":[],"missing_documents":["nrc"]}}`, "", "documents_required", "document_handoff", false},
		{"ready", `{"application":{"id":44,"status":"draft"},"application_completion":{"is_complete":true,"missing_profile_fields":[],"missing_documents":[]}}`, "", "ready_for_submission", "review_submission", true},
		{"inconsistent", `{"application":{"id":44,"status":"draft"},"application_completion":{"is_complete":true,"missing_profile_fields":["dob"],"missing_documents":[]}}`, "", "draft", "continue_application", false},
		{"review", `{"application":{"id":44,"status":"pending"}}`, "", "under_review", "view_status", false},
		{"approved_not_offer", `{"application":{"id":44,"status":"approved"}}`, "", "approved", "view_status", false},
		{"active", "", `{"data":[{"status":"active","amountRemaining":"100.50"}]}`, "loan_active", "view_loans", false},
		{"unknown_balance", "", `{"data":[{"status":"active","amountRemaining":null}]}`, "account_review", "view_loans", false},
		{"unread_page", "", `{"data":[],"meta":{"last_page":2}}`, "account_review", "view_loans", false},
		{"unknown_state", "", `{"data":[{"status":"unmapped","amountRemaining":"0"}]}`, "account_review", "view_loans", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, l := empty, noLoans
			if c.app != "" {
				a = Result{200, json.RawMessage(c.app)}
			}
			if c.loans != "" {
				l = Result{200, json.RawMessage(c.loans)}
			}
			j, e := planJourney(a, l, true)
			if e != nil || j.State != c.state || j.Action.Code != c.action || j.Action.ConfirmationRequired != c.confirm {
				t.Fatalf("%+v %v", j, e)
			}
			if len(j.Version) != 64 {
				t.Fatal("missing version")
			}
		})
	}
}
func TestJourneyDoesNotCallMutatingStatusOrAssessment(t *testing.T) {
	paths := []string{}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/v1/users/profile":
			w.Write([]byte(`{"data":{"id":"user-1"}}`))
		case "/api/v1/loan-applications/current":
			w.Write([]byte(`{"application":{"id":44,"status":"pending"}}`))
		case "/api/v1/loans":
			w.Write([]byte(`{"data":[]}`))
		default:
			t.Errorf("unexpected call %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer upstream.Close()
	c, _ := NewClient(upstream.URL + "/api/v1")
	c.http.Transport = upstream.Client().Transport
	s := &Server{Client: c, ServiceKey: serviceKey}
	w := request(s, "GET", "/v1/journey", "alice", "", "")
	if w.Code != 200 || len(paths) != 3 {
		t.Fatal(w.Code, paths, w.Body.String())
	}
	w = request(s, "GET", "/v1/kyc/status", "alice", "", "")
	if w.Code != 503 || len(paths) != 3 {
		t.Fatal("legacy mutation endpoint called")
	}
}
func TestSubmissionRequiresConfirmationAndFreshSnapshot(t *testing.T) {
	s, writes := fixture(t)
	r := httptest.NewRequest("POST", "/v1/applications/44/submit", strings.NewReader(`{}`))
	r.Header.Set("X-USSD-Service-Key", serviceKey)
	r.Header.Set("Authorization", "Bearer alice")
	r.Header.Set("Idempotency-Key", "reviewed")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 400 || writes.Load() != 0 {
		t.Fatal(w.Code, writes.Load())
	}
	r = httptest.NewRequest("POST", "/v1/applications/44/submit", strings.NewReader(`{}`))
	r.Header.Set("X-USSD-Service-Key", serviceKey)
	r.Header.Set("Authorization", "Bearer alice")
	r.Header.Set("Idempotency-Key", "stale")
	r.Header.Set("X-USSD-Confirmation", "submit")
	r.Header.Set("X-USSD-Journey-Version", strings.Repeat("0", 64))
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 409 || writes.Load() != 0 {
		t.Fatal(w.Code, writes.Load())
	}
}
func TestIncompleteApplicationCannotSubmit(t *testing.T) {
	body := []byte(`{"application":{"id":44,"status":"draft"},"application_completion":{"is_complete":false,"missing_documents":["nrc"]}}`)
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer up.Close()
	s, _ := fixture(t)
	s.Client, _ = NewClient(up.URL + "/api/v1")
	s.Client.http.Transport = up.Client().Transport
	hash := sha256.Sum256(body)
	result, e := s.confirmSubmission(context.Background(), "alice", "/loan-applications/44/submit", hex.EncodeToString(hash[:]))
	if e != nil || result.Status != 422 {
		t.Fatal(result, e)
	}
}
func TestIdentitySessionNeedsConfirmationAndReplays(t *testing.T) {
	calls := 0
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/users/profile" {
			w.Write([]byte(`{"data":{"id":"user-1"}}`))
			return
		}
		if r.URL.Path != "/api/v1/didit/session" || r.Method != "POST" {
			t.Error(r.Method, r.URL.Path)
		}
		calls++
		w.Write([]byte(`{"success":true,"already_verified":true,"url":null}`))
	}))
	defer up.Close()
	s, _ := fixture(t)
	s.Client, _ = NewClient(up.URL + "/api/v1")
	s.Client.http.Transport = up.Client().Transport
	w := request(s, "POST", "/v1/identity/session", "alice", "kyc", `{}`)
	if w.Code != 400 || calls != 0 {
		t.Fatal(w.Code, calls)
	}
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("POST", "/v1/identity/session", strings.NewReader(`{"verificationType":"kyc"}`))
		r.Header.Set("X-USSD-Service-Key", serviceKey)
		r.Header.Set("Authorization", "Bearer alice")
		r.Header.Set("X-USSD-Confirmation", "verify_identity")
		r.Header.Set("Idempotency-Key", "kyc-confirmed")
		w = httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if calls != 1 {
		t.Fatal("duplicate provider sessions", calls)
	}
}
