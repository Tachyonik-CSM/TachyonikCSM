// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

package logger

import (
	"io"
	"log"
	"os"
)

type LogLevel int

const (
	DEBUG LogLevel = iota
	INFO
	WARN
	ERROR
)

var (
	currentLevel LogLevel
	debugLogger  *log.Logger
	infoLogger   *log.Logger
	warnLogger   *log.Logger
	errorLogger  *log.Logger
)

func Setup(level LogLevel, writers ...io.Writer) (*os.File, error) {
	currentLevel = level

	var output io.Writer
	if len(writers) == 0 {
		output = os.Stdout
	} else if len(writers) == 1 {
		output = writers[0]
	} else {
		output = io.MultiWriter(writers...)
	}

	debugLogger = log.New(output, "[DEBUG] ", log.Ldate|log.Ltime|log.Lshortfile)
	infoLogger = log.New(output, "[INFO] ", log.Ldate|log.Ltime|log.Lshortfile)
	warnLogger = log.New(output, "[WARN] ", log.Ldate|log.Ltime|log.Lshortfile)
	errorLogger = log.New(output, "[ERROR] ", log.Ldate|log.Ltime|log.Lshortfile)

	return nil, nil
}

func ParseLogLevel(level string) LogLevel {
	switch level {
	case "DEBUG":
		return DEBUG
	case "INFO":
		return INFO
	case "WARN":
		return WARN
	case "ERROR":
		return ERROR
	default:
		return INFO
	}
}

func Debugf(format string, v ...interface{}) {
	if currentLevel <= DEBUG && debugLogger != nil {
		debugLogger.Printf(format, v...)
	}
}

func Infof(format string, v ...interface{}) {
	if currentLevel <= INFO && infoLogger != nil {
		infoLogger.Printf(format, v...)
	}
}

func Warnf(format string, v ...interface{}) {
	if currentLevel <= WARN && warnLogger != nil {
		warnLogger.Printf(format, v...)
	}
}

func Errorf(format string, v ...interface{}) {
	if currentLevel <= ERROR && errorLogger != nil {
		errorLogger.Printf(format, v...)
	}
}

func Fatalf(format string, v ...interface{}) {
	if errorLogger != nil {
		errorLogger.Printf(format, v...)
	}
	os.Exit(1)
}
