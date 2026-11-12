package ussdgateway

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type testLeads struct {
	record  LeadRecord
	actions []string
	fail    bool
}

func (f *testLeads) Request(_ context.Context, phone, id, action string, fields map[string]any) (LeadRecord, error) {
	if f.fail {
		return LeadRecord{}, fmt.Errorf("unavailable")
	}
	f.actions = append(f.actions, action)
	switch action {
	case "catalogue":
		return LeadRecord{Products: []LeadProduct{{ID: 10, Name: "Salary loan"}, {ID: 11, Name: "Business loan"}}}, nil
	case "start":
		if f.record.Reference == "" {
			f.record = LeadRecord{Reference: "LEAD-000123", Editable: true}
		}
	case "save":
		if v, ok := fields["product_id"]; ok {
			f.record.ProductID = v.(int64)
			f.record.NextStep = "amount"
		}
		if v, ok := fields["amount"]; ok {
			if v == nil {
				f.record.Amount = nil
			} else {
				a := v.(string)
				f.record.Amount = &a
			}
			f.record.NextStep = "name"
		}
		if _, ok := fields["name"]; ok {
			f.record.NextStep = "review"
		}
	case "submit":
		f.record.NextStep = "recorded"
	case "assistance":
		if f.record.Reference == "" {
			f.record.Reference = "LEAD-000123"
		}
	}
	return f.record, nil
}
func TestProgressiveUssdLeadWithoutAccountOrCredentials(t *testing.T) {
	f := &testLeads{}
	s := &Server{Key: testKey, Leads: f, Reader: &fakeReader{}}
	for _, step := range []struct{ answer, kind, want, flow string }{
		{"29", "1", "Welcome to MightyFin", "FC"}, {"1", "2", "2 Continue", "FC"},
		{"1", "2", "2 Continue", "FC"}, // a retry of opening Apply cannot record consent
		{"2", "2", "1 Salary loan", "FC"}, {"1", "2", "Requested amount", "FC"},
		{"500", "2", "Your full name", "FC"}, {"Example Person", "2", "Requested: K500.00", "FC"},
		{"1", "2", "LEAD-000123", "FB"}, {"1", "2", "LEAD-000123", "FB"},
	} {
		w := hobby(s, "lead-flow", "0762167007", step.answer, step.kind)
		body := w.Body.String()
		if w.Code != 200 || !strings.Contains(body, step.want) || w.Header().Get("FreeFlow") != step.flow || phoneUnits(body) > 160 || strings.Contains(body, "OTP") || strings.Contains(body, "PIN") {
			t.Fatal(step, w.Code, body)
		}
	}
	n := 0
	for _, a := range f.actions {
		if a == "submit" {
			n++
		}
	}
	if n != 1 {
		t.Fatal("duplicate follow-up", f.actions)
	}
	// New session resumes the same already recorded enquiry, without another account.
	hobby(s, "lead-resume", "0762167007", "29", "1")
	hobby(s, "lead-resume", "0762167007", "1", "2")
	w := hobby(s, "lead-resume", "0762167007", "2", "2")
	if !strings.Contains(w.Body.String(), "LEAD-000123 is recorded") {
		t.Fatal(w.Body.String())
	}
}
func TestLeadDraftResumeSkipAndAssistance(t *testing.T) {
	f := &testLeads{}
	s := &Server{Key: testKey, Leads: f, Reader: &fakeReader{}}
	for _, a := range []string{"29", "1", "2", "1", "0"} {
		kind := "2"
		if a == "29" {
			kind = "1"
		}
		hobby(s, "draft", "0762167007", a, kind)
	}
	hobby(s, "new-session", "0762167007", "29", "1")
	hobby(s, "new-session", "0762167007", "1", "2")
	w := hobby(s, "new-session", "0762167007", "2", "2")
	if !strings.Contains(w.Body.String(), "Your full name") {
		t.Fatal(w.Body.String())
	}
	w = hobby(s, "new-session", "0762167007", "0", "2")
	if !strings.Contains(w.Body.String(), "Officer to advise") {
		t.Fatal(w.Body.String())
	}
	hobby(s, "assist", "0762167007", "29", "1")
	hobby(s, "assist", "0762167007", "3", "2")
	w = hobby(s, "assist", "0762167007", "2", "2")
	if !strings.Contains(w.Body.String(), "Assistance requested") {
		t.Fatal(w.Body.String())
	}
}

func TestExistingBorrowerGetsRepaymentFirst(t *testing.T) {
	f := &testLeads{record: LeadRecord{HasLoan: true}}
	p := &fakePayments{status: "pending"}
	s := &Server{Key: testKey, Leads: f, Payments: p, Reader: &fakeReader{}}
	w := hobby(s, "borrower", "0762167007", "29", "1")
	if !strings.Contains(w.Body.String(), "1 Repay your loan") || strings.Contains(w.Body.String(), "Apply for") {
		t.Fatal(w.Body.String())
	}
	w = hobby(s, "borrower", "0762167007", "1", "2")
	if !strings.Contains(w.Body.String(), "Payment number") {
		t.Fatal(w.Body.String())
	}
}

func TestLeadNameDoesNotBecomeAccountGreeting(t *testing.T) {
	f := &testLeads{record: LeadRecord{Reference: "LEAD-000123", Name: "Prospect Name", Editable: true}}
	s := &Server{Key: testKey, Leads: f, Reader: &fakeReader{}}
	w := hobby(s, "prospect-name", "0762167007", "29", "1")
	if !strings.HasPrefix(w.Body.String(), "Welcome to MightyFin\n") || strings.Contains(w.Body.String(), "Prospect Name") {
		t.Fatal(w.Body.String())
	}
}
