package app

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var nrcPattern = regexp.MustCompile(`^[0-9]{6}/[0-9]{2}/[0-9]$`)
var bankReferencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9/-]{2,22}[A-Za-z0-9]$`)
var requestKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,99}$`)

// Reject hidden controls before trimming so they cannot become valid commands.
func cleanInput(input string, max int) (string, error) {
	if !utf8.ValidString(input) {
		return "", errors.New("Enter readable text without invalid characters.")
	}
	if utf8.RuneCountInString(input) > max {
		return "", errors.New("Your answer is too long. Enter up to " + strconv.Itoa(max) + " characters.")
	}
	for _, r := range input {
		if r == utf8.RuneError || unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return "", errors.New("Enter your answer on one line without hidden or control characters.")
		}
	}
	return strings.TrimSpace(input), nil
}
func menuNumber(input string) (int, error) {
	n, err := strconv.Atoi(input)
	if err != nil || n < 1 || strconv.Itoa(n) != input {
		return 0, errors.New("Choose a listed number exactly as shown.")
	}
	return n, nil
}
func validTerm(input string, p *Product) (int, bool) {
	n, err := menuNumber(input)
	if err != nil || p == nil {
		return 0, false
	}
	for _, term := range p.Terms {
		if n == term {
			return n, true
		}
	}
	return 0, false
}
func customerName(input string) (string, error) {
	text, err := cleanInput(input, 60)
	if err != nil {
		return "", err
	}
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) < 3 {
		return "", errors.New("Enter your name using 3–60 characters, with letters, spaces, apostrophes or hyphens.")
	}
	letters := 0
	hasBase := false
	for _, r := range text {
		switch {
		case unicode.IsLetter(r):
			letters++
			hasBase = true
		case unicode.IsMark(r) && hasBase:
		case r == ' ' || r == '\'' || r == '’' || r == '-' || r == '.':
			hasBase = false
		default:
			return "", errors.New("Names cannot contain numbers or symbols. Use letters, spaces, apostrophes or hyphens.")
		}
	}
	runes := []rune(text)
	if letters < 2 || !unicode.IsLetter(runes[0]) || (!unicode.IsLetter(runes[len(runes)-1]) && !unicode.IsMark(runes[len(runes)-1]) && runes[len(runes)-1] != '.') {
		return "", errors.New("Enter a name beginning with a letter, such as George Munganga.")
	}
	return text, nil
}
func screeningText(input string) (string, error) {
	text, err := cleanInput(input, 100)
	if err != nil {
		return "", err
	}
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) < 2 {
		return "", errors.New("Enter 2–100 characters and include the employer name or business activity.")
	}
	hasLetter := false
	hasBase := false
	for _, r := range text {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
			hasBase = true
		case unicode.IsDigit(r):
			hasBase = false
		case unicode.IsMark(r) && hasBase:
		case r == ' ' || strings.ContainsRune("&.,'’-/()+", r):
			hasBase = false
		default:
			return "", errors.New("Use words for the employer or business, with letters, numbers and ordinary punctuation only.")
		}
	}
	if !hasLetter {
		return "", errors.New("Enter the employer name or business activity using words, not numbers only.")
	}
	return text, nil
}
func (field ScreeningField) validate(input string) (string, error) {
	switch field.Kind {
	case "money":
		n, err := parseMoney(input)
		if err != nil {
			return "", errors.New("Enter a positive income in kwacha, using digits and up to 2 decimal places, e.g. 2500.50.")
		}
		return strings.TrimPrefix(money(n), "K"), nil
	case "text":
		return screeningText(input)
	default:
		return "", errors.New("This question is unavailable. Please get assistance.")
	}
}
func bankReference(input string) (string, error) {
	if !bankReferencePattern.MatchString(input) {
		return "", errors.New("Enter a 4–24 character bank reference using letters, numbers, - or /, starting and ending with a letter or number.")
	}
	return input, nil
}

// Recheck saved answers at the commitment boundary, including drafts from older versions.
func validateDraft(s *Session, d *Application) bool {
	p := product(d.Product)
	if p == nil {
		s.Screen = "apply_product"
		s.Notice = "Select an available product."
		return false
	}
	if d.Amount < p.Min || d.Amount > p.Max {
		s.Screen = "apply_amount"
		s.Notice = "Enter an amount within the displayed range."
		return false
	}
	if _, ok := validTerm(strconv.Itoa(d.Term), p); !ok {
		s.Screen = "apply_term"
		s.Notice = "Select a permitted number of months."
		return false
	}
	for i, field := range p.Screening {
		if _, err := field.validate(d.Answers[field.Name]); err != nil {
			s.Screen = "apply_screening"
			s.Page = i
			s.Notice = err.Error()
			return false
		}
	}
	return true
}

type inputSpec struct {
	Mode      string
	MaxLength int
	Hint      string
}

func (a *App) inputSpec(s *Session) inputSpec {
	spec := inputSpec{"numeric", 2, "Enter one of the menu numbers shown."}
	switch s.Screen {
	case "register_name":
		return inputSpec{"text", 60, "Full name: letters, spaces, apostrophes or hyphens."}
	case "register_nrc":
		return inputSpec{"text", 11, "NRC format: 123456/78/1."}
	case "apply_amount", "repay_other":
		return inputSpec{"decimal", 12, "Amount in kwacha, e.g. 500 or 500.50. No commas."}
	case "apply_term":
		return inputSpec{"numeric", 2, "Enter one of the listed terms in months."}
	case "apply_screening":
		if d := a.db.Applications[s.ApplicationID]; d != nil {
			if p := product(d.Product); p != nil && s.Page >= 0 && s.Page < len(p.Screening) {
				if p.Screening[s.Page].Kind == "money" {
					return inputSpec{"decimal", 12, "Income in kwacha, e.g. 2500.50. No commas."}
				}
				return inputSpec{"text", 100, "Employer name or business activity; include words."}
			}
		}
	case "repay_source":
		if s.Method == "BANK" {
			return inputSpec{"text", 24, "Bank reference: 4–24 letters/numbers, - or /."}
		}
		return inputSpec{"tel", 20, "Your mobile number, e.g. 0970000001 or +260970000001."}
	}
	return spec
}
