package lmsbridge

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mightyfin/mightyfin-ussd/internal/ussdgateway"
)

type testBridgeTransport struct{ target *url.URL }

func (t testBridgeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "127.0.0.1:28091" {
		return nil, fmt.Errorf("unexpected target")
	}
	clone := r.Clone(r.Context())
	u := *r.URL
	clone.URL = &u
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// Actual gateway -> actual BridgeReader -> actual LMS adapter -> TLS contract
// fixture. The fixture is NOT a live LMS or operator network acceptance test.
func TestGatewayAdapterHTTPJourney(t *testing.T) {
	cases := []struct{ name, app, loans, want string }{
		{"new", ``, `{"data":[]}`, "No active application"},
		{"draft", `{"application":{"id":44,"status":"draft"},"application_completion":{"is_complete":false,"missing_profile_fields":["dob"],"missing_documents":[]}}`, `{"data":[]}`, "needs more details"},
		{"documents", `{"application":{"id":44,"status":"draft"},"application_completion":{"is_complete":false,"missing_profile_fields":[],"missing_documents":["nrc"]}}`, `{"data":[]}`, "Documents are needed"},
		{"complete", `{"application":{"id":44,"status":"draft"},"application_completion":{"is_complete":true,"missing_profile_fields":[],"missing_documents":[]}}`, `{"data":[]}`, "not submitted"},
		{"pending", `{"application":{"id":44,"status":"pending"}}`, `{"data":[]}`, "under review"},
		{"approved", `{"application":{"id":44,"status":"approved"}}`, `{"data":[]}`, "not confirmation of payment"},
		{"rejected", `{"application":{"id":44,"status":"rejected"}}`, `{"data":[]}`, "not approved"},
		{"loan", ``, `{"data":[{"status":"active","amountRemaining":"20.00"}]}`, "outstanding loan"},
		{"unknown_balance", ``, `{"data":[{"status":"active","amountRemaining":null}]}`, "needs review"},
		{"malformed", `{"application":null}`, `{"data":[]}`, "END Unable"},
		{"outage", `outage`, `{"data":[]}`, "END Unable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mutations, reads atomic.Int32
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					mutations.Add(1)
					w.WriteHeader(500)
					return
				}
				token := r.Header.Get("Authorization")
				if token != "Bearer alice" && token != "Bearer bob" {
					w.WriteHeader(401)
					fmt.Fprint(w, `{"error":"unauthorized"}`)
					return
				}
				reads.Add(1)
				switch r.URL.Path {
				case "/api/v1/users/profile":
					id := 1
					if token == "Bearer bob" {
						id = 2
					}
					fmt.Fprintf(w, `{"data":{"id":"user-%d"}}`, id)
				case "/api/v1/loan-applications/current":
					if token == "Bearer bob" {
						fmt.Fprint(w, `{"application":{"id":55,"status":"rejected"}}`)
						return
					}
					if tc.app == "outage" {
						w.WriteHeader(503)
						fmt.Fprint(w, `{"secret":"database-password"}`)
						return
					}
					if tc.app == "" {
						w.WriteHeader(404)
						fmt.Fprint(w, `{"status":"empty","data":null}`)
						return
					}
					fmt.Fprint(w, tc.app)
				case "/api/v1/loans":
					fmt.Fprint(w, tc.loans)
				default:
					t.Errorf("unexpected LMS route: %s", r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer upstream.Close()
			client, _ := NewClient(upstream.URL + "/api/v1")
			client.http.Transport = upstream.Client().Transport
			bridge := httptest.NewServer(&Server{Client: client, ServiceKey: serviceKey})
			defer bridge.Close()
			base, _ := url.Parse(bridge.URL)
			reader := &ussdgateway.BridgeReader{Key: serviceKey, HTTP: &http.Client{Transport: testBridgeTransport{base}, Timeout: time.Second}}
			gateway := httptest.NewServer(&ussdgateway.Server{Key: serviceKey, Reader: reader})
			defer gateway.Close()
			send := func(id, text, token string) string {
				t.Helper()
				form := url.Values{"sessionId": {id}, "phoneNumber": {"260970000001"}, "serviceCode": {"*123#"}, "text": {text}}
				req, _ := http.NewRequest("POST", gateway.URL+"/ussd", strings.NewReader(form.Encode()))
				req.Header.Set("Authorization", "Bearer "+serviceKey)
				req.Header.Set("X-LMS-Customer-Token", token)
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				res, e := gateway.Client().Do(req)
				if e != nil {
					t.Fatal(e)
				}
				defer res.Body.Close()
				b, _ := io.ReadAll(res.Body)
				if res.StatusCode != 200 || len(b) > 160 || res.Header.Get("Cache-Control") != "no-store" {
					t.Fatal(res.StatusCode, string(b))
				}
				if strings.Contains(string(b), "secret") || strings.Contains(string(b), "user-") {
					t.Fatal("sensitive fields exposed")
				}
				return string(b)
			}
			send("alice-session", "", "alice")
			first := send("alice-session", "2", "alice")
			if !strings.Contains(first, tc.want) {
				t.Fatal(first)
			}
			count := reads.Load()
			if retry := send("alice-session", "2", "alice"); retry != first || reads.Load() != count {
				t.Fatal("retry not replayed")
			}
			send("bob-session", "", "bob")
			if !strings.Contains(send("bob-session", "2", "bob"), "not approved") {
				t.Fatal("customer separation failed")
			}
			send("invalid-session", "", "invalid-token")
			if !strings.HasPrefix(send("invalid-session", "2", "invalid-token"), "END Unable") {
				t.Fatal("invalid token accepted")
			}
			if mutations.Load() != 0 {
				t.Fatal("gateway wrote to LMS")
			}
		})
	}
}

func TestGatewayAllScreenLengths(t *testing.T) {
	for _, state := range []string{"draft", "documents_required", "ready_for_submission", "under_review", "approved", "rejected", "loan_active", "account_review", "no_active_journey", "review_status"} {
		b, _ := json.Marshal(renderScreen(Journey{State: state}))
		var screen struct{ Text string }
		_ = json.Unmarshal(b, &screen)
		if len("CON "+screen.Text) > 160 {
			t.Fatalf("%s too long", state)
		}
	}
}
