package lmsbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
)

var decimalBalance = regexp.MustCompile(`^[0-9]{1,18}(\.[0-9]{1,8})?$`)

type NextAction struct {
	Code                 string `json:"code"`
	Label                string `json:"label"`
	Enabled              bool   `json:"enabled"`
	ConfirmationRequired bool   `json:"confirmation_required"`
}
type Journey struct {
	State            string          `json:"state"`
	Message          string          `json:"message"`
	Version          string          `json:"version"`
	ApplicationID    json.RawMessage `json:"application_id,omitempty"`
	MissingProfile   []string        `json:"missing_profile_fields"`
	MissingDocuments []string        `json:"missing_documents"`
	Action           NextAction      `json:"next_action"`
}
type applicationSnapshot struct {
	Status      string `json:"status"`
	Application *struct {
		ID     json.RawMessage `json:"id"`
		Status string          `json:"status"`
	} `json:"application"`
	Completion *struct {
		Complete         bool     `json:"is_complete"`
		MissingProfile   []string `json:"missing_profile_fields"`
		MissingDocuments []string `json:"missing_documents"`
	} `json:"application_completion"`
}
type loanSnapshot struct {
	Data []struct {
		Status    string          `json:"status"`
		Remaining json.RawMessage `json:"amountRemaining"`
	} `json:"data"`
	Meta struct {
		LastPage int `json:"last_page"`
	} `json:"meta"`
	Links struct {
		Next *string `json:"next"`
	} `json:"links"`
}

// Journey only reads documented customer-scoped resources. Never call KYC status:
// the deployed legacy GET route mutates draft completion state.
func (s *Server) Journey(ctx context.Context, token string) (Result, error) {
	app, e := s.Client.Call(ctx, "GET", "/loan-applications/current", token, nil)
	if e != nil {
		return Result{}, e
	}
	if app.Status != 200 && app.Status != 404 {
		return app, nil
	}
	if app.Status == 404 {
		var empty struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(app.Body, &empty) != nil || empty.Status != "empty" {
			return Result{}, errors.New("unexpected application response")
		}
	}
	loans, e := s.Client.Call(ctx, "GET", "/loans?perPage=100", token, nil)
	if e != nil {
		return Result{}, e
	}
	if loans.Status != 200 {
		return loans, nil
	}
	journey, e := planJourney(app, loans, s.Writes)
	if e != nil {
		return Result{}, e
	}
	b, e := json.Marshal(journey)
	return Result{200, b}, e
}
func planJourney(app, loans Result, writes bool) (Journey, error) {
	j := Journey{State: "review_status", Message: "Please review your application status.", MissingProfile: []string{}, MissingDocuments: []string{}, Action: NextAction{Code: "view_status", Label: "Check status", Enabled: true}}
	hash := sha256.Sum256(app.Body)
	j.Version = hex.EncodeToString(hash[:])
	var a applicationSnapshot
	if app.Status == 200 {
		if json.Unmarshal(app.Body, &a) != nil || a.Application == nil {
			return j, errors.New("application contract unavailable")
		}
		j.ApplicationID = a.Application.ID
		switch a.Application.Status {
		case "draft":
			j.State = "draft"
			j.Message = "Continue your saved application."
			j.Action = NextAction{Code: "continue_application", Label: "Continue application", Enabled: writes}
			if a.Completion != nil {
				j.MissingProfile = append(j.MissingProfile, a.Completion.MissingProfile...)
				j.MissingDocuments = append(j.MissingDocuments, a.Completion.MissingDocuments...)
				if len(j.MissingProfile) == 0 && len(j.MissingDocuments) > 0 {
					j.State = "documents_required"
					j.Message = "Documents are needed. Continue through the existing customer portal or an officer."
					j.Action = NextAction{Code: "document_handoff", Label: "Complete documents", Enabled: false}
				}
				if a.Completion.Complete && a.Completion.MissingProfile != nil && a.Completion.MissingDocuments != nil && len(j.MissingProfile) == 0 && len(j.MissingDocuments) == 0 {
					j.State = "ready_for_submission"
					j.Message = "Your application is ready for review and confirmation."
					j.Action = NextAction{Code: "review_submission", Label: "Review application", Enabled: writes, ConfirmationRequired: true}
				}
			}
			return j, nil
		case "pending":
			j.State = "under_review"
			j.Message = "Your application is being reviewed. Check here for updates."
			return j, nil
		case "approved":
			j.State = "approved"
			j.Message = "LMS has approved the application. Check the customer portal or your officer for the next step."
			return j, nil
		case "rejected":
			j.State = "rejected"
			j.Message = "Check the customer portal or your officer for the decision and available next steps."
			return j, nil
		case "closed": // Look for another active loan before offering a new application.
		default:
			return j, nil
		}
	}
	var l loanSnapshot
	if json.Unmarshal(loans.Body, &l) != nil || l.Data == nil {
		return j, errors.New("loan contract unavailable")
	}
	for _, loan := range l.Data {
		switch loan.Status {
		case "active", "overdue", "arrears", "written_off", "disbursed":
			amount, ok := positiveAmount(loan.Remaining)
			if !ok {
				j.State = "account_review"
				j.Message = "Check your loan details before continuing."
				j.Action = NextAction{Code: "view_loans", Label: "View loans", Enabled: true}
				return j, nil
			}
			if amount {
				j.State = "loan_active"
				j.Message = "View your outstanding loan and repayment information."
				j.Action = NextAction{Code: "view_loans", Label: "View loan and repayment details", Enabled: true}
				return j, nil
			}
		case "closed", "repaid", "rejected", "cancelled":
		default:
			j.State = "account_review"
			j.Message = "Check your existing applications and loans."
			j.Action = NextAction{Code: "view_loans", Label: "View loans", Enabled: true}
			return j, nil
		}
	}
	if l.Meta.LastPage > 1 || l.Links.Next != nil {
		j.State = "account_review"
		j.Message = "Review your existing loans before starting another application."
		j.Action = NextAction{Code: "view_loans", Label: "View loans", Enabled: true}
		return j, nil
	}
	j.State = "no_active_journey"
	j.Message = "View available loan products. Eligibility is checked by LMS."
	j.Action = NextAction{Code: "view_products", Label: "View loan products", Enabled: true}
	return j, nil
}
func positiveAmount(raw json.RawMessage) (bool, bool) {
	value := string(raw)
	if len(raw) > 0 && raw[0] == '"' {
		if e := json.Unmarshal(raw, &value); e != nil {
			return false, false
		}
	}
	if !decimalBalance.MatchString(value) {
		return false, false
	}
	// No float conversions or financial calculations in the channel.
	v, ok := new(big.Rat).SetString(value)
	if !ok || v.Sign() < 0 {
		return false, false
	}
	return v.Sign() > 0, true
}

// Verify that a submission still refers to the current, complete LMS draft.
func (s *Server) confirmSubmission(ctx context.Context, token, path, version string) (Result, error) {
	r, e := s.Client.Call(ctx, "GET", "/loan-applications/current", token, nil)
	if e != nil {
		return Result{}, e
	}
	if r.Status != 200 {
		return r, nil
	}
	snapshot := sha256.Sum256(r.Body)
	if version != hex.EncodeToString(snapshot[:]) {
		return Result{409, json.RawMessage(`{"error":"APPLICATION_CHANGED","message":"Review the latest application before confirming."}`)}, nil
	}
	var a applicationSnapshot
	if json.Unmarshal(r.Body, &a) != nil || a.Application == nil {
		return Result{}, errors.New("application contract unavailable")
	}
	var id string
	if json.Unmarshal(a.Application.ID, &id) != nil {
		var n json.Number
		if json.Unmarshal(a.Application.ID, &n) == nil {
			id = n.String()
		}
	}
	expected := "/loan-applications/" + id + "/submit"
	if id == "" || path != expected || a.Application.Status != "draft" {
		return Result{409, json.RawMessage(`{"error":"APPLICATION_CHANGED","message":"Refresh the application before confirming."}`)}, nil
	}
	if a.Completion == nil || !a.Completion.Complete || a.Completion.MissingProfile == nil || a.Completion.MissingDocuments == nil || len(a.Completion.MissingProfile) > 0 || len(a.Completion.MissingDocuments) > 0 {
		return Result{422, json.RawMessage(`{"error":"APPLICATION_INCOMPLETE","message":"Complete the LMS requirements before submitting."}`)}, nil
	}
	return Result{}, nil
}
