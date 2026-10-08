package statsapi

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// Lenient scalar types: they accept numbers, numeric strings, booleans and
// null without ever failing, so a single odd field never drops a whole event.

// Num is a float64 that decodes from number / string / bool / null.
type Num float64

// Int is an int that decodes from number (int or float) / string / bool / null.
type Int int

// Bool decodes from true/false, 0/1, "true"/"false"/"1"/"0", null.
type Bool bool

// Str decodes from a string, or the textual form of any scalar.
type Str string

func trimRaw(b []byte) []byte { return bytes.TrimSpace(b) }

func parseNum(b []byte) (float64, bool) {
	b = trimRaw(b)
	if len(b) == 0 || string(b) == "null" {
		return 0, false
	}
	switch b[0] {
	case '"':
		var s string
		if json.Unmarshal(b, &s) != nil {
			return 0, false
		}
		s = strings.TrimSpace(s)
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f, true
		}
		switch strings.ToLower(s) {
		case "true":
			return 1, true
		case "false":
			return 0, true
		}
		return 0, false
	case 't':
		return 1, true
	case 'f':
		return 0, true
	}
	f, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func (n *Num) UnmarshalJSON(b []byte) error {
	f, ok := parseNum(b)
	if ok && !math.IsNaN(f) && !math.IsInf(f, 0) {
		*n = Num(f)
	} else {
		*n = 0
	}
	return nil
}

func (n *Int) UnmarshalJSON(b []byte) error {
	f, ok := parseNum(b)
	if ok && !math.IsNaN(f) && !math.IsInf(f, 0) && math.Abs(f) < 1e15 {
		*n = Int(math.Round(f))
	} else {
		*n = 0
	}
	return nil
}

func (v *Bool) UnmarshalJSON(b []byte) error {
	f, ok := parseNum(b)
	*v = Bool(ok && f != 0)
	return nil
}

func (s *Str) UnmarshalJSON(b []byte) error {
	b = trimRaw(b)
	if len(b) == 0 || string(b) == "null" {
		*s = ""
		return nil
	}
	if b[0] == '"' {
		var x string
		if json.Unmarshal(b, &x) == nil {
			*s = Str(x)
			return nil
		}
		*s = ""
		return nil
	}
	if b[0] == '{' || b[0] == '[' {
		*s = ""
		return nil
	}
	*s = Str(string(b))
	return nil
}

// lenientUnmarshal decodes JSON into v. On a type error for some field the
// standard library keeps decoding the remaining fields, so we only surface
// genuine syntax errors.
func lenientUnmarshal(data []byte, v any) error {
	err := json.Unmarshal(data, v)
	if err == nil {
		return nil
	}
	if _, ok := err.(*json.UnmarshalTypeError); ok {
		return nil
	}
	return err
}
