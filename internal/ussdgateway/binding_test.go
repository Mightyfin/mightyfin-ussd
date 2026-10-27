package ussdgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCustomerPhoneBinding(t *testing.T) {
	for _, tc := range []struct {
		phone string
		match bool
	}{
		{"260970000001", true}, {"+260970000001", true}, {"0970000001", true}, {"970000001", true},
		{"260970000002", false}, {"", false}, {"970 000 001", false}, {"user-1", false},
	} {
		t.Run(tc.phone, func(t *testing.T) {
			reads := 0
			s := &Server{Key: testKey, Reader: readerFunc(func(_ context.Context, path, token string) ([]byte, error) {
				if path == "/v1/me" {
					return json.Marshal(map[string]any{"data": map[string]string{"id": "user-1", "phone": tc.phone}})
				}
				if path == "/v1/journey/screen" {
					reads++
					return []byte(`{"text":"Mighty Finance\nUnder review\n9 Refresh\n0 Exit"}`), nil
				}
				return nil, fmt.Errorf("unexpected resource")
			})}
			callback(s, "a", "", "260970000001", "token")
			w := callback(s, "a", "2", "260970000001", "token")
			if tc.match {
				if !strings.HasPrefix(w.Body.String(), "CON ") || reads != 1 {
					t.Fatal(w.Body.String())
				}
			} else {
				if !strings.HasPrefix(w.Body.String(), "END Unable") || reads != 0 {
					t.Fatal("mismatch reached account", w.Body.String())
				}
			}
		})
	}
}
func TestRevokedTokenCannotReplayCachedStatus(t *testing.T) {
	valid := true
	reads := 0
	s := &Server{Key: testKey, Reader: readerFunc(func(_ context.Context, path, token string) ([]byte, error) {
		if path == "/v1/me" {
			if !valid {
				return nil, fmt.Errorf("revoked")
			}
			return []byte(`{"data":{"id":"user-1","phone":"260970000001"}}`), nil
		}
		reads++
		return []byte(`{"text":"Mighty Finance\nUnder review\n9 Refresh\n0 Exit"}`), nil
	})}
	callback(s, "a", "", "260970000001", "token")
	first := callback(s, "a", "2", "260970000001", "token")
	if !strings.HasPrefix(first.Body.String(), "CON ") {
		t.Fatal(first.Body.String())
	}
	valid = false
	second := callback(s, "a", "2", "260970000001", "token")
	if !strings.HasPrefix(second.Body.String(), "END Unable") || reads != 1 {
		t.Fatal("cached status leaked")
	}
	if w := callback(s, "a", "", "260970000001", "token"); w.Code != 409 {
		t.Fatal("session reopened")
	}
}
