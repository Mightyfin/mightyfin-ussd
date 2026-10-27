package main

import "testing"

func TestRejectNonLoopbackBind(t *testing.T) {
	for _, address := range []string{"0.0.0.0:8090", ":8090", "example.com:8090"} {
		t.Setenv("USSD_HTTP_ADDR", address)
		if err := run(); err == nil {
			t.Fatalf("accepted public bind %q", address)
		}
	}
}
