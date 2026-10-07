package cmd

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

// authProfileItem is one local credential profile in auth status. It never
// shows the account, a reference or the bootstrap reference.
type authProfileItem struct {
	Profile          string   `json:"profile"`
	Mode             string   `json:"mode"`
	Session          string   `json:"session"` // active | expired | none | unknown
	SessionExpiresAt string   `json:"sessionExpiresAt,omitempty"`
	Connections      []string `json:"connections"`
}

// profileRows lists the local profiles that an enabled connection uses, or
// only profile when it is set. Session state comes from a running daemon,
// which is never started: stopped is "none", unreachable or incompatible
// "unknown".
func profileRows(ctx context.Context, paths config.Paths, snapshot config.Snapshot, profile string) []authProfileItem {
	bound := map[string][]string{}
	for id, row := range snapshot.Effective.Connections {
		if row.Enabled && row.Connection != nil && row.Connection.CredentialProfile != "" {
			bound[row.Connection.CredentialProfile] = append(bound[row.Connection.CredentialProfile], id)
		}
	}
	var names []string
	for name := range snapshot.Local.CredentialProfiles {
		if profile == name || profile == "" && len(bound[name]) > 0 {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	slices.Sort(names)
	sessions, known := daemonSessions(ctx, paths)
	rows := make([]authProfileItem, 0, len(names))
	for _, name := range names {
		ids := bound[name]
		slices.Sort(ids)
		row := authProfileItem{Profile: name, Mode: snapshot.Local.CredentialProfiles[name].Mode, Session: "unknown", Connections: append([]string{}, ids...)}
		if known {
			row.Session = "none"
			if s, ok := sessions[name]; ok {
				row.Session = s.State
				row.SessionExpiresAt = s.ExpiresAt.UTC().Format(time.RFC3339)
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// daemonSessions asks a running daemon for its credential sessions without
// starting one; known is false when the answer is unavailable.
func daemonSessions(ctx context.Context, paths config.Paths) (map[string]runtimeclient.CredentialSession, bool) {
	client := runtimeclient.Client{Paths: paths, Version: version}
	status, err := client.Status(ctx)
	if err != nil || status.Running && !status.Compatible {
		return nil, false
	}
	out := map[string]runtimeclient.CredentialSession{}
	for _, s := range status.CredentialSessions {
		out[s.Profile] = s
	}
	return out, true
}

func writeProfileRows(b *strings.Builder, rows []authProfileItem) {
	for _, row := range rows {
		fmt.Fprintf(b, "profile %s  %s  %s", output.DisplayMetadata(row.Profile), output.DisplayMetadata(row.Mode), row.Session)
		if row.SessionExpiresAt != "" {
			fmt.Fprintf(b, "  until %s", row.SessionExpiresAt)
		}
		b.WriteString("\n")
	}
}
