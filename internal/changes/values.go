package changes

import (
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"dbviewer/internal/model"
)

// value is a decoded wire value ready for binding.
type value struct {
	isDefault bool
	arg       any // nil, int64, float64, string, bool, []byte
}

// decode converts a wire CellValue (as decoded from JSON) into a bindable value.
// See CONTRACT.md "Value encoding".
func decode(v any, col model.Column, driver string) (value, error) {
	switch x := v.(type) {
	case nil:
		return value{}, nil
	case bool:
		return value{arg: x}, nil
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 && col.Kind != model.KindFloat {
			return value{arg: int64(x)}, nil
		}
		return value{arg: x}, nil
	case int64, int, int32, uint64:
		return value{arg: x}, nil
	case string:
		if col.Kind == model.KindBool && driver != model.DriverPostgres {
			switch strings.ToLower(strings.TrimSpace(x)) {
			case "true", "t", "yes", "y", "on":
				return value{arg: true}, nil
			case "false", "f", "no", "n", "off":
				return value{arg: false}, nil
			}
		}
		return value{arg: x}, nil
	case []byte:
		return value{arg: x}, nil
	case map[string]any:
		switch x["$type"] {
		case "default":
			return value{isDefault: true}, nil
		case "binary":
			h, _ := x["hex"].(string)
			h = strings.TrimPrefix(strings.TrimPrefix(h, "0x"), "\\x")
			if tr, _ := x["truncated"].(bool); tr {
				return value{}, model.Errf(model.ErrInvalid, "column %s: cannot write a truncated binary value", col.Name)
			}
			b, err := hex.DecodeString(h)
			if err != nil {
				return value{}, model.Errf(model.ErrInvalid, "column %s: invalid hex: %v", col.Name, err)
			}
			return value{arg: b}, nil
		case "text":
			if tr, _ := x["truncated"].(bool); tr {
				return value{}, model.Errf(model.ErrInvalid, "column %s: cannot write a truncated text value (load the full value first)", col.Name)
			}
			t, _ := x["text"].(string)
			return value{arg: t}, nil
		}
	}
	return value{}, model.Errf(model.ErrInvalid, "column %s: unsupported value %T", col.Name, v)
}

// dialect renders SQL for one driver.
type dialect struct {
	driver string
	quote  func(string) string
	ph     func(int) string
}

// bindArg turns a value into the bound argument + SQL placeholder expression.
// Postgres: every non-null, non-binary value is sent as text and cast to the
// column type server-side (CAST($n::text AS <type>)), which works for every type
// with a text input function (numeric, timestamp, jsonb, uuid, arrays, enums...).
func (d dialect) bindArg(v value, col model.Column, n int) (string, any) {
	ph := d.ph(n)
	switch d.driver {
	case model.DriverPostgres:
		switch a := v.arg.(type) {
		case nil:
			return ph, nil
		case []byte:
			return ph, a
		default:
			typ := col.CastType
			if typ == "" {
				typ = col.Type
			}
			if typ == "" {
				typ = "text"
			}
			return "CAST(" + ph + "::text AS " + typ + ")", textOf(a)
		}
	default:
		if b, ok := v.arg.(bool); ok {
			if b {
				return ph, int64(1)
			}
			return ph, int64(0)
		}
		return ph, v.arg
	}
}

func textOf(a any) string {
	switch x := a.(type) {
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}

var numericLit = regexp.MustCompile(`^-?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$`)

func isNumericKind(k string) bool {
	return k == model.KindInt || k == model.KindFloat || k == model.KindDecimal
}

// literal renders a value as an SQL literal for display (preview, copy as SQL).
func (d dialect) literal(v value, col model.Column) string {
	if v.isDefault {
		return "DEFAULT"
	}
	switch a := v.arg.(type) {
	case nil:
		return "NULL"
	case bool:
		if d.driver == model.DriverPostgres {
			if a {
				return "TRUE"
			}
			return "FALSE"
		}
		if a {
			return "1"
		}
		return "0"
	case int64:
		return strconv.FormatInt(a, 10)
	case int:
		return strconv.Itoa(a)
	case uint64:
		return strconv.FormatUint(a, 10)
	case float64:
		if math.IsNaN(a) || math.IsInf(a, 0) {
			return d.quoteString(strconv.FormatFloat(a, 'g', -1, 64))
		}
		return strconv.FormatFloat(a, 'g', -1, 64)
	case []byte:
		if d.driver == model.DriverPostgres {
			return `'\x` + hex.EncodeToString(a) + `'::bytea`
		}
		return "X'" + hex.EncodeToString(a) + "'"
	case string:
		if isNumericKind(col.Kind) && numericLit.MatchString(a) {
			return a
		}
		return d.quoteString(a)
	}
	return d.quoteString(fmt.Sprint(v.arg))
}

func (d dialect) quoteString(s string) string {
	s = strings.ReplaceAll(s, "'", "''")
	if d.driver == model.DriverMySQL {
		s = strings.ReplaceAll(s, `\`, `\\`)
	}
	return "'" + s + "'"
}

// stmt is a generated statement in two forms: parameterized (executed) and with
// inlined literals (displayed).
type stmt struct {
	sql       strings.Builder
	display   strings.Builder
	args      []any
	expectOne bool     // UPDATE/DELETE by PK: must affect exactly 1 row
	returning []string // INSERT ... RETURNING these PK columns
	pkAutoInc string   // mysql: PK column filled from LastInsertId
	pkValues  map[string]any
}

func (s *stmt) raw(text string) {
	s.sql.WriteString(text)
	s.display.WriteString(text)
}

func (s *stmt) val(d dialect, v value, col model.Column) {
	s.display.WriteString(d.literal(v, col))
	if v.isDefault {
		s.sql.WriteString("DEFAULT")
		return
	}
	ph, arg := d.bindArg(v, col, len(s.args)+1)
	s.args = append(s.args, arg)
	s.sql.WriteString(ph)
}

// Literal renders a wire value as an SQL literal in the driver's dialect (used by
// export and copy-as-SQL).
func Literal(driverName string, v any, col model.Column) string {
	d := dialect{driver: driverName}
	val, err := decode(v, col, driverName)
	if err != nil {
		return "NULL"
	}
	return d.literal(val, col)
}
