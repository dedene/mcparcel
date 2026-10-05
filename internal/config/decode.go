package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/dedene/mcparcel/internal/jsonutil"
)

var (
	identifier = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	envName    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	headerName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
)

func fieldError(path, reason string) error { return fmt.Errorf("%w: %s: %s", ErrConfig, path, reason) }
func decodeStrict(data []byte, dest any, path string) error {
	if len(data) > maxConfigBytes {
		return fieldError(path, "configuration exceeds 2097152 bytes")
	}
	v, err := jsonutil.Decode(data)
	if err != nil {
		if duplicate := duplicatePath(data); duplicate != "" {
			return fieldError(duplicate, "duplicate key")
		}
		return fieldError(path, "invalid JSON")
	}
	typ := reflect.TypeOf(dest).Elem()
	if err := checkShape(v, typ, path); err != nil {
		return err
	}
	if err := checkRequired(v, typ, path); err != nil {
		return err
	}
	canonical, err := json.Marshal(normalizeIntegers(v))
	if err != nil {
		return fieldError(path, "invalid JSON")
	}
	if err := json.Unmarshal(canonical, dest); err != nil {
		return fieldError(path, "invalid field shape")
	}
	return nil
}

// checkShape rejects nulls and unknown fields before encoding/json can silently
// accept null into a string, slice or struct. Paths contain field names only.
func checkShape(v any, typ reflect.Type, path string) error {
	if typ == reflect.TypeFor[Value]() {
		if _, ok := v.(string); ok {
			return nil
		}
		obj, ok := v.(map[string]any)
		if !ok {
			return fieldError(path, "expected literal string or secret object")
		}
		if _, ok = obj["input"]; ok {
			if text, ok := obj["input"].(string); ok && !identifier.MatchString(text) {
				return fieldError(path+".input", "invalid input reference")
			}
			return checkShape(v, reflect.TypeFor[InputRef](), path)
		}
		if _, ok = obj["secret"]; !ok {
			return fieldError(path, "secret required")
		}
		if text, ok := obj["secret"].(string); ok && !validValueRef(text) {
			return fieldError(path+".secret", "invalid secret reference")
		}
		for _, key := range []string{"prefix", "suffix"} {
			if text, ok := obj[key].(string); ok && strings.ContainsRune(text, 0) {
				return fieldError(path+"."+key, "invalid affix")
			}
		}
		return checkShape(v, reflect.TypeFor[SecretRef](), path)
	}
	if typ == reflect.TypeFor[Transport]() {
		obj, ok := v.(map[string]any)
		if !ok {
			return fieldError(path, "transport object required")
		}
		tag, ok := obj["type"].(string)
		if !ok {
			return fieldError(path+".type", "transport type required")
		}
		fields := make(map[string]any, len(obj)-1)
		for k, val := range obj {
			if k != "type" {
				fields[k] = val
			}
		}
		switch tag {
		case "stdio":
			if err := checkShape(fields, reflect.TypeFor[Stdio](), path); err != nil {
				return err
			}
			return checkRequired(fields, reflect.TypeFor[Stdio](), path)
		case "http":
			if err := checkShape(fields, reflect.TypeFor[HTTP](), path); err != nil {
				return err
			}
			return checkRequired(fields, reflect.TypeFor[HTTP](), path)
		default:
			return fieldError(path+".type", "unknown field")
		}
	}
	if typ.Kind() == reflect.Pointer {
		if v == nil {
			return fieldError(path, "null is invalid")
		}
		return checkShape(v, typ.Elem(), path)
	}
	switch typ.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return fieldError(path, "object required")
		}
		fields := make(map[string]reflect.Type)
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				fields[name] = f.Type
			}
		}
		for _, key := range sortedKeys(obj) {
			ft, ok := fields[key]
			if !ok {
				return fieldError(path+"."+key, "unknown field")
			}
			if err := checkShape(obj[key], ft, path+"."+key); err != nil {
				return err
			}
		}
	case reflect.Map:
		obj, ok := v.(map[string]any)
		if !ok {
			return fieldError(path, "object required")
		}
		for _, k := range sortedKeys(obj) {
			if err := checkShape(obj[k], typ.Elem(), path+"."+k); err != nil {
				return err
			}
		}
	case reflect.Slice:
		arr, ok := v.([]any)
		if !ok {
			return fieldError(path, "array required")
		}
		for i, val := range arr {
			if err := checkShape(val, typ.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case reflect.String:
		if _, ok := v.(string); !ok {
			return fieldError(path, "string required")
		}
	case reflect.Bool:
		if _, ok := v.(bool); !ok {
			return fieldError(path, "boolean required")
		}
	case reflect.Int, reflect.Uint64:
		n, ok := v.(json.Number)
		if !ok {
			return fieldError(path, "integer required")
		}
		r, ok := new(big.Rat).SetString(string(n))
		if !ok || !r.IsInt() {
			return fieldError(path, "integer required")
		}
		if typ.Kind() == reflect.Uint64 {
			if r.Sign() < 0 || !r.Num().IsUint64() {
				return fieldError(path, "integer out of range")
			}
		} else {
			limit := new(big.Int).Lsh(big.NewInt(1), uint(typ.Bits()-1))
			min := new(big.Int).Neg(limit)
			max := new(big.Int).Sub(new(big.Int).Set(limit), big.NewInt(1))
			if r.Num().Cmp(min) < 0 || r.Num().Cmp(max) > 0 {
				return fieldError(path, "integer out of range")
			}
		}
	default:
		return fieldError(path, "unsupported field shape")
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// envRefName returns NAME for a valid "env:NAME" reference: the temporary
// environment credential bridge (personal definitions only; replaced by
// 1Password profiles in stage 6). Protected names are never valid.
func envRefName(ref string) (string, bool) {
	name, ok := strings.CutPrefix(ref, "env:")
	return name, ok && envName.MatchString(name) && !ProtectedEnv(name)
}

func validValueRef(ref string) bool { _, ok := envRefName(ref); return ok || validRef(ref) }

func validRef(ref string) bool {
	if !strings.HasPrefix(ref, "op://") || strings.ContainsAny(ref, "@?#\x00") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(ref, "op://"), "/")
	if len(parts) != 3 && len(parts) != 4 {
		return false
	}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return false
		}
	}
	return true
}

func ProtectedEnv(name string) bool {
	switch name {
	case "OP_SERVICE_ACCOUNT_TOKEN", "OP_CONNECT_TOKEN", "GH_TOKEN", "GITHUB_TOKEN", "GITHUB_API_TOKEN", "GIT_ASKPASS", "SSH_AUTH_SOCK", "BASH_ENV", "ENV", "ZDOTDIR":
		return true
	}
	for _, prefix := range []string{"OP_", "AWS_", "AZURE_", "GOOGLE_", "DYLD_", "LD_"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func forbiddenExplicit(name string) bool {
	switch name {
	case "OP_SERVICE_ACCOUNT_TOKEN", "OP_CONNECT_TOKEN", "BASH_ENV", "ENV", "ZDOTDIR":
		return true
	}
	return strings.HasPrefix(name, "DYLD_") || strings.HasPrefix(name, "LD_")
}

// Only used on a rejected document to identify duplicate field paths without
// including values or arbitrary decoder diagnostics in a configuration error.
func duplicatePath(data []byte) string {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var stopped bool
	var walk func(string, int) string
	walk = func(path string, depth int) string {
		if stopped || depth > 128 {
			stopped = true
			return ""
		}
		tok, err := d.Token()
		if err != nil {
			stopped = true
			return ""
		}
		switch tok {
		case json.Delim('{'):
			seen := make(map[string]bool)
			for d.More() {
				key, err := d.Token()
				if err != nil {
					stopped = true
					return ""
				}
				s, ok := key.(string)
				if !ok {
					return ""
				}
				child := s
				if path != "" {
					child = path + "." + s
				}
				if seen[s] {
					return child
				}
				seen[s] = true
				if result := walk(child, depth+1); result != "" {
					return result
				}
				if stopped {
					return ""
				}
			}
			if _, err := d.Token(); err != nil {
				stopped = true
			}
		case json.Delim('['):
			for i := 0; d.More(); i++ {
				if result := walk(fmt.Sprintf("%s[%d]", path, i), depth+1); result != "" {
					return result
				}
				if stopped {
					return ""
				}
			}
			if _, err := d.Token(); err != nil {
				stopped = true
			}
		}
		return ""
	}
	return walk("", 0)
}

func normalizeIntegers(v any) any {
	switch v := v.(type) {
	case json.Number:
		if r, ok := new(big.Rat).SetString(string(v)); ok && r.IsInt() {
			return json.Number(r.Num().String())
		}
	case map[string]any:
		next := make(map[string]any, len(v))
		for k, x := range v {
			next[k] = normalizeIntegers(x)
		}
		return next
	case []any:
		next := make([]any, len(v))
		for i, x := range v {
			next[i] = normalizeIntegers(x)
		}
		return next
	}
	return v
}

func checkRequired(v any, typ reflect.Type, path string) error {
	if typ.Kind() == reflect.Pointer {
		return checkRequired(v, typ.Elem(), path)
	}
	if typ == reflect.TypeFor[Value]() {
		if obj, ok := v.(map[string]any); ok {
			if _, ok = obj["input"]; ok {
				return checkRequired(v, reflect.TypeFor[InputRef](), path)
			}
			return checkRequired(v, reflect.TypeFor[SecretRef](), path)
		}
		return nil
	}
	if typ == reflect.TypeFor[Transport]() {
		obj := v.(map[string]any)
		if obj["type"] == "stdio" {
			return checkRequired(v, reflect.TypeFor[Stdio](), path)
		}
		return checkRequired(v, reflect.TypeFor[HTTP](), path)
	}
	switch typ.Kind() {
	case reflect.Struct:
		obj := v.(map[string]any)
		var required []string
		switch typ {
		case reflect.TypeFor[Catalog]():
			required = []string{"schemaVersion", "connections"}
		case reflect.TypeFor[Local]():
			required = []string{"schemaVersion"}
		case reflect.TypeFor[Selections]():
			required = []string{"schemaVersion", "revision", "connections"}
		case reflect.TypeFor[Connection]():
			required = []string{"transport"}
			if text, ok := obj["startupTimeout"].(string); ok && text == "" {
				return fieldError(path+".startupTimeout", "positive duration required")
			}
		case reflect.TypeFor[Stdio]():
			required = []string{"command"}
		case reflect.TypeFor[HTTP]():
			required = []string{"url"}
		case reflect.TypeFor[OAuth]():
			required = []string{"type"}
			for _, key := range []string{"tokenEndpointAuthMethod", "redirectUrl", "issuerUrl"} {
				if text, ok := obj[key].(string); ok && text == "" {
					return fieldError(path+"."+key, "nonempty value required")
				}
			}
		case reflect.TypeFor[Input]():
			required = []string{"kind", "description"}
		case reflect.TypeFor[Domain]():
			required = []string{"label"}
		case reflect.TypeFor[InputRef]():
			required = []string{"input"}
		case reflect.TypeFor[SecretRef]():
			required = []string{"secret"}
		case reflect.TypeFor[Profile]():
			required = []string{"mode", "account"}
			if obj["mode"] == "desktop-service-account" {
				required = append(required, "bootstrapRef")
			}
			if obj["mode"] == "desktop" {
				if _, ok := obj["bootstrapRef"]; ok {
					return fieldError(path+".bootstrapRef", "forbidden for desktop")
				}
			}
		case reflect.TypeFor[Selection]():
			required = []string{"enabled"}
		case reflect.TypeFor[Source]():
			required = []string{"id", "repositoryId", "owner", "repo", "path", "ref", "commit", "pinned"}
		case reflect.TypeFor[Lifecycle]():
			if text, ok := obj["idleTimeout"].(string); ok && text == "" {
				return fieldError(path+".idleTimeout", "positive duration or session required")
			}
		}
		for _, key := range required {
			if _, ok := obj[key]; !ok {
				return fieldError(path+"."+key, "required field")
			}
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			key := strings.Split(f.Tag.Get("json"), ",")[0]
			if x, ok := obj[key]; ok {
				if err := checkRequired(x, f.Type, path+"."+key); err != nil {
					return err
				}
			}
		}
	case reflect.Map:
		for _, key := range sortedKeys(v.(map[string]any)) {
			if err := checkRequired(v.(map[string]any)[key], typ.Elem(), path+"."+key); err != nil {
				return err
			}
		}
	case reflect.Slice:
		for i, x := range v.([]any) {
			if err := checkRequired(x, typ.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}
