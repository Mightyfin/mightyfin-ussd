package ussdgateway

import (
	"context"
	"strconv"
	"strings"
)

func normalizePaymentPhone(value string) string {
	phone := strings.TrimPrefix(strings.TrimSpace(value), "+")
	if len(phone) == 10 && phone[0] == '0' {
		phone = "260" + phone[1:]
	}
	if phoneNumber.MatchString(phone) {
		return phone
	}
	return ""
}
func paymentContactScreen(c *session, caller string) string {
	name := "Repay loan"
	if c.hobbyPayerPhone == caller && strings.HasPrefix(c.hobbyWelcome, "Welcome ") && c.hobbyWelcome != "Welcome to MightyFin" {
		name = c.hobbyWelcome
	}
	return "CON " + name + "\nPayment number +" + c.hobbyPayerPhone + "\n1 Confirm\n2 Enter phone number\n* Previous\n# Main"
}
func (s *Server) paymentContact(ctx context.Context, c *session, caller, input string) string {
	if input == "#" || input == "0" || (input == "*" && c.last == "payment-contact") {
		c.last = "home"
		return hobbyHome(c)
	}
	if c.last == "payment-contact-number" {
		if input == "*" {
			c.last = "payment-contact"
			return paymentContactScreen(c, caller)
		}
		phone := normalizePaymentPhone(input)
		if phone == "" {
			return "CON Enter a valid mobile-money number, e.g. 0970000001.\n* Previous\n# Main"
		}
		c.hobbyPayerPhone = phone
		c.last = "payment-contact"
		return paymentContactScreen(c, caller)
	}
	if input == "2" {
		c.last = "payment-contact-number"
		return "CON Enter the mobile-money number to collect payment from.\n* Previous\n# Main"
	}
	if input != "1" {
		return paymentContactScreen(c, caller)
	}
	c.last = "payment"
	c.hobbyPaymentPath = "3"
	c.payPage = 0
	c.payLoans = nil
	return s.hobbitonLoanScreen(ctx, c, caller)
}
func maskedLoan(id int64) string {
	label := strconv.FormatInt(id, 10)
	if len(label) > 6 {
		label = label[len(label)-6:]
	}
	return label
}
func repaymentLoanList(c *session) string {
	text := "CON Select loan"
	for i := c.payPage * 3; i < len(c.payLoans) && i < c.payPage*3+3; i++ {
		loan := c.payLoans[i]
		text += "\n" + strconv.Itoa(i-c.payPage*3+1) + " ..." + maskedLoan(loan.ID)
		if amount, ok := amountString(loan.Due); ok {
			text += " Due K" + amount
		}
	}
	if (c.payPage+1)*3 < len(c.payLoans) {
		text += "\n4 More"
	}
	if c.payPage > 0 {
		text += "\n5 Previous page"
	}
	return text + "\n* Previous\n# Main"
}
func (s *Server) hobbitonLoanScreen(ctx context.Context, c *session, caller string) string {
	if !strings.Contains(c.hobbyPaymentPath, "*") {
		loans, err := s.Payments.Loans(ctx, caller, c.hobbyPaymentPath == "4")
		if err != nil || len(loans) > 30 {
			return "END Payment options are unavailable. Please contact your loan officer."
		}
		if len(loans) == 0 {
			if c.hobbyPaymentPath == "4" {
				return "END No payment request was found for your loans on this channel."
			}
			return "END You have no pending loan to pay back."
		}
		for _, loan := range loans {
			if loan.ID <= 0 {
				return "END Payment options are unavailable."
			}
		}
		c.payLoans = loans
		return repaymentLoanList(c)
	}
	text := s.paymentScreen(ctx, c, c.hobbyPaymentPath, caller, c.hobbySessionID)
	return paymentNavigation(text)
}
