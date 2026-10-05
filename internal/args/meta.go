package args

import (
	"fmt"
	"strings"

	"github.com/dedene/mcparcel/internal/jsonutil"
)

const MaxMetaBytes = 64 * 1024

// ParseMeta validates a tools/call _meta object. The SDK owns progressToken and
// the MCP specification reserves prefixes whose second label is
// modelcontextprotocol or mcp.
func ParseMeta(data []byte) (map[string]any, error) {
	if len(data) > MaxMetaBytes {
		return nil, fmt.Errorf("%w: --meta exceeds 64 KiB", ErrInvalidArgs)
	}
	v, err := jsonutil.Decode(data)
	m, ok := v.(map[string]any)
	if err != nil || !ok {
		return nil, fmt.Errorf("%w: --meta must be one JSON object", ErrInvalidArgs)
	}
	for key := range m {
		labels := []string{}
		if i := strings.LastIndexByte(key, '/'); i >= 0 {
			labels = strings.Split(key[:i], ".")
		}
		if key == "progressToken" || len(labels) > 1 && (strings.EqualFold(labels[1], "modelcontextprotocol") || strings.EqualFold(labels[1], "mcp")) {
			return nil, fmt.Errorf("%w: --meta key %q is reserved", ErrInvalidArgs, key)
		}
	}
	return m, nil
}
