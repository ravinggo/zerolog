package zerolog

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"sync"
	"time"
)

var eventPool = &sync.Pool{
	New: func() interface{} {
		return &Event{
			buf:       make([]byte, 0, 1024),
			fieldsBuf: make([]byte, 0, 1024),
			json:      true,
		}
	},
}

// Event represents a log event. It is instanced by one of the level method of
// Logger and finalized by the Msg or Msgf method.
type Event struct {
	buf       []byte
	fieldsBuf []byte
	w         LevelWriter
	level     Level
	done      func(msg string)
	stack     bool            // enable error stack trace
	ch        []Hook          // hooks from context
	ctx       context.Context // Optional Go context for event
	json      bool            // JSON format
}

func putEvent(e *Event) {
	// Proper usage of a sync.Pool requires each entry to have approximately
	// the same memory cost. To obtain this property when the stored type
	// contains a variably-sized buffer, we add a hard limit on the maximum buffer
	// to place back in the pool.
	//
	// See https://golang.org/issue/23199
	const maxSize = 1 << 16 // 64KiB
	if cap(e.buf) > maxSize {
		return
	}
	eventPool.Put(e)
}

// LogObjectMarshaler provides a strongly-typed and encoding-agnostic interface
// to be implemented by types used with Event/Context's Object methods.
type LogObjectMarshaler interface {
	MarshalZerologObject(e *Event)
}

// LogArrayMarshaler provides a strongly-typed and encoding-agnostic interface
// to be implemented by types used with Event/Context's Array methods.
type LogArrayMarshaler interface {
	MarshalZerologArray(a *Array)
}

func newEvent(w LevelWriter, level Level, isJson bool) *Event {
	e := eventPool.Get().(*Event)
	e.buf = e.buf[:0]
	e.fieldsBuf = e.fieldsBuf[:0]
	e.json = isJson
	e.ch = nil
	if e.json {
		e.buf = enc.AppendBeginMarker(e.buf)
	} else {
		e.fieldsBuf = enc.AppendBeginMarker(e.fieldsBuf)
	}
	e.w = w
	e.level = level
	e.stack = false
	return e
}

func (e *Event) write() (err error) {
	if e == nil {
		return nil
	}
	if e.level != Disabled {
		if e.json {
			e.buf = enc.AppendEndMarker(e.buf)
			e.buf = enc.AppendLineBreak(e.buf)
		} else {
			e.fieldsBuf = enc.AppendEndMarker(e.fieldsBuf)
			e.fieldsBuf = enc.AppendLineBreak(e.fieldsBuf)
			e.buf = append(e.buf, e.fieldsBuf...)
		}

		if e.w != nil {
			_, err = e.w.WriteLevel(e.level, e.buf)
		}
	}
	putEvent(e)
	return
}

// Enabled return false if the *Event is going to be filtered out by
// log level or sampling.
func (e *Event) Enabled() bool {
	return e != nil && e.level != Disabled
}

// Discard disables the event so Msg(f) won't print it.
func (e *Event) Discard() *Event {
	if e == nil {
		return e
	}
	e.level = Disabled
	return nil
}

// Msg sends the *Event with msg added as the message field if not empty.
//
// NOTICE: once this method is called, the *Event should be disposed.
// Calling Msg twice can have unexpected result.
func (e *Event) Msg(msg string) {
	if e == nil {
		return
	}
	e.msg(msg)
}

// Send is equivalent to calling Msg("").
//
// NOTICE: once this method is called, the *Event should be disposed.
func (e *Event) Send() {
	if e == nil {
		return
	}
	e.msg("")
}

// Msgf sends the event with formatted msg added as the message field if not empty.
//
// NOTICE: once this method is called, the *Event should be disposed.
// Calling Msgf twice can have unexpected result.
func (e *Event) Msgf(format string, v ...interface{}) {
	if e == nil {
		return
	}
	e.msg(fmt.Sprintf(format, v...))
}

func (e *Event) MsgFunc(createMsg func() string) {
	if e == nil {
		return
	}
	e.msg(createMsg())
}

func (e *Event) msg(msg string) {
	for _, hook := range e.ch {
		hook.Run(e, e.level, msg)
	}
	if msg != "" {
		if e.json {
			e.buf = enc.AppendString(enc.AppendKey(e.buf, MessageFieldName), msg)
		} else {
			e.buf = append(e.buf, msg...)
			e.buf = append(e.buf, '\t')
		}
	}
	if e.done != nil {
		defer e.done(msg)
	}
	if err := e.write(); err != nil {
		if ErrorHandler != nil {
			ErrorHandler(err)
		} else {
			fmt.Fprintf(os.Stderr, "zerolog: could not write event: %v\n", err)
		}
	}
}

// Fields is a helper function to use a map or slice to set fields using type assertion.
// Only map[string]interface{} and []interface{} are accepted. []interface{} must
// alternate string keys and arbitrary values, and extraneous ones are ignored.
func (e *Event) Fields(fields interface{}) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = appendFields(e.buf, fields, e.stack)
	} else {
		e.fieldsBuf = appendFields(e.fieldsBuf, fields, e.stack)
	}

	return e
}

// Dict adds the field key with a dict to the event context.
// Use zerolog.Dict() to create the dictionary.
func (e *Event) Dict(key string, dict *Event) *Event {
	if e == nil {
		return e
	}
	dict.buf = enc.AppendEndMarker(dict.buf)
	if e.json {
		e.buf = append(enc.AppendKey(e.buf, key), dict.buf...)
	} else {
		e.fieldsBuf = append(enc.AppendKey(e.fieldsBuf, key), dict.buf...)
	}

	putEvent(dict)
	return e
}

// Dict creates an Event to be used with the *Event.Dict method.
// Call usual field methods like Str, Int etc to add fields to this
// event and give it as argument the *Event.Dict method.
func Dict() *Event {
	return newEvent(nil, 0, true)
}

// Array adds the field key with an array to the event context.
// Use zerolog.Arr() to create the array or pass a type that
// implement the LogArrayMarshaler interface.
func (e *Event) Array(key string, arr LogArrayMarshaler) *Event {
	if e == nil {
		return e
	}
	var a *Array
	if aa, ok := arr.(*Array); ok {
		a = aa
	} else {
		a = Arr()
		arr.MarshalZerologArray(a)
	}
	if e.json {
		e.buf = enc.AppendKey(e.buf, key)
		e.buf = a.write(e.buf)
	} else {
		e.fieldsBuf = enc.AppendKey(e.fieldsBuf, key)
		e.fieldsBuf = a.write(e.fieldsBuf)
	}

	return e
}

func (e *Event) appendObject(obj LogObjectMarshaler) {
	e.appendSwitch(enc.AppendBeginMarker)
	obj.MarshalZerologObject(e)
	e.appendSwitch(enc.AppendEndMarker)
}

func (e *Event) appendSwitch(f func([]byte) []byte) {
	if e.json {
		e.buf = f(e.buf)
	} else {
		e.fieldsBuf = f(e.fieldsBuf)
	}
}

func (e *Event) appendSwitchKey(f func([]byte, string) []byte, key string) {
	if e.json {
		e.buf = f(e.buf, key)
	} else {
		e.fieldsBuf = f(e.fieldsBuf, key)
	}
}

// Object marshals an object that implement the LogObjectMarshaler interface.
func (e *Event) Object(key string, obj LogObjectMarshaler) *Event {
	if e == nil {
		return e
	}
	e.appendSwitchKey(enc.AppendKey, key)
	if obj == nil {
		e.appendSwitch(enc.AppendNil)

		return e
	}

	e.appendObject(obj)
	return e
}

// Func allows an anonymous func to run only if the event is enabled.
func (e *Event) Func(f func(e *Event)) *Event {
	if e != nil && e.Enabled() {
		f(e)
	}
	return e
}

// EmbedObject marshals an object that implement the LogObjectMarshaler interface.
func (e *Event) EmbedObject(obj LogObjectMarshaler) *Event {
	if e == nil {
		return e
	}
	if obj == nil {
		return e
	}
	obj.MarshalZerologObject(e)
	return e
}

// Str adds the field key with val as a string to the *Event context.
func (e *Event) Str(key, val string) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendString(enc.AppendKey(e.buf, key), val)
	} else {
		e.fieldsBuf = enc.AppendString(enc.AppendKey(e.fieldsBuf, key), val)
	}

	return e
}

// Strs adds the field key with vals as a []string to the *Event context.
func (e *Event) Strs(key string, vals []string) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendStrings(enc.AppendKey(e.buf, key), vals)
	} else {
		e.fieldsBuf = enc.AppendStrings(enc.AppendKey(e.fieldsBuf, key), vals)
	}
	return e
}

// Stringer adds the field key with val.String() (or null if val is nil)
// to the *Event context.
func (e *Event) Stringer(key string, val fmt.Stringer) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendStringer(enc.AppendKey(e.buf, key), val)
	} else {
		e.fieldsBuf = enc.AppendStringer(enc.AppendKey(e.fieldsBuf, key), val)
	}

	return e
}

// Stringers adds the field key with vals where each individual val
// is used as val.String() (or null if val is empty) to the *Event
// context.
func (e *Event) Stringers(key string, vals []fmt.Stringer) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendStringers(enc.AppendKey(e.buf, key), vals)
	} else {
		e.fieldsBuf = enc.AppendStringers(enc.AppendKey(e.fieldsBuf, key), vals)
	}

	return e
}

// Bytes adds the field key with val as a string to the *Event context.
//
// Runes outside of normal ASCII ranges will be hex-encoded in the resulting
// JSON.
func (e *Event) Bytes(key string, val []byte) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendBytes(enc.AppendKey(e.buf, key), val)
	} else {
		e.fieldsBuf = enc.AppendBytes(enc.AppendKey(e.fieldsBuf, key), val)
	}

	return e
}

// Hex adds the field key with val as a hex string to the *Event context.
func (e *Event) Hex(key string, val []byte) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendHex(enc.AppendKey(e.buf, key), val)
	} else {
		e.fieldsBuf = enc.AppendHex(enc.AppendKey(e.fieldsBuf, key), val)
	}

	return e
}

// RawJSON adds already encoded JSON to the log line under key.
//
// No sanity check is performed on b; it must not contain carriage returns and
// be valid JSON.
func (e *Event) RawJSON(key string, b []byte) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = appendJSON(enc.AppendKey(e.buf, key), b)
	} else {
		e.fieldsBuf = appendJSON(enc.AppendKey(e.fieldsBuf, key), b)
	}

	return e
}

// RawCBOR adds already encoded CBOR to the log line under key.
//
// No sanity check is performed on b
// Note: The full featureset of CBOR is supported as data will not be mapped to json but stored as data-url
func (e *Event) RawCBOR(key string, b []byte) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = appendCBOR(enc.AppendKey(e.buf, key), b)
	} else {
		e.fieldsBuf = appendCBOR(enc.AppendKey(e.fieldsBuf, key), b)
	}
	return e
}

// AnErr adds the field key with serialized err to the *Event context.
// If err is nil, no field is added.
func (e *Event) AnErr(key string, err error) *Event {
	if e == nil {
		return e
	}
	switch m := ErrorMarshalFunc(err).(type) {
	case nil:
		return e
	case LogObjectMarshaler:
		return e.Object(key, m)
	case error:
		if m == nil || isNilValue(m) {
			return e
		} else {
			return e.Str(key, m.Error())
		}
	case string:
		return e.Str(key, m)
	default:
		return e.Interface(key, m)
	}
}

// Errs adds the field key with errs as an array of serialized errors to the
// *Event context.
func (e *Event) Errs(key string, errs []error) *Event {
	if e == nil {
		return e
	}
	arr := Arr()
	for _, err := range errs {
		switch m := ErrorMarshalFunc(err).(type) {
		case LogObjectMarshaler:
			arr = arr.Object(m)
		case error:
			arr = arr.Err(m)
		case string:
			arr = arr.Str(m)
		default:
			arr = arr.Interface(m)
		}
	}

	return e.Array(key, arr)
}

// Err adds the field "error" with serialized err to the *Event context.
// If err is nil, no field is added.
//
// To customize the key name, change zerolog.ErrorFieldName.
//
// If Stack() has been called before and zerolog.ErrorStackMarshaler is defined,
// the err is passed to ErrorStackMarshaler and the result is appended to the
// zerolog.ErrorStackFieldName.
func (e *Event) Err(err error) *Event {
	if e == nil {
		return e
	}
	if e.stack && ErrorStackMarshaler != nil {
		switch m := ErrorStackMarshaler(err).(type) {
		case nil:
		case LogObjectMarshaler:
			e.Object(ErrorStackFieldName, m)
		case error:
			if m != nil && !isNilValue(m) {
				e.Str(ErrorStackFieldName, m.Error())
			}
		case string:
			e.Str(ErrorStackFieldName, m)
		default:
			e.Interface(ErrorStackFieldName, m)
		}
	}
	return e.AnErr(ErrorFieldName, err)
}

// Stack enables stack trace printing for the error passed to Err().
//
// ErrorStackMarshaler must be set for this method to do something.
func (e *Event) Stack() *Event {
	if e != nil {
		e.stack = true
	}
	return e
}

// Ctx adds the Go Context to the *Event context.  The context is not rendered
// in the output message, but is available to hooks and to Func() calls via the
// GetCtx() accessor. A typical use case is to extract tracing information from
// the Go Ctx.
func (e *Event) Ctx(ctx context.Context) *Event {
	if e != nil {
		e.ctx = ctx
	}

	return e
}

// GetCtx retrieves the Go context.Context which is optionally stored in the
// Event.  This allows Hooks and functions passed to Func() to retrieve values
// which are stored in the context.Context.  This can be useful in tracing,
// where span information is commonly propagated in the context.Context.
func (e *Event) GetCtx() context.Context {
	if e == nil || e.ctx == nil {
		return context.Background()
	}

	return e.ctx
}

// Bool adds the field key with val as a bool to the *Event context.
func (e *Event) Bool(key string, b bool) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendBool(enc.AppendKey(e.buf, key), b)
	} else {
		e.fieldsBuf = enc.AppendBool(enc.AppendKey(e.fieldsBuf, key), b)
	}

	return e
}

// Bools adds the field key with val as a []bool to the *Event context.
func (e *Event) Bools(key string, b []bool) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendBools(enc.AppendKey(e.buf, key), b)
	} else {
		e.fieldsBuf = enc.AppendBools(enc.AppendKey(e.fieldsBuf, key), b)
	}

	return e
}

// Int adds the field key with i as a int to the *Event context.
func (e *Event) Int(key string, i int) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendInt(enc.AppendKey(e.buf, key), i)
	} else {
		e.fieldsBuf = enc.AppendInt(enc.AppendKey(e.fieldsBuf, key), i)
	}

	return e
}

// Ints adds the field key with i as a []int to the *Event context.
func (e *Event) Ints(key string, i []int) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendInts(enc.AppendKey(e.buf, key), i)
	} else {
		e.fieldsBuf = enc.AppendInts(enc.AppendKey(e.fieldsBuf, key), i)
	}

	return e
}

// Int8 adds the field key with i as a int8 to the *Event context.
func (e *Event) Int8(key string, i int8) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendInt8(enc.AppendKey(e.buf, key), i)
	} else {
		e.fieldsBuf = enc.AppendInt8(enc.AppendKey(e.fieldsBuf, key), i)
	}

	return e
}

// Ints8 adds the field key with i as a []int8 to the *Event context.
func (e *Event) Ints8(key string, i []int8) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendInts8(enc.AppendKey(e.buf, key), i)
	} else {
		e.fieldsBuf = enc.AppendInts8(enc.AppendKey(e.fieldsBuf, key), i)
	}
	return e
}

// Int16 adds the field key with i as a int16 to the *Event context.
func (e *Event) Int16(key string, i int16) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendInt16(enc.AppendKey(e.buf, key), i)
	} else {
		e.fieldsBuf = enc.AppendInt16(enc.AppendKey(e.fieldsBuf, key), i)
	}
	return e
}

// Ints16 adds the field key with i as a []int16 to the *Event context.
func (e *Event) Ints16(key string, i []int16) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendInts16(enc.AppendKey(e.buf, key), i)
	} else {
		e.fieldsBuf = enc.AppendInts16(enc.AppendKey(e.fieldsBuf, key), i)
	}
	return e
}

// Int32 adds the field key with i as a int32 to the *Event context.
func (e *Event) Int32(key string, i int32) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendInt32(enc.AppendKey(e.buf, key), i)
	} else {
		e.fieldsBuf = enc.AppendInt32(enc.AppendKey(e.fieldsBuf, key), i)
	}

	return e
}

// Ints32 adds the field key with i as a []int32 to the *Event context.
func (e *Event) Ints32(key string, i []int32) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendInts32(enc.AppendKey(e.buf, key), i)
	} else {
		e.fieldsBuf = enc.AppendInts32(enc.AppendKey(e.fieldsBuf, key), i)
	}
	return e
}

// Int64 adds the field key with i as a int64 to the *Event context.
func (e *Event) Int64(key string, i int64) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendInt64(enc.AppendKey(e.buf, key), i)
	} else {
		e.fieldsBuf = enc.AppendInt64(enc.AppendKey(e.fieldsBuf, key), i)
	}
	return e
}

// Ints64 adds the field key with i as a []int64 to the *Event context.
func (e *Event) Ints64(key string, i []int64) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendInts64(enc.AppendKey(e.buf, key), i)
	} else {
		e.fieldsBuf = enc.AppendInts64(enc.AppendKey(e.fieldsBuf, key), i)
	}
	return e
}

// Uint adds the field key with i as a uint to the *Event context.
func (e *Event) Uint(key string, i uint) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendUint(enc.AppendKey(e.buf, key), uint(i))
	} else {
		e.fieldsBuf = enc.AppendUint(enc.AppendKey(e.fieldsBuf, key), uint(i))
	}
	return e
}

// Uints adds the field key with i as a []int to the *Event context.
func (e *Event) Uints(key string, i []uint) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendUints(enc.AppendKey(e.buf, key), i)
	} else {
		e.fieldsBuf = enc.AppendUints(enc.AppendKey(e.fieldsBuf, key), i)
	}

	return e
}

// Uint8 adds the field key with i as a uint8 to the *Event context.
func (e *Event) Uint8(key string, i uint8) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendUint8(enc.AppendKey(e.buf, key), i)
	} else {
		e.fieldsBuf = enc.AppendUint8(enc.AppendKey(e.fieldsBuf, key), i)
	}
	return e
}

// Uints8 adds the field key with i as a []int8 to the *Event context.
func (e *Event) Uints8(key string, i []uint8) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendUints8(enc.AppendKey(e.buf, key), i)
	} else {
		e.fieldsBuf = enc.AppendUints8(enc.AppendKey(e.fieldsBuf, key), i)
	}
	return e
}

// Uint16 adds the field key with i as a uint16 to the *Event context.
func (e *Event) Uint16(key string, i uint16) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendUint16(enc.AppendKey(e.buf, key), i)
	} else {
		e.fieldsBuf = enc.AppendUint16(enc.AppendKey(e.fieldsBuf, key), i)
	}
	return e
}

// Uints16 adds the field key with i as a []int16 to the *Event context.
func (e *Event) Uints16(key string, i []uint16) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendUints16(enc.AppendKey(e.buf, key), i)
	} else {
		e.fieldsBuf = enc.AppendUints16(enc.AppendKey(e.fieldsBuf, key), i)
	}
	return e
}

// Uint32 adds the field key with i as a uint32 to the *Event context.
func (e *Event) Uint32(key string, i uint32) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendUint32(enc.AppendKey(e.buf, key), i)
	} else {
		e.fieldsBuf = enc.AppendUint32(enc.AppendKey(e.fieldsBuf, key), i)
	}
	return e
}

// Uints32 adds the field key with i as a []int32 to the *Event context.
func (e *Event) Uints32(key string, i []uint32) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendUints32(enc.AppendKey(e.buf, key), i)
	} else {
		e.fieldsBuf = enc.AppendUints32(enc.AppendKey(e.fieldsBuf, key), i)
	}
	return e
}

// Uint64 adds the field key with i as a uint64 to the *Event context.
func (e *Event) Uint64(key string, i uint64) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendUint64(enc.AppendKey(e.buf, key), i)
	} else {
		e.fieldsBuf = enc.AppendUint64(enc.AppendKey(e.fieldsBuf, key), i)
	}
	return e
}

// Uints64 adds the field key with i as a []int64 to the *Event context.
func (e *Event) Uints64(key string, i []uint64) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendUints64(enc.AppendKey(e.buf, key), i)
	} else {
		e.fieldsBuf = enc.AppendUints64(enc.AppendKey(e.fieldsBuf, key), i)
	}
	return e
}

// Float32 adds the field key with f as a float32 to the *Event context.
func (e *Event) Float32(key string, f float32) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendFloat32(enc.AppendKey(e.buf, key), f, FloatingPointPrecision)
	} else {
		e.fieldsBuf = enc.AppendFloat32(enc.AppendKey(e.fieldsBuf, key), f, FloatingPointPrecision)
	}
	return e
}

// Floats32 adds the field key with f as a []float32 to the *Event context.
func (e *Event) Floats32(key string, f []float32) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendFloats32(enc.AppendKey(e.buf, key), f, FloatingPointPrecision)
	} else {
		e.fieldsBuf = enc.AppendFloats32(enc.AppendKey(e.fieldsBuf, key), f, FloatingPointPrecision)
	}
	return e
}

// Float64 adds the field key with f as a float64 to the *Event context.
func (e *Event) Float64(key string, f float64) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendFloat64(enc.AppendKey(e.buf, key), f, FloatingPointPrecision)
	} else {
		e.fieldsBuf = enc.AppendFloat64(enc.AppendKey(e.fieldsBuf, key), f, FloatingPointPrecision)
	}
	return e
}

// Floats64 adds the field key with f as a []float64 to the *Event context.
func (e *Event) Floats64(key string, f []float64) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendFloats64(enc.AppendKey(e.buf, key), f, FloatingPointPrecision)
	} else {
		e.fieldsBuf = enc.AppendFloats64(enc.AppendKey(e.fieldsBuf, key), f, FloatingPointPrecision)
	}
	return e
}

// Timestamp adds the current local time as UNIX timestamp to the *Event context with the "time" key.
// To customize the key name, change zerolog.TimestampFieldName.
//
// NOTE: It won't dedupe the "time" key if the *Event (or *Context) has one
// already.
func (e *Event) Timestamp() *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendTime(enc.AppendKey(e.buf, TimestampFieldName), TimestampFunc(), TimeFieldFormat)
	} else {
		e.fieldsBuf = enc.AppendTime(enc.AppendKey(e.fieldsBuf, TimestampFieldName), TimestampFunc(), TimeFieldFormat)
	}
	return e
}

func (e *Event) baseTimestamp() *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendTime(enc.AppendKey(e.buf, TimestampFieldName), TimestampFunc(), TimeFieldFormat)
	} else {
		e.buf = TimestampFunc().AppendFormat(e.buf, TimestampFieldName)
		e.buf = append(e.buf, '\t')
	}
	return e
}

// Time adds the field key with t formatted as string using zerolog.TimeFieldFormat.
func (e *Event) Time(key string, t time.Time) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendTime(enc.AppendKey(e.buf, key), t, TimeFieldFormat)
	} else {
		e.fieldsBuf = enc.AppendTime(enc.AppendKey(e.fieldsBuf, key), t, TimeFieldFormat)
	}
	return e
}

// Times adds the field key with t formatted as string using zerolog.TimeFieldFormat.
func (e *Event) Times(key string, t []time.Time) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendTimes(enc.AppendKey(e.buf, key), t, TimeFieldFormat)
	} else {
		e.fieldsBuf = enc.AppendTimes(enc.AppendKey(e.fieldsBuf, key), t, TimeFieldFormat)
	}
	return e
}

// Dur adds the field key with duration d stored as zerolog.DurationFieldUnit.
// If zerolog.DurationFieldInteger is true, durations are rendered as integer
// instead of float.
func (e *Event) Dur(key string, d time.Duration) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendDuration(enc.AppendKey(e.buf, key), d, DurationFieldUnit, DurationFieldInteger, FloatingPointPrecision)
	} else {
		e.fieldsBuf = enc.AppendDuration(enc.AppendKey(e.fieldsBuf, key), d, DurationFieldUnit, DurationFieldInteger, FloatingPointPrecision)
	}
	return e
}

// Durs adds the field key with duration d stored as zerolog.DurationFieldUnit.
// If zerolog.DurationFieldInteger is true, durations are rendered as integer
// instead of float.
func (e *Event) Durs(key string, d []time.Duration) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendDurations(enc.AppendKey(e.buf, key), d, DurationFieldUnit, DurationFieldInteger, FloatingPointPrecision)
	} else {
		e.fieldsBuf = enc.AppendDurations(enc.AppendKey(e.fieldsBuf, key), d, DurationFieldUnit, DurationFieldInteger, FloatingPointPrecision)
	}
	return e
}

// TimeDiff adds the field key with positive duration between time t and start.
// If time t is not greater than start, duration will be 0.
// Duration format follows the same principle as Dur().
func (e *Event) TimeDiff(key string, t time.Time, start time.Time) *Event {
	if e == nil {
		return e
	}
	var d time.Duration
	if t.After(start) {
		d = t.Sub(start)
	}
	if e.json {
		e.buf = enc.AppendDuration(enc.AppendKey(e.buf, key), d, DurationFieldUnit, DurationFieldInteger, FloatingPointPrecision)
	} else {
		e.fieldsBuf = enc.AppendDuration(enc.AppendKey(e.fieldsBuf, key), d, DurationFieldUnit, DurationFieldInteger, FloatingPointPrecision)
	}
	return e
}

// Any is a wrapper around Event.Interface.
func (e *Event) Any(key string, i interface{}) *Event {
	return e.Interface(key, i)
}

// Interface adds the field key with i marshaled using reflection.
func (e *Event) Interface(key string, i interface{}) *Event {
	if e == nil {
		return e
	}
	if obj, ok := i.(LogObjectMarshaler); ok {
		return e.Object(key, obj)
	}
	if e.json {
		e.buf = enc.AppendInterface(enc.AppendKey(e.buf, key), i)
	} else {
		e.fieldsBuf = enc.AppendInterface(enc.AppendKey(e.fieldsBuf, key), i)
	}
	return e
}

// Type adds the field key with val's type using reflection.
func (e *Event) Type(key string, val interface{}) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendType(enc.AppendKey(e.buf, key), val)
	} else {
		e.fieldsBuf = enc.AppendType(enc.AppendKey(e.fieldsBuf, key), val)
	}
	return e
}

// Caller adds the file:line of the caller with the zerolog.CallerFieldName key.
// The argument skip is the number of stack frames to ascend
// Skip If not passed, use the global variable CallerSkipFrameCount
func (e *Event) Caller(skip ...int) *Event {
	sk := CallerSkipFrameCount
	if len(skip) > 0 {
		sk = skip[0] + CallerSkipFrameCount
	}
	return e.caller(sk)
}

func (e *Event) caller(skip int) *Event {
	if e == nil {
		return e
	}
	pc, file, line, ok := runtime.Caller(skip)
	if !ok {
		return e
	}
	if e.json {
		e.buf = enc.AppendString(enc.AppendKey(e.buf, CallerFieldName), CallerMarshalFunc(pc, file, line))
	} else {
		e.buf = append(e.buf, CallerFieldName...)
		e.buf = append(e.buf, '\t')
	}
	return e
}

// IPAddr adds IPv4 or IPv6 Address to the event
func (e *Event) IPAddr(key string, ip net.IP) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendIPAddr(enc.AppendKey(e.buf, key), ip)
	} else {
		e.fieldsBuf = enc.AppendIPAddr(enc.AppendKey(e.fieldsBuf, key), ip)
	}

	return e
}

// IPPrefix adds IPv4 or IPv6 Prefix (address and mask) to the event
func (e *Event) IPPrefix(key string, pfx net.IPNet) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendIPPrefix(enc.AppendKey(e.buf, key), pfx)
	} else {
		e.fieldsBuf = enc.AppendIPPrefix(enc.AppendKey(e.fieldsBuf, key), pfx)
	}
	return e
}

// MACAddr adds MAC address to the event
func (e *Event) MACAddr(key string, ha net.HardwareAddr) *Event {
	if e == nil {
		return e
	}
	if e.json {
		e.buf = enc.AppendMACAddr(enc.AppendKey(e.buf, key), ha)
	} else {
		e.fieldsBuf = enc.AppendMACAddr(enc.AppendKey(e.fieldsBuf, key), ha)
	}
	return e
}
