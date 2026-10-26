package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testApp(t *testing.T) *App {
	t.Helper()
	a, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}
func call(t *testing.T, a *App, path string, body any) (int, []byte) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "http://127.0.0.1:8090"+path, bytes.NewReader(b))
	req.Header.Set("X-Demo-Token", a.token)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

type handset struct {
	t        *testing.T
	a        *App
	session  string
	sequence int
	last     Request
	response Response
}

func dial(t *testing.T, a *App) *handset { t.Helper(); h := &handset{t: t, a: a}; h.send(""); return h }
func (h *handset) send(input string) Response {
	h.t.Helper()
	h.last = Request{SessionID: h.session, Phone: "0970000001", Sequence: h.sequence + 1, RequestID: id("REQ"), Input: input}
	code, b := call(h.t, h.a, "/api/sessions", h.last)
	if code != 200 {
		h.t.Fatalf("input %q returned %d: %s", input, code, b)
	}
	if err := json.Unmarshal(b, &h.response); err != nil {
		h.t.Fatal(err)
	}
	h.session = h.response.SessionID
	h.sequence = h.response.Sequence
	return h.response
}
func (h *handset) inputs(values ...string) {
	h.t.Helper()
	for _, v := range values {
		h.send(v)
	}
}
func (h *handset) has(s string) {
	h.t.Helper()
	if !strings.Contains(h.response.Text, s) {
		h.t.Fatalf("expected %q in %s", s, h.response.Text)
	}
}
func register(t *testing.T, a *App) *handset {
	t.Helper()
	h := dial(t, a)
	h.inputs("1", "1", "1", "Demo Customer", "123456/78/1", "1")
	h.has("Select demo product")
	return h
}
func application(t *testing.T, a *App) (*handset, string) {
	t.Helper()
	h := register(t, a)
	h.inputs("1", "500", "3", "Demo Employer", "2000", "1")
	h.has("KYC pending")
	for key := range a.db.Applications {
		return h, key
	}
	t.Fatal("no application")
	return nil, ""
}
func demoAction(t *testing.T, a *App, action, ref, event string) {
	t.Helper()
	code, b := call(t, a, "/api/demo/actions", DemoAction{action, ref, event})
	if code != 200 {
		t.Fatalf("demo %s: %d %s", action, code, b)
	}
}
func accepted(t *testing.T, a *App) (*handset, string) {
	t.Helper()
	h, key := application(t, a)
	demoAction(t, a, "kyc_accept", key, "")
	demoAction(t, a, "offer", key, "")
	h.inputs("00", "3", "1", "1")
	h.has("1/3")
	h.send("1")
	h.has("2/3")
	h.send("1")
	h.has("3/3")
	h.send("1")
	h.has("Offer accepted")
	return h, key
}
func funded(t *testing.T, a *App) string {
	t.Helper()
	_, key := accepted(t, a)
	demoAction(t, a, "release_payout", key, "")
	for ref := range a.db.Operations {
		demoAction(t, a, "succeed", ref, "payout-success")
		return ref
	}
	t.Fatal("no payout")
	return ""
}
func repayment(t *testing.T, a *App, method, amount string) *handset {
	t.Helper()
	h := dial(t, a)
	h.inputs("1", "1", "1")
	h.has("includes overdue")
	if amount == "" {
		h.send("1")
	} else {
		h.inputs("3", amount)
	}
	h.send(method)
	source := "0970000001"
	if method == "2" {
		source = "DEMO-BANK-1234"
	}
	h.send(source)
	h.has("1 Confirm")
	h.send("1")
	return h
}

func TestCompleteJourneyAndFinancialFinality(t *testing.T) {
	a := testApp(t)
	payout := funded(t, a)
	p := a.db.Operations[payout]
	if len(a.db.Ledger) != 1 || p.Posting != "POSTED" {
		t.Fatal("payout not posted")
	}
	loan := a.db.Loans[p.LoanID]
	initial := loan.Balance
	h := repayment(t, a, "1", "100")
	h.has("Please wait for the Lipila payment prompt")
	if !h.response.End {
		t.Fatal("customer session should close for provider authorisation")
	}
	op := a.db.Operations[a.db.Sessions[h.session].TransactionID]
	if loan.Balance != initial {
		t.Fatal("initiation changed balance")
	}
	demoAction(t, a, "unknown", op.ID, "unknown-1")
	if loan.Balance != initial {
		t.Fatal("unknown result changed balance")
	}
	second := dial(t, a)
	second.inputs("1", "1")
	second.has("Do not repeat this payment")
	if len(a.db.Operations) != 2 {
		t.Fatal("pending retry created another payment")
	}
	demoAction(t, a, "succeed_unposted", op.ID, "success-1")
	second.send("1")
	second.has("account update pending")
	if loan.Balance != initial {
		t.Fatal("unposted success changed balance")
	}
	demoAction(t, a, "post", op.ID, "")
	if loan.Balance != initial-10000 || len(a.db.Ledger) != 2 {
		t.Fatal("incorrect repayment allocation")
	}
	demoAction(t, a, "succeed_unposted", op.ID, "success-1")
	demoAction(t, a, "post", op.ID, "")
	if len(a.db.Ledger) != 2 {
		t.Fatal("duplicate posting")
	}
	demoAction(t, a, "fail", op.ID, "late-failure")
	if op.Status != "SUCCEEDED" || loan.Balance != initial-10000 {
		t.Fatal("late failure regressed success")
	}
	demoAction(t, a, "reverse", op.ID, "")
	demoAction(t, a, "reverse", op.ID, "")
	if loan.Balance != initial || len(a.db.Ledger) != 3 {
		t.Fatal("reversal not linked and idempotent")
	}
	if a.db.Ledger[2].LinkedEntry != a.db.Ledger[1].ID {
		t.Fatal("missing reversal link")
	}
	bank := repayment(t, a, "2", "")
	bank.has("No transfer is required")
	bankOp := a.db.Operations[a.db.Sessions[bank.session].TransactionID]
	demoAction(t, a, "succeed", bankOp.ID, "bank-success")
	if loan.Balance != 0 || loan.Status != "CLOSED" {
		t.Fatal("bank repayment did not settle demo loan")
	}
}
func TestReplayConflictPersistenceAndNoCredentials(t *testing.T) {
	a := testApp(t)
	h, key := application(t, a)
	original := h.response
	code, b := call(t, a, "/api/sessions", h.last)
	if code != 200 {
		t.Fatal(string(b))
	}
	var replay Response
	_ = json.Unmarshal(b, &replay)
	if replay != original || len(a.db.Applications) != 1 {
		t.Fatal("submit replay differed")
	}
	changed := h.last
	changed.Input = "2"
	if code, _ = call(t, a, "/api/sessions", changed); code != 409 {
		t.Fatal("altered replay accepted")
	}
	encoded, err := json.Marshal(a.db)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(`"pin_hash"`)) || bytes.Contains(encoded, []byte("RegistrationHash")) {
		t.Fatal("obsolete credential fields persisted")
	}
	encrypted, err := os.ReadFile(filepath.Join(a.dir, "state.enc"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte("Demo Customer")) {
		t.Fatal("state is not encrypted")
	}
	dir := a.dir
	a.Close()
	restored, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if restored.db.Applications[key].Status != "KYC_PENDING" {
		t.Fatal("application lost on restart")
	}
	code, _ = call(t, restored, "/api/sessions", h.last)
	if code != 200 {
		t.Fatal("durable replay lost on restart")
	}
}
func TestExpiredConfirmationCannotReplayPrivateData(t *testing.T) {
	a := testApp(t)
	h, _ := application(t, a)
	private := h.last
	a.db.Sessions[h.session].ConfirmedUntil = time.Now().Add(-time.Minute)
	if code, _ := call(t, a, "/api/sessions", private); code != 409 {
		t.Fatal("cached private response bypassed expired confirmation")
	}
	h.send("00")
	h.has("Session expired")
	h = dial(t, a)
	h.inputs("1")
	h.has("Welcome to MightyFin, Demo Customer")
	req := httptest.NewRequest("GET", "http://127.0.0.1:8090/api/demo/state", nil)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal("staff state exposed without local token")
	}
	req = httptest.NewRequest("GET", "http://evil.example:8090/", nil)
	w = httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("DNS rebinding host accepted")
	}
}
func TestDraftResumptionAndStaleConcurrentEdit(t *testing.T) {
	a := testApp(t)
	h := register(t, a)
	h.inputs("1", "500")
	h.has("tenure")
	s := a.db.Sessions[h.session]
	key := s.ApplicationID
	second := dial(t, a)
	second.inputs("1", "1")
	second.has("tenure")
	second.send("3")
	h.send("6")
	h.has("Application changed")
	if a.db.Applications[key].Term != 3 {
		t.Fatal("stale session overwrote draft")
	}
	if len(a.db.Applications) != 1 {
		t.Fatal("duplicate draft")
	}
}
func TestQuoteRevalidationAndPaymentRequestReplay(t *testing.T) {
	a := testApp(t)
	funded(t, a)
	h := dial(t, a)
	h.inputs("1", "1", "1", "1", "1", "0970000001")
	h.has("1 Confirm")
	s := a.db.Sessions[h.session]
	a.db.Quotes[s.QuoteID].ExpiresAt = time.Now().Add(-time.Minute)
	h.send("1")
	h.has("Quote expired")
	if len(a.db.Operations) != 1 {
		t.Fatal("expired quote initiated payment")
	}
	h.inputs("1", "1", "0970000001", "1")
	h.has("Please wait for the Lipila payment prompt")
	req := h.last
	code, _ := call(t, a, "/api/sessions", req)
	if code != 200 || len(a.db.Operations) != 2 {
		t.Fatal("payment replay duplicated collection")
	}
}
func TestOfferVersionsAndExplicitAcceptance(t *testing.T) {
	a := testApp(t)
	h, key := application(t, a)
	demoAction(t, a, "kyc_accept", key, "")
	demoAction(t, a, "offer", key, "")
	h.inputs("00", "3", "1", "1")
	h.has("1/3")
	h.send("1")
	for _, o := range a.db.Offers {
		if o.Status == "ACCEPTED" {
			t.Fatal("next inferred acceptance")
		}
	}
	demoAction(t, a, "revise_offer", key, "")
	h.send("1")
	h.has("Offer changed")
	for _, o := range a.db.Offers {
		if o.Status == "ACCEPTED" {
			t.Fatal("stale offer accepted")
		}
	}
	code, _ := call(t, a, "/api/demo/actions", DemoAction{Action: "release_payout", Reference: key})
	if code != 409 {
		t.Fatal("payout allowed without acceptance")
	}
}
func TestWithdrawalRacesPayout(t *testing.T) {
	a := testApp(t)
	_, key := accepted(t, a)
	s := &Session{CustomerID: a.db.Applications[key].CustomerID, ApplicationID: key}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = a.transaction(func() error { a.withdraw(s); return nil }) }()
	go func() {
		defer wg.Done()
		call(t, a, "/api/demo/actions", DemoAction{Action: "release_payout", Reference: key})
	}()
	wg.Wait()
	if a.db.Applications[key].Status == "WITHDRAWN" {
		for _, op := range a.db.Operations {
			if op.Kind == "PAYOUT" {
				t.Fatal("withdrawal and payout both succeeded")
			}
		}
	}
}
func TestRollbackRemovesNewRecords(t *testing.T) {
	a := testApp(t)
	err := a.transaction(func() error { a.db.Cases["new"] = &Case{ID: "new"}; return errors.New("abort") })
	if err == nil || len(a.db.Cases) != 0 {
		t.Fatal("failed transaction retained new records")
	}
}
func TestMoneyUsesExactMinorUnits(t *testing.T) {
	for input, want := range map[string]int64{"1": 100, "1.01": 101, "999999999.99": 99999999999} {
		n, e := parseMoney(input)
		if e != nil || n != want {
			t.Fatalf("%s: got %d %v", input, n, e)
		}
	}
	for _, v := range []string{"0", "-1", "NaN", "1e3", "1.001", "1.", ".5", "1,000"} {
		if _, e := parseMoney(v); e == nil {
			t.Fatal(fmt.Sprintf("accepted invalid money %q", v))
		}
	}
}
