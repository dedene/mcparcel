package runtime

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/mcpclient"
	"github.com/dedene/mcparcel/internal/output"
)

// maxCallDataBytes leaves room for the response frame around a call's data
// within MaxResponseFrameBytes; tests lower it.
var maxCallDataBytes = MaxResponseFrameBytes - 1<<20

// callData encodes a dispatched call's result for the response frame. A
// result too large for the frame is result_too_large, which leaves the
// session in service: the MCP message itself was within its limit.
func callData(canonical, tool string, r mcpclient.Result, warnings []output.Error) (json.RawMessage, error) {
	b, err := json.Marshal(output.CallData{Connection: canonical, Tool: tool, Result: r.JSON, Warnings: warnings})
	if err != nil {
		return nil, output.NewError("protocol_error", nil)
	}
	if len(b) > maxCallDataBytes {
		return nil, output.NewError("result_too_large", nil)
	}
	return b, nil
}

func poolError(err, cause error, id string, dispatched bool) *output.Error {
	if err == nil {
		return nil
	}
	code := "internal_error"
	var safe *output.Error
	if errors.As(err, &safe) && safe != nil {
		code = safe.Code
	} else {
		switch {
		case errors.Is(err, args.ErrInvalidArgs):
			code = "invalid_arguments"
		case errors.Is(err, args.ErrInvalidSchema):
			code = "invalid_schema"
		case errors.Is(err, config.ErrAmbiguousID):
			code = "ambiguous_id"
		case errors.Is(err, config.ErrDisabled):
			code = "connection_disabled"
		case errors.Is(err, config.ErrReviewRequired):
			code = "review_required"
		case errors.Is(err, config.ErrToolDenied):
			code = "tool_denied"
		case errors.Is(err, config.ErrRuntimeUnsupported):
			code = "runtime_unsupported"
		case errors.Is(err, config.ErrConfig):
			code = "invalid_config"
		case errors.Is(err, config.ErrConfigRequired):
			code = "config_required"
		case errors.Is(err, config.ErrUnsafePath):
			code = "unsafe_local_path"
		case errors.Is(err, config.ErrNotFound):
			code = "connection_unavailable"
		case errors.Is(err, auth.ErrRequired):
			code = "auth_required"
		case errors.Is(err, auth.ErrExpired):
			code = "auth_expired"
		case errors.Is(err, auth.ErrAccountConflict):
			code = "auth_account_conflict"
		case errors.Is(err, auth.ErrProvider):
			code = "auth_failed"
		case errors.Is(err, context.Canceled):
			code = "canceled"
		case errors.Is(err, context.DeadlineExceeded):
			code = "timeout"
		}
	}
	if dispatched && (code == "timeout" || errors.Is(cause, auth.ErrExpired) || errors.Is(cause, errForced)) {
		code = "outcome_unknown"
	}
	var details *output.Details
	var ambiguous *config.AmbiguousIDError
	if errors.As(err, &ambiguous) {
		details = &output.Details{Candidates: append([]string(nil), ambiguous.Candidates...)}
	}
	if dispatched {
		details = &output.Details{RequestID: id, Dispatched: true}
		switch code {
		case "outcome_unknown", "canceled", "result_too_large", "protocol_error":
			details.Outcome = "unknown"
		}
		if safe != nil && safe.Details != nil && safe.Details.RPCCode != nil {
			details.RPCCode = safe.Details.RPCCode
		}
	}
	out := output.NewError(code, details)
	if safe != nil {
		out.Message = safe.Message
		out.NextAction = safe.NextAction
		if out.Code != safe.Code {
			out = output.NewError(code, details)
		}
	}
	return out
}
