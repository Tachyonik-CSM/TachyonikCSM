// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package logger is a small leveled logger (DEBUG, INFO, WARN, ERROR) built on
// the standard library log package. It writes to any combination of writers
// (console and/or file) configured at startup and exposes package-level helpers
// so callers can log without threading a logger through.
package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
)

// LogLevel represents the severity level of a log message
type LogLevel int

const (
	DEBUG LogLevel = iota
	INFO
	WARN
	ERROR
)

// String returns the string representation of a log level
func (l LogLevel) String() string {
	switch l {
	case DEBUG:
		return "DEBUG"
	case INFO:
		return "INFO"
	case WARN:
		return "WARN"
	case ERROR:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// ParseLogLevel parses a string into a LogLevel
func ParseLogLevel(level string) LogLevel {
	switch strings.ToUpper(level) {
	case "DEBUG":
		return DEBUG
	case "INFO":
		return INFO
	case "WARN", "WARNING":
		return WARN
	case "ERROR":
		return ERROR
	default:
		return INFO // Default to INFO
	}
}

// Logger represents a logger with configurable level
type Logger struct {
	level  LogLevel
	logger *log.Logger
}

var globalLogger *Logger

// Setup initializes the global logger to write to every non-nil writer given
// (defaulting to stdout when none are). Opening and closing a log file is the
// caller's job — pass the open *os.File in as one of the writers.
func Setup(level LogLevel, writers ...io.Writer) {
	var finalWriters []io.Writer

	// Filter out nil writers
	for _, w := range writers {
		if w != nil {
			finalWriters = append(finalWriters, w)
		}
	}

	// If no writers, default to stdout
	if len(finalWriters) == 0 {
		finalWriters = append(finalWriters, os.Stdout)
	}

	multiWriter := io.MultiWriter(finalWriters...)

	globalLogger = &Logger{
		level:  level,
		logger: log.New(multiWriter, "", log.LstdFlags|log.Lshortfile),
	}
}

// GetLevel returns the current log level
func GetLevel() LogLevel {
	if globalLogger != nil {
		return globalLogger.level
	}
	return INFO
}

func (l *Logger) log(level LogLevel, format string, v ...interface{}) {
	if l.level <= level {
		prefix := fmt.Sprintf("[%s] ", level.String())
		msg := fmt.Sprintf(format, v...)
		l.logger.Output(3, prefix+msg)
	}
}

// Debug logs a debug message
func Debug(format string, v ...interface{}) {
	if globalLogger != nil {
		globalLogger.log(DEBUG, format, v...)
	}
}

// Debugf is an alias for Debug
func Debugf(format string, v ...interface{}) {
	Debug(format, v...)
}

// Info logs an info message
func Info(format string, v ...interface{}) {
	if globalLogger != nil {
		globalLogger.log(INFO, format, v...)
	}
}

// Infof is an alias for Info
func Infof(format string, v ...interface{}) {
	Info(format, v...)
}

// Warn logs a warning message
func Warn(format string, v ...interface{}) {
	if globalLogger != nil {
		globalLogger.log(WARN, format, v...)
	}
}

// Warnf is an alias for Warn
func Warnf(format string, v ...interface{}) {
	Warn(format, v...)
}

// Error logs an error message
func Error(format string, v ...interface{}) {
	if globalLogger != nil {
		globalLogger.log(ERROR, format, v...)
	}
}

// Errorf is an alias for Error
func Errorf(format string, v ...interface{}) {
	Error(format, v...)
}

// Fatal logs a fatal error and exits
func Fatal(format string, v ...interface{}) {
	if globalLogger != nil {
		globalLogger.log(ERROR, format, v...)
	}
	os.Exit(1)
}

// Fatalf is an alias for Fatal
func Fatalf(format string, v ...interface{}) {
	Fatal(format, v...)
}
