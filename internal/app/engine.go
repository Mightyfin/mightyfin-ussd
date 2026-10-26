package app

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

func (a *App) customerConfirmed(s *Session) bool {
	c := a.db.Customers[s.CustomerID]
	// Local customer confirmation only. Payment authorisation belongs to Lipila.
	return c != nil && !s.RecognitionSuppressed && time.Now().Before(s.ConfirmedUntil)
}
func (a *App) end(s *Session, text string) {
	s.Screen = "end"
	s.Notice = text
	s.Ended = true
}
func (a *App) gate(s *Session, intent string) {
	s.Intent = intent
	if a.customerConfirmed(s) {
		a.startIntent(s)
		return
	}
	if s.RecognitionSuppressed {
		s.Screen = "unknown"
		return
	}
	matches := []*Customer{}
	for _, c := range a.db.Customers {
		if c.Phone == s.Phone {
			matches = append(matches, c)
		}
	}
	if len(matches) == 1 {
		s.CustomerID = matches[0].ID
		s.Screen = "recognise"
		return
	}
	s.CustomerID = ""
	if len(matches) > 1 {
		c := a.newCase(s, "DUPLICATE_IDENTITY", s.Phone)
		a.end(s, caseText(c))
		return
	}
	s.Screen = "unknown"
}
func (a *App) startIntent(s *Session) {
	switch s.Intent {
	case "home":
		s.Screen = "main"
	case "primary":
		a.openPrimary(s, a.primaryAction(s.CustomerID))
	case "apply":
		a.startApplication(s)
	case "repay":
		a.selectLoans(s)
	case "status":
		s.Screen = "status_menu"
	case "account":
		s.Screen = "account"
	default:
		s.Screen = "help"
	}
}
func (a *App) newCase(s *Session, category, related string) *Case {
	owner := s.CustomerID
	if owner == "" {
		owner = "phone:" + a.fingerprint(s.Phone)[:12]
	}
	for _, c := range a.db.Cases {
		if c.CustomerID == owner && c.Category == category && c.Related == related && c.Status != "CLOSED" && c.Status != "RESOLVED" {
			return c
		}
	}
	team := "Customer support"
	if strings.Contains(category, "PAYMENT") || strings.Contains(category, "PAYOUT") || category == "REFUND" {
		team = "Finance reconciliation"
	}
	if category == "OFFICER" {
		team = "Loan officer queue"
	}
	// Numeric deadlines deliberately remain unconfigured until operating SLAs are agreed.
	c := &Case{ID: id("CASE"), CustomerID: owner, Category: category, Related: related, Status: "ASSIGNED", Owner: team, NextAction: "Demo staff review required; update deadline to be agreed", ContactPreference: "Registered contact", CreatedAt: time.Now().UTC()}
	a.db.Cases[c.ID] = c
	a.audit("customer", "case.created", c.ID)
	return c
}
func caseText(c *Case) string {
	return fmt.Sprintf("Case %s\n%s\nNext: %s", c.ID, c.Owner, c.NextAction)
}
func choice(input string, ids []string) string {
	n, e := menuNumber(input)
	if e != nil || n < 1 || n > len(ids) {
		return ""
	}
	return ids[n-1]
}
func (a *App) advance(s *Session, input string) {
	s.Notice = ""
	public := map[string]bool{"main": true, "recognise": true, "unknown": true, "register_consent": true, "register_name": true, "register_nrc": true, "register_phone": true, "help": true, "recovery": true}
	if !public[s.Screen] && !a.customerConfirmed(s) {
		s.ConfirmedUntil = time.Time{}
		a.end(s, "Session expired. Dial again and confirm your name.")
		return
	}
	cleaned, err := cleanInput(input, 200)
	if err != nil {
		s.Notice = err.Error()
		return
	}
	input = cleaned
	if input == "99" {
		a.end(s, "Thank you for using MightyFin.")
		return
	}
	if input == "00" {
		s.Screen = "main"
		s.Page = 0
		return
	}
	if input == "0" {
		a.back(s)
		return
	}
	if strings.HasPrefix(s.Screen, "apply_") && s.ApplicationID != "" {
		d := a.db.Applications[s.ApplicationID]
		if d == nil || d.CustomerID != s.CustomerID {
			a.end(s, "Application unavailable.")
			return
		}
		if d.Version != s.ApplicationVersion || d.Status != "DRAFT" {
			s.Screen = "application_status"
			s.Notice = "Application changed. Review its current status."
			return
		}
	}
	switch s.Screen {
	case "main":
		if input == "1" && a.customerConfirmed(s) {
			next := a.primaryAction(s.CustomerID)
			if next.Action != s.MenuAction || next.Reference != s.MenuReference {
				s.Notice = "Your next step has changed. Please choose again."
				return
			}
			a.openPrimary(s, next)
			return
		}
		if input == "2" && a.customerConfirmed(s) {
			a.transactionList(s)
			return
		}
		intents := map[string]string{"1": "primary", "2": "repay", "3": "status", "4": "account"}
		if input == "5" {
			s.Screen = "help"
		} else if v := intents[input]; v != "" {
			a.gate(s, v)
		} else {
			s.Notice = "Choose 1 to 5."
		}
	case "recognise":
		if input == "1" {
			if s.RecognitionSuppressed || a.db.Customers[s.CustomerID] == nil {
				s.Screen = "unknown"
				return
			}
			s.ConfirmedUntil = time.Now().UTC().Add(5 * time.Minute)
			a.audit("customer", "identity.confirmed", s.CustomerID)
			a.startIntent(s)
		} else if input == "2" {
			s.CustomerID = ""
			s.ConfirmedUntil = time.Time{}
			s.RecognitionSuppressed = true
			s.ApplicationID, s.OfferID, s.LoanID, s.QuoteID, s.TransactionID = "", "", "", "", ""
			s.MenuAction, s.MenuReference = "", ""
			s.Choices = nil
			s.Screen = "unknown"
		} else {
			s.Notice = "Choose 1 or 2."
		}
	case "unknown":
		if input == "1" {
			s.Screen = "register_consent"
		} else if input == "2" {
			s.Screen = "recovery"
		} else {
			s.Notice = "Choose 1 or 2."
		}
	case "register_consent":
		if input == "1" {
			s.Screen = "register_name"
		} else if input == "2" {
			a.end(s, "Registration cancelled. No customer created.")
		} else {
			s.Notice = "Choose 1 or 2."
		}
	case "register_name":
		name, err := customerName(input)
		if err != nil {
			s.Notice = err.Error()
		} else {
			s.RegistrationName = name
			s.Screen = "register_nrc"
		}
	case "register_nrc":
		if !nrcPattern.MatchString(input) {
			s.Notice = "Use format 123456/78/1 (demo validation)."
			return
		}
		for _, c := range a.db.Customers {
			if c.NRC == input {
				task := a.newCase(s, "ACCOUNT_LINKING", "")
				a.end(s, "Existing identity found. Verification required.\n"+caseText(task))
				return
			}
		}
		s.RegistrationNRC = input
		s.Screen = "register_phone"
	case "register_phone":
		if input == "1" {
			a.completeRegistration(s)
		} else if input == "2" {
			c := a.newCase(s, "SHARED_PHONE", "")
			a.end(s, caseText(c))
		} else {
			s.Notice = "Choose 1 or 2."
		}
	case "account":
		if input == "1" {
			s.Screen = "recovery"
		} else if input == "2" {
			a.end(s, caseText(a.newCase(s, "CONTACT_CHANGE", "")))
		} else {
			s.Notice = "Choose 1 or 2."
		}
	case "recovery":
		categories := map[string]string{"1": "SHARED_PHONE", "2": "CONTACT_CHANGE", "3": "ACCOUNT_ASSISTANCE"}
		if cat := categories[input]; cat != "" {
			c := a.newCase(s, cat, "")
			a.end(s, "Staff verification required.\n"+caseText(c))
		} else {
			s.Notice = "Choose 1 to 3."
		}
	case "help":
		categories := map[string]string{"1": "APPLICATION_HELP", "2": "PAYMENT_DISPUTE", "3": "PAYOUT_MISSING", "4": "OFFICER", "5": "ACCOUNT_ASSISTANCE"}
		if cat := categories[input]; cat != "" {
			a.end(s, caseText(a.newCase(s, cat, "")))
		} else {
			s.Notice = "Choose 1 to 5."
		}
	case "resume":
		if input == "1" {
			a.resumeDraft(s)
		} else if input == "2" {
			s.Screen = "application_status"
		} else {
			s.Notice = "Choose 1 or 2."
		}
	case "apply_product":
		p := choice(input, []string{products[0].ID, products[1].ID})
		if p == "" {
			s.Notice = "Select a listed product."
			return
		}
		d := a.db.Applications[s.ApplicationID]
		d.Product = p
		d.Amount = 0
		d.Term = 0
		d.Answers = map[string]string{}
		d.Version++
		s.Screen = "apply_amount"
	case "apply_amount":
		d := a.db.Applications[s.ApplicationID]
		p := product(d.Product)
		n, err := parseMoney(input)
		if err != nil || n < p.Min || n > p.Max {
			s.Notice = fmt.Sprintf("Enter %s to %s.", money(p.Min), money(p.Max))
			return
		}
		d.Amount = n
		d.Version++
		s.Screen = "apply_term"
	case "apply_term":
		d := a.db.Applications[s.ApplicationID]
		n, valid := validTerm(input, product(d.Product))
		if !valid {
			s.Notice = "Enter a listed number of months."
			return
		}
		d.Term = n
		d.Version++
		s.Page = 0
		s.Screen = "apply_screening"
	case "apply_screening":
		d := a.db.Applications[s.ApplicationID]
		fields := product(d.Product).Screening
		if s.Page >= len(fields) {
			s.Screen = "apply_confirm"
			return
		}
		field := fields[s.Page]
		answer, err := field.validate(input)
		if err != nil {
			s.Notice = err.Error()
			return
		}
		d.Answers[field.Name] = answer
		d.Version++
		s.Page++
		if s.Page == len(fields) {
			s.Screen = "apply_confirm"
		}
	case "apply_confirm":
		d := a.db.Applications[s.ApplicationID]
		switch input {
		case "1":
			if !validateDraft(s, d) {
				return
			}
			d.SubmissionKey = "submit:" + d.ID
			d.ConsentVersion = "demo-application-v1"
			d.ConsentedAt = time.Now().UTC()
			d.Status = "KYC_PENDING"
			d.KYC = "MISSING"
			d.NextAction = "Complete KYC documents"
			d.Version++
			a.audit("customer", "application.submitted", d.ID)
			s.Screen = "kyc"
		case "2":
			s.Screen = "apply_product"
		case "3":
			a.end(s, "Draft saved. Dial again and confirm your name to resume.")
		default:
			s.Notice = "Choose 1 Submit, 2 Edit or 3 Save and exit."
		}
	case "kyc":
		routes := map[string]string{"1": "KYC_WEB", "2": "KYC_WHATSAPP", "3": "OFFICER"}
		if cat := routes[input]; cat != "" {
			c := a.newCase(s, cat, s.ApplicationID)
			a.end(s, "Demo handoff recorded; no link or message sent.\n"+caseText(c))
		} else {
			s.Notice = "Choose 1 to 3."
		}
	case "status_menu":
		switch input {
		case "1":
			a.currentStatus(s)
		case "2":
			a.transactionList(s)
		case "3":
			a.caseList(s)
		default:
			s.Notice = "Choose 1 to 3."
		}
	case "application_status":
		d := a.db.Applications[s.ApplicationID]
		if d == nil {
			s.Screen = "status_menu"
			return
		}
		switch input {
		case "1":
			switch d.Status {
			case "DRAFT":
				a.resumeDraft(s)
			case "KYC_PENDING":
				s.Screen = "kyc"
			case "OFFER_READY":
				a.openOffer(s, d.ID)
			default:
				s.Notice = d.NextAction
			}
		case "2":
			if d.Status == "WITHDRAWN" || d.Status == "DECLINED" {
				s.Notice = "Application is already closed."
			} else {
				s.Screen = "withdraw_confirm"
			}
		case "3":
			a.end(s, caseText(a.newCase(s, "APPLICATION_HELP", d.ID)))
		default:
			s.Notice = "Choose 1 Next action, 2 Withdraw or 3 Assistance."
		}
	case "withdraw_confirm":
		if input == "1" {
			a.withdraw(s)
		} else if input == "2" {
			s.Screen = "application_status"
		} else {
			s.Notice = "Choose 1 or 2."
		}
	case "offer":
		o := a.db.Offers[s.OfferID]
		if o == nil || o.Status != "ISSUED" || o.Version != s.OfferVersion || time.Now().After(o.ExpiresAt) {
			s.Screen = "application_status"
			s.Notice = "Offer changed, expired or already answered. Review current status."
			return
		}
		if s.Page < 2 {
			if input == "1" {
				s.Page++
			} else {
				s.Notice = "Choose 1 Next. Acceptance is on the final page."
			}
			return
		}
		if input == "1" {
			o.Status = "ACCEPTED"
			now := time.Now().UTC()
			o.AcceptedAt = &now
			o.AcceptanceKey = fmt.Sprintf("accept:%s:%d", o.ID, o.Version)
			d := a.db.Applications[o.ApplicationID]
			d.NextAction = "Await internal payout approval"
			a.audit("customer", "offer.accepted", o.ID)
			a.end(s, "Offer accepted. Await internal payout approval. No funds have been released.")
		} else if input == "2" {
			o.Status = "DECLINED"
			d := a.db.Applications[o.ApplicationID]
			d.Status = "WITHDRAWN"
			d.NextAction = "Offer declined; no payout"
			d.Version++
			a.audit("customer", "offer.declined", o.ID)
			a.end(s, "Offer declined. No payout will be released.")
		} else if input == "3" {
			a.end(s, caseText(a.newCase(s, "ASSISTED_TERMS", o.ID)))
		} else {
			s.Notice = "Choose 1 Accept, 2 Decline or 3 Assisted terms."
		}
	case "loans":
		if input == "98" && s.Page+1 < (len(s.Choices)+2)/3 {
			s.Page++
			return
		}
		idx, e := menuNumber(input)
		start := s.Page * 3
		if e != nil || idx < 1 || idx > 3 || start+idx > len(s.Choices) {
			s.Notice = "Select a listed loan."
			return
		}
		s.LoanID = s.Choices[start+idx-1]
		s.Screen = "repay_amount"
	case "repay_amount":
		l := a.db.Loans[s.LoanID]
		if l == nil || l.CustomerID != s.CustomerID {
			a.end(s, "Loan unavailable.")
			return
		}
		switch input {
		case "1":
			s.Amount = l.Due
			if s.Amount > l.Balance {
				s.Amount = l.Balance
			}
			s.Screen = "repay_method"
		case "2":
			s.Amount = l.Balance
			s.Screen = "repay_method"
		case "3":
			s.Screen = "repay_other"
		default:
			s.Notice = "Choose 1 to 3."
		}
	case "repay_other":
		n, e := parseMoney(input)
		l := a.db.Loans[s.LoanID]
		if e != nil || n > l.Balance {
			s.Notice = "Enter a positive amount up to " + money(l.Balance)
			return
		}
		s.Amount = n
		s.Screen = "repay_method"
	case "repay_method":
		if input == "1" {
			s.Method = "MOBILE_MONEY"
			s.Screen = "repay_source"
		} else if input == "2" {
			s.Method = "BANK"
			s.Screen = "repay_source"
		} else {
			s.Notice = "Choose mobile money or bank."
		}
	case "repay_source":
		if s.Method == "MOBILE_MONEY" {
			p, e := phone(input)
			if e != nil {
				s.Notice = e.Error()
				return
			}
			if p != s.Phone {
				s.Notice = "Demo supports your registered number only. Third-party payments require assistance."
				return
			}
			s.Source = p
		} else if s.Method == "BANK" {
			ref, err := bankReference(input)
			if err != nil {
				s.Notice = err.Error()
				return
			}
			s.Source = ref
		} else {
			s.Screen = "repay_method"
			s.Notice = "Choose mobile money or bank."
			return
		}
		a.makeQuote(s)
	case "repay_confirm":
		if input == "1" {
			a.initiatePayment(s)
		} else if input == "2" {
			s.QuoteID = ""
			s.Screen = "repay_amount"
			s.Notice = "Cancelled before initiation. No debit."
		} else {
			s.Notice = "Choose 1 Confirm or 2 Cancel."
		}
	case "transactions":
		if input == "98" && s.Page+1 < (len(s.Choices)+2)/3 {
			s.Page++
			return
		}
		n, e := menuNumber(input)
		idx := s.Page*3 + n - 1
		if e != nil || n < 1 || n > 3 || idx >= len(s.Choices) {
			s.Notice = "Select a listed transaction."
			return
		}
		s.TransactionID = s.Choices[idx]
		s.Screen = "transaction"
	case "transaction":
		if input == "1" {
			a.pendingCase(s)
		} else if input == "2" {
			a.end(s, caseText(a.newCase(s, "PAYMENT_DISPUTE", s.TransactionID)))
		} else {
			s.Notice = "Choose 1 Check or 2 Get assistance."
		}
	case "cases":
		s.Notice = "Cases are shown with their assigned owner. 00 Main menu."
	default:
		s.Notice = "Use a listed option."
	}
}
func (a *App) back(s *Session) {
	switch s.Screen {
	case "offer":
		if s.Page > 0 {
			s.Page--
			return
		}
		s.Screen = "application_status"
	case "apply_amount":
		s.Screen = "apply_product"
	case "apply_term":
		s.Screen = "apply_amount"
	case "apply_screening":
		if s.Page > 0 {
			s.Page--
		} else {
			s.Screen = "apply_term"
		}
	case "apply_confirm":
		s.Screen = "apply_screening"
		s.Page = len(product(a.db.Applications[s.ApplicationID].Product).Screening) - 1
	case "repay_confirm":
		s.QuoteID = ""
		s.Screen = "repay_source"
	case "repay_source":
		s.Screen = "repay_method"
	case "repay_method", "repay_other":
		s.Screen = "repay_amount"
	case "repay_amount":
		a.selectLoans(s)
	case "withdraw_confirm", "kyc":
		s.Screen = "application_status"
	case "transaction":
		a.transactionList(s)
	case "transactions", "cases", "application_status":
		s.Screen = "status_menu"
	default:
		s.Screen = "main"
	}
}
func (a *App) startApplication(s *Session) {
	for _, l := range a.db.Loans {
		if l.CustomerID == s.CustomerID && l.Balance > 0 {
			s.Screen = "status_menu"
			s.Notice = "Demo product rule: repay the active loan before another application."
			return
		}
	}
	for _, d := range a.db.Applications {
		if d.CustomerID == s.CustomerID && d.Status != "WITHDRAWN" && d.Status != "DECLINED" {
			completed := false
			for _, l := range a.db.Loans {
				if l.ApplicationID == d.ID && l.Status == "CLOSED" && l.Balance == 0 {
					completed = true
				}
			}
			if completed {
				continue
			}
			s.ApplicationID = d.ID
			if d.Status == "DRAFT" {
				s.Screen = "resume"
			} else {
				s.Screen = "application_status"
			}
			return
		}
	}
	d := &Application{ID: id("APP"), CustomerID: s.CustomerID, Status: "DRAFT", Answers: map[string]string{}, KYC: "MISSING", Version: 1, NextAction: "Complete and confirm application"}
	a.db.Applications[d.ID] = d
	s.ApplicationID = d.ID
	s.Screen = "apply_product"
	a.audit("customer", "draft.created", d.ID)
}
func (a *App) resumeDraft(s *Session) {
	d := a.db.Applications[s.ApplicationID]
	s.Page = 0
	switch {
	case d.Product == "":
		s.Screen = "apply_product"
	case d.Amount == 0:
		s.Screen = "apply_amount"
	case d.Term == 0:
		s.Screen = "apply_term"
	default:
		for i, f := range product(d.Product).Screening {
			if d.Answers[f.Name] == "" {
				s.Screen = "apply_screening"
				s.Page = i
				return
			}
		}
		s.Screen = "apply_confirm"
	}
}
func (a *App) currentStatus(s *Session) {
	ids := []string{}
	for _, d := range a.db.Applications {
		if d.CustomerID == s.CustomerID {
			ids = append(ids, d.ID)
		}
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		s.Notice = "No application found."
		return
	}
	s.ApplicationID = ids[len(ids)-1]
	for _, key := range ids {
		d := a.db.Applications[key]
		if d.Status != "DECLINED" && d.Status != "WITHDRAWN" {
			s.ApplicationID = key
			break
		}
	}
	s.Screen = "application_status"
}
func (a *App) openOffer(s *Session, application string) {
	for _, o := range a.db.Offers {
		if o.ApplicationID == application && o.Status == "ISSUED" && time.Now().Before(o.ExpiresAt) {
			s.OfferID = o.ID
			s.OfferVersion = o.Version
			s.Page = 0
			s.Screen = "offer"
			return
		}
	}
	s.Notice = "No current offer available. Request assistance."
}
func (a *App) selectLoans(s *Session) {
	s.Choices = nil
	s.Page = 0
	for _, l := range a.db.Loans {
		if l.CustomerID == s.CustomerID && l.Balance > 0 && l.Status == "ACTIVE" {
			s.Choices = append(s.Choices, l.ID)
		}
	}
	sort.Strings(s.Choices)
	if len(s.Choices) == 0 {
		a.end(s, "You have no outstanding loans")
		return
	}
	s.Screen = "loans"
}
func (a *App) transactionList(s *Session) {
	s.Choices = nil
	s.Page = 0
	for _, op := range a.db.Operations {
		if op.CustomerID == s.CustomerID {
			s.Choices = append(s.Choices, op.ID)
		}
	}
	sort.Strings(s.Choices)
	if len(s.Choices) == 0 {
		s.Notice = "No transactions found."
		return
	}
	s.Screen = "transactions"
}
func (a *App) caseList(s *Session) {
	s.Choices = nil
	for _, c := range a.db.Cases {
		if c.CustomerID == s.CustomerID {
			s.Choices = append(s.Choices, c.ID)
		}
	}
	sort.Strings(s.Choices)
	s.Screen = "cases"
}
func (a *App) withdraw(s *Session) {
	d := a.db.Applications[s.ApplicationID]
	for _, op := range a.db.Operations {
		if op.ApplicationID == d.ID && op.Kind == "PAYOUT" && op.Status != "FAILED" {
			a.end(s, "Payout has started. Cancellation requires review.\n"+caseText(a.newCase(s, "PAYOUT_CANCELLATION", op.ID)))
			return
		}
	}
	d.Status = "WITHDRAWN"
	d.NextAction = "Application withdrawn"
	d.Version++
	for _, o := range a.db.Offers {
		if o.ApplicationID == d.ID && o.Status == "ISSUED" {
			o.Status = "EXPIRED"
		}
	}
	a.audit("customer", "application.withdrawn", d.ID)
	a.end(s, "Application "+d.ID+" withdrawn. No payout released.")
}
func (a *App) makeQuote(s *Session) {
	l := a.db.Loans[s.LoanID]
	if l == nil || l.CustomerID != s.CustomerID || s.Amount <= 0 || s.Amount > l.Balance {
		s.Screen = "repay_amount"
		s.Notice = "Balance changed. Select the amount again."
		return
	}
	q := &Quote{ID: id("QUOTE"), LoanID: l.ID, CustomerID: s.CustomerID, Method: s.Method, Source: s.Source, Amount: s.Amount, Fee: 0, Total: s.Amount, Remaining: l.Balance - s.Amount, LoanVersion: l.Version, ExpiresAt: time.Now().UTC().Add(2 * time.Minute)}
	a.db.Quotes[q.ID] = q
	s.QuoteID = q.ID
	s.Screen = "repay_confirm"
}
func (a *App) initiatePayment(s *Session) {
	q := a.db.Quotes[s.QuoteID]
	if q == nil || q.CustomerID != s.CustomerID {
		a.end(s, "Quote unavailable.")
		return
	}
	l := a.db.Loans[q.LoanID]
	for _, op := range a.db.Operations {
		if op.CustomerID == s.CustomerID && op.Kind == "REPAYMENT" && op.QuoteID == q.ID {
			s.TransactionID = op.ID
			s.Screen = "transaction"
			return
		}
	}
	for _, op := range a.db.Operations {
		if op.LoanID == l.ID && op.Kind == "REPAYMENT" && (op.Status == "PENDING" || op.Status == "UNKNOWN" || op.Status == "AWAITING_AUTHORISATION" || op.Status == "SUCCEEDED" && op.Posting != "POSTED") {
			s.TransactionID = op.ID
			s.Screen = "transaction"
			s.Notice = "An unresolved payment already exists. Check it before trying again."
			return
		}
	}
	if time.Now().After(q.ExpiresAt) || l.Version != q.LoanVersion || q.Amount > l.Balance {
		s.Screen = "repay_amount"
		s.Notice = "Quote expired or balance changed. Review a new quote."
		return
	}
	op := &Operation{ID: id("PAY"), Kind: "REPAYMENT", CustomerID: s.CustomerID, LoanID: l.ID, Status: "AWAITING_AUTHORISATION", Posting: "NOT_POSTED", Method: q.Method, Source: q.Source, Amount: q.Amount, Fee: q.Fee, QuoteID: q.ID, Key: "payment:" + q.ID, CreatedAt: time.Now().UTC(), Events: map[string]string{}}
	a.db.Operations[op.ID] = op
	s.TransactionID = op.ID
	s.Screen = "transaction"
	if op.Method == "MOBILE_MONEY" {
		s.Screen = "payment_wait"
		s.Ended = true
	}
	a.audit("customer", "payment.initiated", op.ID)
}

func (a *App) completeRegistration(s *Session) {
	name, err := customerName(s.RegistrationName)
	if err != nil {
		s.Screen = "register_name"
		s.Notice = err.Error()
		return
	}
	if !nrcPattern.MatchString(s.RegistrationNRC) {
		s.Screen = "register_nrc"
		s.Notice = "Use format 123456/78/1."
		return
	}
	if _, err = phone(s.Phone); err != nil {
		a.end(s, "The mobile number is invalid. Please dial again.")
		return
	}
	for _, c := range a.db.Customers {
		if c.Phone == s.Phone || c.NRC == s.RegistrationNRC {
			a.end(s, caseText(a.newCase(s, "ACCOUNT_LINKING", "")))
			return
		}
	}
	c := &Customer{ID: id("CUS"), Name: name, NRC: s.RegistrationNRC, Phone: s.Phone}
	a.db.Customers[c.ID] = c
	s.CustomerID = c.ID
	s.RegistrationName = ""
	s.RegistrationNRC = ""
	s.RecognitionSuppressed = false
	s.ConfirmedUntil = time.Now().UTC().Add(5 * time.Minute)
	a.audit("customer", "demo.activation", c.ID)
	a.startIntent(s)
}
func (a *App) pendingCase(s *Session) {
	op := a.db.Operations[s.TransactionID]
	if op == nil || op.CustomerID != s.CustomerID {
		a.end(s, "Transaction unavailable.")
		return
	}
	if op.Status == "UNKNOWN" || op.Status == "PENDING" {
		c := a.newCase(s, "PAYMENT_UNKNOWN", op.ID)
		s.Notice = caseText(c)
	} else {
		s.Notice = "Local demo status refreshed. No provider has been contacted."
	}
}
