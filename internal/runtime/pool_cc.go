package runtime

import (
	"golang.org/x/oauth2"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// clientCredentials reports whether c signs in with the OAuth
// client_credentials grant: no browser, no Keychain, no health record.
func clientCredentials(c config.Connection) bool {
	return c.Auth != nil && c.Auth.Grant == config.GrantClientCredentials
}

// clientCredentialsHandler builds the connection's token handler from its
// resolved values. Each pooled session owns one and closes it with the
// session, so a config change or runtime restart drops the token. It reads
// no keyring and records no health event; the token lives in memory only.
func (p *pool) clientCredentialsHandler(c config.Connection, values map[string]string) (*auth.ClientCredentials, error) {
	id, err := oauthClientValue(c.Auth.ClientID, values)
	if err != nil {
		return nil, err
	}
	secret, err := oauthClientValue(c.Auth.ClientSecret, values)
	if err != nil {
		return nil, err
	}
	style := oauth2.AuthStyleInParams
	if c.Auth.TokenEndpointAuthMethod == "client_secret_basic" {
		style = oauth2.AuthStyleInHeader
	}
	return auth.NewClientCredentials(auth.ClientCredentialsConfig{
		TokenURL: c.Auth.TokenURL, ClientID: id, Secret: secret, Scopes: c.Auth.Scopes,
		AuthStyle: style, Now: p.opts.Now, Log: p.opts.TokenLog,
	})
}

// ccTokenRejected replaces auth_required on a client_credentials
// connection. The SDK hands a 401 to the handler, which mints a new token,
// and resends once; a 401 to that resend never reaches the handler and
// surfaces as the transport's auth_required. It means the server rejected a
// token minted for this request (D7: a second 401 is auth_failed).
func ccTokenRejected(details *output.Details) *output.Error {
	e := output.NewError("auth_failed", details)
	e.Message = "The server rejected a newly issued access token (token_rejected)."
	e.NextAction = "Check that auth.tokenUrl issues tokens for this MCP server."
	return e
}
