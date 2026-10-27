package lmsbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const serviceKey = "test-service-key-that-is-at-least-32-bytes"

func fixture(t *testing.T) (*Server, *atomic.Int32) {
	t.Helper()
	writes := &atomic.Int32{}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/users/profile" {
			switch r.Header.Get("Authorization") {
			case "Bearer alice":
				fmt.Fprint(w, `{"data":{"id":"user-1"}}`)
			case "Bearer bob":
				fmt.Fprint(w, `{"data":{"id":"user-2"}}`)
			default:
				w.WriteHeader(401)
				fmt.Fprint(w, `{"error":"unauthenticated"}`)
			}
			return
		}
		if strings.Contains(r.URL.Path, "/loan-applications/44") && r.Header.Get("Authorization") == "Bearer bob" {
			w.WriteHeader(403)
			fmt.Fprint(w, `{"message":"Forbidden"}`)
			return
		}
		switch r.URL.Path {
		case "/api/v1/loans/config":
			fmt.Fprint(w, `{"success":true,"data":[{"id":7,"name":"LMS product"}]}`)
		case "/api/v1/loans/quote":
			w.WriteHeader(422)
			fmt.Fprint(w, `{"message":"Validation failed","errors":{"loanAmount":["Required"]}}`)
		case "/api/v1/loan-applications":
			writes.Add(1)
			w.WriteHeader(201)
			fmt.Fprint(w, `{"application":{"id":44,"status":"draft"}}`)
		case "/api/v1/loan-applications/44/submit":
			writes.Add(1)
			w.WriteHeader(422)
			fmt.Fprint(w, `{"status":"incomplete","missing_documents":["nrc_front"]}`)
		case "/api/v1/loan-applications/99/submit":
			writes.Add(1)
			w.WriteHeader(502)
			fmt.Fprint(w, `{"secret":"do not disclose"}`)
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	}))
	t.Cleanup(upstream.Close)
	client, e := NewClient(upstream.URL + "/api/v1")
	if e != nil {
		t.Fatal(e)
	}
	client.http.Transport = upstream.Client().Transport
	j, e := OpenJournal(filepath.Join(t.TempDir(), "requests.enc"), bytes.Repeat([]byte{1}, 32))
	if e != nil {
		t.Fatal(e)
	}
	return &Server{client, j, serviceKey, true, false}, writes
}
func request(s *Server, method, path, token, key, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-USSD-Service-Key", serviceKey)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestDelegatesProductsAndValidation(t *testing.T) {
	s, _ := fixture(t)
	w := request(s, "GET", "/v1/products", "", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "LMS product") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(s, "POST", "/v1/quote", "", "", `{}`)
	if w.Code != 422 {
		t.Fatal(w.Code)
	}
	w = request(s, "POST", "/v1/applications/44/submit", "alice", "submit-44", `{}`)
	if w.Code != 422 || !strings.Contains(w.Body.String(), "missing_documents") {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestAuthOwnershipAndGates(t *testing.T) {
	s, _ := fixture(t)
	for _, c := range []struct {
		method, path, token, body string
		status                    int
	}{
		{"GET", "/v1/me", "invalid", "", 401},
		{"GET", "/v1/me", "", "", 401},
		{"GET", "/v1/applications/44/assessment", "bob", "", 403},
		{"POST", "/v1/applications", "alice", `{"user_id":2}`, 400},
		{"GET", "/v1/loans?user_id=2", "alice", "", 400},
		{"GET", "/v1/integrations/loan-applications", "alice", "", 404},
		{"POST", "/v1/repayments", "alice", `{}`, 503},
	} {
		w := request(s, c.method, c.path, c.token, "req", c.body)
		if w.Code != c.status {
			t.Fatalf("%s: %d %s", c.path, w.Code, w.Body.String())
		}
	}
	s.Writes = false
	if w := request(s, "POST", "/v1/applications", "alice", "create", `{}`); w.Code != 503 {
		t.Fatal(w.Code)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/v1/products", nil))
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}
func TestConcurrentReplayAndConflict(t *testing.T) {
	s, writes := fixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := request(s, "POST", "/v1/applications", "alice", "same-key", `{"loan_product_id":7}`)
			if w.Code != 201 {
				t.Errorf("%d %s", w.Code, w.Body.String())
			}
		}()
	}
	wg.Wait()
	if writes.Load() != 1 {
		t.Fatal("duplicate upstream writes", writes.Load())
	}
	w := request(s, "POST", "/v1/applications", "alice", "same-key", `{"loan_product_id":8}`)
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	// A different authenticated customer has an independent key namespace.
	w = request(s, "POST", "/v1/applications", "bob", "same-key", `{"loan_product_id":7}`)
	if w.Code != 201 || writes.Load() != 2 {
		t.Fatal(w.Code, writes.Load())
	}
}
func TestUncertainWriteSurvivesRestartWithoutResend(t *testing.T) {
	s, writes := fixture(t)
	w := request(s, "POST", "/v1/applications/99/submit", "alice", "uncertain", `{}`)
	if w.Code != 503 || strings.Contains(w.Body.String(), "secret") {
		t.Fatal(w.Code, w.Body.String())
	}
	raw, e := os.ReadFile(s.Journal.path)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(raw, []byte("uncertain")) {
		t.Fatal("journal stored plaintext")
	}
	j, e := OpenJournal(s.Journal.path, bytes.Repeat([]byte{1}, 32))
	if e != nil {
		t.Fatal(e)
	}
	s.Journal = j
	w = request(s, "POST", "/v1/applications/99/submit", "alice", "uncertain", `{}`)
	if w.Code != 409 || writes.Load() != 1 {
		t.Fatal(w.Code, writes.Load())
	}
}
func TestCompletedReplaySurvivesRestart(t *testing.T) {
	s, writes := fixture(t)
	a := request(s, "POST", "/v1/applications", "alice", "done", `{}`)
	j, e := OpenJournal(s.Journal.path, bytes.Repeat([]byte{1}, 32))
	if e != nil {
		t.Fatal(e)
	}
	s.Journal = j
	b := request(s, "POST", "/v1/applications", "alice", "done", `{}`)
	if a.Code != 201 || b.Code != 201 || a.Body.String() != b.Body.String() || writes.Load() != 1 {
		t.Fatal("replay mismatch")
	}
	if _, e = OpenJournal(s.Journal.path, bytes.Repeat([]byte{2}, 32)); e == nil {
		t.Fatal("accepted wrong encryption key")
	}
}
func TestRejectUnsafeConfigAndRedirects(t *testing.T) {
	for _, u := range []string{"http://admin.test/api/v1", "https://u:p@admin.test/api/v1", "https://admin.test/api/v1?token=x", "https://admin.test/"} {
		if _, e := NewClient(u); e == nil {
			t.Fatal("accepted", u)
		}
	}
	targetCalls := 0
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls++ }))
	defer target.Close()
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redirect.Close()
	c, _ := NewClient(redirect.URL + "/api/v1")
	c.http.Transport = redirect.Client().Transport
	_, e := c.Call(context.Background(), "GET", "/users/profile", "alice", nil)
	if e == nil || targetCalls != 0 {
		t.Fatal("redirect followed")
	}
}
func TestJournalFailurePreventsWrite(t *testing.T) {
	s, writes := fixture(t)
	s.Journal.path = filepath.Join(t.TempDir(), "missing", "journal")
	w := request(s, "POST", "/v1/applications", "alice", "blocked", `{}`)
	if w.Code != 503 || writes.Load() != 0 {
		t.Fatal(w.Code, writes.Load())
	}
}
func TestResponsesAreJSON(t *testing.T) {
	s, _ := fixture(t)
	w := request(s, "GET", "/v1/products", "", "", "")
	if !json.Valid(w.Body.Bytes()) {
		t.Fatal("invalid JSON")
	}
}

func TestPaymentFlagCannotEnableDirectLedgerPosting(t *testing.T) {
	s, writes := fixture(t)
	s.Payments = true
	w := request(s, "POST", "/v1/repayments", "alice", "payment", `{"loanId":"44","repaymentAmount":"10.00"}`)
	if w.Code != 501 || writes.Load() != 0 {
		t.Fatal(w.Code, writes.Load())
	}
}
