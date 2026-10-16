package ui

import (
	"strings"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// A search filters label, ID and description (see matches), but a row
// shows only its label. When the focused row matched on its ID or its
// description alone, the compact summary line and the details pane say
// so, with a snippet around the match: `Matches description: ...library
// documentation.`

// matchReason names what of row matched query when its label does not:
// "ID" or "description", with that text. ok is false without a search, or
// when the label the row shows matches.
func matchReason(row config.EffectiveConnection, query string) (field, text string, ok bool) {
	if query == "" {
		return "", "", false
	}
	query = strings.ToLower(query)
	has := func(text string) bool { return strings.Contains(strings.ToLower(text), query) }
	label := row.ID
	if row.Definition != nil && row.Definition.Label != "" {
		label = row.Definition.Label
	}
	switch {
	case has(label):
		return "", "", false
	case has(row.ID):
		return "ID", row.ID, true
	case row.Definition != nil && has(row.Definition.Description):
		return "description", row.Definition.Description, true
	}
	return "", "", false
}

// matchLine says why row matched the search, in at most n cells, the
// matched text painted roleMatch; "" when matchReason has nothing to say.
func (m *Model) matchLine(row config.EffectiveConnection, n int) string {
	field, text, ok := matchReason(row, m.search.String())
	if !ok {
		return ""
	}
	lead := "Matches " + field + ": "
	before, match, after := snippet(output.DisplayMetadata(strings.Join(strings.Fields(text), " ")), m.search.String(), n-textWidth(lead))
	return fit(lead+before+m.theme.paint(roleMatch, match)+after, n)
}

// snippet cuts text, which is ASCII (output.DisplayMetadata), to at most n
// cells around the first match of query, marking each cut with "...", and
// splits it around the match. Text that fits shows whole, and text whose
// match fits before the cut keeps its start.
// Where query is not found in text (DisplayMetadata escaped it), text is
// cut at the end and nothing is the match.
func snippet(text, query string, n int) (before, match, after string) {
	// Lowered first: the match is as long as the lowered query (the Kelvin
	// sign lowers to an ASCII k).
	query = strings.ToLower(query)
	at := strings.Index(strings.ToLower(text), query)
	if at < 0 || query == "" || n <= 6 {
		return fit(text, n), "", ""
	}
	end := at + len(query)
	start, head, tail := 0, "", ""
	if len(text) > n && end > n-3 {
		// The match gets about a third of what it leaves before it, from the
		// start of a word: the one cut into when the match still fits, else
		// the next.
		start = max(at-max((n-len(query))/3-3, 0), 0)
		if start > 0 && text[start-1] != ' ' {
			if back := strings.LastIndexByte(text[:start], ' ') + 1; 3+at-back+len(query)+3 <= n {
				start = back
			} else if space := strings.IndexByte(text[start:at], ' '); space >= 0 {
				start += space + 1
			}
		}
		// A match near the end shows as much as fits before it.
		if fill := len(text) - (n - 3); start > fill {
			start = fill
			if space := strings.IndexByte(text[start:at], ' '); space >= 0 && text[start-1] != ' ' {
				start += space + 1
			}
		}
		if start > 0 {
			head = "..."
		}
	}
	shown := text[start:]
	if len(head)+len(shown) > n {
		shown, tail = shown[:n-len(head)-3], "..."
	}
	// A match longer than the room is cut with the text.
	at, end = min(at-start, len(shown)), min(end-start, len(shown))
	return head + shown[:at], shown[at:end], shown[end:] + tail
}
