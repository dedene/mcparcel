package runtime

import (
	"encoding/json"
	"errors"
	"io"
)

var tokenTriggers = map[string]bool{"first": true, "expiry": true, "401": true}

// WriteTokenEvent logs a client_credentials token event:
// oauth_token_minted with trigger (first, expiry or 401) and ttl seconds,
// oauth_token_mint_failed or oauth_token_rejected with a sanitized code and
// an HTTP status (0 when there was no answer). Any other event or field is
// refused, so no connection URL, client ID, secret or token reaches the log.
func WriteTokenEvent(w io.Writer, event string, fields map[string]any) error {
	var line any
	switch event {
	case "oauth_token_minted":
		trigger, _ := fields["trigger"].(string)
		ttl, ok := fields["ttl"].(int64)
		if len(fields) != 2 || !tokenTriggers[trigger] || !ok || ttl < 0 {
			return errors.New("invalid token event")
		}
		line = struct {
			Event   string `json:"event"`
			Trigger string `json:"trigger"`
			TTL     int64  `json:"ttl"`
		}{event, trigger, ttl}
	case "oauth_token_mint_failed", "oauth_token_rejected":
		code, _ := fields["code"].(string)
		status, ok := fields["status"].(int)
		if len(fields) != 2 || !signInCode.MatchString(code) || !ok || status < 0 || status > 999 {
			return errors.New("invalid token event")
		}
		line = struct {
			Event  string `json:"event"`
			Code   string `json:"code"`
			Status int    `json:"status"`
		}{event, code, status}
	default:
		return errors.New("invalid log event")
	}
	b, e := json.Marshal(line)
	if e != nil {
		return e
	}
	b = append(b, '\n')
	n, e := w.Write(b)
	if e == nil && n != len(b) {
		e = io.ErrShortWrite
	}
	return e
}
