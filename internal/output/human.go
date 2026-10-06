package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/dedene/mcparcel/internal/elicit"
)

func WriteHuman(w io.Writer, data any) error {
	var b strings.Builder
	switch value := data.(type) {
	case CallData:
		if err := humanCall(&b, value); err != nil {
			return err
		}
	case *CallData:
		if value == nil {
			return NewError("protocol_error", nil)
		}
		if err := humanCall(&b, *value); err != nil {
			return err
		}
	case ToolList:
		if err := humanTools(&b, value); err != nil {
			return err
		}
	case *ToolList:
		if value == nil {
			return NewError("protocol_error", nil)
		}
		if err := humanTools(&b, *value); err != nil {
			return err
		}
	case SourceMutationData:
		b.WriteString(humanSourceMutation(value))
	case *SourceMutationData:
		if value == nil {
			return NewError("protocol_error", nil)
		}
		b.WriteString(humanSourceMutation(*value))
	case SyncData:
		b.WriteString(humanSync(value))
	case *SyncData:
		if value == nil {
			return NewError("protocol_error", nil)
		}
		b.WriteString(humanSync(*value))
	case MetadataData:
		b.WriteString(humanMetadata(value))
	case *MetadataData:
		if value == nil {
			return NewError("protocol_error", nil)
		}
		b.WriteString(humanMetadata(*value))
	case InspectData:
		b.WriteString(humanInspect(value))
	case *InspectData:
		if value == nil {
			return NewError("protocol_error", nil)
		}
		b.WriteString(humanInspect(*value))
	case string:
		b.WriteString(value)
	case error:
		var e *Error
		if !errors.As(value, &e) || e == nil {
			e = NewError("internal_error", nil)
		}
		b.WriteString(e.Message)
		b.WriteByte('\n')
		if e.NextAction != "" {
			b.WriteString(e.NextAction)
			b.WriteByte('\n')
		}
	default:
		return NewError("protocol_error", nil)
	}
	return writeOnce(w, []byte(b.String()))
}

var blockType = regexp.MustCompile(`^[a-z_]{1,32}$`)

// humanCall prints text blocks cleaned for the terminal and one label per other
// block; it never fails on an odd block shape. Without text blocks it prints
// structuredContent as compact JSON.
func humanCall(b *strings.Builder, data CallData) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data.Result, &top); err != nil {
		return NewError("protocol_error", nil)
	}
	saved := map[int]string{}
	for _, a := range data.Artifacts {
		saved[a.Index] = a.Path
	}
	var content []json.RawMessage
	_ = json.Unmarshal(top["content"], &content)
	texts := 0
	for i, raw := range content {
		var block map[string]json.RawMessage
		var kind, text string
		_ = json.Unmarshal(raw, &block)
		_ = json.Unmarshal(block["type"], &kind)
		switch path, ok := saved[i]; {
		case kind == "text" && isString(block["text"]) && json.Unmarshal(block["text"], &text) == nil:
			texts++
			b.WriteString(elicit.CleanLines(text))
		case ok && (kind == "image" || kind == "audio"):
			b.WriteString("[" + kind + " saved: " + elicit.CleanLines(path) + "]")
		case blockType.MatchString(kind):
			b.WriteString("[" + kind + "]")
		default:
			b.WriteString("[content]")
		}
		b.WriteByte('\n')
	}
	if structured := top["structuredContent"]; texts == 0 && len(structured) > 0 && string(structured) != "null" {
		var compact bytes.Buffer
		if json.Compact(&compact, structured) == nil {
			b.WriteString(elicit.CleanLines(compact.String()))
			b.WriteByte('\n')
		}
	}
	return nil
}

func isString(raw json.RawMessage) bool { return len(raw) > 0 && raw[0] == '"' }

func humanTools(b *strings.Builder, data ToolList) error {
	type tool struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	items := make([]tool, 0, len(data.Items))
	for _, raw := range data.Items {
		var item tool
		if err := json.Unmarshal(raw, &item); err != nil || item.Name == "" {
			return NewError("protocol_error", nil)
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	for _, item := range items {
		b.WriteString(item.Name)
		if item.Description != "" {
			b.WriteString(": ")
			b.WriteString(item.Description)
		}
		b.WriteByte('\n')
	}
	return nil
}
