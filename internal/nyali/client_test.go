package nyali

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewTLSServer(h)
	t.Cleanup(s.Close)
	c, e := New("fixture-only-not-a-real-key", s.Client())
	if e != nil {
		t.Fatal(e)
	}
	c.base = s.URL + "/public/api/partner"
	c.interval = 0
	return c
}
func TestPartnerContractRequests(t *testing.T) {
	calls := map[string]int{}
	charges := 0
	seen := map[string]bool{}
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-only-not-a-real-key" {
			t.Error("missing bearer")
		}
		w.Header().Set("Content-Type", "application/json")
		calls[r.Method+" "+r.URL.Path]++
		switch r.URL.Path {
		case "/public/api/partner/loans/verify":
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			if b["phone"] != "260970000001" {
				t.Error(b)
			}
			w.Write([]byte(`{"ok":true,"clients":[{"client_id":3,"name":"Fixture Person","mobile":"0970000001","loans":[]}]}`))
		case "/public/api/partner/loans/42/collect":
			var b struct {
				Amount json.Number `json:"amount"`
				Phone  string      `json:"msisdn"`
				Ref    string      `json:"external_reference"`
			}
			json.NewDecoder(r.Body).Decode(&b)
			if b.Amount != "25.50" || b.Phone != "260970000002" {
				t.Error(b)
			}
			if !seen[b.Ref] {
				charges++
				seen[b.Ref] = true
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "loan_id": 42, "reference": "provider-fixture", "external_reference": b.Ref, "status": "Pending", "amount": b.Amount, "msisdn": b.Phone, "posted": false})
		case "/public/api/partner/payments":
			if r.URL.Query().Get("limit") != "500" {
				t.Error(r.URL.RawQuery)
			}
			w.Write([]byte(`{"ok":true,"count":0,"payments":[]}`))
		default:
			t.Error(r.URL.Path)
			w.WriteHeader(404)
		}
	})
	if _, e := c.Verify(context.Background(), "+260 970 000 001"); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if _, e := c.Collect(context.Background(), 42, "0970000002", "25.5", "USSD-fixture-0001"); e != nil {
			t.Fatal(e)
		}
	}
	if charges != 1 || calls["POST /public/api/partner/loans/42/collect"] != 2 {
		t.Fatal(charges, calls)
	}
	if _, e := c.Payments(context.Background(), "2026-11-20"); e != nil {
		t.Fatal(e)
	}
	if _, e := c.PaymentsRange(context.Background(), "2026-11-19", "2026-11-21"); e != nil {
		t.Fatal(e)
	}
}
func TestUncertainResponseDoesNotRetry(t *testing.T) {
	n := 0
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(500)
		w.Write([]byte(`{"ok":false,"reason":"internal error"}`))
	})
	_, e := c.Collect(context.Background(), 42, "0970000002", "1.00", "USSD-fixture-0001")
	if !Unknown(e) || n != 1 {
		t.Fatal(e, n)
	}
}
func TestMismatchAndUnpostedSuccess(t *testing.T) {
	wrong := true
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		ref := "USSD-fixture-0001"
		if wrong {
			ref = "wrong-ref"
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "loan_id": 42, "reference": "provider-fixture", "external_reference": ref, "status": "Successful", "amount": 1, "msisdn": "260970000002", "posted": false})
	})
	if _, e := c.Collect(context.Background(), 42, "0970000002", "1.00", "USSD-fixture-0001"); !Unknown(e) {
		t.Fatal(e)
	}
	wrong = false
	r, e := c.Collect(context.Background(), 42, "0970000002", "1.00", "USSD-fixture-0001")
	if e != nil || r.Posted {
		t.Fatal(r, e)
	}
}
func TestRedirectsAndThrottle(t *testing.T) {
	var calls atomic.Int32
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(429)
		w.Write([]byte(`{"ok":false,"reason":"rate limit"}`))
	})
	c.Payments(context.Background(), "2026-11-20")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, e := c.Payments(ctx, "2026-11-20")
	if e == nil || calls.Load() != 1 {
		t.Fatal(e, calls.Load())
	}
	followed := false
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed = true }))
	defer s.Close()
	c = fixture(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, s.URL, 302) })
	if _, e := c.Verify(context.Background(), "0970000001"); e == nil || followed {
		t.Fatal(e, followed)
	}
}
func TestMoneyAndInputValidation(t *testing.T) {
	for _, bad := range []string{"1e3", "-1", "01", "0.001", "NaN", ""} {
		if _, e := Amount(bad); e == nil {
			t.Fatal(bad)
		}
	}
	if a, e := Amount("0.01"); e != nil || a != "0.01" {
		t.Fatal(a, e)
	}
	if _, e := New("bad\ncredential", nil); e == nil {
		t.Fatal("header injection accepted")
	}
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid input reached network") })
	if _, e := c.Collect(context.Background(), 42, "0970000001", "1", "bad"); e == nil {
		t.Fatal("short reference accepted")
	}
	if strings.Contains(c.String(), "fixture-only") {
		t.Fatal("credential leaked")
	}
}
