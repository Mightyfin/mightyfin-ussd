package lmsbridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScreensFitPhoneAndDoNotExposePrivateData(t *testing.T) {
	states := []string{"draft", "documents_required", "ready_for_submission", "under_review", "approved", "rejected", "loan_active", "account_review", "no_active_journey", "review_status"}
	for _, state := range states {
		t.Run(state, func(t *testing.T) {
			screen := renderScreen(Journey{State: state, Message: "private upstream text", ApplicationID: json.RawMessage(`123456`), MissingProfile: []string{"private_field"}, Version: "snapshot"})
			if len(screen.Text) > 160 || len(screen.Text) == 0 {
				t.Fatalf("screen length %d: %s", len(screen.Text), screen.Text)
			}
			for _, char := range screen.Text {
				if char != '\n' && (char < 32 || char > 126) {
					t.Fatalf("non-ASCII/control character %q", char)
				}
			}
			b, _ := json.Marshal(screen)
			if strings.Contains(string(b), "private") || strings.Contains(string(b), "123456") {
				t.Fatal("private LMS fields exposed")
			}
			if len(screen.Choices) != 2 || screen.Choices[0].Action != "refresh" || screen.Choices[1].Action != "exit" {
				t.Fatal("unexpected customer action", screen.Choices)
			}
		})
	}
}

func TestScreenRequiresCustomerAndServiceAuthentication(t *testing.T) {
	s, writes := fixture(t)
	for _, token := range []string{"", "invalid"} {
		w := request(s, "GET", "/v1/journey/screen", token, "", "")
		if w.Code != 401 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/v1/journey/screen", nil))
	if w.Code != 401 {
		t.Fatal("missing service authentication accepted")
	}
	s.Writes = false
	for i := 0; i < 2; i++ {
		w = request(s, "GET", "/v1/journey/screen", "alice", "", "")
		var screen Screen
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &screen) != nil || screen.State != "ready_for_submission" || !strings.Contains(screen.Text, "not submitted") {
			t.Fatal(w.Code, w.Body.String())
		}
		if len(screen.Version) != 64 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing freshness/privacy controls")
		}
	}
	if writes.Load() != 0 {
		t.Fatal("viewing a screen mutated LMS")
	}
	for _, path := range []string{"/v1/journey/screen?user_id=2", "/v1/journey/screen?input=1"} {
		if w := request(s, "GET", path, "alice", "", ""); w.Code != 400 {
			t.Fatal("unexpected query accepted", w.Code)
		}
	}
	if w := request(s, "POST", "/v1/journey/screen", "alice", "", `{}`); w.Code != 404 {
		t.Fatal("screen accepted a mutation", w.Code)
	}
}

func TestScreenDoesNotMaskLMSFailure(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/users/profile" {
			w.Write([]byte(`{"data":{"id":"user-1"}}`))
			return
		}
		w.WriteHeader(503)
		w.Write([]byte(`{"secret":"internal failure detail"}`))
	}))
	defer up.Close()
	s, _ := fixture(t)
	s.Client, _ = NewClient(up.URL + "/api/v1")
	s.Client.http.Transport = up.Client().Transport
	w := request(s, "GET", "/v1/journey/screen", "alice", "", "")
	if w.Code != 503 || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "No active") {
		t.Fatal(w.Code, w.Body.String())
	}
}
