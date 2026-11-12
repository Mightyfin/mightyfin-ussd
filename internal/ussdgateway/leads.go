package ussdgateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

type LeadProduct struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}
type LeadRecord struct {
	ApplicationReference string        `json:"application_reference"`
	Reference            string        `json:"reference"`
	Editable             bool          `json:"editable"`
	ProductID            int64         `json:"product_id"`
	Amount               *string       `json:"amount"`
	Name                 string        `json:"name"`
	NextStep             string        `json:"next_step"`
	HasLoan              bool          `json:"has_loan"`
	Products             []LeadProduct `json:"products"`
}
type LeadService interface {
	Request(context.Context, string, string, string, map[string]any) (LeadRecord, error)
}
type LeadClient struct{ PaymentClient }

func (p *LeadClient) Request(c context.Context, phone, id, action string, fields map[string]any) (LeadRecord, error) {
	data := map[string]any{"phone": phone, "action": action}
	for k, v := range fields {
		data[k] = v
	}
	raw, _ := json.Marshal(data)
	sum := sha256.Sum256(append([]byte("ussd-lead-v1\x00"+id+"\x00"), raw...))
	data["request_id"] = hex.EncodeToString(sum[:])
	b, e := p.call(c, "/v1/leads", data)
	if e != nil {
		return LeadRecord{}, e
	}
	var r LeadRecord
	e = json.Unmarshal(b, &r)
	return r, e
}

var leadName = regexp.MustCompile(`^[\pL\pM][\pL\pM .'-]{2,79}$`)

func (s *Server) leadRequest(ctx context.Context, c *session, phone, action string, fields map[string]any) (LeadRecord, error) {
	if action == "save" || action == "submit" {
		copy := make(map[string]any)
		for k, v := range fields {
			copy[k] = v
		}
		copy["lead_ref"] = c.leadRecord.Reference
		fields = copy
	}
	return s.Leads.Request(ctx, phone, c.hobbySessionID, action, fields)
}
func leadProductsScreen(c *session) string {
	result := "CON Choose loan product"
	start := c.leadPage * 3
	for i := start; i < len(c.leadProducts) && i < start+3; i++ {
		name := asciiLabel(c.leadProducts[i].Name, 20)
		result += fmt.Sprintf("\n%d %s", i-start+1, name)
	}
	if start+3 < len(c.leadProducts) {
		result += "\n4 More"
	}
	if start > 0 {
		result += "\n5 Previous page"
	}
	return result + "\n* Previous\n# Main"
}
func asciiLabel(text string, limit int) string {
	var b strings.Builder
	for _, c := range text {
		if c >= 32 && c <= 126 && c != '`' {
			b.WriteRune(c)
		}
	}
	text = b.String()
	for phoneUnits(text) > limit {
		text = text[:len(text)-1]
	}
	return text
}
func leadReview(c *session) string {
	amount := "Officer to advise"
	if c.leadRecord.Amount != nil {
		amount = "K" + *c.leadRecord.Amount
	}
	return "CON Loan enquiry\n" + asciiLabel(c.leadProductName, 23) + "\nRequested: " + amount + "\n1 Send for follow-up\n2 Edit\n* Previous\n# Main"
}
func (s *Server) leadMenu(ctx context.Context, c *session, phone, input string) string {
	call := func(action string, data map[string]any) (LeadRecord, error) {
		return s.leadRequest(ctx, c, phone, action, data)
	}
	if input == "#" {
		c.last = "home"
		return hobbyHome(c)
	}
	switch c.last {
	case "lead-consent":
		if input == "*" || input == "0" {
			c.last = "home"
			return hobbyHome(c)
		}
		if input != "2" {
			return "CON May MightyFin record your enquiry and contact you?\n2 Continue\n0 Cancel\n# Main"
		}
		r, e := call("start", map[string]any{"consent": true})
		if e != nil {
			return "CON Could not confirm your enquiry. Please retry 2.\n# Main"
		}
		c.leadRecord = r
		if !r.Editable {
			return "END Your enquiry " + r.Reference + " is already with the team. Please contact your loan officer to continue."
		}
		products, e := call("catalogue", nil)
		if e != nil || len(products.Products) == 0 {
			return "END Enquiry " + r.Reference + " recorded. A loan officer can help choose a product."
		}
		c.leadProducts = products.Products
		c.leadPage = 0
		if r.NextStep == "recorded" {
			return "END Enquiry " + r.Reference + " is recorded for follow-up. A loan officer can help complete it."
		}
		for _, p := range products.Products {
			if p.ID == r.ProductID {
				c.leadProductName = p.Name
				switch r.NextStep {
				case "amount":
					c.last = "lead-amount"
					return "CON Requested amount in ZMW?\n0 Officer to advise\n* Previous\n# Main"
				case "name":
					c.last = "lead-name"
					return "CON Your full name?\n0 Skip for now\n* Previous\n# Main"
				case "review":
					c.last = "lead-review"
					return leadReview(c)
				}
			}
		}
		c.last = "lead-products"
		return leadProductsScreen(c)
	case "lead-products":
		if input == "*" {
			c.last = "home"
			return hobbyHome(c)
		}
		start := c.leadPage * 3
		if input == "4" && start+3 < len(c.leadProducts) {
			c.leadPage++
			return leadProductsScreen(c)
		}
		if input == "5" && start > 0 {
			c.leadPage--
			return leadProductsScreen(c)
		}
		if len(input) != 1 || input[0] < '1' || input[0] > '3' {
			return leadProductsScreen(c)
		}
		i := start + int(input[0]-'1')
		if i >= len(c.leadProducts) {
			return leadProductsScreen(c)
		}
		p := c.leadProducts[i]
		r, e := call("save", map[string]any{"product_id": p.ID})
		if e != nil {
			return "CON Product could not be saved. Retry your selection.\n# Main"
		}
		c.leadRecord = r
		c.leadProductName = p.Name
		c.last = "lead-amount"
		return "CON Requested amount in ZMW?\n0 Let an officer advise\n* Previous\n# Main"
	case "lead-amount":
		if input == "*" {
			c.last = "lead-products"
			return leadProductsScreen(c)
		}
		var amount any
		if input != "0" {
			a, ok := amountString(input)
			if !ok {
				return "CON Enter a positive amount, e.g. 500.00.\n0 Officer to advise\n* Previous\n# Main"
			}
			amount = a
		}
		r, e := call("save", map[string]any{"amount": amount})
		if e != nil {
			return "CON Amount could not be saved. Retry it.\n# Main"
		}
		c.leadRecord = r
		c.last = "lead-name"
		return "CON Your full name?\n0 Skip for now\n* Previous\n# Main"
	case "lead-name":
		if input == "*" {
			c.last = "lead-amount"
			return "CON Requested amount in ZMW?\n0 Officer to advise\n* Previous\n# Main"
		}
		var name any
		if input != "0" {
			if !leadName.MatchString(input) {
				return "CON Enter your name using letters (3-80 characters).\n0 Skip\n* Previous\n# Main"
			}
			name = strings.Join(strings.Fields(input), " ")
		}
		r, e := call("save", map[string]any{"name": name})
		if e != nil {
			return "CON Name could not be saved. Retry it or choose 0.\n# Main"
		}
		c.leadRecord = r
		c.last = "lead-review"
		return leadReview(c)
	case "lead-review":
		if input == "*" {
			c.last = "lead-name"
			return "CON Your full name?\n0 Skip for now\n* Previous\n# Main"
		}
		if input == "2" {
			c.last = "lead-products"
			return leadProductsScreen(c)
		}
		if input != "1" {
			return leadReview(c)
		}
		r, e := call("submit", map[string]any{"consent": true})
		if e != nil {
			return "CON Could not confirm follow-up. Retry 1 with the same enquiry.\n# Main"
		}
		return "END Enquiry " + r.Reference + " recorded. This is not loan approval. A loan officer can help complete your information and documents."
	case "assistance-consent":
		if input == "*" || input == "0" {
			c.last = "home"
			return hobbyHome(c)
		}
		if input != "2" {
			return "CON May our team contact you on this number?\n2 Request assistance\n0 Cancel\n# Main"
		}
		r, e := call("assistance", map[string]any{"consent": true})
		if e != nil {
			return "CON Request could not be confirmed. Retry 1.\n# Main"
		}
		return "END Assistance requested. Reference " + r.Reference + ". Please keep this reference for follow-up."
	}
	c.last = "home"
	return hobbyHome(c)
}
