package ui

import "github.com/dedene/mcparcel/internal/config"

// signIn reports whether the connection signs in through a browser: HTTP
// with OAuth, except the client_credentials grant, which needs no user.
func signIn(d *config.Connection) bool {
	return d.Transport.HTTP != nil && d.Auth != nil && d.Auth.Grant != config.GrantClientCredentials
}
