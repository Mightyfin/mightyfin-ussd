package app

import (
	"sort"
	"time"
)

type menuAction struct{ Action, Label, Reference string }

// The local demo shows personalised actions after name confirmation. This is not
// payment authorisation. Sorting makes selection stable across map iterations.
func (a *App) primaryAction(customerID string) menuAction {
	operations := []string{}
	for key, op := range a.db.Operations {
		if op.CustomerID == customerID {
			operations = append(operations, key)
		}
	}
	sort.Strings(operations)
	for _, key := range operations {
		op := a.db.Operations[key]
		if op.Kind == "REPAYMENT" && unresolved(op) {
			return menuAction{"transaction", "Check your payment", key}
		}
	}
	for _, l := range a.db.Loans {
		if l.CustomerID == customerID && l.Status == "ACTIVE" && l.Balance > 0 {
			return menuAction{"repay", "Repay your loan", ""}
		}
	}
	for _, key := range operations {
		op := a.db.Operations[key]
		if op.Kind == "PAYOUT" && unresolved(op) {
			return menuAction{"transaction", "Check your payout", key}
		}
	}
	applications := []string{}
	for key, d := range a.db.Applications {
		if d.CustomerID != customerID || d.Status == "WITHDRAWN" || d.Status == "DECLINED" {
			continue
		}
		completed := false
		for _, l := range a.db.Loans {
			if l.ApplicationID == key && l.Status == "CLOSED" && l.Balance == 0 {
				completed = true
			}
		}
		if !completed {
			applications = append(applications, key)
		}
	}
	sort.Strings(applications)
	for _, key := range applications {
		d := a.db.Applications[key]
		switch d.Status {
		case "DRAFT":
			return menuAction{"draft", "Continue your application", key}
		case "KYC_PENDING":
			return menuAction{"kyc", "Complete your documents", key}
		case "OFFER_READY":
			for _, o := range a.db.Offers {
				if o.ApplicationID == key && o.Status == "ISSUED" && time.Now().Before(o.ExpiresAt) {
					return menuAction{"offer", "Review your offer", key}
				}
			}
			return menuAction{"application", "Check your progress", key}
		default:
			return menuAction{"application", "Check your progress", key}
		}
	}
	return menuAction{"apply", "Apply for a loan", ""}
}
func unresolved(op *Operation) bool {
	return op.Status == "CREATED" || op.Status == "SUBMITTED" || op.Status == "AWAITING_AUTHORISATION" || op.Status == "PENDING" || op.Status == "UNKNOWN" || (op.Status == "SUCCEEDED" && op.Posting != "POSTED")
}
func (a *App) openPrimary(s *Session, action menuAction) {
	switch action.Action {
	case "repay":
		a.selectLoans(s)
	case "transaction":
		s.TransactionID = action.Reference
		s.Screen = "transaction"
	case "draft":
		s.ApplicationID = action.Reference
		a.resumeDraft(s)
	case "kyc":
		s.ApplicationID = action.Reference
		s.Screen = "kyc"
	case "offer":
		s.ApplicationID = action.Reference
		a.openOffer(s, action.Reference)
	case "application":
		s.ApplicationID = action.Reference
		s.Screen = "application_status"
	default:
		a.startApplication(s)
	}
}
