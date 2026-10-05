package config

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestValueArms(t *testing.T) {
	for _, raw := range []string{`""`, `{"input":"endpoint"}`, `{"secret":"op://v/i/f","prefix":"Bearer ","suffix":"!"}`} {
		var v Value
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatal(err)
		}
		arms := 0
		if v.Literal != nil {
			arms++
		}
		if v.Input != nil {
			arms++
		}
		if v.Secret != nil {
			arms++
		}
		if arms != 1 {
			t.Fatal(v)
		}
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var again Value
		if err = json.Unmarshal(b, &again); err != nil || !reflect.DeepEqual(v, again) {
			t.Fatalf("roundtrip %s: %v", b, err)
		}
	}
}

func TestValueRejectsMixedArms(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `{"input":"x","secret":"op://v/i/f"}`, `{"input":"x","prefix":"x"}`, `{"secret":"op://v/i/f","unknown":"x"}`, `{"input":"a","input":"b"}`} {
		v := Literal("original")
		before := v
		if err := json.Unmarshal([]byte(raw), &v); !errors.Is(err, ErrConfig) {
			t.Fatalf("%s: %v", raw, err)
		}
		if !reflect.DeepEqual(v, before) {
			t.Fatal("receiver changed")
		}
	}
	for _, v := range []Value{{}, {Literal: new("x"), Input: &InputRef{Input: "x"}}, {Input: &InputRef{Input: "x"}, Secret: &SecretRef{Secret: "op://v/i/f"}}, {Literal: new("x"), Secret: &SecretRef{Secret: "op://v/i/f"}}} {
		if _, err := json.Marshal(v); !errors.Is(err, ErrConfig) {
			t.Fatal(err)
		}
	}
}

func TestLiteralText(t *testing.T) {
	for _, v := range []Value{Literal("007"), {Input: &InputRef{Input: "x"}}, {Secret: &SecretRef{Secret: "op://v/i/f"}}} {
		s, err := LiteralText(v)
		if v.Literal != nil {
			if s != "007" || err != nil {
				t.Fatal(s, err)
			}
		} else if s != "" || !errors.Is(err, ErrConfigRequired) {
			t.Fatal(s, err)
		}
	}
}
