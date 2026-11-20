package ussdgateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Mightyfin/mightyfin-ussd/internal/nyali"
)

// Route the real fixed-URL Nyali client to a TLS contract fixture. This tests
// handset callback -> HTTP client -> partner JSON -> durable payment -> posting.
type nyaliFixtureTransport struct {
	target *url.URL
	inner  http.RoundTripper
}

func (f nyaliFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c := r.Clone(r.Context())
	u := *r.URL
	c.URL = &u
	c.URL.Scheme, c.URL.Host = f.target.Scheme, f.target.Host
	return f.inner.RoundTrip(c)
}
func TestNyaliHTTPThroughHandsetAndPosting(t *testing.T) {
	var mu sync.Mutex
	posts := 0
	posted := false
	ref := ""
	var paidAmount json.Number
	verifyCalls := 0
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer fixture-nyali-service-token" {
			t.Error("missing partner bearer")
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /public/api/partner/loans/verify":
			var b map[string]string
			if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b["phone"] != "260970000001" {
				t.Error("incorrect borrower identity", b, err)
			}
			verifyCalls++
			loans := []any{}
			if !posted {
				loans = append(loans, map[string]any{"loan_id": 42, "status": "disbursed", "outstanding": map[string]any{"total": "1000.00", "arrears": "50.00"}})
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "clients": []any{map[string]any{"client_id": 3, "name": "George Fixture", "mobile": "0970000001", "loans": loans}}})
		case "POST /public/api/partner/loans/42/collect":
			var b struct {
				Amount json.Number `json:"amount"`
				Phone  string      `json:"msisdn"`
				Ref    string      `json:"external_reference"`
			}
			if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
				t.Error(err)
			}
			if b.Phone != "260970000002" || b.Amount != "20.00" || !strings.HasPrefix(b.Ref, "USSD-") {
				t.Error("collection payload", b)
			}
			posts++
			ref = b.Ref
			paidAmount = b.Amount
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "loan_id": 42, "reference": "fixture-provider-reference", "external_reference": ref, "status": "Pending", "amount": paidAmount, "msisdn": b.Phone, "posted": false})
		case "GET /public/api/partner/payments":
			if r.URL.Query().Get("limit") != "500" {
				t.Error("incorrect payment limit")
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "count": 1, "payments": []any{map[string]any{"reference": "fixture-provider-reference", "external_reference": ref, "loan_id": 42, "amount": paidAmount, "msisdn": "260970000002", "currency": "ZMW", "status": "Successful", "posted": posted}}})
		default:
			t.Error("unexpected partner route", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	httpClient := upstream.Client()
	httpClient.Transport = nyaliFixtureTransport{target: target, inner: httpClient.Transport}
	api, err := nyali.New("fixture-nyali-service-token", httpClient)
	if err != nil {
		t.Fatal(err)
	}
	store, err := nyali.OpenStore(filepath.Join(t.TempDir(), "payments.enc"), []byte(strings.Repeat("x", 32)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p := &NyaliPayments{Client: api, Store: store}
	s := &Server{Key: testKey, Payments: p, Leads: &testLeads{}, Reader: &fakeReader{}}
	for _, step := range []struct{ answer, kind, want, flow string }{
		{"29", "1", "Welcome George", "FC"}, {"1", "2", "Payment number", "FC"}, {"2", "2", "Enter the mobile-money number", "FC"},
		{"0970000002", "2", "260970000002", "FC"}, {"1", "2", "Due K50.00", "FC"}, {"1", "2", "Maximum K1000.00", "FC"},
		{"20", "2", "Pay ZMW 20.00", "FC"}, {"1", "2", "Please wait for the payment prompt.", "FB"}, {"1", "2", "Please wait for the payment prompt.", "FB"},
	} {
		w := hobby(s, "nyali-http-real-client", "0970000001", step.answer, step.kind)
		if w.Code != 200 || w.Header().Get("FreeFlow") != step.flow || !strings.Contains(w.Body.String(), step.want) {
			t.Fatal(step, w.Code, w.Body.String())
		}
	}
	status, err := p.Status(context.Background(), "260970000001", 42)
	if err != nil || status != "posting_pending" {
		t.Fatal(status, err)
	}
	mu.Lock()
	posted = true
	mu.Unlock()
	status, err = p.Status(context.Background(), "260970000001", 42)
	if err != nil || status != "completed" {
		t.Fatal(status, err)
	}
	// The paid loan disappeared from open loans, but its receipt must remain.
	loans, err := p.Loans(context.Background(), "260970000001", true)
	if err != nil || len(loans) != 1 || loans[0].ID != 42 {
		t.Fatal(loans, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 || verifyCalls < 4 {
		t.Fatal("collection or ownership checks", posts, verifyCalls)
	}
}
