package godatabend

import (
	"bytes"
	"database/sql/driver"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"
)

var (
	reflectTypeString      = reflect.TypeOf("")
	reflectTypeBool        = reflect.TypeOf(true)
	reflectTypeTime        = reflect.TypeOf(time.Time{})
	reflectTypeEmptyStruct = reflect.TypeOf(struct{}{})
	reflectTypeInt8        = reflect.TypeOf(int8(0))
	reflectTypeInt16       = reflect.TypeOf(int16(0))
	reflectTypeInt32       = reflect.TypeOf(int32(0))
	reflectTypeInt64       = reflect.TypeOf(int64(0))
	reflectTypeUInt8       = reflect.TypeOf(uint8(0))
	reflectTypeUInt16      = reflect.TypeOf(uint16(0))
	reflectTypeUInt32      = reflect.TypeOf(uint32(0))
	reflectTypeUInt64      = reflect.TypeOf(uint64(0))
	reflectTypeFloat32     = reflect.TypeOf(float32(0))
	reflectTypeFloat64     = reflect.TypeOf(float64(0))
)

func readNumber(s io.RuneScanner) (string, error) {
	var builder bytes.Buffer

loop:
	for {
		r := read(s)

		switch r {
		case eof:
			break loop
		case ',', ']', ')':
			_ = s.UnreadRune()
			break loop
		}

		builder.WriteRune(r)
	}

	return builder.String(), nil
}

func readUnquoted(s io.RuneScanner, length int) (string, error) {
	var builder bytes.Buffer

	runesRead := 0
loop:
	for length == 0 || runesRead < length {
		r := read(s)

		switch r {
		case eof:
			break loop
		case '\\':
			escaped, err := readEscaped(s)
			if err != nil {
				return "", fmt.Errorf("incorrect escaping in string: %v", err)
			}
			r = escaped
		}

		builder.WriteRune(r)
		runesRead++
	}

	if length != 0 && runesRead != length {
		return "", fmt.Errorf("unexpected string length %d, expected %d", runesRead, length)
	}

	return builder.String(), nil
}

func readString(s io.RuneScanner, length int, unquote bool) (string, error) {
	if unquote {
		if r := read(s); r != '\'' {
			return "", fmt.Errorf("unexpected character instead of a quote")
		}
	}

	str, err := readUnquoted(s, length)
	if err != nil {
		return "", fmt.Errorf("failed to read string")
	}

	if unquote {
		if r := read(s); r != '\'' {
			return "", fmt.Errorf("unexpected character instead of a quote")
		}
	}

	return str, nil
}

func peakNull(s io.RuneScanner) bool {
	r := read(s)
	if r != 'N' {
		_ = s.UnreadRune()
		return false
	}

	r = read(s)
	if r != 'U' {
		_ = s.UnreadRune()
		_ = s.UnreadRune()
		return false
	}

	r = read(s)
	if r != 'L' {
		_ = s.UnreadRune()
		_ = s.UnreadRune()
		_ = s.UnreadRune()
		return false
	}

	r = read(s)
	if r != 'L' {
		_ = s.UnreadRune()
		_ = s.UnreadRune()
		_ = s.UnreadRune()
		_ = s.UnreadRune()
		return false
	}

	r = read(s)
	if r != eof {
		_ = s.UnreadRune()
		_ = s.UnreadRune()
		_ = s.UnreadRune()
		_ = s.UnreadRune()
		_ = s.UnreadRune()
		return false
	}

	return true
}

// DataParser implements parsing of a driver value and reporting its type.
type DataParser interface {
	Parse(io.RuneScanner) (driver.Value, error)
	Nullable() bool
	Type() reflect.Type
}

type nullParser struct {
	DataParser
}

func (p *nullParser) Parse(s io.RuneScanner) (driver.Value, error) {
	var dB *bytes.Buffer

	dType := p.DataParser.Type()

	switch dType {
	case reflectTypeInt8, reflectTypeInt16, reflectTypeInt32, reflectTypeInt64,
		reflectTypeUInt8, reflectTypeUInt16, reflectTypeUInt32, reflectTypeUInt64,
		reflectTypeFloat32, reflectTypeFloat64:
		d, err := readNumber(s)
		if err != nil {
			return nil, fmt.Errorf("error: %v", err)
		}

		dB = bytes.NewBufferString(d)
	case reflectTypeString:
		runes := ""
		iter := 0

		isNotString := false
		for {
			r, size, err := s.ReadRune()
			if err == io.EOF && size == 0 {
				break
			}

			if err != nil {
				return nil, fmt.Errorf("error: %v", err)
			}

			if r != '\'' && iter == 0 {
				err = s.UnreadRune()
				if err != nil {
					return nil, fmt.Errorf("error: %v", err)
				}
				d := readRaw(s)
				dB = d
				isNotString = true
				break
			}

			isEscaped := false
			if r == '\\' {
				escaped, err := readEscaped(s)
				if err != nil {
					return "", fmt.Errorf("incorrect escaping in string: %v", err)
				}

				isEscaped = true
				r = escaped
				if r == '\'' {
					runes += string('\\')
				}
			}

			runes += string(r)

			if r == '\'' && iter != 0 && !isEscaped {
				break
			}
			iter++
		}

		if bytes.Equal([]byte(runes), []byte(`'N'`)) {
			return nil, nil
		}

		if !isNotString {
			dB = bytes.NewBufferString(runes)
		}
	case reflectTypeTime:
		runes := ""

		iter := 0
		for {
			r, _, err := s.ReadRune()
			if err != nil {
				if err != io.EOF {
					return nil, fmt.Errorf("unexpected error on ReadRune: %v", err)
				}
				break
			}

			runes += string(r)

			if r == '\'' && iter != 0 {
				break
			}
			iter++
		}

		if runes == "0000-00-00" || runes == "0000-00-00 00:00:00" {
			return time.Time{}, nil
		}

		if bytes.Equal([]byte(runes), []byte(`'\N'`)) {
			return nil, nil
		}

		dB = bytes.NewBufferString(runes)
	case reflectTypeEmptyStruct:
		d := readRaw(s)
		dB = d
	default:
		d := readRaw(s)
		dB = d
	}

	if bytes.Equal(dB.Bytes(), []byte(`\N`)) {
		return nil, nil
	}

	return p.DataParser.Parse(dB)
}

type stringParser struct {
	unquote bool
	length  int
}

func (p *stringParser) Parse(s io.RuneScanner) (driver.Value, error) {
	return readString(s, p.length, p.unquote)
}

func (p *stringParser) Type() reflect.Type {
	return reflectTypeString
}

func (p *stringParser) Nullable() bool {
	return false
}

type booleanParser struct {
	length int
}

func (p *booleanParser) Parse(s io.RuneScanner) (driver.Value, error) {
	str, err := readUnquoted(s, p.length)
	if err != nil {
		return nil, fmt.Errorf("failed to read the string representation of boolean: %v", err)
	}
	return strconv.ParseBool(str)
}

func (p *booleanParser) Type() reflect.Type {
	return reflectTypeBool
}

func (p *booleanParser) Nullable() bool {
	return false
}

type dateTimeParser struct {
	unquote   bool
	format    string
	location  *time.Location
	precision int
}

func (p *dateTimeParser) Parse(s io.RuneScanner) (driver.Value, error) {
	l := len(p.format)
	if p.precision > 0 {
		if i := strings.Index(p.format, "."); i >= 0 {
			l = i + p.precision + 1
		}
	}

	str, err := readString(s, l, p.unquote)
	if err != nil {
		return nil, fmt.Errorf("failed to read the string representation of date or datetime: %v", err)
	}

	test := str
	if i := strings.Index(str, " "); i >= 0 {
		test = str[:i]
	}

	if test == "0000-00-00" {
		return time.Time{}, nil
	}

	return time.ParseInLocation(p.format, str, p.location)
}

func (p *dateTimeParser) Type() reflect.Type {
	return reflectTypeTime
}

func (p *dateTimeParser) Nullable() bool {
	return false
}

type tupleParser struct {
	args []DataParser
}

func (p *tupleParser) Type() reflect.Type {
	fields := make([]reflect.StructField, len(p.args))
	for i, arg := range p.args {
		fields[i].Name = "Field" + strconv.Itoa(i)
		fields[i].Type = arg.Type()
	}
	return reflect.StructOf(fields)
}

func (p *tupleParser) Nullable() bool {
	return false
}

func (p *tupleParser) Parse(s io.RuneScanner) (driver.Value, error) {
	r := read(s)
	if r != '(' {
		return nil, fmt.Errorf("unexpected character '%c', expected '(' at the beginning of tuple", r)
	}

	rStruct := reflect.New(p.Type()).Elem()
	for i, arg := range p.args {
		if i > 0 {
			r := read(s)
			if r != ',' {
				return nil, fmt.Errorf("unexpected character '%c', expected ',' between tuple elements", r)
			}
		}

		v, err := arg.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("failed to parse tuple element: %v", err)
		}

		rStruct.Field(i).Set(reflect.ValueOf(v))
	}

	r = read(s)
	if r != ')' {
		return nil, fmt.Errorf("unexpected character '%c', expected ')' at the end of tuple", r)
	}

	return rStruct.Interface(), nil
}

type arrayParser struct {
	arg DataParser
}

func (p *arrayParser) Type() reflect.Type {
	return reflect.SliceOf(p.arg.Type())
}

func (p *arrayParser) Nullable() bool {
	return false
}

func (p *arrayParser) Parse(s io.RuneScanner) (driver.Value, error) {
	r := read(s)
	if r != '[' {
		return nil, fmt.Errorf("unexpected character '%c', expected '[' at the beginning of array", r)
	}

	slice := reflect.MakeSlice(p.Type(), 0, 0)
	for i := 0; ; i++ {
		r := read(s)
		_ = s.UnreadRune()
		if r == ']' {
			break
		}

		v, err := p.arg.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("failed to parse array element: %v", err)
		}

		if v == nil {
			if reflect.TypeOf(p.arg) != reflect.TypeOf(&nullParser{}) {
				//need check if v is nil: panic otherwise
				return nil, fmt.Errorf("unexpected nil element")
			}
		} else {
			slice = reflect.Append(slice, reflect.ValueOf(v))
		}

		r = read(s)
		if r != ',' {
			_ = s.UnreadRune()
		}
	}

	r = read(s)
	if r != ']' {
		return nil, fmt.Errorf("unexpected character '%c', expected ']' at the end of array", r)
	}

	return slice.Interface(), nil
}

type mapParser struct {
	key   DataParser
	value DataParser
}

func (p *mapParser) Type() reflect.Type {
	return reflect.MapOf(p.key.Type(), p.value.Type())
}

func (p *mapParser) Nullable() bool {
	return false
}

func (p *mapParser) Parse(s io.RuneScanner) (driver.Value, error) {
	r := read(s)
	if r != '{' {
		return nil, fmt.Errorf("unexpected character '%c', expected '{' at the beginning of map", r)
	}

	m := reflect.MakeMap(p.Type())
	for i := 0; ; i++ {
		r := read(s)
		_ = s.UnreadRune()
		if r == '}' {
			break
		}

		k, err := p.key.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("failed to parse map key: %v", err)
		}

		r = read(s)
		if r != ':' {
			return nil, fmt.Errorf("unexpected character '%c', expected ':' at the middle of map", r)
		}

		v, err := p.value.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("failed to parse map value: %v", err)
		}

		m.SetMapIndex(reflect.ValueOf(k), reflect.ValueOf(v))

		r = read(s)
		if r != ',' {
			_ = s.UnreadRune()
		}
	}

	r = read(s)
	if r != '}' {
		return nil, fmt.Errorf("unexpected character '%c', expected '}' at the end of map", r)
	}

	return m.Interface(), nil
}

func newDateTimeParser(format string, loc *time.Location, precision int, unquote bool) (DataParser, error) {
	return &dateTimeParser{
		unquote:   unquote,
		format:    format,
		location:  loc,
		precision: precision,
	}, nil
}

type intParser struct {
	signed  bool
	bitSize int
}

func (p *intParser) Parse(s io.RuneScanner) (driver.Value, error) {
	repr, err := readNumber(s)
	if err != nil {
		return nil, err
	}

	if p.signed {
		f, err := strconv.ParseFloat(repr, 64)
		//v, err := strconv.ParseInt(repr, 10, p.bitSize)
		switch p.bitSize {
		case 8:
			return int8(f), err
		case 16:
			return int16(f), err
		case 32:
			return int32(f), err
		case 64:
			return int64(f), err
		default:
			panic("unsupported bit size")
		}
	} else {
		f, err := strconv.ParseFloat(repr, 64)
		//v, err := strconv.ParseUint(repr, 10, p.bitSize)
		switch p.bitSize {
		case 8:
			return uint8(f), err
		case 16:
			return uint16(f), err
		case 32:
			return uint32(f), err
		case 64:
			return uint64(f), err
		default:
			panic("unsupported bit size")
		}
	}
}

func (p *intParser) Type() reflect.Type {
	if p.signed {
		switch p.bitSize {
		case 8:
			return reflectTypeInt8
		case 16:
			return reflectTypeInt16
		case 32:
			return reflectTypeInt32
		case 64:
			return reflectTypeInt64
		default:
			panic("unsupported bit size")
		}
	} else {
		switch p.bitSize {
		case 8:
			return reflectTypeUInt8
		case 16:
			return reflectTypeUInt16
		case 32:
			return reflectTypeUInt32
		case 64:
			return reflectTypeUInt64
		default:
			panic("unsupported bit size")
		}
	}
}

func (p *intParser) Nullable() bool {
	return false
}

type floatParser struct {
	bitSize int
}

func (p *floatParser) Parse(s io.RuneScanner) (driver.Value, error) {
	repr, err := readNumber(s)
	if err != nil {
		return nil, err
	}

	v, err := strconv.ParseFloat(repr, p.bitSize)
	switch p.bitSize {
	case 32:
		return float32(v), err
	case 64:
		return float64(v), err
	default:
		panic("unsupported bit size")
	}
}

func (p *floatParser) Type() reflect.Type {
	switch p.bitSize {
	case 32:
		return reflectTypeFloat32
	case 64:
		return reflectTypeFloat64
	default:
		panic("unsupported bit size")
	}
}

func (p *floatParser) Nullable() bool {
	return false
}

type nothingParser struct{}

func (p *nothingParser) Parse(s io.RuneScanner) (driver.Value, error) {
	return nil, nil
}

func (p *nothingParser) Type() reflect.Type {
	return reflectTypeEmptyStruct
}

func (p *nothingParser) Nullable() bool {
	return true
}

// DataParserOptions describes DataParser options.
// Ex.: Fields Location and UseDBLocation specify timezone options.
type DataParserOptions struct {
	// Location describes default location for DateTime and Date field without Timezone argument.
	Location *time.Location
	// UseDBLocation if false: always use Location, ignore DateTime argument.
	UseDBLocation bool
}

// NewDataParser creates a new DataParser based on the
// given TypeDesc.
func NewDataParser(t *TypeDesc, opt *DataParserOptions) (DataParser, error) {
	return newDataParser(t, false, opt)
}

type nullableParser struct {
	innerParser DataParser
	innerType   string
}

func (p *nullableParser) Parse(s io.RuneScanner) (driver.Value, error) {
	switch p.innerType {
	case "String":
		return p.innerParser.Parse(s)
	default:
		// for compatibility with old databend versions
		if peakNull(s) {
			return nil, nil
		}
		return p.innerParser.Parse(s)
	}
}

func (p *nullableParser) Type() reflect.Type {
	return p.innerParser.Type()
}

func (p *nullableParser) Nullable() bool {
	return true
}

func newDataParser(t *TypeDesc, unquote bool, opt *DataParserOptions) (DataParser, error) {
	if t.Nullable {
		t.Nullable = false
		inner, err := newDataParser(t, unquote, opt)
		if err != nil {
			return nil, err
		}
		return &nullableParser{innerParser: inner, innerType: t.Name}, nil
	}
	switch t.Name {
	case "Nothing":
		return &nothingParser{}, nil
	case "Nullable":
		inner, err := newDataParser(t.Args[0], unquote, opt)
		if err != nil {
			return nil, err
		}
		return &nullableParser{innerParser: inner, innerType: t.Args[0].Name}, nil
	case "NULL":
		inner := &stringParser{unquote: unquote}
		return &nullableParser{innerParser: inner, innerType: "String"}, nil
	case "Date":
		loc := time.UTC
		if opt != nil && opt.Location != nil {
			loc = opt.Location
		}
		return newDateTimeParser(dateFormat, loc, 0, unquote)
	case "DateTime":
		loc := time.UTC
		if (opt == nil || opt.Location == nil || opt.UseDBLocation) && len(t.Args) > 0 {
			var err error
			loc, err = time.LoadLocation(t.Args[0].Name)
			if err != nil {
				return nil, err
			}
		} else if opt != nil && opt.Location != nil {
			loc = opt.Location
		}
		return newDateTimeParser(timeFormat, loc, 0, unquote)
	case "DateTime64":
		if len(t.Args) < 1 {
			return nil, fmt.Errorf("tick size not specified for DateTime64")
		}

		loc := time.UTC
		if (opt == nil || opt.Location == nil || opt.UseDBLocation) && len(t.Args) > 1 {
			var err error
			loc, err = time.LoadLocation(t.Args[1].Name)
			if err != nil {
				return nil, err
			}
		} else if opt != nil && opt.Location != nil {
			loc = opt.Location
		}

		precision, err := strconv.Atoi(t.Args[0].Name)
		if err != nil {
			return nil, err
		}

		if precision < 0 {
			return nil, fmt.Errorf("malformed tick size specified for DateTime64")
		}

		return newDateTimeParser(dateTime64Format, loc, precision, unquote)
	case "Timestamp":
		loc := time.UTC
		if opt != nil && opt.Location != nil {
			loc = opt.Location
		}

		return newDateTimeParser(dateTime64Format, loc, 1, unquote)

	case "Boolean":
		return &booleanParser{}, nil
	case "UInt8":
		return &intParser{false, 8}, nil
	case "UInt16":
		return &intParser{false, 16}, nil
	case "UInt32":
		return &intParser{false, 32}, nil
	case "UInt64":
		return &intParser{false, 64}, nil
	case "Int8":
		return &intParser{true, 8}, nil
	case "Int16":
		return &intParser{true, 16}, nil
	case "Int32":
		return &intParser{true, 32}, nil
	case "Int64":
		return &intParser{true, 64}, nil
	case "Float32":
		return &floatParser{32}, nil
	case "Float64":
		return &floatParser{64}, nil
	case "Decimal", "String", "Enum8", "Bitmap", "Enum16", "UUID", "IPv4", "IPv6", "Variant", "VariantObject":
		return &stringParser{unquote: unquote}, nil
	case "FixedString":
		if len(t.Args) != 1 {
			return nil, fmt.Errorf("length not specified for FixedString")
		}
		length, err := strconv.Atoi(t.Args[0].Name)
		if err != nil {
			return nil, fmt.Errorf("malformed length specified for FixedString: %v", err)
		}
		return &stringParser{unquote: unquote, length: length}, nil
	case "Array":
		if len(t.Args) != 1 {
			return nil, fmt.Errorf("element type not specified for Array")
		}
		subParser, err := newDataParser(t.Args[0], true, opt)
		if err != nil {
			return nil, fmt.Errorf("failed to create parser for array elements: %v", err)
		}
		return &arrayParser{subParser}, nil
	case "Tuple":
		if len(t.Args) < 1 {
			return nil, fmt.Errorf("element types not specified for Tuple")
		}
		subParsers := make([]DataParser, len(t.Args))
		for i, arg := range t.Args {
			subParser, err := newDataParser(arg, true, opt)
			if err != nil {
				return nil, fmt.Errorf("failed to create parser for tuple element: %v", err)
			}
			subParsers[i] = subParser
		}
		return &tupleParser{subParsers}, nil
	case "Map":
		if len(t.Args) != 2 {
			return nil, fmt.Errorf("incorrect number of arguments for Map")
		}
		keyParser, err := newDataParser(t.Args[0], true, opt)
		if err != nil {
			return nil, fmt.Errorf("failed to create parser for map keys: %v", err)
		}
		valueParser, err := newDataParser(t.Args[1], true, opt)
		if err != nil {
			return nil, fmt.Errorf("failed to create parser for map values: %v", err)
		}
		return &mapParser{
			key:   keyParser,
			value: valueParser,
		}, nil
	default:
		return nil, fmt.Errorf("type %s is not supported", t.Name)
	}
}
