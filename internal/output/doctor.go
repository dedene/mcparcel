package output

import (
	"fmt"
	"strings"
)

// DoctorCheck is one doctor row. ID is a stable contract (docs/cli.md); no
// field ever carries a secret, a reference, a URL, an account or a value.
type DoctorCheck struct {
	ID         string `json:"id"`
	Subject    string `json:"subject,omitempty"` // canonical connection ID, document name, source ID or storage name
	Status     string `json:"status"`            // ok | warn | fail | skip
	Message    string `json:"message"`
	NextAction string `json:"nextAction,omitempty"`
	Code       string `json:"code,omitempty"` // error code the failing command would give
}

type DoctorSummary struct {
	OK   int `json:"ok"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
	Skip int `json:"skip"`
}

type DoctorData struct {
	Version string        `json:"version"`
	Mode    string        `json:"mode"` // desktop | headless | unknown
	Live    bool          `json:"live"`
	Checks  []DoctorCheck `json:"checks"`
	Summary DoctorSummary `json:"summary"`
}

// DoctorFailed is doctor_failed for n failed rows; first is the first failed
// row, whose next action becomes the error's when it has one.
func DoctorFailed(n int, first DoctorCheck) *Error {
	err := NewError("doctor_failed", nil)
	if n == 1 {
		err.Message = "1 check failed."
	} else {
		err.Message = fmt.Sprintf("%d checks failed.", n)
	}
	if first.NextAction != "" {
		err.NextAction = first.NextAction
	}
	return err
}

// maxSubjectColumn caps the subject column; a longer subject pushes the
// message right instead of being truncated.
const maxSubjectColumn = 40

func humanDoctor(b *strings.Builder, data DoctorData) {
	checkWidth, subjectWidth := len("Check"), len("Subject")
	for _, c := range data.Checks {
		checkWidth = max(checkWidth, len(DisplayMetadata(c.ID)))
		subjectWidth = max(subjectWidth, min(len(DisplayMetadata(c.Subject)), maxSubjectColumn))
	}
	row := func(status, id, subject, message string) {
		line := fmt.Sprintf("%-7s %-*s %-*s %s", status, checkWidth, id, subjectWidth, subject, message)
		b.WriteString(strings.TrimRight(line, " "))
		b.WriteByte('\n')
	}
	row("Status", "Check", "Subject", "Message")
	for _, c := range data.Checks {
		row(DisplayMetadata(c.Status), DisplayMetadata(c.ID), DisplayMetadata(c.Subject), DisplayMetadata(c.Message))
		if c.NextAction != "" {
			b.WriteString("          next: " + DisplayMetadata(c.NextAction) + "\n")
		}
	}
	s := data.Summary
	fmt.Fprintf(b, "%d ok, %d warn, %d fail, %d skip.\n", s.OK, s.Warn, s.Fail, s.Skip)
}
