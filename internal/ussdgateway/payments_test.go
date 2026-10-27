package ussdgateway

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type fakePayments struct {
	calls              int
	phone, amount, key string
	status             string
	uncertain          bool
	loan               int64
}

func (f *fakePayments) Loans(context.Context, string, bool) ([]PayLoan, error) {
	return []PayLoan{{ID: 201}}, nil
}
func (f *fakePayments) Collect(_ context.Context, phone string, loan int64, amount, key string) (string, error) {
	f.calls++
	f.phone = phone
	f.loan = loan
	f.amount = amount
	f.key = key
	if f.uncertain {
		return "", fmt.Errorf("timeout")
	}
	return f.status, nil
}
func (f *fakePayments) Status(context.Context, string, int64) (string, error) { return f.status, nil }
func TestPaymentConfirmationAndRetry(t *testing.T) {
	f := &fakePayments{status: "pending"}
	s := &Server{Key: testKey, Reader: &fakeReader{}, Payments: f}
	for _, step := range []struct{ text, want string }{{"", "3 Make payment"}, {"3", "Select loan"}, {"3*1", "Enter payment amount"}, {"3*1*12.5", "Pay ZMW 12.50"}, {"3*1*12.5*1", "enter your wallet PIN there"}, {"3*1*12.5*1", "enter your wallet PIN there"}} {
		w := callback(s, "pay", step.text, "+260970000001", "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), step.want) || phoneUnits(w.Body.String()) > 160 || strings.Contains(strings.ToLower(w.Body.String()), "lipila") {
			t.Fatal(w.Code, w.Body.String())
		}
		if step.text != "3*1*12.5*1" && f.calls != 0 {
			t.Fatal("payment before consent")
		}
	}
	if f.calls != 1 || f.phone != "260970000001" || f.loan != 201 || f.amount != "12.50" || len(f.key) != 64 {
		t.Fatal("wrong payment binding", f)
	}
}
func TestPaymentCancellationAndAmounts(t *testing.T) {
	for _, text := range []string{"3*1*0", "3*1*0.00", "3*1*25*0", "3*1*25*2"} {
		f := &fakePayments{}
		s := &Server{Key: testKey, Payments: f}
		parts := strings.Split(text, "*")
		callback(s, "pay", "", "260970000001", "")
		for i := 1; i <= len(parts); i++ {
			callback(s, "pay", strings.Join(parts[:i], "*"), "260970000001", "")
		}
		if f.calls != 0 {
			t.Fatal("unconfirmed payment")
		}
	}
	for _, input := range []string{"0", "0.00", "-1", "1e2", "1.001", "10000000", "01", ".50", " 1"} {
		if _, ok := amountString(input); ok {
			t.Fatal(input)
		}
	}
	for _, input := range []string{"0.01", "1", "1.2", "9999999.99"} {
		if _, ok := amountString(input); !ok {
			t.Fatal(input)
		}
	}
}
func TestPaymentAfterBackAndUncertainResponse(t *testing.T) {
	f := &fakePayments{uncertain: true}
	s := &Server{Key: testKey, Reader: &fakeReader{}, Payments: f}
	for _, text := range []string{"", "1", "1*9", "1*9*3", "1*9*3*1", "1*9*3*1*10", "1*9*3*1*10*1"} {
		w := callback(s, "pay", text, "260970000001", "")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := callback(s, "pay", "1*9*3*1*10*1", "260970000001", "")
	if f.calls != 1 || !strings.Contains(w.Body.String(), "processing") || strings.Contains(w.Body.String(), "submitted") {
		t.Fatal(f.calls, w.Body.String())
	}
}
func TestPaymentStatesNeverInventSuccess(t *testing.T) {
	for _, state := range []string{"pending", "unknown", "review_required", "initiating", "failed", "none", "unexpected", "completed"} {
		f := &fakePayments{status: state}
		s := &Server{Key: testKey, Payments: f}
		callback(s, "status", "", "260970000001", "")
		callback(s, "status", "4", "260970000001", "")
		w := callback(s, "status", "4*1", "260970000001", "")
		if strings.Contains(w.Body.String(), "confirmed and recorded") != (state == "completed") {
			t.Fatal(state, w.Body.String())
		}
		if f.calls != 0 || phoneUnits(w.Body.String()) > 160 {
			t.Fatal("status created payment")
		}
	}
}
