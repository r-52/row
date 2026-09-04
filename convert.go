package row

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"time"
)

// This file assigns driver values to Go destinations.
//
// database/sql has an equivalent, but it is unexported, and row needs to own
// this step for two reasons. First, it is how a LEFT JOIN can leave a nested
// struct pointer nil: row must see that every column belonging to that struct
// was NULL, which is invisible if the driver value goes straight into a field.
// Second, it is where scan errors get the context that makes them useful —
// which column, which field, which types.
//
// Drivers hand back a small set of types: nil, int64, float64, bool, []byte,
// string and time.Time (database/sql guarantees this much, and permits others).
// Everything below maps those onto arbitrary Go destinations.

var (
	timeType    = reflect.TypeOf(time.Time{})
	scannerType = reflect.TypeOf((*sql.Scanner)(nil)).Elem()
	valuerType  = reflect.TypeOf((*driver.Valuer)(nil)).Elem()
)

// assign copies src into dst, which must be settable.
func assign(dst reflect.Value, src any) error {
	// A destination that knows how to scan itself always wins. This is what
	// makes sql.Null[T], sql.NullString, pgtype values and user-defined types
	// work without row knowing anything about them.
	if dst.CanAddr() {
		if sc, ok := dst.Addr().Interface().(sql.Scanner); ok {
			return sc.Scan(src)
		}
	}

	if src == nil {
		return assignNull(dst)
	}

	// Exact type match is the common case and costs nothing to check.
	//
	// Byte slices are excluded: assigning one directly would alias the
	// driver's buffer, which it is entitled to overwrite on the next row. They
	// fall through to the reflect.Slice case below, which copies.
	sv := reflect.ValueOf(src)
	if sv.Type() == dst.Type() && !isByteSlice(dst.Type()) {
		dst.Set(sv)
		return nil
	}

	switch dst.Kind() {
	case reflect.Pointer:
		// Allocate on demand and assign through.
		elem := reflect.New(dst.Type().Elem())
		if err := assign(elem.Elem(), src); err != nil {
			return err
		}
		dst.Set(elem)
		return nil

	case reflect.Interface:
		if dst.NumMethod() == 0 {
			dst.Set(sv)
			return nil
		}

	case reflect.String:
		s, err := toString(src)
		if err != nil {
			return err
		}
		dst.SetString(s)
		return nil

	case reflect.Bool:
		b, err := toBool(src)
		if err != nil {
			return err
		}
		dst.SetBool(b)
		return nil

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := toInt64(src)
		if err != nil {
			return err
		}
		if dst.OverflowInt(n) {
			return fmt.Errorf("value %d overflows %s", n, dst.Type())
		}
		dst.SetInt(n)
		return nil

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := toInt64(src)
		if err != nil {
			return err
		}
		if n < 0 {
			return fmt.Errorf("value %d is negative and %s is unsigned", n, dst.Type())
		}
		if dst.OverflowUint(uint64(n)) {
			return fmt.Errorf("value %d overflows %s", n, dst.Type())
		}
		dst.SetUint(uint64(n))
		return nil

	case reflect.Float32, reflect.Float64:
		f, err := toFloat64(src)
		if err != nil {
			return err
		}
		if dst.OverflowFloat(f) {
			return fmt.Errorf("value %v overflows %s", f, dst.Type())
		}
		dst.SetFloat(f)
		return nil

	case reflect.Slice:
		if dst.Type().Elem().Kind() == reflect.Uint8 {
			b, err := toBytes(src)
			if err != nil {
				return err
			}
			// Copy: the driver may reuse its buffer after the next row.
			out := reflect.MakeSlice(dst.Type(), len(b), len(b))
			reflect.Copy(out, reflect.ValueOf(b))
			dst.Set(out)
			return nil
		}

	case reflect.Array:
		if dst.Type().Elem().Kind() == reflect.Uint8 {
			b, err := toBytes(src)
			if err != nil {
				return err
			}
			if len(b) != dst.Len() {
				return fmt.Errorf("cannot fit %d bytes into %s", len(b), dst.Type())
			}
			reflect.Copy(dst, reflect.ValueOf(b))
			return nil
		}

	case reflect.Struct:
		if dst.Type() == timeType {
			t, ok := src.(time.Time)
			if !ok {
				return fmt.Errorf("cannot convert %T to time.Time", src)
			}
			dst.Set(reflect.ValueOf(t))
			return nil
		}
	}

	// Last resort: let reflect try. This covers named types over the basic
	// kinds and any driver that returns something exotic but convertible.
	if sv.Type().ConvertibleTo(dst.Type()) {
		// Guard against nonsense conversions reflect would happily perform,
		// such as int64 to string producing a rune.
		if !(sv.Kind() == reflect.Int64 && dst.Kind() == reflect.String) {
			dst.Set(sv.Convert(dst.Type()))
			return nil
		}
	}
	return fmt.Errorf("cannot convert %T to %s", src, dst.Type())
}

// isByteSlice reports whether t is a slice of bytes, whose contents must be
// copied out of the driver's buffer rather than referenced.
func isByteSlice(t reflect.Type) bool {
	return t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8
}

// assignNull writes the Go representation of SQL NULL into dst, or explains
// why dst cannot hold one.
func assignNull(dst reflect.Value) error {
	switch dst.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		dst.SetZero()
		return nil
	default:
		return fmt.Errorf("cannot scan NULL into %s; use a pointer or sql.Null[%s]",
			dst.Type(), dst.Type())
	}
}

func toString(src any) (string, error) {
	switch v := src.(type) {
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	case bool:
		return strconv.FormatBool(v), nil
	case time.Time:
		return v.Format(time.RFC3339Nano), nil
	}
	if rv := reflect.ValueOf(src); rv.Kind() == reflect.String {
		return rv.String(), nil
	}
	return "", fmt.Errorf("cannot convert %T to string", src)
}

func toBytes(src any) ([]byte, error) {
	switch v := src.(type) {
	case []byte:
		return v, nil
	case string:
		return []byte(v), nil
	}
	if rv := reflect.ValueOf(src); rv.Kind() == reflect.String {
		return []byte(rv.String()), nil
	}
	return nil, fmt.Errorf("cannot convert %T to []byte", src)
}

func toBool(src any) (bool, error) {
	switch v := src.(type) {
	case bool:
		return v, nil
	case int64:
		switch v {
		case 0:
			return false, nil
		case 1:
			return true, nil
		}
		return false, fmt.Errorf("cannot convert integer %d to bool", v)
	case []byte:
		return strconv.ParseBool(string(v))
	case string:
		return strconv.ParseBool(v)
	}
	return false, fmt.Errorf("cannot convert %T to bool", src)
}

func toInt64(src any) (int64, error) {
	switch v := src.(type) {
	case int64:
		return v, nil
	case float64:
		if v != math.Trunc(v) {
			return 0, fmt.Errorf("cannot convert %v to an integer without losing precision", v)
		}
		if v > math.MaxInt64 || v < math.MinInt64 {
			return 0, fmt.Errorf("value %v is out of int64 range", v)
		}
		return int64(v), nil
	case bool:
		if v {
			return 1, nil
		}
		return 0, nil
	case []byte:
		return strconv.ParseInt(string(v), 10, 64)
	case string:
		return strconv.ParseInt(v, 10, 64)
	}
	rv := reflect.ValueOf(src)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		u := rv.Uint()
		if u > math.MaxInt64 {
			return 0, fmt.Errorf("value %d is out of int64 range", u)
		}
		return int64(u), nil
	}
	return 0, fmt.Errorf("cannot convert %T to an integer", src)
}

func toFloat64(src any) (float64, error) {
	switch v := src.(type) {
	case float64:
		return v, nil
	case float32:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case []byte:
		return strconv.ParseFloat(string(v), 64)
	case string:
		return strconv.ParseFloat(v, 64)
	}
	rv := reflect.ValueOf(src)
	switch rv.Kind() {
	case reflect.Float32, reflect.Float64:
		return rv.Float(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), nil
	}
	return 0, fmt.Errorf("cannot convert %T to a float", src)
}

// isLeaf reports whether t should be scanned into directly rather than
// descended into as a nested struct.
func isLeaf(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if reflect.PointerTo(t).Implements(scannerType) || t.Implements(valuerType) {
		return true
	}
	if t == timeType {
		return true
	}
	return t.Kind() != reflect.Struct
}
