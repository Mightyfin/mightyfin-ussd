package ussdgateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

type PayLoan struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`
}
type Payments interface {
	Loans(context.Context, string, bool) ([]PayLoan, error)
	Collect(context.Context, string, int64, string, string) (string, error)
	Status(context.Context, string, int64) (string, error)
}

var paymentInput = regexp.MustCompile(`^[34](?:\*[0-9]{1,7}(?:\.[0-9]{1,2})?)*$`)
var moneyInput = regexp.MustCompile(`^(?:[1-9][0-9]{0,6}|0)(?:\.[0-9]{1,2})?$`)

func paymentPath(text string) string {
	parts := strings.Split(text, "*")
	state := "home"
	for i, input := range parts {
		if state == "home" && (input == "3" || input == "4") {
			return strings.Join(parts[i:], "*")
		}
		if state == "home" && input == "1" {
			state = "products"
			continue
		}
		if state == "products" && input == "9" {
			state = "home"
			continue
		}
		return ""
	}
	return ""
}
func amountString(input string) (string, bool) {
	if !moneyInput.MatchString(input) {
		return "", false
	}
	parts := strings.Split(input, ".")
	whole, _ := strconv.ParseInt(parts[0], 10, 64)
	fraction := "00"
	if len(parts) == 2 {
		fraction = parts[1]
		if len(fraction) == 1 {
			fraction += "0"
		}
	}
	cents, _ := strconv.ParseInt(fraction, 10, 64)
	if whole == 0 && cents == 0 {
		return "", false
	}
	return fmt.Sprintf("%d.%02d", whole, cents), true
}
func (s *Server) paymentScreen(ctx context.Context, session *session, text, phone, id string) string {
	parts := strings.Split(text, "*")
	if len(parts) > 1 && parts[len(parts)-1] == "0" {
		return "END Payment cancelled. No new request was submitted."
	}
	if len(parts) == 1 {
		loans, err := s.Payments.Loans(ctx, phone, parts[0] == "4")
		if err != nil || len(loans) == 0 || len(loans) > 5 {
			return "END Payment options are unavailable. Please contact your loan officer."
		}
		session.payLoans = loans
		result := "CON Select loan for payment"
		if parts[0] == "4" {
			result = "CON Select loan to check payment"
		}
		for i, loan := range loans {
			if loan.ID <= 0 {
				return "END Payment options are unavailable."
			}
			// Build labels from validated internal IDs; never render arbitrary LMS text.
			label := strconv.FormatInt(loan.ID, 10)
			if len(label) > 6 {
				label = label[len(label)-6:]
			}
			result += fmt.Sprintf("\n%d Loan ...%s", i+1, label)
		}
		return result + "\n0 Cancel"
	}
	selected, err := strconv.Atoi(parts[1])
	if err != nil || selected < 1 || selected > len(session.payLoans) {
		return "END Invalid loan selection. Please dial again."
	}
	loan := session.payLoans[selected-1].ID
	if parts[0] == "4" {
		if len(parts) != 2 {
			return "END Invalid selection. Please dial again."
		}
		status, err := s.Payments.Status(ctx, phone, loan)
		if err != nil {
			return "END Payment status is unavailable. Please check later before retrying payment."
		}
		return paymentStatus(status)
	}
	if len(parts) == 2 {
		return "CON Enter payment amount in ZMW (for example 100.00).\n0 Cancel"
	}
	amount, ok := amountString(parts[2])
	if !ok {
		return "END Invalid amount. Please enter a positive amount with up to 2 decimal places."
	}
	if len(parts) == 3 {
		return "CON Pay ZMW " + amount + " from this mobile-money number?\n1 Confirm\n0 Cancel"
	}
	if len(parts) != 4 || parts[3] != "1" {
		return "END Payment not confirmed. No request was submitted."
	}
	sum := sha256.Sum256([]byte("ussd-payment-v1\x00" + id + "\x00" + phone + "\x00" + strconv.FormatInt(loan, 10) + "\x00" + amount))
	status, err := s.Payments.Collect(ctx, phone, loan, amount, hex.EncodeToString(sum[:]))
	// A timeout can occur after a collection was created. Never say it failed or
	// invite a fresh payment. A repeated callback uses the same durable LMS key.
	if err != nil {
		return "END Payment request is processing. Check payment status before retrying."
	}
	switch status {
	case "unavailable":
		return "END Payments are temporarily unavailable. Please contact your loan officer."
	case "pending":
		return "END Payment request submitted. Please wait for your mobile-money prompt and enter your wallet PIN there."
	case "completed":
		return "END Payment confirmed and recorded. Thank you."
	case "failed":
		return "END Payment was not completed. Check payment status before trying again."
	default:
		return "END Payment request is processing. Check payment status before retrying."
	}
}
func paymentStatus(status string) string {
	switch status {
	case "completed":
		return "END Payment confirmed and recorded. Thank you."
	case "failed":
		return "END Payment was not completed. You may start a new payment request."
	case "none":
		return "END No payment request was found for this loan on this channel."
	case "pending":
		return "END Payment is awaiting confirmation. Complete your mobile-money prompt, or check again later."
	default:
		return "END Payment is still being checked. Please check again later before starting another payment."
	}
}

type PaymentClient struct {
	Key  string
	HTTP *http.Client
}

func (p *PaymentClient) Ready(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:28094/healthz", nil)
	if err != nil {
		return false
	}
	res, err := p.HTTP.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	var state struct {
		Account  bool `json:"payment_account_configured"`
		Provider bool `json:"provider_configured"`
	}
	return res.StatusCode == 200 && json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&state) == nil && state.Account && state.Provider
}

func (p *PaymentClient) call(ctx context.Context, path string, data any) ([]byte, error) {
	b, _ := json.Marshal(data)
	req, err := http.NewRequestWithContext(ctx, "POST", "http://127.0.0.1:28094"+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.Key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := p.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("payment response unavailable")
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 32769))
	if err == nil && len(body) <= 32768 && res.StatusCode == 503 {
		var unavailable struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &unavailable) == nil && unavailable.Error == "PAYMENT_ACCOUNT_NOT_CONFIGURED" {
			return []byte(`{"status":"unavailable"}`), nil
		}
	}
	if err != nil || len(body) > 32768 || res.StatusCode < 200 || res.StatusCode >= 300 || !json.Valid(body) {
		return nil, fmt.Errorf("payment response unavailable")
	}
	return body, nil
}
func (p *PaymentClient) Loans(c context.Context, phone string, status bool) ([]PayLoan, error) {
	path := "/v1/loans"
	if status {
		path = "/v1/status-loans"
	}
	b, e := p.call(c, path, map[string]any{"phone": phone})
	if e != nil {
		return nil, e
	}
	var result struct {
		Loans []PayLoan `json:"loans"`
	}
	e = json.Unmarshal(b, &result)
	return result.Loans, e
}
func (p *PaymentClient) Collect(c context.Context, phone string, loan int64, amount, key string) (string, error) {
	b, e := p.call(c, "/v1/collect", map[string]any{"phone": phone, "loan_id": loan, "amount": amount, "request_id": key, "confirmed": true})
	if e != nil {
		return "", e
	}
	var result struct {
		Status string `json:"status"`
	}
	e = json.Unmarshal(b, &result)
	return result.Status, e
}
func (p *PaymentClient) Status(c context.Context, phone string, loan int64) (string, error) {
	b, e := p.call(c, "/v1/status", map[string]any{"phone": phone, "loan_id": loan})
	if e != nil {
		return "", e
	}
	var result struct {
		Status string `json:"status"`
	}
	e = json.Unmarshal(b, &result)
	return result.Status, e
}
