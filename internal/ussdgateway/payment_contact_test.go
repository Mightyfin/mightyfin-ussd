package ussdgateway

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type fundingPayments struct {
	fakePayments
	caller, payer, lookup string
	loans                 []PayLoan
}

func (f *fundingPayments) Welcome(context.Context, string) (string, error) { return "George", nil }
func (f *fundingPayments) Loans(_ context.Context, phone string, _ bool) ([]PayLoan, error) {
	f.lookup = phone
	return f.loans, nil
}
func (f *fundingPayments) CollectFrom(c context.Context, caller, payer string, id int64, amount, key string) (string, error) {
	f.caller = caller
	f.payer = payer
	return f.Collect(c, caller, id, amount, key)
}
func TestPaymentNumberConfirmationAndAlternateWallet(t *testing.T) {
	f := &fundingPayments{fakePayments: fakePayments{status: "pending"}, loans: []PayLoan{{ID: 201, Due: "25.50"}}}
	s := &Server{Key: testKey, Payments: f, Reader: &fakeReader{}}
	for _, step := range []struct{ answer, kind, want, flow string }{
		{"29", "1", "Welcome George", "FC"}, {"3", "2", "Payment number +260762167007", "FC"},
		{"2", "2", "Enter the mobile-money number", "FC"}, {"123", "2", "valid mobile-money number", "FC"},
		{"0962223168", "2", "Payment number +260962223168", "FC"}, {"1", "2", "Due K25.50", "FC"},
		{"1", "2", "Enter payment amount", "FC"}, {"25.50", "2", "From +260962223168", "FC"},
		{"1", "2", "Please wait for the payment prompt.", "FB"}, {"1", "2", "Please wait for the payment prompt.", "FB"},
	} {
		w := hobby(s, "funding", "0762167007", step.answer, step.kind)
		if w.Code != 200 || !strings.Contains(w.Body.String(), step.want) || w.Header().Get("FreeFlow") != step.flow || phoneUnits(w.Body.String()) > 160 {
			t.Fatal(step, w.Code, w.Body.String())
		}
	}
	if f.lookup != "260762167007" || f.caller != "260762167007" || f.payer != "260962223168" || f.calls != 1 {
		t.Fatal("payer selection changed account or duplicated debit", f)
	}
}
func TestNoDueLoansAndNumberNavigation(t *testing.T) {
	f := &fundingPayments{}
	s := &Server{Key: testKey, Payments: f, Reader: &fakeReader{}}
	hobby(s, "no-due", "0762167007", "29", "1")
	hobby(s, "no-due", "0762167007", "3", "2")
	w := hobby(s, "no-due", "0762167007", "1", "2")
	if w.Body.String() != "You have no pending loan to pay back." || w.Header().Get("FreeFlow") != "FB" || f.calls != 0 {
		t.Fatal(w.Body.String())
	}
	f.loans = []PayLoan{{ID: 1, Due: "10.00"}}
	hobby(s, "nav-source", "0762167007", "29", "1")
	for _, a := range []string{"3", "2", "*", "1", "*", "#"} {
		w = hobby(s, "nav-source", "0762167007", a, "2")
		if w.Code != 200 || w.Header().Get("FreeFlow") != "FC" {
			t.Fatal(a, w.Body.String())
		}
	}
	if f.calls != 0 {
		t.Fatal("navigation initiated payment")
	}
}
func TestDueLoanPagination(t *testing.T) {
	f := &fundingPayments{}
	for i := 1; i <= 8; i++ {
		f.loans = append(f.loans, PayLoan{ID: int64(i), Due: "9999999.99"})
	}
	s := &Server{Key: testKey, Payments: f, Reader: &fakeReader{}}
	hobby(s, "pages", "0762167007", "29", "1")
	hobby(s, "pages", "0762167007", "3", "2")
	w := hobby(s, "pages", "0762167007", "1", "2")
	for i := 0; i < 2; i++ {
		if phoneUnits(w.Body.String()) > 160 {
			t.Fatal(w.Body.String())
		}
		w = hobby(s, "pages", "0762167007", "4", "2")
	}
	if !strings.Contains(w.Body.String(), "...7") {
		t.Fatal(fmt.Sprint(w.Body.String()))
	}
	w = hobby(s, "pages", "0762167007", "1", "2")
	if s.sessions["hobbiton:pages"].hobbyPaymentPath != "3*7" {
		t.Fatal("wrong loan selected on page 3", w.Body.String())
	}
}
