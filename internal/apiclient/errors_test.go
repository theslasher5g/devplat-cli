package apiclient

import "strings"

import "testing"

func TestAPIErrorMessages(t *testing.T) {
	cases := []struct {
		name     string
		err      APIError
		contains string
	}{
		{
			name:     "expired token names the fix",
			err:      APIError{Status: 401, Code: "api_token_expired", Detail: "This API token has expired."},
			contains: "devplat login --token",
		},
		{
			name:     "ip restriction explains CI vs laptop",
			err:      APIError{Status: 403, Code: "ip_not_allowed", Detail: "restricted"},
			contains: "CI runners",
		},
		{
			name:     "team 2FA points at the profile",
			err:      APIError{Status: 403, Code: "two_factor_required"},
			contains: "two-factor",
		},
		{
			name:     "unknown code falls back to the server's detail",
			err:      APIError{Status: 400, Code: "some_future_error", Detail: "the server explained itself"},
			contains: "the server explained itself",
		},
		{
			name:     "unknown code with no detail still says something useful",
			err:      APIError{Status: 400, Code: "some_future_error"},
			contains: "some_future_error",
		},
		{
			name:     "no code at all falls back to the status",
			err:      APIError{Status: 502},
			contains: "502",
		},
	}
	for _, c := range cases {
		got := c.err.Error()
		if !strings.Contains(got, c.contains) {
			t.Errorf("%s: Error() = %q, want it to contain %q", c.name, got, c.contains)
		}
	}
}

func TestExpiredPredicate(t *testing.T) {
	if !(&APIError{Code: "api_token_expired"}).Expired() {
		t.Error("expired token should report Expired() == true")
	}
	if (&APIError{Code: "invalid_api_token"}).Expired() {
		t.Error("a revoked token is not an expired one")
	}
}

// A hint must never be an empty string for a code we claim to handle —
// otherwise Error() silently falls through and the mapping is dead weight.
func TestKnownCodesAllHaveHints(t *testing.T) {
	known := []string{
		"api_token_expired", "invalid_api_token", "ip_not_allowed",
		"two_factor_required", "seat_limit_reached", "session_revoked",
		"no_team", "rate_limited", "email_not_verified",
	}
	for _, code := range known {
		if hintFor(code) == "" {
			t.Errorf("code %q is listed as handled but has no hint", code)
		}
	}
	if hintFor("definitely_not_a_real_code") != "" {
		t.Error("unknown codes must fall through to the server's own wording")
	}
}
