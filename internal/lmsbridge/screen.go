package lmsbridge

// Screen is an operator-neutral status screen. It contains no bearer token,
// customer identifiers, provider URLs or raw LMS messages. The gateway must
// authenticate each refresh and translate its own wire/session protocol.
type Screen struct {
	Text    string         `json:"text"`
	State   string         `json:"state"`
	Version string         `json:"version"`
	Choices []ScreenChoice `json:"choices"`
}

type ScreenChoice struct {
	Key    string `json:"key"`
	Action string `json:"action"`
}

// Keep every screen within 160 ASCII characters, including navigation. Only
// refresh and exit are offered until authenticated operator actions exist.
// In particular, a ready draft is not itself consent to submit an application.
func renderScreen(j Journey) Screen {
	message := "Please check your application with your loan officer."
	switch j.State {
	case "draft":
		message = "Your saved application needs more details. Please contact your loan officer to complete it."
	case "documents_required":
		message = "Documents are needed for your application. Please contact your loan officer to complete them."
	case "ready_for_submission":
		message = "Your draft is complete but not submitted. Please review and confirm it with your loan officer."
	case "under_review":
		message = "Your application is under review. No action is needed on this screen."
	case "approved":
		message = "Your application is approved. Contact your loan officer for next steps. This is not confirmation of payment."
	case "rejected":
		message = "Your application was not approved. Contact your loan officer for details and next steps."
	case "loan_active":
		message = "You have an outstanding loan. Contact your loan officer for repayment details."
	case "account_review":
		message = "Your loan account needs review. Contact your loan officer before starting another application."
	case "no_active_journey":
		message = "No active application or loan was found. Contact your loan officer to apply. Approval is subject to checks."
	}
	return Screen{
		Text:  "Mighty Finance\n" + message + "\n9 Refresh\n0 Exit",
		State: j.State, Version: j.Version,
		Choices: []ScreenChoice{{Key: "9", Action: "refresh"}, {Key: "0", Action: "exit"}},
	}
}
