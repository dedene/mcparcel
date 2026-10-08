package output

// The errors of a service-account credential profile. They name the profile,
// the token variable or the token file, never the token.

// rejectedRetry ends every ServiceAccountRejectedError next action: the
// provider's negative cache answers an unchanged rejected token itself.
const rejectedRetry = " A rejected token is retried after a short backoff (up to 10 minutes) unless it changes."

// ServiceAccountTokenEnvError is config_required for a service-account
// profile whose tokenEnv variable is missing from the runtime's environment.
func ServiceAccountTokenEnvError(id, name string, headless bool) *Error {
	err := NewError("config_required", &Details{Variables: []string{name}})
	name = DisplayMetadata(name)
	err.Message = "Environment variable " + name + ", the 1Password service-account token of profile " + DisplayMetadata(id) + ", is not set in the runtime's environment."
	if headless {
		err.NextAction = "Set " + name + " in the environment that starts mcparcel (Kubernetes: env.valueFrom.secretKeyRef; claw-wrap: map a credential to " + name + " in the tool's env:), then restart the runtime."
	} else {
		err.NextAction = "Export " + name + " where your login shell reads it (bash: ~/.profile, zsh: ~/.zprofile) or switch the profile to tokenFile, then run mcparcel runtime restart."
	}
	return err
}

// ServiceAccountTokenFileError is config_required for a service-account
// profile whose tokenFile is missing, unreadable, empty or not one token.
func ServiceAccountTokenFileError(id, path string) *Error {
	err := NewError("config_required", nil)
	path = DisplayMetadata(path)
	err.Message = "The 1Password service-account token file " + path + " of profile " + DisplayMetadata(id) + " is missing, unreadable, empty or not a single token."
	err.NextAction = "Write the token to " + path + " (one line, mode 600, owned by the user that runs mcparcel). MCParcel reads it again on the next call; no restart is needed."
	return err
}

// ServiceAccountTokenUnsafeError is unsafe_local_path for a service-account
// profile whose tokenFile fails the ownership or permission check.
func ServiceAccountTokenUnsafeError(id, path string) *Error {
	err := NewError("unsafe_local_path", nil)
	path = DisplayMetadata(path)
	err.Message = "The token file " + path + " of profile " + DisplayMetadata(id) + " is unsafe: it must be a regular file owned by you or root, never readable by others, and readable or writable by its group only on a read-only mount."
	err.NextAction = "Run chmod 600 " + path + ". In Kubernetes, mount the Secret with defaultMode 0440 and fsGroup set to the container's group."
	return err
}

// ServiceAccountRejectedError is auth_failed for a service-account profile
// whose token 1Password refused, could not check, or whose service account
// cannot read the connection's items. Exactly one of env and file is set.
func ServiceAccountRejectedError(id, env, file string, headless bool) *Error {
	err := NewError("auth_failed", nil)
	source := "token from " + DisplayMetadata(env)
	if env == "" {
		source = "token from file " + DisplayMetadata(file)
	}
	err.Message = "1Password did not accept the service-account token of profile " + DisplayMetadata(id) + " (" + source + "), could not be reached, or the service account cannot read these items."
	action := "Check the token and the service account's vault access; "
	switch {
	case env == "":
		action += "after replacing the file, run the command again."
	case headless:
		action += "after changing " + DisplayMetadata(env) + " where mcparcel starts, restart the runtime."
	default:
		action += "after changing " + DisplayMetadata(env) + " in your login environment, run mcparcel runtime restart."
	}
	err.NextAction = action + rejectedRetry
	return err
}
