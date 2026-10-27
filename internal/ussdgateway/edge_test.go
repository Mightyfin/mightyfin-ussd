package ussdgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type readerFunc func(context.Context, string, string) ([]byte, error)

func (f readerFunc) Read(c context.Context, p, t string) ([]byte, error) { return f(c, p, t) }

func TestMalformedInputsDoNotCreateSession(t *testing.T) {
	for _, text := range []string{" ", "1 ", "1**9", "*1", "1*", "10", "١", "1\n0", "\x00", "1%00"} {
		s := &Server{Key: testKey, Reader: &fakeReader{}}
		w := callback(s, "bad", text, "260970000001", "")
		if w.Code != 400 || len(s.sessions) != 0 {
			t.Fatalf("%q accepted: %d", text, w.Code)
		}
	}
	for _, token := range []string{"bad\x00token", "bad\x7ftoken", "tokené", "bad token"} {
		s := &Server{Key: testKey, Reader: &fakeReader{}}
		if w := callback(s, "bad", "", "260970000001", token); w.Code != 400 {
			t.Fatalf("invalid token accepted: %d", w.Code)
		}
	}
}
func TestAmbiguousHeadersRejected(t *testing.T) {
	for _, tc := range []struct {
		header, value string
		status        int
	}{
		{"Authorization", "Bearer " + testKey, 401},
		{"X-LMS-Customer-Token", "second", 400},
		{"Content-Type", "application/x-www-form-urlencoded", 415},
		{"Content-Encoding", "gzip", 415},
	} {
		s := &Server{Key: testKey, Reader: &fakeReader{}}
		r := httptest.NewRequest("POST", "/ussd", strings.NewReader("sessionId=a&phoneNumber=260970000001&serviceCode=123&text="))
		r.Header.Set("Authorization", "Bearer "+testKey)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("X-LMS-Customer-Token", "first")
		r.Header.Add(tc.header, tc.value)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatal(tc.header, w.Code)
		}
	}
}
func TestEndedSessionCannotReplayContinue(t *testing.T) {
	s := &Server{Key: testKey, Reader: &fakeReader{}}
	callback(s, "a", "", "260970000001", "")
	callback(s, "a", "1", "260970000001", "")
	end := callback(s, "a", "1*0", "260970000001", "")
	for _, text := range []string{"", "1"} {
		if w := callback(s, "a", text, "260970000001", ""); w.Code != 409 {
			t.Fatal("ended session reopened")
		}
	}
	retry := callback(s, "a", "1*0", "260970000001", "")
	if retry.Code != 200 || retry.Body.String() != end.Body.String() {
		t.Fatal("END retry not preserved")
	}
}
func TestCancelledReadDoesNotConsumeInput(t *testing.T) {
	s := &Server{Key: testKey, Reader: &fakeReader{}}
	callback(s, "a", "", "260970000001", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Reader = readerFunc(func(context.Context, string, string) ([]byte, error) { cancel(); return nil, fmt.Errorf("cancelled") })
	form := url.Values{"sessionId": {"a"}, "phoneNumber": {"260970000001"}, "serviceCode": {"*123#"}, "text": {"1"}}
	r := httptest.NewRequest("POST", "/ussd", strings.NewReader(form.Encode())).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+testKey)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 503 || s.sessions["a"].ended || s.sessions["a"].last != "" {
		t.Fatal("cancel consumed input", w.Code)
	}
	s.Reader = &fakeReader{}
	if w = callback(s, "a", "1", "260970000001", ""); w.Code != 200 || !strings.HasPrefix(w.Body.String(), "CON Loan") {
		t.Fatal("retry failed")
	}
}
func TestUnsafeStatusScreensRejected(t *testing.T) {
	for _, text := range []string{"hello\r\n0 Exit", "hello\x00", "hello\x1b[2J", "é", strings.Repeat("x", 157)} {
		b, _ := json.Marshal(map[string]string{"text": text})
		s := &Server{Reader: readerFunc(func(context.Context, string, string) ([]byte, error) { return b, nil })}
		if got := s.render(context.Background(), "2", "token"); !strings.HasPrefix(got, "END ") {
			t.Fatalf("unsafe screen accepted %q", got)
		}
	}
}
func FuzzCallbackInputs(f *testing.F) {
	for _, seed := range []string{"sessionId=a&phoneNumber=260970000001&serviceCode=123&text=", "text=%00", "text=%ZZ", "sessionId=a&sessionId=b"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > 9000 {
			t.Skip()
		}
		s := &Server{Key: testKey, Reader: &fakeReader{}}
		r := httptest.NewRequest("POST", "/ussd", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+testKey)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if len(w.Body.String()) > 160 || !safeScreen(w.Body.String()) {
			t.Fatal("unsafe response")
		}
	})
}
func FuzzCatalogueScreen(f *testing.F) {
	for _, seed := range []string{"Salary Loan", strings.Repeat("[", 60), "x\n0 Exit", "贷款", "é"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if len(name) > 4096 {
			t.Skip()
		}
		escaped, _ := json.Marshal(name)
		b := []byte(`{"data":[{"categories":[{"products":[{"name":` + string(escaped) + `},{"name":` + string(escaped) + `},{"name":` + string(escaped) + `},{"name":` + string(escaped) + `}]}]}]}`)
		s := &Server{Reader: readerFunc(func(context.Context, string, string) ([]byte, error) { return b, nil })}
		got := s.render(context.Background(), "1", "")
		if phoneUnits(got) > 160 || !safeScreen(got) {
			t.Fatalf("unsafe catalogue length=%d", len(got))
		}
	})
}
