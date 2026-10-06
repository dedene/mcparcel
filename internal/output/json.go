package output

import (
	"encoding/json"
	"io"
)

type Envelope struct {
	SchemaVersion int    `json:"schemaVersion"`
	OK            bool   `json:"ok"`
	Data          any    `json:"data"`
	Error         *Error `json:"error"`
}
type CallData struct {
	Connection string          `json:"connection"`
	Tool       string          `json:"tool"`
	Result     json.RawMessage `json:"result"`
	// Artifacts lists the files --output-dir saved; only the CLI sets it.
	Artifacts []Artifact `json:"artifacts,omitempty"`
	Warnings  []Error    `json:"warnings,omitempty"`
}
type ToolList struct {
	Connection      string            `json:"connection"`
	Items           []json.RawMessage `json:"items"`
	SourceRevisions map[string]string `json:"sourceRevisions"`
	CacheAgeSeconds *float64          `json:"cacheAgeSeconds"`
}

func WriteJSON(w io.Writer, data any, failure *Error) error {
	if failure != nil {
		if failure.Code == "tool_error" || failure.Code == "input_required" || failure.Code == "export_failed" {
			switch data.(type) {
			case CallData, *CallData:
			default:
				data = nil
			}
		} else {
			data = nil
		}
	}
	switch value := data.(type) {
	case ToolList:
		if value.Items == nil {
			value.Items = []json.RawMessage{}
		}
		data = value
	case *ToolList:
		if value != nil {
			clone := *value
			if clone.Items == nil {
				clone.Items = []json.RawMessage{}
			}
			data = clone
		}
	}
	encoded, err := json.Marshal(Envelope{SchemaVersion: 1, OK: failure == nil, Data: data, Error: failure})
	if err != nil {
		return err
	}
	return writeOnce(w, append(encoded, '\n'))
}

func writeOnce(w io.Writer, data []byte) error {
	n, err := w.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}
