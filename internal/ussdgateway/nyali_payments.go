package ussdgateway

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Mightyfin/mightyfin-ussd/internal/nyali"
)

type nyaliMemoKey struct{}
type nyaliMemo struct {
	phone    string
	customer nyali.Customer
	err      error
	loaded   bool
}

func nyaliRequestContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, nyaliMemoKey{}, &nyaliMemo{})
}

type NyaliAPI interface {
	Verify(context.Context, string) (nyali.VerifyResponse, error)
	Collect(context.Context, int64, string, string, string) (nyali.Collection, error)
	Payments(context.Context, string) (nyali.Payments, error)
}
type NyaliPayments struct {
	Client   NyaliAPI
	Store    *nyali.Store
	Fallback interface {
		Welcome(context.Context, string) (string, error)
	}
	disabledUntil atomic.Int64
}

var errNyaliNoMatch = errors.New("Nyali customer not found")

func (p *NyaliPayments) customer(ctx context.Context, phone string, fresh bool) (result nyali.Customer, resultErr error) {
	canonical, e := nyali.Phone(phone)
	if e != nil {
		return nyali.Customer{}, e
	}
	memo, _ := ctx.Value(nyaliMemoKey{}).(*nyaliMemo)
	if !fresh && memo != nil && memo.loaded && memo.phone == canonical {
		return memo.customer, memo.err
	}

	if memo != nil {
		defer func() {
			memo.loaded, memo.phone = true, canonical
			memo.customer, memo.err = result, resultErr
		}()
	}
	r, e := p.Client.Verify(ctx, canonical)
	if e != nil {
		return nyali.Customer{}, e
	}
	if len(r.Clients) == 0 {
		return nyali.Customer{}, errNyaliNoMatch
	}
	if len(r.Clients) != 1 || r.Clients[0].ID <= 0 {
		return nyali.Customer{}, errors.New("Nyali account needs assistance")
	}
	c := r.Clients[0]
	mobile, e := nyali.Phone(c.Mobile)
	if e != nil || mobile != canonical {
		return nyali.Customer{}, errors.New("Nyali caller ownership mismatch")
	}
	if memo != nil {
		memo.loaded = true
		memo.phone = canonical
		memo.customer = c
		memo.err = nil
	}
	return c, nil
}
func nyaliPayable(l nyali.Loan) (bool, error) {
	if l.Status != "disbursed" {
		return false, nil
	}
	n, e := nyali.Cents(l.Outstanding.Total.String())
	if e != nil {
		return false, e
	}
	return n > 0, nil
}
func (p *NyaliPayments) Welcome(ctx context.Context, phone string) (string, error) {
	c, e := p.customer(ctx, phone, false)
	if e == nil {
		if name := strings.Fields(c.Name); len(name) > 0 {
			return name[0], nil
		}
	}
	if p.Fallback != nil {
		return p.Fallback.Welcome(ctx, phone)
	}
	return "", e
}
func (p *NyaliPayments) HasPayable(ctx context.Context, phone string) (bool, error) {
	c, e := p.customer(ctx, phone, false)
	if errors.Is(e, errNyaliNoMatch) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	for _, l := range c.Loans {
		ok, e := nyaliPayable(l)
		if e != nil {
			return false, e
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}
func (p *NyaliPayments) Ready(context.Context) bool {
	return p.Store.Healthy() && time.Now().Unix() >= p.disabledUntil.Load()
}

type NyaliLookupError struct{ message string }

func (e *NyaliLookupError) Error() string        { return e.message }
func (e *NyaliLookupError) CustomerText() string { return e.message }
func (p *NyaliPayments) Loans(ctx context.Context, phone string, history bool) ([]PayLoan, error) {
	c, e := p.customer(ctx, phone, false)
	if errors.Is(e, errNyaliNoMatch) {
		if history {
			for _, r := range p.Store.Snapshot() {
				if r.BorrowerHash == nyali.Caller(phone) {
					return nil, errors.New("Nyali account needs review")
				}
			}
		}
		return nil, &NyaliLookupError{"No loan account matches your registered number. Please get assistance."}
	}
	if e != nil {
		return nil, e
	}
	result := []PayLoan{}
	if history {
		seen := map[int64]bool{}
		for _, r := range p.Store.Snapshot() {
			if r.BorrowerHash == nyali.Caller(phone) && r.ClientID == c.ID && !seen[r.LoanID] {
				seen[r.LoanID] = true
				result = append(result, PayLoan{ID: r.LoanID})
			}
		}
	} else {
		for _, l := range c.Loans {
			ok, e := nyaliPayable(l)
			if e != nil {
				return nil, e
			}
			if !ok {
				continue
			}
			if l.ID <= 0 {
				return nil, errors.New("invalid Nyali loan")
			}
			total, _ := nyali.Amount(l.Outstanding.Total.String())
			item := PayLoan{ID: l.ID, Outstanding: total}
			if a, e := nyali.Amount(l.Outstanding.Arrears.String()); e == nil {
				item.Due = a
				item.DueLabel = "Due"
			} else if l.Next != nil {
				if a, e := nyali.Amount(l.Next.TotalDue.String()); e == nil {
					item.Due = a
					item.DueLabel = "Next"
				}
			}
			result = append(result, item)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
func nyaliStatus(r nyali.Entry) string {
	switch r.Status {
	case "Successful":
		if r.Posted {
			return "completed"
		}
		return "posting_pending"
	case "Failed":
		return "failed"
	case "Pending":
		return "pending"
	default:
		return "processing"
	}
}
func (p *NyaliPayments) Collect(ctx context.Context, phone string, id int64, amount, key string) (string, error) {
	return p.CollectFrom(ctx, phone, phone, id, amount, key)
}
func (p *NyaliPayments) CollectFrom(ctx context.Context, phone, payer string, id int64, amount, key string) (string, error) {
	canonical, e := nyali.Phone(phone)
	if e != nil {
		return "unavailable", nil
	}
	payer, e = nyali.Phone(payer)
	if e != nil {
		return "unavailable", nil
	}
	a, e := nyali.Amount(amount)
	if e != nil || len(key) != 64 {
		return "unavailable", nil
	}
	// Verify the trusted caller and current ownership/balance before any charge.
	c, e := p.customer(ctx, canonical, true)
	if e != nil {
		return "unavailable", nil
	}
	// A retry belongs to its original operation even after repayment closes the
	// loan. Revalidate the customer, then compare all immutable intent fields.
	if old, ok := p.Store.Lookup("USSD-" + key); ok {
		if old.BorrowerHash != nyali.Caller(canonical) || old.ClientID != c.ID || old.LoanID != id || old.Payer != payer || old.Amount != a {
			return "unavailable", nil
		}
		return nyaliStatus(old), nil
	}
	if !p.Ready(ctx) {
		return "unavailable", nil
	}
	found := false
	for _, l := range c.Loans {
		if l.ID == id {
			ok, e := nyaliPayable(l)
			if e != nil || !ok {
				return "balance_changed", nil
			}
			total, e := nyali.Cents(l.Outstanding.Total.String())
			requested, _ := nyali.Cents(a)
			if e != nil || requested > total {
				return "balance_changed", nil
			}
			found = true
			break
		}
	}
	if !found {
		return "balance_changed", nil
	}
	r, fresh, e := p.Store.Reserve(nyali.Entry{Ref: "USSD-" + key, BorrowerHash: nyali.Caller(canonical), ClientID: c.ID, LoanID: id, Payer: payer, Amount: a})
	if e != nil {
		return "unavailable", nil
	}
	if !fresh {
		if r.BorrowerHash != nyali.Caller(canonical) {
			return "existing_pending", nil
		}
		return nyaliStatus(r), nil
	}
	result, e := p.Client.Collect(ctx, id, payer, a, r.Ref)
	if e != nil {
		r.Status = "Unknown"
		r.ErrorCode = "INITIATION_UNCONFIRMED"
		var api *nyali.APIError
		if errors.As(e, &api) && !api.Uncertain {
			r.Status = "Failed"
			r.ErrorCode = fmt.Sprintf("NOT_SUBMITTED_HTTP_%d", api.Status)
			if api.Status == 503 {
				p.disabledUntil.Store(time.Now().Add(time.Minute).Unix())
			}
		}
		if se := p.Store.Update(r); se != nil {
			return "processing", nil
		}
		if r.Status == "Failed" {
			return "unavailable", nil
		}
		return "processing", nil
	}
	r.Status = result.Status
	r.Posted = result.Posted
	r.ProviderReference = result.Reference
	if e = p.Store.Update(r); e != nil {
		return "processing", nil
	}
	return nyaliStatus(r), nil
}

var nyaliZone = time.FixedZone("CAT", 2*60*60)

func (p *NyaliPayments) reconcile(ctx context.Context, day string) error {
	var response nyali.Payments
	var e error
	from, to := day, day
	for _, r := range p.Store.Snapshot() {
		local := r.Created.In(nyaliZone)
		if !r.Open() || local.Format("2006-01-02") != day {
			continue
		}
		if local.Hour() == 0 && local.Minute() < 3 {
			from = local.AddDate(0, 0, -1).Format("2006-01-02")
		}
		if local.Hour() == 23 && local.Minute() >= 57 {
			to = local.AddDate(0, 0, 1).Format("2006-01-02")
		}
	}
	if ranged, ok := p.Client.(interface {
		PaymentsRange(context.Context, string, string) (nyali.Payments, error)
	}); ok && (from != to) {
		response, e = ranged.PaymentsRange(ctx, from, to)
	} else {
		response, e = p.Client.Payments(ctx, day)
	}
	if e != nil {
		return e
	}
	records := p.Store.Snapshot()
	byRef := map[string]nyali.Payment{}
	for _, payment := range response.Payments {
		if _, exists := byRef[payment.External]; exists {
			return errors.New("duplicate Nyali external reference")
		}
		byRef[payment.External] = payment
	}
	for _, r := range records {
		if r.Created.In(nyaliZone).Format("2006-01-02") != day || !r.Open() {
			continue
		}
		payment, ok := byRef[r.Ref]
		if !ok {
			continue
		} // never infer failure from a capped/absent list entry
		amount, ae := nyali.Amount(payment.Amount.String())
		payer, pe := nyali.Phone(payment.MSISDN)
		match := ae == nil && pe == nil && payment.Reference != "" && payment.LoanID == r.LoanID && amount == r.Amount && payer == r.Payer && payment.Currency == "ZMW" && (r.ProviderReference == "" || r.ProviderReference == payment.Reference)
		if !match {
			r.ErrorCode = "PROVIDER_DETAILS_MISMATCH"
			if e = p.Store.Update(r); e != nil {
				return e
			}
			continue
		}
		switch payment.Status {
		case "Pending", "Submitting", "Unknown", "Failed", "Successful":
		default:
			continue
		}
		if payment.Posted && payment.Status != "Successful" {
			continue
		}
		r.Status = payment.Status
		r.Posted = payment.Posted
		r.ProviderReference = payment.Reference
		r.ErrorCode = ""
		if e = p.Store.Update(r); e != nil {
			return e
		}
	}
	if len(response.Payments) >= 500 || response.Count > len(response.Payments) {
		log.Printf("Nyali payment listing reached its window limit; unmatched attempts remain unresolved")
	}
	return nil
}
func (p *NyaliPayments) Status(ctx context.Context, phone string, id int64) (string, error) {
	c, e := p.customer(ctx, phone, false)
	if e != nil {
		return "", e
	}
	r, ok := p.Store.Latest(phone, id)
	if !ok {
		return "none", nil
	}
	if r.ClientID != c.ID {
		return "", errors.New("Nyali account mismatch")
	}
	if r.Open() {
		if e = p.reconcile(ctx, r.Created.In(nyaliZone).Format("2006-01-02")); e != nil {
			return "processing", nil
		}
		r, _ = p.Store.Latest(phone, id)
	}
	return nyaliStatus(r), nil
}
func (p *NyaliPayments) Reference(phone string, id int64) string {
	r, ok := p.Store.Latest(phone, id)
	if !ok {
		return ""
	}
	return r.ShortRef()
}

// Run batches status reads per day, leaving all unknown outcomes reserved. No
// background collection POST is performed, including after process restarts.
func (p *NyaliPayments) Run(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	last := map[string]time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			days := map[string]bool{}
			aged := 0
			for _, r := range p.Store.Snapshot() {
				if !r.Open() {
					continue
				}
				day := r.Created.In(nyaliZone).Format("2006-01-02")
				days[day] = true
				if time.Since(r.Created) > 3*time.Minute {
					aged++
				}
			}
			if aged > 0 {
				log.Printf("Nyali reconciliation requires follow-up: aged_attempts=%d", aged)
			}
			ordered := make([]string, 0, len(days))
			for day := range days {
				ordered = append(ordered, day)
			}
			sort.Slice(ordered, func(i, j int) bool {
				if last[ordered[i]].Equal(last[ordered[j]]) {
					return ordered[i] < ordered[j]
				}
				return last[ordered[i]].Before(last[ordered[j]])
			})
			for _, day := range ordered {
				if time.Since(last[day]) < 15*time.Second {
					continue
				}
				c, cancel := context.WithTimeout(ctx, 4*time.Second)
				e := p.reconcile(c, day)
				cancel()
				last[day] = time.Now()
				if e != nil {
					log.Printf("Nyali status reconciliation deferred; attempts remain reserved")
				}
				break
			}
		}
	}
}

func (p *NyaliPayments) BackendName() string { return "nyali" }
func (p *NyaliPayments) NormalizeAmount(value string) (string, bool) {
	a, e := nyali.Amount(value)
	return a, e == nil
}
