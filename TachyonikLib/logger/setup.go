// TachyonikLib
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Wiring the logger from a service's log configuration.
//
// Every service reads the same four settings — console on, file on, which file,
// which level — and every one of them had its own copy of the dozen lines that
// turn those into writers. This is that, once.
//
// Not every service uses it. Four of the managers treat a log file that cannot
// be opened as a warning and carry on with the console; TachyonikProxy resolves
// a relative path against its config directory and creates the directory first.
// Those are deliberate differences in policy rather than duplication, so they
// keep their own wiring rather than having this one bent to cover them.

package logger

import (
	"io"
	"os"
)

// FileOptions is a service's log configuration, in the shape every service's
// config already holds it.
type FileOptions struct {
	// ToConsole sends log lines to stdout.
	ToConsole bool
	// ToFile sends them to FilePath as well, appending to what is there.
	ToFile bool
	// FilePath is where ToFile writes. Ignored when ToFile is false.
	FilePath string
	// Level is DEBUG, INFO, WARN or ERROR, in any case; anything else is read
	// as INFO.
	Level string
}

// SetupFromOptions configures the package logger and returns the log file it
// opened, so the caller can close it on shutdown. The file is nil when file
// logging is off.
//
// A file that cannot be opened is an error and the logger is left untouched:
// for the services that use this, logging to a file is configuration the
// operator asked for, and starting up having quietly failed to honour it is
// worse than not starting.
//
// Both destinations off is allowed and leaves the logger with no writers. That
// is a choice an operator can make, and it is not this function's place to
// override it.
func SetupFromOptions(o FileOptions) (*os.File, error) {
	var writers []io.Writer
	var logFile *os.File

	if o.ToConsole {
		writers = append(writers, os.Stdout)
	}

	if o.ToFile {
		var err error
		// 0600, not 0644. A service log carries rule text, organisation
		// names and — at debug level — generated routine source. None of that
		// needs to be readable by every account on the host. An existing
		// file's mode is not changed: OpenFile applies this only on create, so
		// an operator who deliberately widened it keeps their choice.
		logFile, err = os.OpenFile(o.FilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return nil, err
		}
		writers = append(writers, logFile)
	}

	Setup(ParseLogLevel(o.Level), writers...)

	return logFile, nil
}
