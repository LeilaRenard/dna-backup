// Package logger offers simple logging
package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"runtime/debug"
	"sync"
)

type severity int

type logger interface {
	Output(calldepth int, s string) error
	SetOutput(w io.Writer)
	SetFlags(flag int)
}

// Severity levels.
const (
	sInfo severity = iota
	sWarning
	sError
	sFatal
)

// Severity tags.
const (
	tagInfo    = "\033[0m[INFO]  "
	tagWarning = "\033[33m[WARN]  "
	tagError   = "\033[31m[ERROR] "
	tagFatal   = "\033[1;31m[FATAL] "
)

const (
	flags    = log.Lmsgprefix | log.Ltime
	resetSeq = "\033[0m"
)

var (
	logLock       sync.Mutex
	defaultLogger *Logger
)

func newLoggers() [4]logger {
	return [4]logger{
		log.New(os.Stderr, tagInfo, flags),
		log.New(os.Stderr, tagWarning, flags),
		log.New(os.Stderr, tagError, flags),
		log.New(os.Stderr, tagFatal, flags),
	}
}

// initialize resets defaultLogger.  Which allows tests to reset environment.
func initialize() {
	defaultLogger = &Logger{
		loggers:     newLoggers(),
		minSeverity: 0,
	}
}

func init() {
	initialize()
}

// Init sets up logging and should be called before log functions, usually in
// the caller's main(). Default log functions can be called before Init(), but
// every severity will be logged.
// The first call to Init populates the default logger and returns the
// generated logger, subsequent calls to Init will only return the generated
// logger.
func Init(level int) *Logger {
	l := Logger{
		loggers:     newLoggers(),
		minSeverity: sFatal - severity(level),
		initialized: true,
	}

	logLock.Lock()
	defer logLock.Unlock()
	if !defaultLogger.initialized {
		defaultLogger = &l
	}

	return &l
}

// A Logger represents an active logging object. Multiple loggers can be used
// simultaneously even if they are using the same writers.
type Logger struct {
	loggers     [4]logger
	minSeverity severity
	initialized bool
}

func (l *Logger) output(s severity, v ...interface{}) {
	if s < l.minSeverity {
		return
	}
	str := fmt.Sprint(v...) + resetSeq
	logLock.Lock()
	defer logLock.Unlock()
	l.loggers[s].Output(3, str)
}

func (l *Logger) outputf(s severity, format string, v ...interface{}) {
	if s < l.minSeverity {
		return
	}
	str := fmt.Sprintf(format, v...) + resetSeq
	logLock.Lock()
	defer logLock.Unlock()
	l.loggers[s].Output(3, str)
}

// SetOutput changes the output of the logger.
func (l *Logger) SetOutput(w io.Writer) {
	for _, logger := range l.loggers {
		logger.SetOutput(w)
	}
}

// SetFlags sets the output flags for the logger.
func (l *Logger) SetFlags(flag int) {
	for _, logger := range l.loggers {
		logger.SetFlags(flag)
	}
}

// Info logs with the Info severity.
// Arguments are handled in the manner of fmt.Print.
func (l *Logger) Info(v ...interface{}) {
	l.output(sInfo, v...)
}

// Infof logs with the Info severity.
// Arguments are handled in the manner of fmt.Printf.
func (l *Logger) Infof(format string, v ...interface{}) {
	l.outputf(sInfo, format, v...)
}

// Warning logs with the Warning severity.
// Arguments are handled in the manner of fmt.Print.
func (l *Logger) Warning(v ...interface{}) {
	l.output(sWarning, v...)
}

// Warningf logs with the Warning severity.
// Arguments are handled in the manner of fmt.Printf.
func (l *Logger) Warningf(format string, v ...interface{}) {
	l.outputf(sWarning, format, v...)
}

// Error logs with the ERROR severity.
// Arguments are handled in the manner of fmt.Print.
func (l *Logger) Error(v ...interface{}) {
	l.output(sError, v...)
	debug.PrintStack()
}

// Errorf logs with the Error severity.
// Arguments are handled in the manner of fmt.Printf.
func (l *Logger) Errorf(format string, v ...interface{}) {
	l.outputf(sError, format, v...)
	debug.PrintStack()
}

// Panic uses the default logger and logs with the Error severity.
// Arguments are handled in the manner of fmt.Print.
func (l *Logger) Panic(v ...interface{}) {
	s := fmt.Sprint(v...)
	l.output(sError, s)
	panic(s)
}

// Panicf uses the default logger and logs with the Error severity.
// Arguments are handled in the manner of fmt.Printf.
func (l *Logger) Panicf(format string, v ...interface{}) {
	s := fmt.Sprintf(format, v...)
	l.output(sError, s)
	panic(s)
}

// Fatal logs with the Fatal severity, and ends with os.Exit(1).
// Arguments are handled in the manner of fmt.Print.
func (l *Logger) Fatal(v ...interface{}) {
	l.output(sFatal, v...)
	debug.PrintStack()
	os.Exit(1)
}

// Fatalf logs with the Fatal severity, and ends with os.Exit(1).
// Arguments are handled in the manner of fmt.Printf.
func (l *Logger) Fatalf(format string, v ...interface{}) {
	l.outputf(sFatal, format, v...)
	debug.PrintStack()
	os.Exit(1)
}

// SetOutput changes the output of the default logger.
func SetOutput(w io.Writer) {
	defaultLogger.SetOutput(w)
}

// SetFlags sets the output flags for the logger.
func SetFlags(flag int) {
	defaultLogger.SetFlags(flag)
}

// Info uses the default logger and logs with the Info severity.
// Arguments are handled in the manner of fmt.Print.
func Info(v ...interface{}) {
	defaultLogger.output(sInfo, v...)
}

// Infof uses the default logger and logs with the Info severity.
// Arguments are handled in the manner of fmt.Printf.
func Infof(format string, v ...interface{}) {
	defaultLogger.outputf(sInfo, format, v...)
}

// Warning uses the default logger and logs with the Warning severity.
// Arguments are handled in the manner of fmt.Print.
func Warning(v ...interface{}) {
	defaultLogger.output(sWarning, v...)
}

// Warningf uses the default logger and logs with the Warning severity.
// Arguments are handled in the manner of fmt.Printf.
func Warningf(format string, v ...interface{}) {
	defaultLogger.outputf(sWarning, format, v...)
}

// Error uses the default logger and logs with the Error severity.
// Arguments are handled in the manner of fmt.Print.
func Error(v ...interface{}) {
	defaultLogger.output(sError, v...)
	debug.PrintStack()
}

// Errorf uses the default logger and logs with the Error severity.
// Arguments are handled in the manner of fmt.Printf.
func Errorf(format string, v ...interface{}) {
	defaultLogger.outputf(sError, format, v...)
	debug.PrintStack()
}

// Panic uses the default logger and logs with the Error severity.
// Arguments are handled in the manner of fmt.Print.
func Panic(v ...interface{}) {
	s := fmt.Sprint(v...)
	defaultLogger.output(sError, s)
	panic(s)
}

// Panicf uses the default logger and logs with the Error severity.
// Arguments are handled in the manner of fmt.Printf.
func Panicf(format string, v ...interface{}) {
	s := fmt.Sprintf(format, v...)
	defaultLogger.output(sError, s)
	panic(s)
}

// Fatal uses the default logger, logs with the Fatal severity,
// and ends with os.Exit(1).
// Arguments are handled in the manner of fmt.Print.
func Fatal(v ...interface{}) {
	defaultLogger.output(sFatal, v...)
	debug.PrintStack()
	os.Exit(1)
}

// Fatalf uses the default logger, logs with the Fatal severity,
// and ends with os.Exit(1).
// Arguments are handled in the manner of fmt.Printf.
func Fatalf(format string, v ...interface{}) {
	defaultLogger.outputf(sFatal, format, v...)
	debug.PrintStack()
	os.Exit(1)
}
