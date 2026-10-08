package output

import (
	"slices"
	"testing"
)

func TestServiceAccountErrors(t *testing.T) {
	retry := " A rejected token is retried after a short backoff (up to 10 minutes) unless it changes."
	for _, tc := range []struct {
		name          string
		err           *Error
		code          string
		exit          int
		message, next string
		variables     []string
	}{
		{
			"env headless", ServiceAccountTokenEnvError("ops", "OP_TOKEN", true), "config_required", 2,
			"Environment variable OP_TOKEN, the 1Password service-account token of profile ops, is not set in the runtime's environment.",
			"Set OP_TOKEN in the environment that starts mcparcel (Kubernetes: env.valueFrom.secretKeyRef; claw-wrap: map a credential to OP_TOKEN in the tool's env:), then restart the runtime.",
			[]string{"OP_TOKEN"},
		},
		{
			"env desktop", ServiceAccountTokenEnvError("ops", "OP_TOKEN", false), "config_required", 2,
			"Environment variable OP_TOKEN, the 1Password service-account token of profile ops, is not set in the runtime's environment.",
			"Export OP_TOKEN where your login shell reads it (bash: ~/.profile, zsh: ~/.zprofile) or switch the profile to tokenFile, then run mcparcel runtime restart.",
			[]string{"OP_TOKEN"},
		},
		{
			"file", ServiceAccountTokenFileError("ops", "/run/secrets/op"), "config_required", 2,
			"The 1Password service-account token file /run/secrets/op of profile ops is missing, unreadable, empty or not a single token.",
			"Write the token to /run/secrets/op (one line, mode 600, owned by the user that runs mcparcel). MCParcel reads it again on the next call; no restart is needed.", nil,
		},
		{
			"unsafe", ServiceAccountTokenUnsafeError("ops", "/run/secrets/op"), "unsafe_local_path", 2,
			"The token file /run/secrets/op of profile ops is unsafe: it must be a regular file owned by you or root, never readable by others, and readable or writable by its group only on a read-only mount.",
			"Run chmod 600 /run/secrets/op. In Kubernetes, mount the Secret with defaultMode 0440 and fsGroup set to the container's group.", nil,
		},
		{
			"unsafe control path", ServiceAccountTokenUnsafeError("o\x1bps", "/run/\nop"), "unsafe_local_path", 2,
			`The token file "/run/\nop" of profile "o\x1bps" is unsafe: it must be a regular file owned by you or root, never readable by others, and readable or writable by its group only on a read-only mount.`,
			`Run chmod 600 "/run/\nop". In Kubernetes, mount the Secret with defaultMode 0440 and fsGroup set to the container's group.`, nil,
		},
		{
			"rejected file", ServiceAccountRejectedError("ops", "", "/run/secrets/op", true), "auth_failed", 3,
			"1Password did not accept the service-account token of profile ops (token from file /run/secrets/op), could not be reached, or the service account cannot read these items.",
			"Check the token and the service account's vault access; after replacing the file, run the command again." + retry, nil,
		},
		{
			"rejected env desktop", ServiceAccountRejectedError("ops", "OP_TOKEN", "", false), "auth_failed", 3,
			"1Password did not accept the service-account token of profile ops (token from OP_TOKEN), could not be reached, or the service account cannot read these items.",
			"Check the token and the service account's vault access; after changing OP_TOKEN in your login environment, run mcparcel runtime restart." + retry, nil,
		},
		{
			"rejected env headless", ServiceAccountRejectedError("ops", "OP_TOKEN", "", true), "auth_failed", 3,
			"1Password did not accept the service-account token of profile ops (token from OP_TOKEN), could not be reached, or the service account cannot read these items.",
			"Check the token and the service account's vault access; after changing OP_TOKEN where mcparcel starts, restart the runtime." + retry, nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := tc.err
			if e.Code != tc.code || ExitCode(e) != tc.exit || e.Message != tc.message || e.NextAction != tc.next {
				t.Fatalf("%#v exit %d", e, ExitCode(e))
			}
			var variables []string
			if e.Details != nil {
				variables = e.Details.Variables
			}
			if !slices.Equal(variables, tc.variables) {
				t.Fatal(variables)
			}
			if NewError(tc.code, nil).Message == e.Message {
				t.Fatal("constructor changed the registry entry")
			}
		})
	}
}
