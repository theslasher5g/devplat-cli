// Package config resolves the CLI's runtime settings: which control plane
// to talk to and which API token authenticates the request. Both come from
// flags first, then environment variables, matching the flag/env precedence
// documented on the Download page ("devplat connect --token $DEVPLAT_TOKEN").
package config

import "os"

const DefaultAPIURL = "https://api.devplat.ch"

// Config is the resolved set of settings a command needs to talk to the
// control plane.
type Config struct {
	APIURL string
	Token  string
}

// Resolve builds a Config from an explicit token/apiURL (typically CLI
// flags) falling back to DEVPLAT_TOKEN / DEVPLAT_API_URL, then defaults.
func Resolve(tokenFlag, apiURLFlag string) Config {
	token := tokenFlag
	if token == "" {
		token = os.Getenv("DEVPLAT_TOKEN")
	}
	apiURL := apiURLFlag
	if apiURL == "" {
		apiURL = os.Getenv("DEVPLAT_API_URL")
	}
	if apiURL == "" {
		apiURL = DefaultAPIURL
	}
	return Config{APIURL: apiURL, Token: token}
}
