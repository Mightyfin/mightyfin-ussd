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
	text := "CON " + current.hobbyWelcome + "\nMightyFin\n1 Loan products"
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
		current.hobbyWelcome = "Welcome"
		current.hobbyHasPayments = s.Payments != nil
		if reader, ok := s.Payments.(interface {
			Welcome(context.Context, string) (string, error)
		}); ok {
			if name, err := reader.Welcome(ctx, phone); err == nil {
				if name = welcomeName(name); name != "" {
					current.hobbyWelcome += " " + name
				}
			}
		}
		current.last = "home"
		return hobbyHome(current)
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
		if s.Payments != nil && (input == "3" || input == "4") {
			current.last = "payment"
			current.hobbyPaymentPath = input
			return paymentNavigation(s.paymentScreen(ctx, current, input, phone, current.hobbySessionID))
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
	if input == "*" {
		if len(parts) == 1 {
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
	response := s.paymentScreen(ctx, current, current.hobbyPaymentPath, phone, current.hobbySessionID)
	parts = strings.Split(current.hobbyPaymentPath, "*")
	if len(parts) == 3 && parts[0] == "3" && parts[2] == "1" {
		response = strings.Replace(response, "1 Confirm", "2 Confirm", 1)
	}
	return paymentNavigation(response)
}
