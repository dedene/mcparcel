package elicit

import (
	"strconv"
	"strings"
	"time"
)

// DialogArgs is the argv for the fixed native dialog script: title, text,
// timeout in seconds, then the offered buttons (Decline, Allow once, and Allow
// for this session when offered). Nil for a form or an invalid prompt.
func DialogArgs(connection string, p Prompt) []string {
	if p.Valid() != nil || len(p.Fields) > 0 {
		return nil
	}
	title := "MCParcel"
	if c := Clean(connection, 100); c != "" {
		title += ": " + c
	}
	argv := []string{
		title, strings.Join(append([]string{p.Message}, details(p)...), "\n"),
		strconv.Itoa(int(PromptTimeout / time.Second)),
	}
	for _, i := range buttons(p) {
		argv = append(argv, choices[i].label)
	}
	return argv
}

// DialogAnswer maps the dialog's returned button to its answer; anything but
// an exact offered label is cancel.
func DialogAnswer(p Prompt, button string) Answer {
	if p.Valid() != nil || len(p.Fields) > 0 {
		return canceled
	}
	for _, i := range buttons(p) {
		if choices[i].label == button {
			return choices[i].answer
		}
	}
	return canceled
}

func buttons(p Prompt) []int {
	var out []int
	for i := range choices {
		if offered(p, i) {
			out = append(out, i)
		}
	}
	return out
}
