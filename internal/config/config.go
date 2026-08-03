// Package config resolves the CLI's runtime settings: which control plane
// to talk to and which API token authenticates the request. Precedence is
// flags first, then environment variables, then credentials saved by
// `devplat login`, then defaults — matching the flag/env order documented on
// the Download page plus the stored-login layer the login command adds.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

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

// Validate refuses a control-plane URL that would put the API token on the
// wire in the clear.
//
// The token is a bearer credential: whoever reads it off the network can start
// environments, read the team's run history and mint nothing further only
// because tokens cannot mint tokens. Nothing in the CLI checked the scheme, so
// DEVPLAT_API_URL=http://api.devplat.ch — a plausible typo, a stale snippet
// copied into a CI file, a downgrade injected upstream — sent that credential
// over plaintext HTTP and printed nothing at all about it.
//
// Loopback is exempt because the help text advertises --api-url for local
// development, and a plaintext connection that never leaves the machine has no
// network to be observed on. "localhost" is matched by name as well as by
// address: it is what a developer actually types, and resolving it here would
// make the check depend on the machine's DNS.
func (c Config) Validate() error {
	u, err := url.Parse(c.APIURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%q is not a valid API URL — expected something like https://api.devplat.ch", c.APIURL)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf(
			"refusing to send your API token in the clear: %s uses http, not https.\n"+
				"       Anyone on the network path could read the token and use it against your team.\n"+
				"       Use https://, or point --api-url at localhost for local development.", c.APIURL)
	default:
		return fmt.Errorf("unsupported API URL scheme %q in %s — expected https", u.Scheme, c.APIURL)
	}
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
