// Package config resolves the CLI's runtime settings: which control plane
// to talk to and which API token authenticates the request. Precedence is
// flags first, then environment variables, then credentials saved by
// `devplat login`, then defaults — matching the flag/env order documented on
// the Download page plus the stored-login layer the login command adds.
package config

import (
	"os"

	"github.com/theslasher5g/devplat-cli/internal/credentials"
)

const DefaultAPIURL = "https://api.devplat.ch"

// Config is the resolved set of settings a command needs to talk to the
// control plane.
type Config struct {
	APIURL string
	Token  string
}

// Resolve builds a Config from an explicit token/apiURL (typically CLI
// flags), falling back to DEVPLAT_TOKEN / DEVPLAT_API_URL, then to the token
// stored by `devplat login`, then to defaults.
func Resolve(tokenFlag, apiURLFlag string) Config {
	// Stored login is the lowest-priority source for both values, so an
	// explicit flag or env var always wins over it. A read error here is not
	// fatal — it just means we fall through to "no token", handled by callers.
	stored, _ := credentials.Load()

	token := tokenFlag
	if token == "" {
		token = os.Getenv("DEVPLAT_TOKEN")
	}
	if token == "" && stored != nil {
		token = stored.Token
	}

	apiURL := apiURLFlag
	if apiURL == "" {
		apiURL = os.Getenv("DEVPLAT_API_URL")
	}
	if apiURL == "" && stored != nil {
		apiURL = stored.APIURL
	}
	if apiURL == "" {
		apiURL = DefaultAPIURL
	}
	return Config{APIURL: apiURL, Token: token}
}
