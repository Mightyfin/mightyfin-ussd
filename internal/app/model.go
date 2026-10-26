package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Customer struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	NRC   string `json:"nrc"`
	Phone string `json:"phone"`
}
type Product struct {
	ID, Name  string
	Min, Max  int64
	Terms     []int
	Screening []string
}

// Synthetic catalogue only. Live limits and questions must come from the LMS.
var products = []Product{
	{"salary", "Demo Salary Loan", 50000, 1000000, []int{1, 3, 6}, []string{"Employer name", "Monthly income (ZMW)"}},
	{"business", "Demo Business Loan", 100000, 2000000, []int{3, 6, 12}, []string{"Business activity", "Monthly income (ZMW)"}},
}

type Application struct {
	ID             string            `json:"id"`
	CustomerID     string            `json:"customer_id"`
	Status         string            `json:"status"`
	Product        string            `json:"product"`
	Amount         int64             `json:"amount_minor"`
	Term           int               `json:"term_months"`
	Answers        map[string]string `json:"screening"`
	ConsentVersion string            `json:"consent_version"`
	ConsentedAt    time.Time         `json:"consented_at"`
	SubmissionKey  string            `json:"submission_key"`
	KYC            string            `json:"kyc"`
	Version        int               `json:"version"`
	NextAction     string            `json:"next_action"`
}
type Offer struct {
	ID, ApplicationID, Status                         string
	Version                                           int
	Principal, Deductions, Interest, Fees, Net, Total int64
	Term                                              int
	IssuedAt, ExpiresAt                               time.Time
	AcceptedAt                                        *time.Time
	AcceptanceKey                                     string
	ReviewedVersion                                   int
}
type Loan struct {
	ID, CustomerID, ApplicationID string
	Balance, Due, Overdue         int64
	Version                       int
	Status                        string
}
type Quote struct {
	ID, LoanID, CustomerID, Method, Source string
	Amount, Fee, Total, Remaining          int64
	LoanVersion                            int
	ExpiresAt                              time.Time
}
type Operation struct {
	ID, Kind, CustomerID, LoanID, ApplicationID, Status, Posting, Method, Source, QuoteID, Key, ProviderRef, Receipt string
	Amount, Fee                                                                                                      int64
	AllocatedDue, AllocatedOverdue                                                                                   int64
	CreatedAt                                                                                                        time.Time
	Events                                                                                                           map[string]string
	ReversalID                                                                                                       string
}
type LedgerEntry struct {
	ID, OperationID, LoanID, Kind, LinkedEntry string
	Amount                                     int64
	CreatedAt                                  time.Time
}
type Case struct {
	ID, CustomerID, Category, Related, Status, Owner, NextAction, ContactPreference string
	CreatedAt                                                                       time.Time
	UpdateDue                                                                       *time.Time
}
type Audit struct {
	At                       time.Time
	Actor, Action, Reference string
}
type Session struct {
	RecognitionSuppressed                                                                         bool
	ID, Phone, Screen, CustomerID, Intent, ApplicationID, OfferID, LoanID, QuoteID, TransactionID string
	ConfirmedUntil, ExpiresAt                                                                     time.Time
	Ended                                                                                         bool
	LastSequence                                                                                  int
	LastRequestID, LastFingerprint                                                                string
	LastResponse                                                                                  Response
	Choices                                                                                       []string
	Page                                                                                          int
	RegistrationName, RegistrationNRC                                                             string
	Amount                                                                                        int64
	Method, Source                                                                                string
	Notice                                                                                        string
	ApplicationVersion, OfferVersion                                                              int
	MenuAction, MenuReference                                                                     string
}
type Database struct {
	SchemaVersion int
	Customers     map[string]*Customer
	Applications  map[string]*Application
	Offers        map[string]*Offer
	Loans         map[string]*Loan
	Quotes        map[string]*Quote
	Operations    map[string]*Operation
	Cases         map[string]*Case
	Sessions      map[string]*Session
	Ledger        []LedgerEntry
	Audit         []Audit
}

func emptyDB() Database {
	return Database{SchemaVersion: 2, Customers: map[string]*Customer{}, Applications: map[string]*Application{}, Offers: map[string]*Offer{}, Loans: map[string]*Loan{}, Quotes: map[string]*Quote{}, Operations: map[string]*Operation{}, Cases: map[string]*Case{}, Sessions: map[string]*Session{}}
}
func id(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + "-" + strings.ToUpper(hex.EncodeToString(b))
}
func money(n int64) string { return fmt.Sprintf("K%d.%02d", n/100, n%100) }
func parseMoney(s string) (int64, error) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ".")
	if len(parts) > 2 || len(parts[0]) == 0 || len(parts[0]) > 9 {
		return 0, errors.New("amount")
	}
	for _, r := range parts[0] {
		if r < '0' || r > '9' {
			return 0, errors.New("amount")
		}
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, err
	}
	n := whole * 100
	if len(parts) == 2 {
		if len(parts[1]) < 1 || len(parts[1]) > 2 {
			return 0, errors.New("amount")
		}
		for _, r := range parts[1] {
			if r < '0' || r > '9' {
				return 0, errors.New("amount")
			}
		}
		frac := parts[1]
		if len(frac) == 1 {
			frac += "0"
		}
		v, _ := strconv.ParseInt(frac, 10, 64)
		n += v
	}
	if n <= 0 {
		return 0, errors.New("amount")
	}
	return n, nil
}
func phone(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, " ", "")
	if strings.HasPrefix(s, "0") {
		s = "+260" + s[1:]
	} else if strings.HasPrefix(s, "260") {
		s = "+" + s
	}
	if len(s) != 13 || !strings.HasPrefix(s, "+260") {
		return "", errors.New("Use a Zambian number, e.g. 0970000001")
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return "", errors.New("Invalid phone number")
		}
	}
	return s, nil
}
func masked(s string) string {
	if len(s) < 4 {
		return "****"
	}
	return "***" + s[len(s)-4:]
}
func product(key string) *Product {
	for i := range products {
		if products[i].ID == key {
			return &products[i]
		}
	}
	return nil
}
func (a *App) audit(actor, action, ref string) {
	a.db.Audit = append(a.db.Audit, Audit{time.Now().UTC(), actor, action, ref})
}
