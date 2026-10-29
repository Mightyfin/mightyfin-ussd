package ussdgateway

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func hobby(s *Server, id, phone, input, kind string) *httptest.ResponseRecorder {
	q := url.Values{"SESSION_ID": {id}, "msisdn": {phone}, "input": {input}, "requestType": {kind}}
	r := httptest.NewRequest("GET", "/ussd?"+q.Encode(), nil)
	r.Header.Set("Authorization", "Bearer "+testKey)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestHobbitonBrowsingAndRetries(t *testing.T) {
	f := &fakeReader{}
	s := &Server{Key: testKey, Reader: f}
	for _, step := range []struct{ input, kind, want string }{
		{"1", "1", "CON Mighty Finance"},
		{"1", "1", "CON Mighty Finance"}, // initial retry must not select a product
		{"1", "2", "CON Loan products"},
		{"1", "2", "CON Loan products"},
		{"9", "2", "CON Mighty Finance"},
		{"1", "2", "CON Loan products"},
		{"0", "2", "END Thank you"},
		{"0", "2", "END Thank you"},
	} {
		w := hobby(s, "browse", "0762167007", step.input, step.kind)
		if w.Code != 200 || !strings.Contains(w.Body.String(), step.want) {
			t.Fatal(w.Code, w.Body.String(), step)
		}
	}
	if f.calls.Load() != 2 {
		t.Fatal("retry performed upstream read", f.calls.Load())
	}
	if w := hobby(s, "browse", "260762167007", "9", "2"); w.Code != 409 {
		t.Fatal(w.Code)
	}
}
func TestHobbitonNormalizationAndIsolation(t *testing.T) {
	s := &Server{Key: testKey, Reader: &fakeReader{}, Payments: &fakePayments{}}
	hobby(s, "same", "0762167007", "", "1")
	if w := hobby(s, "same", "+260762167007", "", "1"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := hobby(s, "same", "260762167008", "", "1"); w.Code != 409 {
		t.Fatal(w.Code)
	}
	// POST and GET cannot share state, even with the same supplied session ID.
	if w := callback(s, "same", "", "260970000001", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	for _, input := range []string{"2", "3", "4"} {
		hobby(s, "private"+input, "260762167007", "", "1")
		w := hobby(s, "private"+input, "260762167007", input, "2")
		if !strings.Contains(w.Body.String(), "unavailable during connection testing") {
			t.Fatal(w.Body.String())
		}
	}
}
func TestHobbitonMalformedAndAuth(t *testing.T) {
	base := "/ussd?SESSION_ID=a&msisdn=0762167007&input=1&requestType=1"
	for _, tt := range []struct {
		path, key string
		status    int
	}{
		{base, "", 401},
		{base, testKey, 200},
		{base + "&input=2", testKey, 400},
		{strings.Replace(base, "SESSION_ID", "sessionId", 1), testKey, 400},
		{strings.Replace(base, "requestType=1", "requestType=-1", 1), testKey, 400},
		{strings.Replace(base, "input=1", "input=1*9", 1), testKey, 400},
		{strings.Replace(base, "input=1", "input=%ZZ", 1), testKey, 400},
		{strings.Replace(base, "0762167007", "abc", 1), testKey, 400},
		{strings.Replace(base, "SESSION_ID=a", "SESSION_ID=", 1), testKey, 400},
		{base + "&extra=1", testKey, 400},
		{base + strings.Repeat("x", 2048), testKey, 414},
	} {
		s := &Server{Key: testKey, Reader: &fakeReader{}}
		r := httptest.NewRequest("GET", tt.path, nil)
		if tt.key != "" {
			r.Header.Set("Authorization", "Bearer "+tt.key)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != tt.status {
			t.Fatal(tt.path, w.Code, w.Body.String())
		}
	}
}
