//go:build !mcparceltest

package cmd

import (
	"errors"
	"net/url"
)

// errSignInURL refuses a sign-in URL the browser opener does not pass on.
var errSignInURL = errors.New("unsupported sign-in URL")

// signInURLBytes are the bytes a sign-in URL may hold: unreserved and
// reserved URL characters plus %. MCParcel percent-encodes the query values
// it builds, so a legitimate URL passes; shell metacharacters, quotes,
// spaces, control bytes and non-ASCII never reach a browser opener.
const signInURLBytes = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~:/?#[]@!&*+,;=%"

var signInURLAllowed = func() (set [256]bool) {
	for i := range len(signInURLBytes) {
		set[signInURLBytes[i]] = true
	}
	return set
}()

// validSignInURL accepts an https URL, or an http URL on a loopback host,
// made of signInURLBytes only.
func validSignInURL(raw string) error {
	for i := range len(raw) {
		if !signInURLAllowed[raw[i]] {
			return errSignInURL
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Scheme != "https" && (u.Scheme != "http" || !loopbackHost(u.Hostname())) {
		return errSignInURL
	}
	return nil
}

func loopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}
