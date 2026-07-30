package apiclient

import "fmt"

// APIError is a structured error from the control plane. Callers can inspect
// Code to react (or just print — Error() already reads as advice, not as a
// dump of a JSON body).
type APIError struct {
	Status int
	Code   string
	Detail string
}

func (e *APIError) Error() string {
	if hint := hintFor(e.Code); hint != "" {
		return hint
	}
	if e.Detail != "" {
		return e.Detail
	}
	if e.Code != "" {
		return fmt.Sprintf("the server rejected the request (%s)", e.Code)
	}
	return fmt.Sprintf("the server returned status %d", e.Status)
}

// Expired reports whether this failure is an expired API token, which is a
// fixable state rather than a broken setup.
func (e *APIError) Expired() bool { return e.Code == "api_token_expired" }

/*
hintFor turns a control-plane error code into something a developer can act on
without opening the dashboard to find out what happened.

The server already sends a `detail` for most of these, but it's written for the
web UI ("...in the dashboard under API tokens"). From a terminal — often inside
a CI log at 3am — the useful answer names the command to run. Codes not listed
here fall through to the server's own wording, so a new server-side error is
never swallowed.
*/
func hintFor(code string) string {
	switch code {
	case "api_token_expired":
		return "your API token has expired\n" +
			"  Create a new one at https://devplat.ch/app/tokens, then run 'devplat login --token <new-token>'."

	case "invalid_api_token":
		return "this API token is not valid (it may have been revoked)\n" +
			"  Run 'devplat login' to sign in again, or create a fresh token in the dashboard."

	case "ip_not_allowed":
		return "this API token is restricted to certain IP ranges, and the current address isn't one of them\n" +
			"  Check the token's allowlist in the dashboard — CI runners often egress from a different range than your machine."

	case "two_factor_required":
		return "your team requires two-factor authentication, and your account doesn't have it yet\n" +
			"  Set it up at https://devplat.ch/app/profile, then run this again."

	case "seat_limit_reached":
		return "your team has no seats left on its current plan\n" +
			"  Upgrade at https://devplat.ch/app/billing or remove an unused member."

	// Distinct from a capacity wait on purpose. This team's parallelism cap is
	// zero — a lapsed free trial, or a team created after the account's one
	// trial was already used — so the request would otherwise sit in "queued,
	// waiting for capacity…" forever, waiting on a limit that can never be met.
	// That reads as our shortage rather than their billing.
	case "plan_required":
		return "this team has no active plan, so it can't start environments\n" +
			"  Choose a plan at https://devplat.ch/app/billing. The free trial is once per account."

	case "session_revoked":
		return "this session was signed out\n" +
			"  Run 'devplat login' to sign in again."

	case "no_team":
		return "your account isn't in a team yet\n" +
			"  Open https://devplat.ch/app to create one or accept an invitation."

	case "rate_limited":
		return "too many requests — slow down and retry shortly"

	case "email_not_verified":
		return "your email address isn't confirmed yet\n" +
			"  Check your inbox for the verification link, then try again."

	default:
		return ""
	}
}
