package app

import (
	"net/http/httptest"
	"testing"
)

func TestDeploymentPortKeepsHostAndTokenRestrictions(t *testing.T) {
	a, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.HandlerForAddress("127.0.0.1:28090")
	for _, c := range []struct {
		host, path string
		status     int
	}{
		{"127.0.0.1:28090", "/healthz", 200},
		{"localhost:28090", "/healthz", 200},
		{"ussd.mightyfinance.co.zm", "/healthz", 403},
		{"127.0.0.1:8090", "/healthz", 403},
		{"127.0.0.1:28090", "/api/demo/state", 401},
	} {
		r := httptest.NewRequest("GET", "http://"+c.host+c.path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.status {
			t.Errorf("%s%s: got %d want %d", c.host, c.path, w.Code, c.status)
		}
	}
}
