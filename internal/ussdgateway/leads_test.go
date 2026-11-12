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
		return LeadRecord{Products: []LeadProduct{{ID: 10, Name: "Salary loan", Minimum: "100.00", Maximum: "1000.00"}, {ID: 11, Name: "Business loan", Minimum: "200.00", Maximum: "2000.00"}}}, nil
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

func TestLeadProductAmountRange(t *testing.T) {
	c := &session{leadRecord: LeadRecord{ProductID: 10}, leadProducts: []LeadProduct{{ID: 10, Minimum: "100.00", Maximum: "1000.00"}}}
	for _, tt := range []struct {
		value string
		valid bool
	}{{"99.99", false}, {"100", true}, {"1000.00", true}, {"1000.01", false}, {"1e3", false}, {"100.001", false}, {"0", false}} {
		_, ok := leadAmountInRange(c, tt.value)
		if ok != tt.valid {
			t.Fatal(tt)
		}
	}
	prompt := leadAmountPrompt(c)
	if !strings.Contains(prompt, "Min K100.00 - Max K1000.00") || phoneUnits(prompt) > 160 {
		t.Fatal(prompt)
	}
	c.leadProducts[0].Minimum = "0.25"
	c.leadProducts[0].Maximum = "1.00"
	if _, ok := leadAmountInRange(c, "0.50"); !ok {
		t.Fatal("fractional amount in configured range rejected")
	}
	c.leadProducts[0].Maximum = ""
	if _, ok := leadAmountInRange(c, "0.50"); ok || !strings.Contains(leadAmountPrompt(c), "unavailable") {
		t.Fatal("missing range invented")
	}
	c.leadProducts[0].Minimum = "10000000.00"
	c.leadProducts[0].Maximum = "9999999999999.99"
	if _, ok := leadAmountInRange(c, "10000000"); !ok || phoneUnits(leadAmountPrompt(c)) > 160 {
		t.Fatal("valid large configured amount rejected")
	}
}
func TestLeadAmountPromptAndCorrection(t *testing.T) {
	f := &testLeads{}
	s := &Server{Key: testKey, Leads: f, Reader: &fakeReader{}}
	for _, step := range []struct{ input, kind, want string }{
		{"29", "1", "Apply for a loan"}, {"1", "2", "2 Continue"}, {"2", "2", "Salary loan"},
		{"1", "2", "Min K100.00 - Max K1000.00"}, {"99.99", "2", "Min K100.00 - Max K1000.00"},
		{"1000.01", "2", "Min K100.00 - Max K1000.00"}, {"100", "2", "Your full name"},
		{"*", "2", "Min K100.00 - Max K1000.00"},
	} {
		w := hobby(s, "limits", "0762167007", step.input, step.kind)
		if !strings.Contains(w.Body.String(), step.want) || w.Header().Get("FreeFlow") != "FC" {
			t.Fatal(step, w.Body.String())
		}
	}
}
