// Package nyali implements the supplied partner contract. It never retries a
// collection automatically or treats an absent list result as payment failure.
package nyali

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const BaseURL = "https://mightyfin.cloud/public/api/partner"

type Outstanding struct {
	Total   json.Number `json:"total"`
	Arrears json.Number `json:"arrears"`
}
type Installment struct {
	DueDate  string      `json:"due_date"`
	TotalDue json.Number `json:"total_due"`
}
type Loan struct {
	ID          int64        `json:"loan_id"`
	Account     string       `json:"account_number"`
	Status      string       `json:"status"`
	Outstanding Outstanding  `json:"outstanding"`
	Next        *Installment `json:"next_installment"`
}
type Customer struct {
	ID     int64  `json:"client_id"`
	Name   string `json:"name"`
	Mobile string `json:"mobile"`
	Loans  []Loan `json:"loans"`
}
type VerifyResponse struct {
	OK      bool       `json:"ok"`
	Clients []Customer `json:"clients"`
}
type LoanResponse struct {
	OK     bool     `json:"ok"`
	Client Customer `json:"client"`
	Loan   Loan     `json:"loan"`
}
type Allocation struct {
	OK          bool        `json:"ok"`
	Loan        Loan        `json:"recommended_loan"`
	Amount      json.Number `json:"request_amount"`
	Overpayment json.Number `json:"overpayment"`
}
type Payment struct {
	Reference string      `json:"reference"`
	External  string      `json:"external_reference"`
	LoanID    int64       `json:"loan_id"`
	Amount    json.Number `json:"amount"`
	MSISDN    string      `json:"msisdn"`
	Currency  string      `json:"currency"`
	Status    string      `json:"status"`
	Posted    bool        `json:"posted"`
	CreatedAt string      `json:"created_at"`
}
type Collection struct {
	OK bool `json:"ok"`
	Payment
}
type Payments struct {
	OK       bool      `json:"ok"`
	Count    int       `json:"count"`
	Payments []Payment `json:"payments"`
}
type APIError struct {
	Status     int
	Reason     string
	RetryAfter time.Duration
	Uncertain  bool
}

func (e *APIError) Error() string { return fmt.Sprintf("Nyali request failed (HTTP %d)", e.Status) }
func Unknown(err error) bool      { var e *APIError; return errors.As(err, &e) && e.Uncertain }

type Client struct {
	base, key string
	http      *http.Client
	gate      chan struct{}
	mu        sync.Mutex
	next      time.Time
	interval  time.Duration
}

func New(key string, h *http.Client) (*Client, error) {
	if strings.TrimSpace(key) != key || key == "" || len(key) > 4096 {
		return nil, errors.New("invalid Nyali credential")
	}
	for _, c := range key {
		if c <= 32 || c >= 127 {
			return nil, errors.New("invalid Nyali credential")
		}
	}
	if h == nil {
		h = &http.Client{Timeout: 4 * time.Second}
	}
	copy := *h
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{base: BaseURL, key: key, http: &copy, gate: make(chan struct{}, 1), interval: 1050 * time.Millisecond}, nil
}
func (c *Client) String() string { return "Nyali partner API client" }
func (c *Client) wait(ctx context.Context) error {
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	for {
		c.mu.Lock()
		wait := time.Until(c.next)
		if wait <= 0 {
			c.next = time.Now().Add(c.interval)
			c.mu.Unlock()
			return nil
		}
		c.mu.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}

}

var money = regexp.MustCompile(`^(?:0|[1-9][0-9]{0,12})(?:\.[0-9]{1,2})?$`)

func Cents(value string) (int64, error) {
	if !money.MatchString(value) {
		return 0, errors.New("invalid decimal amount")
	}
	p := strings.Split(value, ".")
	whole, _ := strconv.ParseInt(p[0], 10, 64)
	fraction := "00"
	if len(p) == 2 {
		fraction = p[1]
		if len(fraction) == 1 {
			fraction += "0"
		}
	}
	c, _ := strconv.ParseInt(fraction, 10, 64)
	return whole*100 + c, nil
}
func Amount(value string) (string, error) {
	n, e := Cents(value)
	if e != nil || n <= 0 {
		return "", errors.New("positive ZMW amount required")
	}
	return fmt.Sprintf("%d.%02d", n/100, n%100), nil
}
func Phone(value string) (string, error) {
	var b strings.Builder
	for _, c := range value {
		if c >= '0' && c <= '9' {
			b.WriteRune(c)
		} else if c != '+' && c != '-' && c != '(' && c != ')' && !unicode.IsSpace(c) {
			return "", errors.New("invalid phone")
		}
	}
	p := b.String()
	if len(p) == 10 && p[0] == '0' {
		p = "260" + p[1:]
	}
	if len(p) == 9 {
		p = "260" + p
	}
	if !regexp.MustCompile(`^260[79][0-9]{8}$`).MatchString(p) {
		return "", errors.New("invalid phone")
	}
	return p, nil
}
func (c *Client) do(ctx context.Context, method, path string, body any, result any, collection bool) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return errors.New("invalid Nyali request")
		}
	}
	if err = c.wait(ctx); err != nil {
		return &APIError{Reason: "request not sent", Uncertain: false}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "MightyFin-USSD/1.0")
	res, err := c.http.Do(req)
	if err != nil {
		return &APIError{Uncertain: collection}
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1048577))
	if err != nil || len(raw) > 1048576 {
		return &APIError{Status: res.StatusCode, Uncertain: collection}
	}
	var envelope struct {
		OK     bool   `json:"ok"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return &APIError{Status: res.StatusCode, Uncertain: collection}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 || !envelope.OK {
		uncertain := collection && (res.StatusCode >= 500 || res.StatusCode == 409 || (res.StatusCode >= 200 && res.StatusCode < 300))
		reason := strings.ToLower(envelope.Reason)
		if reason == "collections disabled" || reason == "loan is not disbursed" {
			uncertain = false
		}
		seconds, _ := strconv.Atoi(res.Header.Get("Retry-After"))
		if res.StatusCode == 429 {
			if seconds <= 0 {
				seconds = 60
			}
			c.mu.Lock()
			until := time.Now().Add(time.Duration(seconds) * time.Second)
			if until.After(c.next) {
				c.next = until
			}
			c.mu.Unlock()
		}
		return &APIError{Status: res.StatusCode, Reason: envelope.Reason, RetryAfter: time.Duration(seconds) * time.Second, Uncertain: uncertain}
	}
	if json.Unmarshal(raw, result) != nil {
		return &APIError{Status: res.StatusCode, Uncertain: collection}
	}
	return nil
}
func (c *Client) Verify(ctx context.Context, phone string) (VerifyResponse, error) {
	p, e := Phone(phone)
	if e != nil {
		return VerifyResponse{}, e
	}
	var r VerifyResponse
	e = c.do(ctx, "POST", "/loans/verify", map[string]string{"phone": p}, &r, false)
	return r, e
}
func (c *Client) Loan(ctx context.Context, id int64) (LoanResponse, error) {
	if id <= 0 {
		return LoanResponse{}, errors.New("invalid loan")
	}
	var r LoanResponse
	e := c.do(ctx, "GET", fmt.Sprintf("/loans/%d", id), nil, &r, false)
	return r, e
}
func (c *Client) Allocate(ctx context.Context, phone, amount string) (Allocation, error) {
	p, e := Phone(phone)
	if e != nil {
		return Allocation{}, e
	}
	a, e := Amount(amount)
	if e != nil {
		return Allocation{}, e
	}
	var r Allocation
	e = c.do(ctx, "POST", "/loans/allocate", map[string]any{"phone": p, "amount": json.Number(a)}, &r, false)
	return r, e
}

var reference = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{3,190}$`)

func (c *Client) Collect(ctx context.Context, id int64, payer, amount, ref string) (Collection, error) {
	if id <= 0 || !reference.MatchString(ref) {
		return Collection{}, errors.New("invalid loan or external reference")
	}
	p, e := Phone(payer)
	if e != nil {
		return Collection{}, e
	}
	a, e := Amount(amount)
	if e != nil {
		return Collection{}, e
	}
	var r Collection
	e = c.do(ctx, "POST", fmt.Sprintf("/loans/%d/collect", id), map[string]any{"amount": json.Number(a), "msisdn": p, "external_reference": ref}, &r, true)
	if e != nil {
		return r, e
	}
	got, e := Amount(r.Amount.String())
	mp, pe := Phone(r.MSISDN)
	if r.External != ref || r.LoanID != id || e != nil || got != a || pe != nil || mp != p {
		return r, &APIError{Status: 200, Uncertain: true}
	}
	switch r.Status {
	case "Pending", "Submitting", "Unknown", "Successful", "Failed":
	default:
		return r, &APIError{Status: 200, Uncertain: true}
	}
	if r.Reference == "" || r.Posted && r.Status != "Successful" {
		return r, &APIError{Status: 200, Uncertain: true}
	}
	return r, nil
}
func (c *Client) Payments(ctx context.Context, day string) (Payments, error) {
	if _, e := time.Parse("2006-01-02", day); e != nil {
		return Payments{}, errors.New("invalid payment date")
	}
	var r Payments
	e := c.do(ctx, "GET", "/payments?"+url.Values{"date": {day}, "limit": {"500"}}.Encode(), nil, &r, false)
	return r, e
}

func (c *Client) PaymentsRange(ctx context.Context, from, to string) (Payments, error) {
	a, e := time.Parse("2006-01-02", from)
	if e != nil {
		return Payments{}, e
	}
	b, e := time.Parse("2006-01-02", to)
	if e != nil || b.Before(a) {
		return Payments{}, errors.New("invalid payment date range")
	}
	var r Payments
	e = c.do(ctx, "GET", "/payments?"+url.Values{"from": {from}, "to": {to}, "limit": {"500"}}.Encode(), nil, &r, false)
	return r, e
}
