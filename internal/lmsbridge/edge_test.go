package lmsbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMalformedBalancesFailClosed(t *testing.T) {
	for _, value := range []string{"1/2", "1e999999999", "0x10", "-1", "+1", "NaN", "Infinity", " 1", "1_000", strings.Repeat("9", 1000), "0.123456789"} {
		b, _ := json.Marshal(value)
		if _, ok := positiveAmount(b); ok {
			t.Fatalf("unexpected balance accepted: %.30s", value)
		}
		j, e := planJourney(Result{404, []byte(`{"status":"empty"}`)}, Result{200, []byte(`{"data":[{"status":"active","amountRemaining":` + string(b) + `}]}`)}, false)
		if e != nil || j.State != "account_review" {
			t.Fatal(j, e)
		}
	}
	for _, value := range []string{`"0.00"`, `0`, `"100.50"`, `10.25`, `"999999999999999999.12345678"`} {
		if _, ok := positiveAmount([]byte(value)); !ok {
			t.Fatal("valid balance rejected", value)
		}
	}
}
func TestIncompleteCompletionContractCannotSubmit(t *testing.T) {
	for _, completion := range []string{`{"is_complete":true}`, `{"is_complete":true,"missing_documents":[],"missing_profile_fields":null}`, `{"is_complete":true,"missing_documents":null,"missing_profile_fields":[]}`} {
		body := []byte(`{"application":{"id":44,"status":"draft"},"application_completion":` + completion + `}`)
		j, e := planJourney(Result{200, body}, Result{200, []byte(`{"data":[]}`)}, true)
		if e != nil || j.State == "ready_for_submission" {
			t.Fatal("malformed completion ready", j, e)
		}
		up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
		c, _ := NewClient(up.URL + "/api/v1")
		c.http.Transport = up.Client().Transport
		hash := sha256.Sum256(body)
		result, e := (&Server{Client: c}).confirmSubmission(context.Background(), "alice", "/loan-applications/44/submit", hex.EncodeToString(hash[:]))
		up.Close()
		if e != nil || result.Status != 422 {
			t.Fatal("malformed completion submitted", result, e)
		}
	}
}
