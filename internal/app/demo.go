package app

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

type DemoAction struct {
	Action    string `json:"action"`
	Reference string `json:"reference"`
	EventID   string `json:"event_id"`
}

func (a *App) demo(w http.ResponseWriter, r *http.Request) {
	var req DemoAction
	if decode(w, r, &req) != nil || !requestKeyPattern.MatchString(req.Action) || !requestKeyPattern.MatchString(req.Reference) || (req.EventID != "" && !requestKeyPattern.MatchString(req.EventID)) {
		writeJSON(w, 400, map[string]string{"error": "VALIDATION_ERROR", "message": "Use letters, numbers, dots, colons, underscores or hyphens for action references and event IDs."})
		return
	}
	result := "Demo action recorded."
	err := a.transaction(func() error {
		d := a.db.Applications[req.Reference]
		op := a.db.Operations[req.Reference]
		switch req.Action {
		case "kyc_accept":
			if d == nil || d.Status != "KYC_PENDING" {
				return conflict("Select a KYC_PENDING application.")
			}
			d.KYC = "ACCEPTED"
			d.Status = "UNDER_REVIEW"
			d.NextAction = "Await credit review"
			d.Version++
		case "action_required":
			if d == nil || d.Status != "UNDER_REVIEW" {
				return conflict("Application must be UNDER_REVIEW.")
			}
			d.Status = "ACTION_REQUIRED"
			d.NextAction = "Contact demo staff to correct income information"
			d.Version++
		case "resume_review":
			if d == nil || d.Status != "ACTION_REQUIRED" {
				return conflict("Application must require action.")
			}
			d.Status = "UNDER_REVIEW"
			d.NextAction = "Await credit review"
			d.Version++
		case "decline":
			if d == nil || d.Status != "UNDER_REVIEW" {
				return conflict("Application must be UNDER_REVIEW.")
			}
			d.Status = "DECLINED"
			d.NextAction = "Demo assessment declined. Contact support for review"
			d.Version++
		case "offer", "revise_offer":
			if d == nil || d.KYC != "ACCEPTED" || (d.Status != "UNDER_REVIEW" && d.Status != "OFFER_READY") {
				return conflict("Accept KYC and complete review first.")
			}
			version := 1
			for _, o := range a.db.Offers {
				if o.ApplicationID == d.ID {
					if o.Status == "ACCEPTED" {
						return conflict("Accepted terms cannot be revised.")
					}
					if o.Version >= version {
						version = o.Version + 1
					}
					if o.Status == "ISSUED" {
						if req.Action == "offer" {
							result = "Existing offer retained."
							return nil
						}
						o.Status = "EXPIRED"
					}
				}
			}
			// Clearly synthetic terms; never used as a production pricing rule.
			interest := d.Amount / 10
			fees := int64(1000)
			o := &Offer{ID: id("OFF"), ApplicationID: d.ID, Status: "ISSUED", Version: version, Principal: d.Amount, Deductions: fees, Interest: interest, Fees: fees, Net: d.Amount - fees, Total: d.Amount + interest, Term: d.Term, IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(24 * time.Hour)}
			a.db.Offers[o.ID] = o
			d.Status = "OFFER_READY"
			d.NextAction = "Review current offer"
			d.Version++
		case "release_payout":
			if d == nil || d.Status != "OFFER_READY" || d.KYC != "ACCEPTED" {
				return conflict("Application is not ready for payout.")
			}
			var accepted *Offer
			for _, o := range a.db.Offers {
				if o.ApplicationID == d.ID && o.Status == "ACCEPTED" {
					accepted = o
				}
			}
			if accepted == nil {
				return conflict("Customer must explicitly accept the current offer.")
			}
			for _, p := range a.db.Operations {
				if p.ApplicationID == d.ID && p.Kind == "PAYOUT" && p.Status != "FAILED" {
					result = "Existing payout retained: " + p.ID
					return nil
				}
			}
			c := a.db.Customers[d.CustomerID]
			p := &Operation{ID: id("OUT"), Kind: "PAYOUT", CustomerID: c.ID, ApplicationID: d.ID, Status: "PENDING", Posting: "NOT_POSTED", Method: "MOBILE_MONEY", Source: c.Phone, Amount: accepted.Net, Key: "payout:" + d.ID, CreatedAt: time.Now().UTC(), Events: map[string]string{}}
			a.db.Operations[p.ID] = p
			d.NextAction = "Demo payout pending; check transaction status"
		case "succeed", "succeed_unposted", "fail", "pending", "unknown", "cancel_confirmed":
			if op == nil {
				return conflict("Select a transaction.")
			}
			if req.EventID == "" {
				return conflict("A stable event ID is required.")
			}
			state := map[string]string{"succeed": "SUCCEEDED", "succeed_unposted": "SUCCEEDED", "fail": "FAILED", "pending": "PENDING", "unknown": "UNKNOWN", "cancel_confirmed": "CANCELLED"}[req.Action]
			eventValue := req.Action
			if old, ok := op.Events[req.EventID]; ok {
				if old != eventValue {
					return conflict("Event ID reused with different content.")
				}
				result = "Duplicate event ignored."
				return nil
			}
			op.Events[req.EventID] = eventValue
			terminal := op.Status == "SUCCEEDED" || op.Status == "FAILED" || op.Status == "CANCELLED"
			if terminal && op.Status != state {
				s := &Session{CustomerID: op.CustomerID}
				c := a.newCase(s, "PAYMENT_CONFLICT", op.ID)
				result = "Conflicting or late event retained for review: " + c.ID
				break
			}
			if op.ReversalID != "" {
				result = "Reversed transaction retained."
				break
			}
			op.Status = state
			if state == "SUCCEEDED" {
				op.ProviderRef = "DEMO-" + op.ID
				if op.Posting != "POSTED" {
					op.Posting = "POSTING_PENDING"
				}
				if req.Action == "succeed" {
					a.post(op)
				}
			}
			if state == "UNKNOWN" {
				a.newCase(&Session{CustomerID: op.CustomerID}, "PAYMENT_UNKNOWN", op.ID)
			}
		case "post":
			if op == nil || op.Status != "SUCCEEDED" {
				return conflict("A verified demo success is required before posting.")
			}
			if op.ReversalID != "" {
				return conflict("Transaction has been reversed.")
			}
			a.post(op)
		case "reverse":
			if op == nil || op.Status != "SUCCEEDED" || op.Posting != "POSTED" {
				return conflict("Select a successful posted transaction.")
			}
			if op.ReversalID != "" {
				result = "Existing reversal retained."
				return nil
			}
			if op.Kind == "PAYOUT" {
				c := a.newCase(&Session{CustomerID: op.CustomerID}, "PAYOUT_REVERSAL", op.ID)
				result = "Payout reversal requires investigation of retained funds: " + c.ID
				return nil
			}
			l := a.db.Loans[op.LoanID]
			l.Balance += op.Amount
			l.Due += op.AllocatedDue
			l.Overdue += op.AllocatedOverdue
			l.Status = "ACTIVE"
			l.Version++
			rev := LedgerEntry{ID: id("REV"), OperationID: op.ID, LoanID: l.ID, Kind: "REPAYMENT_REVERSAL", Amount: op.Amount, CreatedAt: time.Now().UTC()}
			for _, e := range a.db.Ledger {
				if e.OperationID == op.ID && e.Kind == "REPAYMENT" {
					rev.LinkedEntry = e.ID
					break
				}
			}
			a.db.Ledger = append(a.db.Ledger, rev)
			op.ReversalID = rev.ID
		case "resolve_case":
			c := a.db.Cases[req.Reference]
			if c == nil {
				return conflict("Select a case.")
			}
			c.Status = "RESOLVED"
			c.NextAction = "Demo resolution recorded; no external action performed"
		default:
			return conflict("Unsupported demo action.")
		}
		a.audit("demo-staff", req.Action, req.Reference)
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"message": result})
}
func (a *App) post(op *Operation) {
	if op.Posting == "POSTED" {
		return
	}
	if op.Kind == "REPAYMENT" {
		l := a.db.Loans[op.LoanID]
		// Excess needs a separately approved allocation. Never silently clamp or move it.
		if l == nil || op.Amount > l.Balance {
			a.newCase(&Session{CustomerID: op.CustomerID}, "PAYMENT_ALLOCATION", op.ID)
			return
		}
		l.Balance -= op.Amount
		op.AllocatedDue = min(op.Amount, l.Due)
		op.AllocatedOverdue = min(op.Amount, l.Overdue)
		l.Due -= op.Amount
		if l.Due < 0 {
			l.Due = 0
		}
		l.Overdue -= op.Amount
		if l.Overdue < 0 {
			l.Overdue = 0
		}
		l.Version++
		if l.Balance == 0 {
			l.Status = "CLOSED"
		}
	} else {
		d := a.db.Applications[op.ApplicationID]
		var o *Offer
		for _, offer := range a.db.Offers {
			if offer.ApplicationID == d.ID && offer.Status == "ACCEPTED" {
				o = offer
			}
		}
		if o == nil {
			return
		}
		l := &Loan{ID: id("LN"), CustomerID: op.CustomerID, ApplicationID: d.ID, Balance: o.Total, Due: o.Total, Status: "ACTIVE", Version: 1}
		a.db.Loans[l.ID] = l
		op.LoanID = l.ID
		d.NextAction = "Payout confirmed in demo. Repay your loan"
	}
	op.Posting = "POSTED"
	op.Receipt = id("RCP")
	kind := strings.ToUpper(op.Kind)
	entry := LedgerEntry{ID: id("LED"), OperationID: op.ID, LoanID: op.LoanID, Kind: kind, Amount: op.Amount, CreatedAt: time.Now().UTC()}
	a.db.Ledger = append(a.db.Ledger, entry)
	a.audit("demo-ledger", fmt.Sprintf("%s.posted", op.Kind), op.ID)
}
