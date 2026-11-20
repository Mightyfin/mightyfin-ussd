package ussdgateway

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Mightyfin/mightyfin-ussd/internal/nyali"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeNyaliAPI struct {
	customer     nyali.Customer
	empty        bool
	verifyErr    error
	calls        int
	err          error
	listed       []nyali.Payment
	verifyPhones []string
	lastPayer    string
	lastRef      string
	amount       string
	posted       bool
	status       string
	paymentErr   error
}

func (f *fakeNyaliAPI) Verify(_ context.Context, phone string) (nyali.VerifyResponse, error) {
	f.verifyPhones = append(f.verifyPhones, phone)
	if f.verifyErr != nil {
		return nyali.VerifyResponse{}, f.verifyErr
	}
	if f.empty {
		return nyali.VerifyResponse{OK: true}, nil
	}
	return nyali.VerifyResponse{OK: true, Clients: []nyali.Customer{f.customer}}, nil
}
func (f *fakeNyaliAPI) Collect(_ context.Context, id int64, payer, amount, ref string) (nyali.Collection, error) {
	f.calls++
	f.lastPayer = payer
	f.lastRef = ref
	f.amount = amount
	if f.err != nil {
		return nyali.Collection{}, f.err
	}
	status := f.status
	if status == "" {
		status = "Pending"
	}
	return nyali.Collection{OK: true, Payment: nyali.Payment{Reference: "provider-fixture", External: ref, LoanID: id, MSISDN: payer, Amount: json.Number(amount), Status: status, Posted: f.posted}}, nil
}
func (f *fakeNyaliAPI) Payments(context.Context, string) (nyali.Payments, error) {
	return nyali.Payments{OK: true, Count: len(f.listed), Payments: f.listed}, f.paymentErr
}
func newNyaliFixture(t *testing.T) (*NyaliPayments, *fakeNyaliAPI, []byte, string) {
	t.Helper()
	key := []byte(strings.Repeat("x", 32))
	path := filepath.Join(t.TempDir(), "payments.enc")
	store, e := nyali.OpenStore(path, key)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(store.Close)
	api := &fakeNyaliAPI{customer: nyali.Customer{ID: 3, Name: "George Fixture", Mobile: "+260 970 000 001", Loans: []nyali.Loan{{ID: 42, Status: "disbursed", Outstanding: nyali.Outstanding{Total: "1000", Arrears: "0"}, Next: &nyali.Installment{TotalDue: "250", DueDate: "2026-12-09"}}}}}
	return &NyaliPayments{Client: api, Store: store}, api, key, path
}
func TestNyaliHandsetCollectionAndPostedState(t *testing.T) {
	p, api, _, _ := newNyaliFixture(t)
	s := &Server{Key: testKey, Payments: p, Leads: &testLeads{}, Reader: &fakeReader{}}
	for _, step := range []struct{ answer, kind, want, flow string }{
		{"29", "1", "Welcome George", "FC"}, {"1", "2", "Payment number +260970000001", "FC"},
		{"2", "2", "Enter the mobile-money number", "FC"}, {"0970000002", "2", "Payment number +260970000002", "FC"},
		{"1", "2", "Next K250.00", "FC"}, {"1", "2", "Enter payment amount", "FC"},
		{"25", "2", "From +260970000002", "FC"}, {"1", "2", "Please wait for the payment prompt.", "FB"},
		{"1", "2", "Please wait for the payment prompt.", "FB"},
	} {
		w := hobby(s, "nyali-flow", "0970000001", step.answer, step.kind)
		if w.Code != 200 || w.Header().Get("FreeFlow") != step.flow || !strings.Contains(w.Body.String(), step.want) || phoneUnits(w.Body.String()) > 160 || strings.Contains(strings.ToLower(w.Body.String()), "nyali") || strings.Contains(strings.ToLower(w.Body.String()), "lipila") {
			t.Fatal(step, w.Code, w.Body.String())
		}
	}
	if api.calls != 1 || api.lastPayer != "260970000002" {
		t.Fatal(api.calls, api.lastPayer)
	}
	for _, phone := range api.verifyPhones {
		if phone != "260970000001" {
			t.Fatal("payer used for borrower lookup", phone)
		}
	}
	api.listed = []nyali.Payment{{Reference: "provider-fixture", External: api.lastRef, LoanID: 42, MSISDN: api.lastPayer, Amount: "25.00", Currency: "ZMW", Status: "Successful", Posted: false}}
	status, e := p.Status(context.Background(), "260970000001", 42)
	if e != nil || status != "posting_pending" {
		t.Fatal(status, e)
	}
	api.listed[0].Posted = true
	status, e = p.Status(context.Background(), "260970000001", 42)
	if e != nil || status != "completed" {
		t.Fatal(status, e)
	}
	api.customer.Loans = nil

	loans, e := p.Loans(context.Background(), "260970000001", true)
	if e != nil || len(loans) != 1 || loans[0].ID != 42 {
		t.Fatal("closed-loan receipt lost", loans, e)
	}
}
func TestNyaliRestartUnknownAndNoNewReference(t *testing.T) {
	p, api, key, path := newNyaliFixture(t)
	api.err = &nyali.APIError{Status: 500, Uncertain: true}
	original := strings.Repeat("a", 64)
	status, e := p.CollectFrom(context.Background(), "260970000001", "260970000002", 42, "25", original)
	if e != nil || status != "processing" {
		t.Fatal(status, e)
	}
	p.Store.Close()
	store, e := nyali.OpenStore(path, key)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	p.Store = store
	for _, ref := range []string{original, strings.Repeat("b", 64)} {
		p.CollectFrom(context.Background(), "260970000001", "260970000002", 42, "25", ref)
	}
	if api.calls != 1 {
		t.Fatal("restart or new session resent collection", api.calls)
	}
	// Absence from the list is never failure, even when the result window is full.
	api.listed = nil
	status, e = p.Status(context.Background(), "260970000001", 42)
	if e != nil || status != "processing" {
		t.Fatal(status, e)
	}
}
func TestNyaliOwnershipAndProviderMismatch(t *testing.T) {
	p, api, _, _ := newNyaliFixture(t)
	p.CollectFrom(context.Background(), "260970000001", "260970000002", 999, "25", strings.Repeat("c", 64))
	if api.calls != 0 {
		t.Fatal("foreign loan charged")
	}
	api.customer.Mobile = "260970000099"
	if _, e := p.Loans(context.Background(), "260970000001", false); e == nil {
		t.Fatal("foreign account disclosed")
	}
	api.customer.Mobile = "260970000001"
	p.CollectFrom(context.Background(), "260970000001", "260970000002", 42, "25", strings.Repeat("d", 64))
	api.listed = []nyali.Payment{{Reference: "provider-fixture", External: api.lastRef, LoanID: 42, MSISDN: api.lastPayer, Amount: "26.00", Currency: "ZMW", Status: "Successful", Posted: true}}
	status, e := p.Status(context.Background(), "260970000001", 42)
	if e != nil || status == "completed" {
		t.Fatal("mismatch claimed posted", status, e)
	}
	api.paymentErr = errors.New("offline")
	status, _ = p.Status(context.Background(), "260970000001", 42)
	if status != "processing" {
		t.Fatal(status)
	}
}
func TestNyaliEligibilityAndAmount(t *testing.T) {
	p, api, _, _ := newNyaliFixture(t)
	for _, state := range []string{"pending", "approved", "reviewed"} {
		api.customer.Loans[0].Status = state

		loans, e := p.Loans(context.Background(), "260970000001", false)
		if e != nil || len(loans) != 0 {
			t.Fatal(state, loans, e)
		}
	}
	api.customer.Loans[0].Status = "disbursed"
	api.customer.Loans[0].Outstanding.Total = "0"

	loans, e := p.Loans(context.Background(), "260970000001", false)
	if e != nil || len(loans) != 0 {
		t.Fatal(loans, e)
	}
	api.customer.Loans[0].Outstanding.Total = "1000"

	status, _ := p.CollectFrom(context.Background(), "260970000001", "260970000002", 42, "1000.01", strings.Repeat("e", 64))
	if status != "balance_changed" || api.calls != 0 {
		t.Fatal(status)
	}
	api.err = &nyali.APIError{Status: 503, Reason: "collections disabled"}
	status, _ = p.CollectFrom(context.Background(), "260970000001", "260970000002", 42, "1", strings.Repeat("f", 64))
	if status != "unavailable" || p.Ready(context.Background()) {
		t.Fatal(status)
	}
	if time.Now().Unix() >= p.disabledUntil.Load() {
		t.Fatal("provider pause not respected")
	}
}

func TestNyaliRequestMemoAndUnknownCaller(t *testing.T) {
	p, api, _, _ := newNyaliFixture(t)
	api.empty = true
	ctx := nyaliRequestContext(context.Background())
	p.Welcome(ctx, "260970000001")
	if found, err := p.HasPayable(ctx, "260970000001"); err != nil || found {
		t.Fatal(found, err)
	}
	if len(api.verifyPhones) != 1 {
		t.Fatal("repeated lookup within initial callback", api.verifyPhones)
	}
	if _, err := p.Loans(context.Background(), "260970000001", false); err == nil {
		t.Fatal("unknown caller treated as settled account")
	}
	api.empty = false
	if found, err := p.HasPayable(nyaliRequestContext(context.Background()), "260970000001"); err != nil || !found {
		t.Fatal("new callback used stale result", found, err)
	}
	api.verifyErr = errors.New("offline")
	ctx = nyaliRequestContext(context.Background())
	count := len(api.verifyPhones)
	p.Welcome(ctx, "260970000001")
	p.HasPayable(ctx, "260970000001")
	if len(api.verifyPhones) != count+1 {
		t.Fatal("outage caused duplicate lookup")
	}
}
