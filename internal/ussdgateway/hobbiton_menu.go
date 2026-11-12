package ussdgateway

import (
	"context"
	"fmt"
	"strings"
)

func welcomeName(name string) string {
	var b strings.Builder
	for _, c := range strings.TrimSpace(name) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '\'' || c == '-' || c == ' ' {
			b.WriteRune(c)
		}
		if b.Len() >= 20 {
			break
		}
	}
	return strings.TrimSpace(b.String())
}
func hobbyHome(current *session) string {
	if current.leadEnabled {
		if current.leadRecord.HasLoan {
			return "CON " + current.hobbyWelcome + "\n1 Repay your loan\n2 Payment status\n3 Get assistance\n0 Exit"
		}
		if current.leadRecord.ApplicationReference != "" {
			return "CON " + current.hobbyWelcome + "\n1 Check application\n2 Repay a loan\n3 Get assistance\n0 Exit"
		}
		label := "Apply for a loan"
		if current.leadRecord.Reference != "" {
			label = "Continue loan enquiry"
		}
		return "CON " + current.hobbyWelcome + "\n1 " + label + "\n2 Repay a loan\n3 Get assistance\n4 Payment status\n0 Exit"
	}
	text := "CON " + current.hobbyWelcome + "\n1 Loan products"
	if current.hobbyHasPayments {
		text += "\n3 Make payment\n4 Payment status"
	}
	return text + "\n0 Exit"
}
func hobbyCatalogue(current *session) string {
	text := "CON Loan products"
	start := current.hobbyPage * 3
	for i := start; i < len(current.hobbyNames) && i < start+3; i++ {
		name := current.hobbyNames[i]
		for phoneUnits(name) > 20 {
			name = name[:len(name)-1]
		}
		text += fmt.Sprintf("\n%d %s", i-start+1, name)
	}
	if start+3 < len(current.hobbyNames) {
		text += "\n4 More"
	}
	if start > 0 {
		text += "\n5 Previous page"
	}
	return text + "\n* Previous\n# Main"
}
func (s *Server) hobbitonMenu(ctx context.Context, current *session, phone, input string) string {
	if current.last == "" {
		current.hobbyWelcome = "Welcome to MightyFin"
		current.hobbyHasPayments = s.Payments != nil
		current.leadEnabled = s.Leads != nil
		if s.Leads != nil {
			if r, e := s.leadRequest(ctx, current, phone, "current", nil); e == nil {
				current.leadRecord = r
			}
		}
		if reader, ok := s.Payments.(interface {
			Welcome(context.Context, string) (string, error)
		}); ok {
			if name, err := reader.Welcome(ctx, phone); err == nil {
				if name = welcomeName(name); name != "" {
					current.hobbyWelcome = "Welcome " + name
				}
			}
		}
		current.last = "home"
		return hobbyHome(current)
	}
	if current.last == "application-existing" {
		if input == "#" || input == "*" {
			current.last = "home"
			return hobbyHome(current)
		}
		if input == "1" {
			current.last = "assistance-consent"
			return "CON May our team contact you on this number?\n2 Request assistance\n0 Cancel\n# Main"
		}
		return "CON Choose 1 for assistance or # for Main."
	}
	if strings.HasPrefix(current.last, "lead-") || current.last == "assistance-consent" {
		return s.leadMenu(ctx, current, phone, input)
	}
	if strings.HasPrefix(current.last, "payment-contact") {
		return s.paymentContact(ctx, current, phone, input)
	}
	if input == "0" {
		if current.last == "payment" {
			return "END Payment cancelled. No new request was submitted."
		}
		return "END Thank you for choosing MightyFin."
	}
	if current.last == "payment" && input != "#" {
		return s.hobbitonPayment(ctx, current, phone, input)
	}
	if input == "#" || (input == "*" && current.last != "product") {
		current.hobbyPaymentPath = ""
		current.last = "home"
		return hobbyHome(current)
	}
	if input == "*" && current.last == "product" {
		current.last = "products"
		return hobbyCatalogue(current)
	}
	switch current.last {
	case "home":
		if s.Leads != nil {
			if current.leadRecord.HasLoan && input != "3" {
				if input == "1" {
					input = "2"
				} else if input == "2" {
					input = "4"
				} else if input != "4" {
					return "CON Please choose an option from the menu.\n# Main"
				}
			}
			if !current.leadRecord.HasLoan && current.leadRecord.ApplicationReference != "" && input == "1" {
				current.last = "application-existing"
				return "CON Application " + current.leadRecord.ApplicationReference + " is already in LMS.\n1 Request assistance to continue\n# Main"
			}
			if input == "1" {
				current.last = "lead-consent"
				return "CON May MightyFin record your loan enquiry and contact you?\n2 Continue\n0 Cancel\n# Main"
			}
			if input == "3" {
				current.last = "assistance-consent"
				return "CON May our team contact you on this number?\n2 Request assistance\n0 Cancel\n# Main"
			}
			if input == "2" {
				input = "3"
			} else if input != "4" {
				return "CON Please choose an option from the menu.\n# Main"
			}
		}
		if s.Payments != nil && (input == "3" || input == "4") {
			current.hobbyPaymentPath = input
			current.payPage = 0
			if input == "3" {
				current.last = "payment-contact"
				current.hobbyPayerPhone = phone
				return paymentContactScreen(current, phone)
			}
			current.last = "payment"
			return s.hobbitonLoanScreen(ctx, current, phone)
		}
		if input != "1" {
			return "CON Please choose an option from the menu.\n# Main"
		}
		data, err := s.Reader.Read(ctx, "/v1/products", "")
		if err != nil {
			return "CON Loan products are unavailable.\n* Previous\n# Main"
		}
		names, err := productNames(data)
		if err != nil || len(names) == 0 {
			return "CON Please contact your loan officer for available products.\n* Previous\n# Main"
		}
		current.hobbyNames = names
		current.hobbyPage = 0
		current.last = "products"
		return hobbyCatalogue(current)
	case "products":
		start := current.hobbyPage * 3
		if input == "4" && start+3 < len(current.hobbyNames) {
			current.hobbyPage++
			return hobbyCatalogue(current)
		}
		if input == "5" && start > 0 {
			current.hobbyPage--
			return hobbyCatalogue(current)
		}
		if len(input) == 1 && input[0] >= '1' && input[0] <= '3' {
			i := start + int(input[0]-'1')
			if i < len(current.hobbyNames) {
				current.last = "product"
				return "CON " + current.hobbyNames[i] + "\nSubject to eligibility.\nApply through your loan officer.\n* Previous\n# Main"
			}
		}
		return hobbyCatalogue(current)
	case "product":
		return "CON Select * for the previous menu or # for the main menu."
	}
	current.last = "home"
	return hobbyHome(current)
}

func paymentNavigation(text string) string {
	if strings.HasPrefix(text, "CON ") {
		text = strings.ReplaceAll(text, "\n0 Cancel", "") + "\n* Previous\n# Main"
	}
	return text
}
func (s *Server) hobbitonPayment(ctx context.Context, current *session, phone, input string) string {
	parts := strings.Split(current.hobbyPaymentPath, "*")
	if len(parts) == 1 && input != "*" {
		start := current.payPage * 3
		if input == "4" && start+3 < len(current.payLoans) {
			current.payPage++
			return repaymentLoanList(current)
		}
		if input == "5" && start > 0 {
			current.payPage--
			return repaymentLoanList(current)
		}
		if len(input) != 1 || input[0] < '1' || input[0] > '3' || start+int(input[0]-'1') >= len(current.payLoans) {
			return repaymentLoanList(current)
		}
		input = fmt.Sprint(start + int(input[0]-'0'))
	}
	if input == "*" {
		if len(parts) == 1 {
			if current.hobbyPaymentPath == "3" && current.hobbyPayerPhone != "" {
				current.last = "payment-contact"
				return paymentContactScreen(current, phone)
			}
			current.last = "home"
			current.hobbyPaymentPath = ""
			return hobbyHome(current)
		}
		current.hobbyPaymentPath = strings.Join(parts[:len(parts)-1], "*")
	} else {
		// A repeated one-digit amount must never be mistaken for payment consent.
		if len(parts) == 3 && parts[0] == "3" {
			confirm := "1"
			if parts[2] == "1" {
				confirm = "2"
			}
			if input != confirm {
				return "END Payment not confirmed. No request was submitted."
			}
			input = "1"
		}
		current.hobbyPaymentPath += "*" + input
	}
	response := s.hobbitonLoanScreen(ctx, current, phone)
	parts = strings.Split(current.hobbyPaymentPath, "*")
	if len(parts) == 3 && parts[0] == "3" && parts[2] == "1" {
		response = strings.Replace(response, "1 Confirm", "2 Confirm", 1)
	}
	return response
}
