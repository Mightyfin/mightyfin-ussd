package app

import (
	"strings"
	"testing"
	"time"
)

func menuSession(a *App) *Session {
	a.db.Customers["customer"] = &Customer{ID: "customer", Name: "Demo Customer", Phone: "+260970000001"}
	return &Session{ID: "menu-session", CustomerID: "customer", Phone: "+260970000001", Screen: "main", ConfirmedUntil: time.Now().Add(time.Minute)}
}
func TestPersonalisedPrimaryAction(t *testing.T) {
	tests := []struct {
		name, status, label, screen string
		setup                       func(*App)
	}{
		{name: "new customer", label: "Apply for a loan", screen: "apply_product"},
		{name: "draft", status: "DRAFT", label: "Continue your application", screen: "apply_product"},
		{name: "documents", status: "KYC_PENDING", label: "Complete your documents", screen: "kyc"},
		{name: "review", status: "UNDER_REVIEW", label: "Check your progress", screen: "application_status"},
		{name: "correction", status: "ACTION_REQUIRED", label: "Check your progress", screen: "application_status"},
		{name: "offer", status: "OFFER_READY", label: "Review your offer", screen: "offer", setup: func(a *App) {
			a.db.Offers["offer"] = &Offer{ID: "offer", ApplicationID: "application", Status: "ISSUED", Version: 1, ExpiresAt: time.Now().Add(time.Hour)}
		}},
		{name: "accepted offer", status: "OFFER_READY", label: "Check your progress", screen: "application_status", setup: func(a *App) {
			a.db.Offers["offer"] = &Offer{ID: "offer", ApplicationID: "application", Status: "ACCEPTED"}
		}},
		{name: "active loan", label: "Repay your loan", screen: "loans", setup: func(a *App) {
			a.db.Loans["loan"] = &Loan{ID: "loan", CustomerID: "customer", Status: "ACTIVE", Balance: 10000}
		}},
		{name: "pending payment before repay", label: "Check your payment", screen: "transaction", setup: func(a *App) {
			a.db.Loans["loan"] = &Loan{ID: "loan", CustomerID: "customer", Status: "ACTIVE", Balance: 10000}
			a.db.Operations["payment"] = &Operation{ID: "payment", CustomerID: "customer", Kind: "REPAYMENT", Status: "UNKNOWN"}
		}},
		{name: "posting pending", label: "Check your payment", screen: "transaction", setup: func(a *App) {
			a.db.Operations["payment"] = &Operation{ID: "payment", CustomerID: "customer", Kind: "REPAYMENT", Status: "SUCCEEDED", Posting: "POSTING_PENDING"}
		}},
		{name: "pending payout", status: "OFFER_READY", label: "Check your payout", screen: "transaction", setup: func(a *App) {
			a.db.Operations["payout"] = &Operation{ID: "payout", CustomerID: "customer", Kind: "PAYOUT", Status: "PENDING"}
		}},
		{name: "closed loan", status: "OFFER_READY", label: "Apply for a loan", screen: "apply_product", setup: func(a *App) {
			a.db.Loans["loan"] = &Loan{ID: "loan", CustomerID: "customer", ApplicationID: "application", Status: "CLOSED", Balance: 0}
		}},
		{name: "another customer's loan", label: "Apply for a loan", screen: "apply_product", setup: func(a *App) {
			a.db.Loans["other"] = &Loan{ID: "other", CustomerID: "someone-else", Status: "ACTIVE", Balance: 10000}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp(t)
			s := menuSession(a)
			if tc.status != "" {
				a.db.Applications["application"] = &Application{ID: "application", CustomerID: "customer", Status: tc.status, Answers: map[string]string{}}
			}
			if tc.setup != nil {
				tc.setup(a)
			}
			view := a.render(s)
			if !strings.Contains(view.Text, "1 "+tc.label) {
				t.Fatalf("wrong menu: %s", view.Text)
			}
			if tc.label != "Apply for a loan" && strings.Contains(view.Text, "Apply for a loan") {
				t.Fatal("irrelevant apply action still visible")
			}
			a.advance(s, "1")
			if s.Screen != tc.screen {
				t.Fatalf("selected %s instead of %s", s.Screen, tc.screen)
			}
		})
	}
}
func TestPersonalisationRequiresAuthentication(t *testing.T) {
	a := testApp(t)
	s := menuSession(a)
	a.db.Loans["loan"] = &Loan{ID: "loan", CustomerID: s.CustomerID, Status: "ACTIVE", Balance: 10000}
	s.ConfirmedUntil = time.Time{}
	view := a.render(s)
	if strings.Contains(view.Text, "your loan") || strings.Contains(view.Text, "100.00") {
		t.Fatal("unauthenticated menu disclosed loan")
	}
	if !strings.Contains(view.Text, "Welcome to MightyFin") {
		t.Fatal("neutral welcome missing")
	}
	a.advance(s, "1")
	if s.Screen != "recognise" {
		t.Fatal("primary action bypassed authentication")
	}
}
func TestChangedMenuRequiresFreshChoice(t *testing.T) {
	a := testApp(t)
	s := menuSession(a)
	_ = a.render(s)
	a.db.Loans["loan"] = &Loan{ID: "loan", CustomerID: s.CustomerID, Status: "ACTIVE", Balance: 10000}
	a.advance(s, "1")
	if s.Screen != "main" || len(a.db.Applications) != 0 {
		t.Fatal("stale option silently changed meaning")
	}
	view := a.render(s)
	if !strings.Contains(view.Text, "choose again") || !strings.Contains(view.Text, "Repay your loan") {
		t.Fatal("fresh menu not returned")
	}
	a.advance(s, "1")
	if s.Screen != "loans" {
		t.Fatal("fresh choice did not open repayment")
	}
}
