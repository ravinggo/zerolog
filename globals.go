package zerolog

import (
	"bytes"
	"encoding/json"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
)

const (
	// TimeFormatUnix defines a time format that makes time fields to be
	// serialized as Unix timestamp integers.
	TimeFormatUnix = ""

	// TimeFormatUnixMs defines a time format that makes time fields to be
	// serialized as Unix timestamp integers in milliseconds.
	TimeFormatUnixMs = "UNIXMS"

	// TimeFormatUnixMicro defines a time format that makes time fields to be
	// serialized as Unix timestamp integers in microseconds.
	TimeFormatUnixMicro = "UNIXMICRO"

	// TimeFormatUnixNano defines a time format that makes time fields to be
	// serialized as Unix timestamp integers in nanoseconds.
	TimeFormatUnixNano = "UNIXNANO"
)

var (
	// TimestampFieldName is the field name used for the timestamp field.
	TimestampFieldName = "time"

	// LevelFieldName is the field name used for the level field.
	LevelFieldName = "level"

	// LevelTraceValue is the value used for the trace level field.
	LevelTraceValue = "trace"
	// LevelDebugValue is the value used for the debug level field.
	LevelDebugValue = "debug"
	// LevelInfoValue is the value used for the info level field.
	LevelInfoValue = "info"
	// LevelWarnValue is the value used for the warn level field.
	LevelWarnValue = "warn"
	// LevelErrorValue is the value used for the error level field.
	LevelErrorValue = "error"
	// LevelFatalValue is the value used for the fatal level field.
	LevelFatalValue = "fatal"
	// LevelPanicValue is the value used for the panic level field.
	LevelPanicValue = "panic"

	// LevelFieldMarshalFunc allows customization of global level field marshaling.
	LevelFieldMarshalFunc = func(l Level) string {
		return l.String()
	}

	// MessageFieldName is the field name used for the message field.
	MessageFieldName = "message"

	// ErrorFieldName is the field name used for error fields.
	ErrorFieldName = "error"

	// CallerFieldName is the field name used for caller field.
	CallerFieldName = "caller"

	// CallerSkipFrameCount is the number of stack frames to skip to find the caller.
	CallerSkipFrameCount = 2

	// CallerMarshalFunc allows customization of global caller marshaling
	CallerMarshalFunc = func(pc uintptr, file string, line int) string {
		return file + ":" + strconv.Itoa(line)
	}

	// ErrorStackFieldName is the field name used for error stacks.
	ErrorStackFieldName = "stack"

	// ErrorStackMarshaler extract the stack from err if any.
	ErrorStackMarshaler func(err error) errors.StackTrace

	// ErrorMarshalFunc allows customization of global error marshaling
	ErrorMarshalFunc = func(err error) interface{} {
		return err
	}

	// InterfaceMarshalFunc allows customization of interface marshaling.
	// Default: "encoding/json.Marshal" with disabled HTML escaping
	InterfaceMarshalFunc = func(v interface{}) ([]byte, error) {
		var buf bytes.Buffer
		encoder := json.NewEncoder(&buf)
		encoder.SetEscapeHTML(false)
		err := encoder.Encode(v)
		if err != nil {
			return nil, err
		}
		b := buf.Bytes()
		if len(b) > 0 {
			// Remove trailing \n which is added by Encode.
			return b[:len(b)-1], nil
		}
		return b, nil
	}

	// TimeFieldFormat defines the time format of the Time field type. If set to
	// TimeFormatUnix, TimeFormatUnixMs, TimeFormatUnixMicro or TimeFormatUnixNano, the time is formatted as a UNIX
	// timestamp as integer.
	TimeFieldFormat = time.RFC3339

	// TimestampFunc defines the function called to generate a timestamp.
	TimestampFunc = time.Now

	// DurationFieldUnit defines the unit for time.Duration type fields added
	// using the Dur method.
	DurationFieldUnit = time.Millisecond

	// DurationFieldInteger renders Dur fields as integer instead of float if
	// set to true.
	DurationFieldInteger = false

	// ErrorHandler is called whenever zerolog fails to write an event on its
	// output. If not set, an error is printed on the stderr. This handler must
	// be thread safe and non-blocking.
	ErrorHandler func(err error)

	// DefaultContextLogger is returned from Ctx() if there is no logger associated
	// with the context.
	DefaultContextLogger *Logger
	// LevelColors are used by ConsoleWriter's consoleDefaultFormatLevel to color
	// log levels.
	noColor     = []byte("\x1b[0m")
	LevelColors = [9][]byte{
		DebugLevel:     noColor,
		InfoLevel:      []byte("\x1b[32m"),
		WarnLevel:      []byte("\x1b[33m"),
		ErrorLevel:     []byte("\x1b[31m"),
		FatalLevel:     []byte("\x1b[31m"),
		PanicLevel:     []byte("\x1b[31m"),
		NoLevel:        noColor,
		Disabled:       noColor,
		TraceLevel + 9: []byte("\x1b[34m"),
	}

	// FormattedLevels are used by ConsoleWriter's consoleDefaultFormatLevel
	// for a short level name.
	FormattedLevels = map[Level]string{
		TraceLevel: "TRC",
		DebugLevel: "DBG",
		InfoLevel:  "INF",
		WarnLevel:  "WRN",
		ErrorLevel: "ERR",
		FatalLevel: "FTL",
		PanicLevel: "PNC",
	}

	// TriggerLevelWriterBufferReuseLimit is a limit in bytes that a buffer is dropped
	// from the TriggerLevelWriter buffer pool if the buffer grows above the limit.
	TriggerLevelWriterBufferReuseLimit = 64 * 1024

	// FloatingPointPrecision , if set to a value other than -1, controls the number
	// of digits when formatting float numbers in JSON. See strconv.FormatFloat for
	// more details.
	FloatingPointPrecision = -1
)

var (
	gLevel          = new(int32)
	disableSampling = new(int32)
)

// SetGlobalLevel sets the global override for log level. If this
// values is raised, all Loggers will use at least this value.
//
// To globally disable logs, set GlobalLevel to Disabled.
func SetGlobalLevel(l Level) {
	atomic.StoreInt32(gLevel, int32(l))
}

// GlobalLevel returns the current global log level
func GlobalLevel() Level {
	return Level(atomic.LoadInt32(gLevel))
}

// DisableSampling will disable sampling in all Loggers if true.
func DisableSampling(v bool) {
	var i int32
	if v {
		i = 1
	}
	atomic.StoreInt32(disableSampling, i)
}

func samplingDisabled() bool {
	return atomic.LoadInt32(disableSampling) == 1
}
