package output

import (
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
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

func humanCall(b *strings.Builder, data CallData) error {
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data.Result, &result); err != nil {
		return NewError("protocol_error", nil)
	}
	for _, block := range result.Content {
		switch block.Type {
		case "text":
			b.WriteString(block.Text)
			b.WriteByte('\n')
		case "image":
			b.WriteString("[image]\n")
		case "audio":
			b.WriteString("[audio]\n")
		default:
			b.WriteString("[resource]\n")
		}
	}
	return nil
}

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
