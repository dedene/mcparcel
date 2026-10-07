package doctor

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// opRef is one 1Password reference and where it sits. Only where is ever shown.
type opRef struct {
	where string // env NAME | header NAME | auth clientId | auth clientSecret | profile bootstrapRef
	ref   string
}

// boundProfile returns the credential profile the connection is bound to,
// when it exists.
func boundProfile(in Input, c *config.Connection) (string, config.Profile, bool) {
	if c == nil || c.CredentialProfile == "" || in.Snapshot == nil {
		return "", config.Profile{}, false
	}
	p, ok := in.Snapshot.Local.CredentialProfiles[c.CredentialProfile]
	return c.CredentialProfile, p, ok
}

// onePasswordRefs walks the fields that may hold a secret (as config.SecretRefs
// does) plus the bound profile's bootstrapRef, keeping op:// references only.
func onePasswordRefs(in Input, c *config.Connection) []opRef {
	var refs []opRef
	add := func(where string, v *config.Value) {
		if v != nil && v.Secret != nil && !strings.HasPrefix(v.Secret.Secret, "env:") {
			refs = append(refs, opRef{where, v.Secret.Secret})
		}
	}
	addMap := func(kind string, m map[string]config.Value) {
		for _, name := range slices.Sorted(maps.Keys(m)) {
			v := m[name]
			add(kind+" "+show(name), &v)
		}
	}
	if s := c.Transport.Stdio; s != nil {
		addMap("env", s.Env)
	}
	if h := c.Transport.HTTP; h != nil {
		addMap("header", h.Headers)
	}
	if a := c.Auth; a != nil {
		add("auth clientId", a.ClientID)
		add("auth clientSecret", a.ClientSecret)
	}
	if _, p, ok := boundProfile(in, c); ok && p.BootstrapRef != "" {
		refs = append(refs, opRef{"profile bootstrapRef", p.BootstrapRef})
	}
	return refs
}

func credentialChecks(in Input, row config.EffectiveConnection) []output.DoctorCheck {
	id := show(row.ID)
	c := row.Connection
	var checks []output.DoctorCheck
	if row.Definition != nil && row.Definition.CredentialProfile != "" {
		if name, p, ok := boundProfile(in, c); ok {
			checks = append(checks, output.DoctorCheck{ID: "credentials.profile", Subject: id, Status: OK, Message: "Bound to profile " + show(name) + " (" + show(p.Mode) + ")."})
		}
	}
	switch Mode(in) {
	case config.ModeDesktop:
		if refs := onePasswordRefs(in, c); len(refs) > 0 {
			checks = append(checks, referenceCheck(id, refs))
		}
		return checks
	case config.ModeHeadless:
	default:
		return checks
	}
	if names := config.EnvRefs(*c); len(names) > 0 {
		checks = append(checks, envCheck(in, id, names))
	}
	if c.Auth != nil && c.Auth.Grant != config.GrantClientCredentials {
		checks = append(checks, output.DoctorCheck{ID: "credentials.oauth", Subject: id, Status: Fail, Code: "auth_required", Message: "This server needs sign-in, which headless mode cannot do.", NextAction: `Use an OAuth client_credentials grant (auth.grant "client_credentials") for this connection in headless mode.`})
	}
	return checks
}

func referenceCheck(id string, refs []opRef) output.DoctorCheck {
	c := output.DoctorCheck{ID: "credentials.reference", Subject: id, Status: OK}
	for _, r := range refs {
		if !config.OnePasswordRefPartsValid(r.ref) {
			c.Status = Fail
			c.Message = "A 1Password reference in " + r.where + " has characters 1Password rejects; refer to the vault or item by its ID."
			c.NextAction = "Replace the vault or item name in that reference with its ID (letters, digits, _, . and - only)."
			return c
		}
	}
	c.Message = fmt.Sprintf("%d 1Password %s well-formed.", len(refs), plural(len(refs), "reference is", "references are"))
	return c
}

func envCheck(in Input, id string, names []string) output.DoctorCheck {
	c := output.DoctorCheck{ID: "credentials.env", Subject: id, Status: OK}
	var missing []string
	for _, name := range names {
		if in.LookupEnv == nil || !in.LookupEnv(name) {
			missing = append(missing, show(name))
		}
	}
	if len(missing) == 0 {
		c.Message = fmt.Sprintf("All %d %s set here.", len(names), plural(len(names), "variable is", "variables are"))
		return c
	}
	c.Status = Warn
	c.Message = fmt.Sprintf("Environment %s %s not set in this shell; the runtime reads its own environment.", plural(len(missing), "variable", "variables"), strings.Join(missing, ", ")+plural(len(missing), " is", " are"))
	c.NextAction = "Check the env: map of the wrapper that runs mcparcel (headless.md)."
	return c
}
