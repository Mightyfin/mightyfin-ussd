package app

import (
	"strings"
	"testing"
)

func TestRecognisedGreetingAndNotMeSuppression(t *testing.T) {
	a := testApp(t)
	register(t, a)
	var customerID string
	for key, c := range a.db.Customers {
		c.Name = "George Munganga"
		customerID = key
	}
	h := dial(t, a)
	h.has("Welcome to MightyFin, George Munganga")
	h.has("1 This is me")
	h.has("2 Not me")
	if a.customerConfirmed(a.db.Sessions[h.session]) {
		t.Fatal("recognition granted account access")
	}
	h.send("2")
	h.has("Welcome to MightyFin")
	assertSuppressed := func() {
		t.Helper()
		s := a.db.Sessions[h.session]
		if strings.Contains(h.response.Text, "George Munganga") || s.CustomerID != "" || !s.RecognitionSuppressed || !s.ConfirmedUntil.IsZero() {
			t.Fatal("Not me retained account recognition")
		}
	}
	assertSuppressed()
	// Back/main and every protected route must respect the rejection for this session.
	for _, input := range []string{"1", "2", "3", "4"} {
		h.inputs("00", input)
		h.has("Existing account assistance")
		assertSuppressed()
	}
	if a.db.Customers[customerID].Name != "George Munganga" {
		t.Fatal("Not me changed the underlying customer")
	}
	// The session choice survives a restart. A genuinely new dial can recognise again.
	dir := a.dir
	a.Close()
	restored, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	a = restored
	h.a = restored
	h.inputs("00", "1")
	assertSuppressed()
	again := dial(t, restored)
	again.has("Welcome to MightyFin, George Munganga")
}
func TestNameConfirmationOpensMenuWithoutChannelPIN(t *testing.T) {
	a := testApp(t)
	register(t, a)
	for key := range a.db.Customers {
		a.db.Loans["loan"] = &Loan{ID: "loan", CustomerID: key, Status: "ACTIVE", Balance: 50000}
	}
	h := dial(t, a)
	if strings.Contains(h.response.Text, "Repay your loan") || strings.Contains(h.response.Text, "500.00") {
		t.Fatal("financial detail exposed before name confirmation")
	}
	h.send("1")
	if strings.Contains(h.response.Text, "PIN") {
		t.Fatal("channel asked for a PIN")
	}
	h.has("Welcome to MightyFin, Demo Customer")
	h.has("Repay your loan")
	h.send("1")
	h.has("Select outstanding loan")
}
