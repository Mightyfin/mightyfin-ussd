package ussdgateway

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "restricted-pilot-key-with-at-least-32-characters"

type fakeReader struct {
	calls atomic.Int32
	fail  bool
}

func (f *fakeReader) Read(_ context.Context, path, token string) ([]byte, error) {
	f.calls.Add(1)
	if f.fail {
		return nil, fmt.Errorf("unavailable")
	}
	if path == "/v1/products" {
		return []byte(`{"data":[{"categories":[{"products":[{"name":"Salary loan"},{"name":"Business loan"}]}]}]}`), nil
	}
	if path == "/v1/me" && token == "valid-customer-token" {
		return []byte(`{"data":{"id":"user-1","phone":"260970000001"}}`), nil
	}
	if path == "/v1/journey/screen" && token == "valid-customer-token" {
		return []byte(`{"text":"Mighty Finance\nYour application is under review.\n9 Refresh\n0 Exit"}`), nil
	}
	return nil, fmt.Errorf("unauthorized")
}
func callback(s *Server, id, text, phone, token string) *httptest.ResponseRecorder {
	form := url.Values{"sessionId": {id}, "phoneNumber": {phone}, "serviceCode": {"*123#"}, "text": {text}}
	r := httptest.NewRequest("POST", "/ussd", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Authorization", "Bearer "+testKey)
	r.Header.Set("X-LMS-Customer-Token", token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestPilotMenuAndNoPhoneImpersonation(t *testing.T) {
	f := &fakeReader{}
	s := &Server{Key: testKey, Reader: f}
	for _, step := range []struct{ text, want string }{{"", "CON Mighty Finance"}, {"1", "Salary loan"}, {"1*9", "1 Loan products"}, {"1*9*2", "Secure customer sign-in is required"}} {
		w := callback(s, "a", step.text, "+260970000001", "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), step.want) || len(w.Body.String()) > 160 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if f.calls.Load() != 1 {
		t.Fatal("phone used as account credential")
	}
}
func TestSessionReplayBindingSequenceAndExpiry(t *testing.T) {
	f := &fakeReader{}
	s := &Server{Key: testKey, Reader: f}
	callback(s, "a", "", "260970000001", "")
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := callback(s, "a", "1", "+260970000001", "")
			if w.Code != 200 {
				t.Error(w.Code)
			}
		}()
	}
	wg.Wait()
	if f.calls.Load() != 1 {
		t.Fatal("retries called upstream again", f.calls.Load())
	}
	for _, c := range []struct{ text, phone, token string }{{"1", "260970000002", ""}, {"1", "260970000001", "changed-token"}, {"1*9*2", "260970000001", ""}, {"2", "260970000001", ""}} {
		if w := callback(s, "a", c.text, c.phone, c.token); w.Code != 409 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	s.sessions["a"].expires = time.Now().Add(-time.Second)
	w := callback(s, "a", "1*9", "260970000001", "")
	if !strings.Contains(w.Body.String(), "Session expired") {
		t.Fatal(w.Body.String())
	}
}
func TestVerifiedStatusRefreshAndFailure(t *testing.T) {
	f := &fakeReader{}
	s := &Server{Key: testKey, Reader: f}
	callback(s, "a", "", "260970000001", "valid-customer-token")
	for _, text := range []string{"2", "2*9"} {
		w := callback(s, "a", text, "260970000001", "valid-customer-token")
		if w.Code != 200 || !strings.Contains(w.Body.String(), "under review") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if f.calls.Load() != 4 {
		t.Fatal("refresh failed")
	}
	f.fail = true
	w := callback(s, "a", "2*9*9", "260970000001", "valid-customer-token")
	if !strings.HasPrefix(w.Body.String(), "END Unable") {
		t.Fatal(w.Body.String())
	}
}
func TestAuthenticationAndStrictContract(t *testing.T) {
	s := &Server{Key: testKey, Reader: &fakeReader{}}
	for _, c := range []struct {
		method, path, content, body, key string
		status                           int
	}{
		{"POST", "/ussd", "application/x-www-form-urlencoded", "", "", 401},
		{"GET", "/ussd", "", "", testKey, 405},
		{"POST", "/ussd", "application/json", "{}", testKey, 415},
		{"POST", "/ussd?text=1", "application/x-www-form-urlencoded", "", testKey, 400},
		{"POST", "/ussd", "application/x-www-form-urlencoded", "sessionId=a&sessionId=b&phoneNumber=260970000001&serviceCode=123&text=", testKey, 400},
		{"POST", "/ussd", "application/x-www-form-urlencoded", strings.Repeat("x", 8193), testKey, 413},
		{"GET", "/v1/products", "", "", testKey, 404},
	} {
		r := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		r.Header.Set("Authorization", "Bearer "+c.key)
		r.Header.Set("Content-Type", c.content)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != c.status {
			t.Fatal(c.path, w.Code, w.Body.String())
		}
	}
}
func TestCapacityAndClosedSessions(t *testing.T) {
	s := &Server{Key: testKey, Reader: &fakeReader{}, sessions: make(map[string]*session)}
	for i := 0; i < 2000; i++ {
		s.sessions[fmt.Sprint(i)] = &session{expires: time.Now().Add(time.Minute)}
	}
	w := callback(s, "new", "", "260970000001", "")
	if !strings.HasPrefix(w.Body.String(), "END Session") {
		t.Fatal(w.Body.String())
	}
	s = &Server{Key: testKey, Reader: &fakeReader{}}
	callback(s, "a", "", "260970000001", "")
	callback(s, "a", "0", "260970000001", "")
	if w := callback(s, "a", "0*1", "260970000001", ""); w.Code != 409 {
		t.Fatal(w.Code)
	}
}

func TestWaitingCallbackHonorsCancellation(t *testing.T) {
	s := &Server{Key: testKey, Reader: &fakeReader{}}
	callback(s, "blocked", "", "260970000001", "")
	s.sessions["blocked"].gate <- struct{}{}
	defer func() { <-s.sessions["blocked"].gate }()
	form := url.Values{"sessionId": {"blocked"}, "phoneNumber": {"260970000001"}, "serviceCode": {"*123#"}, "text": {"1"}}
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest("POST", "/ussd", strings.NewReader(form.Encode())).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+testKey)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { s.ServeHTTP(w, r); close(done) }()
	cancel()
	select {
	case <-done:
		if w.Code != 503 {
			t.Fatal(w.Code)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled callback remained blocked behind session lock")
	}
}
