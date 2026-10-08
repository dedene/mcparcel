package output

import "slices"

// AuthSubcommands are the auth subcommands. A connection name equal to one
// is shadowed by it in mcparcel auth <mcp>; its canonical ID (which always
// holds a colon) still reaches it.
var AuthSubcommands = []string{"lock", "logout", "status"}

// AuthTarget is what to write after "mcparcel auth " for a connection the
// user called name: name itself, or canonical when name is empty or an auth
// subcommand.
func AuthTarget(name, canonical string) string {
	if name == "" || slices.Contains(AuthSubcommands, name) {
		return canonical
	}
	return name
}

// AuthAction is the command that makes the connection's credentials fresh.
func AuthAction(name, canonical string) string {
	return "mcparcel auth " + AuthTarget(name, canonical)
}

// RereadNeedsInputError is auth_required for auth <mcp> --no-input on a
// connection whose "desktop" profile may prompt on any 1Password read. Its
// cached values are kept.
func RereadNeedsInputError(name, canonical string) *Error {
	err := NewError("auth_required", nil)
	err.Message = "Reading " + name + "'s 1Password secrets again may need approval in the 1Password app, which --no-input does not allow."
	err.NextAction = "Run " + AuthAction(name, canonical) + " without --no-input."
	return err
}
