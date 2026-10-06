package testutil

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MediaPNG is the 1×1 PNG the media fixture returns.
var MediaPNG, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")

// MediaWAV is the short 8 kHz mono WAV the media fixture returns.
var MediaWAV = func() []byte {
	samples := []byte{128, 160, 192, 160, 128, 96, 64, 96}
	b := make([]byte, 0, 44+len(samples))
	le := binary.LittleEndian
	b = append(b, "RIFF"...)
	b = le.AppendUint32(b, uint32(36+len(samples)))
	b = append(b, "WAVEfmt "...)
	b = le.AppendUint32(b, 16)
	b = le.AppendUint16(b, 1)    // PCM
	b = le.AppendUint16(b, 1)    // mono
	b = le.AppendUint32(b, 8000) // sample rate
	b = le.AppendUint32(b, 8000) // byte rate
	b = le.AppendUint16(b, 1)    // block align
	b = le.AppendUint16(b, 8)    // bits per sample
	b = append(b, "data"...)
	b = le.AppendUint32(b, uint32(len(samples)))
	return append(b, samples...)
}()

// addMediaTool adds "media": count PNG image blocks (default 1), one WAV
// audio block and a text block; fail marks the result isError.
func addMediaTool(server *mcp.Server) {
	server.AddTool(&mcp.Tool{Name: "media", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"count": map[string]any{"type": "integer"}, "fail": map[string]any{"type": "boolean"}}}}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		in := struct {
			Count *int
			Fail  bool
		}{}
		if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
			return nil, err
		}
		count := 1
		if in.Count != nil {
			count = max(0, *in.Count)
		}
		r := &mcp.CallToolResult{IsError: in.Fail}
		for range count {
			r.Content = append(r.Content, &mcp.ImageContent{MIMEType: "image/png", Data: MediaPNG})
		}
		r.Content = append(r.Content, &mcp.AudioContent{MIMEType: "audio/wav", Data: MediaWAV}, &mcp.TextContent{Text: "media"})
		return r, nil
	})
}
