package query

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"dbviewer/internal/model"
)

const (
	// MaxCellBytes: text cells longer than this are sent as a truncated
	// {"$type":"text"} object; the full value is loaded with CellValue.
	MaxCellBytes = 8 * 1024
	// BinaryPreviewBytes: binary cells carry at most this many bytes as hex.
	BinaryPreviewBytes = 256
	maxSafeInt         = 1<<53 - 1
)

// kindFloat4 is an internal encoding hint for single-precision columns.
const kindFloat4 = "float4"

// EncodeKind returns the kind to pass to Encode for a metadata column.
func EncodeKind(c model.Column) string {
	if c.DataType == "float4" {
		return kindFloat4
	}
	return c.Kind
}

// Binary is the wire form of binary values.
type Binary struct {
	Type      string `json:"$type"` // "binary"
	Hex       string `json:"hex"`
	Size      int    `json:"size"`
	Truncated bool   `json:"truncated"`
}

// LongText is the wire form of truncated text values.
type LongText struct {
	Type      string `json:"$type"` // "text"
	Text      string `json:"text"`
	Size      int    `json:"size"`
	Truncated bool   `json:"truncated"`
}

// Encode converts a scanned driver value into the wire encoding (see model.QueryResult).
// full=true disables truncation (CellValue).
func Encode(v any, kind string, full bool) any {
	if kind == kindFloat4 {
		// single precision (pg real): the driver widened it to float64; print the
		// shortest float32 representation (0.1, not 0.10000000149011612)
		if f, ok := v.(float64); ok {
			v = float32(f)
		}
		kind = model.KindFloat
	}
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		if kind == model.KindBinary || (kind == model.KindOther && !utf8.Valid(x)) {
			return encodeBinary(x, full)
		}
		return encodeString(string(x), kind, full)
	case string:
		return encodeString(x, kind, full)
	case int64:
		if kind == model.KindBool && (x == 0 || x == 1) {
			return x == 1
		}
		return encodeInt(x) // other values in a boolean-ish column stay numbers
	case int32:
		return Encode(int64(x), kind, full)
	case int:
		return Encode(int64(x), kind, full)
	case uint64:
		if x > maxSafeInt {
			return strconv.FormatUint(x, 10)
		}
		return int64(x)
	case float64:
		if kind == model.KindDecimal {
			return strconv.FormatFloat(x, 'f', -1, 64)
		}
		return encodeFloat(x)
	case float32:
		// shortest float32 representation, then widen (avoids 0.10000000149)
		f, _ := strconv.ParseFloat(strconv.FormatFloat(float64(x), 'g', -1, 32), 64)
		return encodeFloat(f)
	case bool:
		return x
	case time.Time:
		return formatTime(x, kind)
	default:
		return encodeString(fmt.Sprint(x), kind, full)
	}
}

func encodeInt(x int64) any {
	if x > maxSafeInt || x < -maxSafeInt {
		return strconv.FormatInt(x, 10)
	}
	return x
}

func encodeFloat(f float64) any {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return f
}

func encodeBinary(b []byte, full bool) any {
	out := Binary{Type: "binary", Size: len(b)}
	if !full && len(b) > BinaryPreviewBytes {
		b = b[:BinaryPreviewBytes]
		out.Truncated = true
	}
	out.Hex = hex.EncodeToString(b)
	return out
}

func encodeString(s, kind string, full bool) any {
	switch kind {
	case model.KindInt:
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return encodeInt(n)
		}
		return s
	case model.KindFloat:
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return encodeFloat(f)
		}
		return s
	case model.KindBool:
		switch strings.ToLower(s) {
		case "1", "t", "true", "\x01":
			return true
		case "0", "f", "false", "\x00":
			return false
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return encodeInt(n) // e.g. tinyint(1) holding 2 or -1
		}
		return s
	}
	if !full && len(s) > MaxCellBytes {
		cut := MaxCellBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		return LongText{Type: "text", Text: s[:cut], Size: len(s), Truncated: true}
	}
	return s
}

func formatTime(t time.Time, kind string) string {
	if kind == model.KindTime {
		return t.Format("15:04:05.999999")
	}
	bc := t.Year() <= 0
	if bc {
		// year 0 is 1 BC (proleptic Gregorian): print as Postgres does, "YYYY-MM-DD BC"
		t = t.AddDate(1-2*t.Year(), 0, 0)
	}
	var s string
	if kind == model.KindDate {
		s = t.Format("2006-01-02")
	} else {
		s = t.Format("2006-01-02 15:04:05.999999")
		if t.Location() != time.UTC {
			s += t.Format("-07:00") // timestamptz
		}
	}
	if bc {
		s += " BC"
	}
	return s
}
