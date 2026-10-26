package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func validationFixture(t *testing.T, screen string, page int, method string) (*App, *Session) {
	t.Helper()
	a := testApp(t)
	s := menuSession(a)
	s.Screen = screen
	s.Page = page
	s.ApplicationID = "application"
	s.OfferID = "offer"
	s.LoanID = "loan"
	s.QuoteID = "quote"
	s.TransactionID = "payment"
	s.Method = method
	s.Amount = 10000
	s.Choices = []string{"loan"}
	a.db.Applications["application"] = &Application{ID: "application", CustomerID: "customer", Product: "salary", Amount: 50000, Term: 3, Status: "DRAFT", Version: 1, Answers: map[string]string{"Employer name": "Demo Employer", "Monthly income (ZMW)": "2000.00"}}
	a.db.Offers["offer"] = &Offer{ID: "offer", ApplicationID: "application", Status: "ISSUED", Version: 1, ExpiresAt: time.Now().Add(time.Hour)}
	s.OfferVersion = 1
	a.db.Loans["loan"] = &Loan{ID: "loan", CustomerID: "customer", Status: "ACTIVE", Balance: 100000, Due: 100000, Version: 1}
	a.db.Quotes["quote"] = &Quote{ID: "quote", CustomerID: "customer", LoanID: "loan", Method: "MOBILE_MONEY", Source: s.Phone, Amount: 10000, Total: 10000, LoanVersion: 1, ExpiresAt: time.Now().Add(time.Minute)}
	a.db.Operations["payment"] = &Operation{ID: "payment", CustomerID: "customer", LoanID: "loan", Kind: "REPAYMENT", Status: "AWAITING_AUTHORISATION", Posting: "NOT_POSTED", Amount: 10000, Events: map[string]string{}}
	if screen == "transactions" {
		s.Choices = []string{"payment"}
	}
	if screen == "cases" {
		s.Choices = nil
	}
	_ = a.render(s)
	return a, s
}
func assertAnswerRejected(t *testing.T, a *App, s *Session, input string) {
	t.Helper()
	screen, page := s.Screen, s.Page
	before, _ := json.Marshal(a.db)
	a.advance(s, input)
	if s.Screen != screen || s.Page != page || s.Ended || s.Notice == "" {
		t.Fatalf("%s accepted %q: moved to %s page %d (%s)", screen, input, s.Screen, s.Page, s.Notice)
	}
	after, _ := json.Marshal(a.db)
	if !bytes.Equal(before, after) {
		t.Fatalf("invalid %q changed saved business records on %s", input, screen)
	}
}
func TestAllNumberedQuestionsRejectUnlistedOrNonCanonicalChoices(t *testing.T) {
	screens := []string{"main", "recognise", "unknown", "register_consent", "register_phone", "account", "recovery", "help", "resume", "apply_product", "apply_confirm", "kyc", "status_menu", "application_status", "withdraw_confirm", "offer", "loans", "repay_amount", "repay_method", "repay_confirm", "transactions", "transaction", "cases"}
	for _, screen := range screens {
		t.Run(screen, func(t *testing.T) {
			a, s := validationFixture(t, screen, 0, "")
			for _, bad := range []string{"", "yes", "+1", "01", "1.0", "-1", "8", "98", "١", "1abc", "1\n", "1\u200b"} {
				assertAnswerRejected(t, a, s, bad)
			}
			if screen == "offer" {
				s.Page = 2
				for _, bad := range []string{"+1", "01", "4", "accept", "1\n"} {
					assertAnswerRejected(t, a, s, bad)
				}
			}
		})
	}
}
func TestTypedAnswersKeepValidDataAndCurrentQuestion(t *testing.T) {
	tests := []struct {
		screen, method string
		page           int
		bad            []string
	}{
		{screen: "register_name", bad: []string{"", "123456", "George 123", "<script>", "George 😀", "George\nMunganga", strings.Repeat("A", 61), strings.Repeat("A", 201)}},
		{screen: "register_nrc", bad: []string{"", "123456781", "123456/7/1", "12345A/78/1", "１２３４５６/78/1", "123456/78/12"}},
		{screen: "apply_amount", bad: []string{"", "five hundred", "-500", "+500", "1,000", "1e3", "NaN", "500.001", "0.00", "499.99", "10000.01", "500\n"}},
		{screen: "apply_term", bad: []string{"", "three", "3 months", "+3", "03", "3.0", "2", "300", "٣"}},
		{screen: "apply_screening", page: 0, bad: []string{"", "123456", "<company>", "😀😀", strings.Repeat("A", 101)}},
		{screen: "apply_screening", page: 1, bad: []string{"", "salary", "0.00", "-100", "+100", "1e3", "1,000", "100.001", "Infinity", strings.Repeat("9", 13)}},
		{screen: "repay_other", bad: []string{"", "one hundred", "0.00", "-100", "+100", "100.001", "1000.01", "100\t"}},
		{screen: "repay_source", method: "MOBILE_MONEY", bad: []string{"", "George", "1234", "0810000001", "+2609700000012", "0970000002", "097000000A", "097\n0000001"}},
		{screen: "repay_source", method: "BANK", bad: []string{"", "ABC", "AB C", "REF<123>", "' OR 1=1--", strings.Repeat("A", 25), "BANK\n123"}},
	}
	for _, tc := range tests {
		t.Run(tc.screen+"/"+tc.method+"/"+string(rune('0'+tc.page)), func(t *testing.T) {
			a, s := validationFixture(t, tc.screen, tc.page, tc.method)
			for _, bad := range tc.bad {
				assertAnswerRejected(t, a, s, bad)
			}
		})
	}
}
func TestValidNamesAndBusinessText(t *testing.T) {
	for _, name := range []string{"George Munganga", "Anne-Marie O’Neill", "José Banda", "Jose\u0301 Banda", "Chanda N'Gandu", "J. Banda", "王小明"} {
		if _, err := customerName(name); err != nil {
			t.Fatalf("valid name %q rejected: %v", name, err)
		}
	}
	name, err := customerName(" George   Munganga ")
	if err != nil || name != "George Munganga" {
		t.Fatal("name whitespace not normalised")
	}
	for _, value := range []string{"3M Zambia Ltd.", "Shop 24 (retail)", "C++ Training & Consulting", "Farming / market sales"} {
		if _, err := screeningText(value); err != nil {
			t.Fatalf("valid business text %q rejected: %v", value, err)
		}
	}
}
func TestValidMoneyPhoneAndBankReference(t *testing.T) {
	for input, want := range map[string]string{"0970000001": "+260970000001", "+260970000001": "+260970000001", "260970000001": "+260970000001", "0770000001": "+260770000001", "097 000 0001": "+260970000001"} {
		got, err := phone(input)
		if err != nil || got != want {
			t.Fatalf("phone %q: %q %v", input, got, err)
		}
	}
	for _, ref := range []string{"1234", "DEMO-BANK-1234", "BANK/1234", "abc123"} {
		if _, err := bankReference(ref); err != nil {
			t.Fatal(err)
		}
	}
	a, s := validationFixture(t, "apply_screening", 1, "")
	a.advance(s, "5")
	if s.Screen != "apply_confirm" || a.db.Applications["application"].Answers["Monthly income (ZMW)"] != "5.00" {
		t.Fatal("single-digit income should be valid and exact")
	}
	a, s = validationFixture(t, "repay_source", 0, "BANK")
	a.advance(s, "BANK/1234")
	if s.Screen != "repay_confirm" {
		t.Fatal("valid bank reference did not create quote")
	}
}
func TestSubmissionRevalidatesPreviouslySavedAnswers(t *testing.T) {
	a, s := validationFixture(t, "apply_confirm", 0, "")
	d := a.db.Applications["application"]
	d.Answers["Employer name"] = "123456"
	a.advance(s, "1")
	if d.Status != "DRAFT" || s.Screen != "apply_screening" || s.Page != 0 {
		t.Fatal("old invalid draft was submitted")
	}
	if d.Amount != 50000 || d.Term != 3 || d.Answers["Monthly income (ZMW)"] != "2000.00" {
		t.Fatal("valid saved answers were lost")
	}
}
func TestValidationDoesNotBypassExpiredConfirmation(t *testing.T) {
	a, s := validationFixture(t, "repay_confirm", 0, "")
	s.ConfirmedUntil = time.Now().Add(-time.Minute)
	a.advance(s, "1\n")
	if !s.Ended || s.Screen != "end" {
		t.Fatal("validation failure rendered private screen after confirmation expired")
	}
}
func TestQuestionKeyboardHintsMatchExpectedType(t *testing.T) {
	for _, tc := range []struct {
		screen, method, mode string
		page, max            int
	}{{"main", "", "numeric", 0, 2}, {"register_name", "", "text", 0, 60}, {"register_nrc", "", "text", 0, 11}, {"apply_amount", "", "decimal", 0, 12}, {"apply_term", "", "numeric", 0, 2}, {"apply_screening", "", "text", 0, 100}, {"apply_screening", "", "decimal", 1, 12}, {"repay_source", "MOBILE_MONEY", "tel", 0, 20}, {"repay_source", "BANK", "text", 0, 24}} {
		a, s := validationFixture(t, tc.screen, tc.page, tc.method)
		r := a.render(s)
		if r.InputMode != tc.mode || r.MaxLength != tc.max || r.InputHint == "" {
			t.Fatalf("incorrect input hints for %s: %+v", tc.screen, r)
		}
	}
}
func TestHTTPValidationCannotBeBypassedWithoutBrowserChecks(t *testing.T) {
	a := testApp(t)
	h := dial(t, a)
	h.inputs("1", "1", "1")
	h.has("full name")
	h.send("123456")
	h.has("Names cannot contain numbers")
	h.send("George Munganga")
	h.has("Enter demo NRC")
	h.send("123456/AA/1")
	h.has("Use format")
	h.send("123456/78/1")
	h.send("1")
	h.inputs("1", "500", "3")
	h.send("1234")
	h.has("not numbers only")
	h.send("3M Zambia")
	h.send("100.001")
	h.has("2 decimal places")
	h.send("5")
	h.has("1 Agree and Submit")
	h.send("1\n")
	if len(a.db.Operations) != 0 {
		t.Fatal("invalid commitment created operation")
	}
	for _, d := range a.db.Applications {
		if d.Status != "DRAFT" {
			t.Fatal("control characters bypassed submission confirmation")
		}
	}
	h.send("1")
	h.has("KYC pending")
	code, _ := call(t, a, "/api/sessions", Request{Phone: "0810000001", RequestID: "request-id", Sequence: 1})
	if code == 200 {
		t.Fatal("non-mobile dial number accepted")
	}
	code, _ = call(t, a, "/api/sessions", Request{Phone: "0970000001", RequestID: "bad\nkey", Sequence: 1})
	if code != 400 {
		t.Fatal("control characters accepted in request key")
	}
	code, _ = call(t, a, "/api/demo/actions", DemoAction{Action: "pending", Reference: "payment", EventID: "bad key"})
	if code != 400 {
		t.Fatal("malformed event ID accepted")
	}
}
