package app

import (
	"fmt"
	"strings"
)

func (a *App) render(s *Session) Response {
	r := Response{SessionID: s.ID, End: s.Ended}
	text := ""
	screen := "S01"
	switch s.Screen {
	case "main":
		text = "Welcome to MightyFin\n1 Apply for a loan\n2 Repay\n3 Check status\n4 Manage account\n5 Get help"
		if a.customerConfirmed(s) {
			next := a.primaryAction(s.CustomerID)
			s.MenuAction, s.MenuReference = next.Action, next.Reference
			text = "Welcome to MightyFin, " + a.db.Customers[s.CustomerID].Name + "\n1 " + next.Label + "\n2 Check payment status\n3 Check progress\n4 Manage account\n5 Get help"
		}
	case "recognise":
		screen = "S02"
		c := a.db.Customers[s.CustomerID]
		text = "Welcome to MightyFin"
		if c != nil && !s.RecognitionSuppressed {
			text += ", " + c.Name
		}
		text += "\n1 This is me\n2 Not me"
	case "unknown":
		screen = "S04"
		text = "Welcome to MightyFin\n1 New customer\n2 Existing account assistance"
	case "register_consent":
		screen = "S04"
		text = "LOCAL DEMO: use synthetic data.\nWe use your details to process your application. Demo consent v1.\n1 Agree and register\n2 Cancel"
	case "register_name":
		screen = "S04"
		text = "Enter your full name (demo data only)."
	case "register_nrc":
		screen = "S04"
		text = "Enter demo NRC\ne.g. 123456/78/1"
	case "register_phone":
		screen = "S04"
		text = "Is " + masked(s.Phone) + " your own number?\n1 Yes\n2 Borrowed or shared — assistance"
	case "account":
		screen = "S17"
		text = "Manage account\n1 Get account help\n2 Changed number\nChanges require staff verification."
	case "recovery":
		screen = "S17"
		text = "Account assistance\n1 Borrowed or shared phone\n2 Changed number / lost SIM\n3 Get assistance"
	case "help":
		screen = "S16"
		text = "Get assistance\n1 Application\n2 Payment dispute\n3 Missing payout\n4 Loan officer\n5 Account help"
	case "resume":
		screen = "S05"
		text = "Draft " + s.ApplicationID + " found\n1 Continue application\n2 Check status"
	case "apply_product":
		screen = "S06"
		text = "Select demo product"
		for i, p := range products {
			text += fmt.Sprintf("\n%d %s", i+1, p.Name)
		}
	case "apply_amount":
		screen = "S06"
		p := product(a.db.Applications[s.ApplicationID].Product)
		text = fmt.Sprintf("%s\nEnter amount in ZMW\n%s–%s", p.Name, money(p.Min), money(p.Max))
	case "apply_term":
		screen = "S06"
		p := product(a.db.Applications[s.ApplicationID].Product)
		terms := []string{}
		for _, n := range p.Terms {
			terms = append(terms, fmt.Sprint(n))
		}
		text = "Enter tenure in months\nOptions: " + strings.Join(terms, ", ")
	case "apply_screening":
		screen = "S06"
		d := a.db.Applications[s.ApplicationID]
		fields := product(d.Product).Screening
		if s.Page >= len(fields) {
			s.Page = len(fields) - 1
		}
		text = "Enter " + fields[s.Page]
	case "apply_confirm":
		screen = "S07"
		d := a.db.Applications[s.ApplicationID]
		p := product(d.Product)
		text = fmt.Sprintf("%s\nAmount %s\nTenure %d months", p.Name, money(d.Amount), d.Term)
		for _, f := range p.Screening {
			text += "\n" + f + ": " + d.Answers[f]
		}
		text += "\nConsent: demo-application-v1\n1 Agree and Submit\n2 Edit\n3 Save and exit"
	case "kyc":
		screen = "S08"
		text = "Received " + s.ApplicationID + "\nKYC pending. This is not approval.\n1 Web upload route\n2 WhatsApp assistance\n3 Loan officer\nDemo: no external messages sent."
	case "status_menu":
		screen = "S10"
		text = "Check status\n1 Application\n2 Payment / payout\n3 My support cases"
	case "application_status":
		screen = "S10"
		d := a.db.Applications[s.ApplicationID]
		if d == nil {
			text = "No application found."
			break
		}
		text = d.ID + "\n" + d.Status + "\nKYC: " + d.KYC + "\nNext: " + d.NextAction + "\n1 Next action\n2 Request withdrawal\n3 Assistance"
	case "withdraw_confirm":
		screen = "S10"
		text = "Request withdrawal of " + s.ApplicationID + "?\nPayout status will be checked.\n1 Confirm\n2 Keep application"
	case "offer":
		screen = "S09"
		o := a.db.Offers[s.OfferID]
		if o == nil {
			text = "Offer unavailable."
			break
		}
		switch s.Page {
		case 0:
			text = fmt.Sprintf("DEMO OFFER v%d (1/3)\nPrincipal %s\nDeductions %s\nNet payout %s\n1 Next", o.Version, money(o.Principal), money(o.Deductions), money(o.Net))
		case 1:
			text = fmt.Sprintf("DEMO TERMS (2/3)\nInterest %s\nFees %s\nTotal repayable %s\n1 Next", money(o.Interest), money(o.Fees), money(o.Total))
		default:
			text = fmt.Sprintf("DEMO TERMS (3/3)\nSingle payment %s on %s\nValid until %s\nNo real loan contract.\n1 Accept this version\n2 Decline\n3 Assisted full terms", money(o.Total), o.IssuedAt.AddDate(0, o.Term, 0).Format("02 Jan 2006"), o.ExpiresAt.Format("02 Jan 15:04 UTC"))
		}
	case "loans":
		screen = "S11"
		text = "Select outstanding loan"
		for i := s.Page * 3; i < len(s.Choices) && i < s.Page*3+3; i++ {
			l := a.db.Loans[s.Choices[i]]
			text += fmt.Sprintf("\n%d %s\nDue %s", i-s.Page*3+1, l.ID, money(l.Due))
		}
		if (s.Page+1)*3 < len(s.Choices) {
			text += "\n98 More"
		}
	case "repay_amount":
		screen = "S12"
		l := a.db.Loans[s.LoanID]
		text = fmt.Sprintf("%s\nDue %s (includes overdue)\nOverdue %s\nSettlement %s\n1 Due\n2 Full settlement\n3 Other (demo allows partial)", l.ID, money(l.Due), money(l.Overdue), money(l.Balance))
	case "repay_other":
		screen = "S12"
		text = "Enter repayment amount in ZMW.\nDemo partial payments are permitted."
	case "repay_method":
		screen = "S12"
		text = "Choose demo payment method\n1 Mobile money\n2 Bank"
	case "repay_source":
		screen = "S12"
		text = "Enter your own mobile number.\nDo not enter a wallet PIN."
		if s.Method == "BANK" {
			text = "Enter a synthetic bank reference.\nNever enter a bank PIN or password."
		}
	case "repay_confirm":
		screen = "S13"
		q := a.db.Quotes[s.QuoteID]
		text = fmt.Sprintf("%s\nPay %s\nCharges %s / Total %s\n%s from %s\nRemaining %s\n1 Confirm\n2 Cancel", q.LoanID, money(q.Amount), money(q.Fee), money(q.Total), q.Method, masked(q.Source), money(q.Remaining))
	case "transactions":
		screen = "S15"
		text = "Select transaction"
		for i := s.Page * 3; i < len(s.Choices) && i < s.Page*3+3; i++ {
			op := a.db.Operations[s.Choices[i]]
			text += fmt.Sprintf("\n%d %s\n%s %s", i-s.Page*3+1, op.ID, op.Kind, op.Status)
		}
		if (s.Page+1)*3 < len(s.Choices) {
			text += "\n98 More"
		}
	case "payment_wait":
		screen = "S14"
		op := a.db.Operations[s.TransactionID]
		if op == nil || op.CustomerID != s.CustomerID {
			text = "Payment reference unavailable. Check status after redialling."
			break
		}
		text = mobileMoneyWait(op)
	case "transaction":
		screen = "S15"
		op := a.db.Operations[s.TransactionID]
		if op == nil || op.CustomerID != s.CustomerID {
			text = "Transaction unavailable."
			break
		}
		text = op.ID + "\n" + op.Kind + " " + money(op.Amount) + "\nStatus: " + op.Status + "\nPosting: " + op.Posting
		switch op.Status {
		case "AWAITING_AUTHORISATION":
			screen = "S14"
			if op.Method == "BANK" {
				text += "\nDemo bank instructions: reference " + op.ID + ". No transfer is required."
			} else {
				text = mobileMoneyWait(op)
			}
		case "PENDING", "UNKNOWN":
			text += "\nDo not repeat this payment. Await verification."
		case "FAILED", "CANCELLED":
			text += "\nConfirmed unsuccessful in demo. You can start a new attempt."
		case "SUCCEEDED":
			if op.Posting != "POSTED" {
				text += "\nDemo funds received; account update pending."
			} else if op.Kind == "REPAYMENT" {
				l := a.db.Loans[op.LoanID]
				text += "\nReceipt " + op.Receipt + "\nCredited " + money(op.Amount) + "\nBalance " + money(l.Balance) + "\nNext due " + money(l.Due)
			} else {
				text += "\nDemo payout confirmed."
			}
		}
		if op.ReversalID != "" {
			text += "\nReversed: " + op.ReversalID
		}
		text += "\n1 Check status\n2 Assistance"
	case "cases":
		screen = "S16"
		text = "My support cases"
		if len(s.Choices) == 0 {
			text += "\nNo cases found."
		}
		for _, key := range s.Choices {
			c := a.db.Cases[key]
			text += "\n" + caseText(c) + "\n" + c.Status
		}
	case "end":
		screen = "S16"
		text = s.Notice
	default:
		text = "Service unavailable. Dial again."
		r.End = true
	}
	if strings.HasPrefix(s.Screen, "apply_") {
		if d := a.db.Applications[s.ApplicationID]; d != nil {
			s.ApplicationVersion = d.Version
		}
	}
	if s.Notice != "" && s.Screen != "end" {
		text = s.Notice + "\n\n" + text
	}
	if !r.End {
		text += "\n\n0 Back  00 Main  99 Exit"
	}
	r.ScreenID = screen
	r.Text = text
	return r
}

func mobileMoneyWait(op *Operation) string {
	return "Please wait for the Lipila payment prompt.\nApprove the payment using your mobile money PIN in that prompt only.\nReference " + op.ID + "\nDo not pay again while waiting.\n\nDemo: Lipila is not connected; no live prompt will arrive."
}
