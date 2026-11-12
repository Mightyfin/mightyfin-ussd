package ussdgateway

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
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
		{"1", "1", "MightyFin"},
		{"1", "1", "MightyFin"}, // initial retry must not select a product
		{"1", "2", "Loan products"},
		{"*", "2", "MightyFin"},
		{"1", "2", "Loan products"},
		{"0", "2", "Thank you"},
		{"0", "2", "Thank you"},
	} {
		w := hobby(s, "browse", "0762167007", step.input, step.kind)
		if w.Code != 200 || !strings.Contains(w.Body.String(), step.want) || strings.HasPrefix(w.Body.String(), "CON ") || strings.HasPrefix(w.Body.String(), "END ") {
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
	for _, input := range []string{"2"} {
		hobby(s, "private"+input, "260762167007", "", "1")
		w := hobby(s, "private"+input, "260762167007", input, "2")
		if !strings.Contains(w.Body.String(), "Please choose an option from the menu") {
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
		{strings.Replace(base, "SESSION_ID", "unknown", 1), testKey, 400},
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

func TestHobbitonSourceAllowlist(t *testing.T) {
	for _, tt := range []struct {
		name, remote, ip, xff, worker string
		duplicate, enabled            bool
		want                          int
	}{
		{"allowed tunnel", "127.0.0.1:1234", "41.175.8.68", "", "", false, true, 200},
		{"allowed ipv6 loopback", "[::1]:1234", "41.175.8.68", "", "", false, true, 200},
		{"wrong client", "127.0.0.1:1234", "41.175.8.69", "", "", false, true, 401},
		{"forged forwarded for", "127.0.0.1:1234", "203.0.113.1", "41.175.8.68", "", false, true, 401},
		{"missing cf", "127.0.0.1:1234", "", "41.175.8.68", "", false, true, 401},
		{"untrusted peer", "203.0.113.1:1234", "41.175.8.68", "", "", false, true, 401},
		{"direct operator not tunnel", "41.175.8.68:1234", "41.175.8.68", "", "", false, true, 401},
		{"comma list", "127.0.0.1:1234", "41.175.8.68, 203.0.113.1", "", "", false, true, 401},
		{"duplicate cf", "127.0.0.1:1234", "41.175.8.68", "", "", true, true, 401},
		{"worker rejected", "127.0.0.1:1234", "41.175.8.68", "", "example.com", false, true, 401},
		{"disabled by default", "127.0.0.1:1234", "41.175.8.68", "", "", false, false, 401},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{Key: testKey, Reader: &fakeReader{}}
			if tt.enabled {
				s.HobbitonIP = "41.175.8.68"
			}
			r := httptest.NewRequest("GET", "/ussd?msisdn=0762167007&SESSION_ID=ip-test&input=1&requestType=1", nil)
			r.RemoteAddr = tt.remote
			if tt.ip != "" {
				r.Header.Set("CF-Connecting-IP", tt.ip)
			}
			if tt.duplicate {
				r.Header.Add("CF-Connecting-IP", tt.ip)
			}
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			if tt.worker != "" {
				r.Header.Set("CF-Worker", tt.worker)
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestHobbitonRequestTypes(t *testing.T) {
	s := &Server{Key: testKey, Reader: &fakeReader{}}
	if w := hobby(s, "missing", "0762167007", "1", "2"); !strings.Contains(w.Body.String(), "Session expired") {
		t.Fatal(w.Body.String())
	}
	for _, kind := range []string{"0", "3", "01", "-1"} {
		if w := hobby(s, "invalid", "0762167007", "1", kind); w.Code != 400 {
			t.Fatal(kind, w.Code)
		}
	}
	hobby(s, "sequence", "0762167007", "1", "1")
	if w := hobby(s, "sequence", "0762167007", "1", "2"); !strings.Contains(w.Body.String(), "Loan products") {
		t.Fatal(w.Body.String())
	}
	if w := hobby(s, "sequence", "0762167007", "1", "1"); w.Code != 409 {
		t.Fatal(w.Code)
	}
	s.sessions["hobbiton:sequence"].expires = time.Now().Add(-time.Second)
	if w := hobby(s, "sequence", "0762167007", "9", "2"); !strings.Contains(w.Body.String(), "Session expired") {
		t.Fatal(w.Body.String())
	}
}

func TestHobbitonParameterCasingAndEntrySelection(t *testing.T) {
	for _, tt := range []struct {
		query    string
		want     int
		contains string
	}{
		{"INPUT=29&MSISDN=260773016722&RequestType=1&SESSION_ID=17939496960758482", 200, "MightyFin"},
		{"iNpUt=29&mSiSdN=260773016722&rEqUeStTyPe=1&sEsSiOn_Id=mixed", 200, "MightyFin"},
		{"input=29&msisdn=260773016722&requestType=1&SESSION_ID=lower", 200, "MightyFin"},
		{"INPUT=29&input=1&MSISDN=260773016722&RequestType=1&SESSION_ID=duplicate", 400, "Invalid callback"},
		{"INPUT=29&MSISDN=260773016722&RequestType=2&SESSION_ID=bad-continuation", 200, "Session expired"},
		{"INPUT=" + strings.Repeat("1", 33) + "&MSISDN=260773016722&RequestType=1&SESSION_ID=long", 400, "Invalid callback"},
		{"INPUT=2%2A9&MSISDN=260773016722&RequestType=1&SESSION_ID=invalid-entry", 400, "Invalid callback"},
	} {
		s := &Server{Key: testKey, Reader: &fakeReader{}, HobbitonIP: "41.175.8.68"}
		r := httptest.NewRequest("GET", "/ussd?"+tt.query, nil)
		r.RemoteAddr = "127.0.0.1:12000"
		r.Header.Set("CF-Connecting-IP", "41.175.8.68")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != tt.want || !strings.Contains(w.Body.String(), tt.contains) {
			t.Fatal(tt.query, w.Code, w.Body.String())
		}
	}
	s := &Server{Key: testKey, Reader: &fakeReader{}}
	for _, query := range []string{
		"INPUT=29&MSISDN=260773016722&RequestType=1&SESSION_ID=case-flow",
		"input=1&msisdn=260773016722&requestType=2&session_id=case-flow",
	} {
		r := httptest.NewRequest("GET", "/ussd?"+query, nil)
		r.Header.Set("Authorization", "Bearer "+testKey)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 200 || (strings.HasPrefix(w.Body.String(), "CON ") || strings.HasPrefix(w.Body.String(), "END ")) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestHobbitonPlainBodyAndInternalTermination(t *testing.T) {
	s := &Server{Key: testKey, Reader: &fakeReader{}}
	if w := hobby(s, "plain", "0762167007", "29", "1"); w.Body.String() != "Welcome to MightyFin\n1 Loan products\n0 Exit" {
		t.Fatal(w.Body.String())
	}
	for i := 0; i < 2; i++ {
		w := hobby(s, "plain", "0762167007", "0", "2")
		if w.Body.String() != "Thank you for choosing MightyFin." {
			t.Fatal(w.Body.String())
		}
	}
	if !s.sessions["hobbiton:plain"].ended {
		t.Fatal("lost internal terminal state")
	}
	if w := hobby(s, "plain", "0762167007", "1", "2"); w.Code != 409 || strings.HasPrefix(w.Body.String(), "END ") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := callback(s, "post-still-prefixed", "", "260970000001", ""); !strings.HasPrefix(w.Body.String(), "CON ") {
		t.Fatal(w.Body.String())
	}
}

func TestHobbitonFreeFlowLifecycle(t *testing.T) {
	s := &Server{Key: testKey, Reader: &fakeReader{}}
	for _, step := range []struct{ input, kind, flow string }{
		{"29", "1", "FC"}, {"29", "1", "FC"},
		{"1", "2", "FC"}, {"1", "2", "FC"}, {"*", "2", "FC"},
		{"0", "2", "FB"}, {"0", "2", "FB"}, {"1", "2", "FB"},
	} {
		w := hobby(s, "freeflow", "0762167007", step.input, step.kind)
		if w.Result().Header.Get("FreeFlow") != step.flow || strings.HasPrefix(w.Body.String(), "CON ") || strings.HasPrefix(w.Body.String(), "END ") {
			t.Fatal(step, w.Result().Header, w.Body.String())
		}
	}
	if w := hobby(s, "expired", "0762167007", "1", "2"); w.Result().Header.Get("FreeFlow") != "FB" {
		t.Fatal(w.Result().Header)
	}
	r := httptest.NewRequest("GET", "/ussd", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 401 || w.Result().Header.Get("FreeFlow") != "FB" {
		t.Fatal(w.Code, w.Result().Header)
	}
	if w := hobby(s, "invalid", "0762167007", "bad", "2"); w.Code != 400 || w.Result().Header.Get("FreeFlow") != "FB" {
		t.Fatal(w.Code, w.Result().Header)
	}
}

type namedPayments struct {
	fakePayments
	name string
}

func (p *namedPayments) Welcome(context.Context, string) (string, error) { return p.name, nil }
func TestLiveWelcomeNumberedProductsAndNavigation(t *testing.T) {
	s := &Server{Key: testKey, Reader: &fakeReader{}, Payments: &namedPayments{name: "George"}}
	for _, step := range []struct{ input, kind, want string }{
		{"29", "1", "Welcome George"}, {"1", "2", "1 Salary loan"},
		{"1", "2", "Salary loan\nSubject to eligibility"}, {"*", "2", "1 Salary loan"},
		{"*", "2", "Welcome George"}, {"1", "2", "2 Business loan"},
		{"2", "2", "Business loan\nSubject to eligibility"}, {"#", "2", "Welcome George"},
	} {
		w := hobby(s, "live-nav", "0762167007", step.input, step.kind)
		if w.Code != 200 || !strings.Contains(w.Body.String(), step.want) || w.Header().Get("FreeFlow") != "FC" || phoneUnits(w.Body.String()) > 160 {
			t.Fatal(step, w.Code, w.Body.String())
		}
	}
}

func TestHobbitonCataloguePagingBudget(t *testing.T) {
	c := &session{hobbyNames: []string{strings.Repeat("[", 32), strings.Repeat("[", 32), strings.Repeat("[", 32), "Fourth"}}
	if text := hobbyCatalogue(c); phoneUnits(strings.TrimPrefix(text, "CON ")) > 160 {
		t.Fatal(text)
	}
	s := &Server{}
	c.last = "products"
	if text := s.hobbitonMenu(context.Background(), c, "", "4"); !strings.Contains(text, "1 Fourth") {
		t.Fatal(text)
	}
	if text := s.hobbitonMenu(context.Background(), c, "", "5"); !strings.Contains(text, "4 More") {
		t.Fatal(text)
	}
}

func TestHobbitonLiveCollectionIntegration(t *testing.T) {
	for _, amount := range []string{"25.50", "1"} {
		f := &fakePayments{status: "pending"}
		s := &Server{Key: testKey, Payments: f, Reader: &fakeReader{}}
		for _, step := range []struct{ input, kind, want string }{
			{"29", "1", "3 Make payment"}, {"3", "2", "Select loan"}, {"1", "2", "Enter payment amount"},
			{amount, "2", "Confirm"},
		} {
			w := hobby(s, "collection", "0762167007", step.input, step.kind)
			if w.Code != 200 || !strings.Contains(w.Body.String(), step.want) || w.Header().Get("FreeFlow") != "FC" || f.calls != 0 {
				t.Fatal(step, w.Code, w.Body.String(), f.calls)
			}
		}
		confirm := "1"
		if amount == "1" {
			confirm = "2"
		}
		for i := 0; i < 2; i++ {
			w := hobby(s, "collection", "0762167007", confirm, "2")
			if w.Code != 200 || w.Header().Get("FreeFlow") != "FB" || !strings.Contains(w.Body.String(), "mobile-money prompt") || strings.Contains(strings.ToLower(w.Body.String()), "lipila") {
				t.Fatal(w.Code, w.Body.String())
			}
		}
		if f.calls != 1 || f.phone != "260762167007" || f.loan != 201 {
			t.Fatal(f)
		}
	}
}
func TestHobbitonCollectionNavigationAndNoAccidentalConsent(t *testing.T) {
	f := &fakePayments{status: "pending"}
	s := &Server{Key: testKey, Payments: f, Reader: &fakeReader{}}
	for _, input := range []string{"29", "3", "1", "25", "*", "*", "*", "3", "#", "3", "1", "1", "1"} {
		kind := "2"
		if input == "29" {
			kind = "1"
		}
		hobby(s, "nav-pay", "0762167007", input, kind)
	}
	if f.calls != 0 {
		t.Fatal("repeated amount was treated as confirmation")
	}
}
func TestHobbitonPaymentStatusUsesLms(t *testing.T) {
	f := &fakePayments{status: "completed"}
	s := &Server{Key: testKey, Payments: f, Reader: &fakeReader{}}
	hobby(s, "receipt", "0762167007", "29", "1")
	w := hobby(s, "receipt", "0762167007", "4", "2")
	if !strings.Contains(w.Body.String(), "Select loan") {
		t.Fatal(w.Body.String())
	}
	w = hobby(s, "receipt", "0762167007", "1", "2")
	if w.Header().Get("FreeFlow") != "FB" || !strings.Contains(w.Body.String(), "confirmed and recorded") || f.calls != 0 {
		t.Fatal(w.Body.String(), f.calls)
	}
}

func TestHobbitonGreetingOnlyUsesRegisteredName(t *testing.T) {
	for _, tt := range []struct{ name, want string }{{"George", "Welcome George"}, {"", "Welcome to MightyFin"}, {"  ", "Welcome to MightyFin"}} {
		s := &Server{Key: testKey, Payments: &namedPayments{name: tt.name}, Reader: &fakeReader{}}
		w := hobby(s, "greeting", "0762167007", "29", "1")
		first := strings.SplitN(w.Body.String(), "\n", 2)[0]
		if first != tt.want {
			t.Fatal(first, tt.want)
		}
	}
}
