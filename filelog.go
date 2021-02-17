// Copyright (C) 2010, Kyle Lemons <kyle@kylelemons.net>.  All rights reserved.

package log4go

import (
	"bufio"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// This log writer sends output to a file
type FileLogWriter struct {
	rec chan *LogRecord
	rot chan bool

	// The opened file
	filename string
	file     *os.File

	// The logging format
	format string

	// File header/trailer
	header, trailer string

	// Rotate at linecount
	maxlines          int
	maxlines_curlines int

	// Rotate at size
	maxsize         int
	maxsize_cursize int

	// Rotate daily
	daily          bool
	maxdays        int
	daily_opendate int

	// Keep old logfiles (.001, .002, etc)
	rotate        bool
	rotateOnStart bool
	maxbackup     int

	// Sanitize newlines to prevent log injection
	sanitize bool
}

// This is the FileLogWriter's output method
func (w *FileLogWriter) LogWrite(rec *LogRecord) {
	w.rec <- rec
}

func (w *FileLogWriter) Close() {
	close(w.rec)
	w.file.Sync()
}

func (w *FileLogWriter) FileInit(debug bool) (bool, error) {

	ok := false

	// Open most recent logfile for
	// reading only.
	fd, err := os.Open(w.filename)

	if err != nil {

		// File does exist but
		// we still can't open it.
		if !os.IsNotExist(err) {
			ok = true
		}

		if debug {
			fmt.Printf("Logfile Exists?: %v\n", ok)
		}

		return ok, fmt.Errorf("FileInit: %s", err)

	}

	// Current logfile exists.
	ok = true
	defer fd.Close()

	// Get info for current logfile.
	info, err := fd.Stat()

	if err != nil {
		return ok, fmt.Errorf("FileInit: %s", err)
	}

	// Create scanner for calculating line
	// numbers.
	scanner := bufio.NewScanner(fd)

	// Set the size (in bytes) of the current
	// logfile to determine if rollover on start
	// is required.
	w.maxsize_cursize = int(info.Size())

	// Set the number of lines in the current
	// logfile to determine if rollover on
	// start is required.
	for scanner.Scan() {
		w.maxlines_curlines++
	}

	if debug {
		fmt.Printf("Total Size: %d, Total Lines: %d\n",
			w.maxsize_cursize, w.maxlines_curlines)
	}

	// Set the file opendate for the current logfile
	// to determine if rollover on start is required
	modifiedtime := info.ModTime()
	w.daily_opendate = modifiedtime.Day()

	return ok, nil
}

func (w *FileLogWriter) isOlderThan(t time.Time) bool {

	// Default if maxDays isn't set
	if w.maxdays <= 0 {
		w.maxdays = 4
	}

	// Get number of hours
	nHours := time.Now().Sub(t).Hours()

	// Compare
	if nHours > float64(w.maxdays)*24 {
		return true
	}

	return false

}

func (w *FileLogWriter) RemoveOldDailyLogs(debug bool) error {

	if debug {
		fmt.Printf("Current FilePath: %s\n", w.filename)
		fmt.Printf("Max Days: %d\n", w.maxdays)
	}

	// Get the log directory
	logDir := filepath.Dir(w.filename)
	// Get info for all files in log directory
	logfiles, err := ioutil.ReadDir(logDir)

	if debug {
		fmt.Printf("Removing old daily logs from: %s\n", logDir)
	}

	if err != nil {

		if debug {
			fmt.Printf("Error Reading Directory %s, %s\n", logDir, err.Error())
		}

		return fmt.Errorf("RemoveOldDailyLogs: %s", err)

	}

	for _, file := range logfiles {

		if file.Mode().IsRegular() &&
			w.isOlderThan(file.ModTime()) {

			filePrefix := filepath.Base(w.filename)

			if debug {
				fmt.Printf("FileName: %s, FilePrefix: %s\n", file.Name(), filePrefix)
			}

			// Are these the log files we want?
			if !strings.HasPrefix(file.Name(), filePrefix) {
				continue
			}

			filePath := logDir + string(os.PathSeparator) + file.Name()

			if debug {
				fmt.Printf("Rotate: Removing Expired Logfile: %s\n", filePath)
			}

			err := os.Remove(filePath)

			if err != nil {
				return fmt.Errorf("RemoveOldDailyLogs: %s", err)
			}

		}

	}

	return nil
}

// NewFileLogWriter creates a new LogWriter which writes to the given file and
// has rotation enabled if rotate is true.
//
// If rotate is true, any time a new log file is opened, the old one is renamed
// with a .### extension to preserve it.  The various Set* methods can be used
// to configure log rotation based on lines, size, and daily.
//
// The standard log-line format is:
//   [%D %T] [%L] (%S) %M
func NewFileLogWriter(fname string, rotate bool, daily bool, maxsize int, maxlines int) *FileLogWriter {
	w := &FileLogWriter{
		rec:       make(chan *LogRecord, LogBufferLength),
		rot:       make(chan bool),
		filename:  fname,
		format:    "[%D %T] [%L] (%S) %M",
		daily:     daily,
		rotate:    rotate,
		maxsize:   maxsize,
		maxlines:  maxlines,
		maxbackup: 5,
		maxdays:   4,
		sanitize:  false, // set to false so as not to break compatibility
	}

	// Get the size, linecount, and opendate for the
	// current logfile if it exists
	fileExists, _ := w.FileInit(false)

	now := time.Now()

	// If the logfile already exists and any of the rotate conditions are
	// satisfied then rollover on start. Otherwise, ensure the current logfile is
	// open for writing.
	if fileExists && ((w.maxlines > 0 && w.maxlines_curlines >= w.maxlines) ||
		(w.maxsize > 0 && w.maxsize_cursize >= w.maxsize) ||
		(w.daily && now.Day() != w.daily_opendate)) {

		if err := w.intRotate(); err != nil {
			fmt.Fprintf(os.Stderr, "FileLogWriter(%q): %s\n", w.filename, err)
			return nil
		}

	} else {

		// Either the file doesn't exist OR we are not ready
		// to rollover yet. In either case, make sure the file is
		// opened in append mode for writing.
		fd, err := os.OpenFile(w.filename, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0660)
		if err != nil {
			fmt.Printf("Error Opening File: %s", err.Error())
		}

		w.file = fd

		// If this is the first time opening this file
		// then set the daily open date to the current date
		if !fileExists {
			w.daily_opendate = now.Day()
		}

	}

	go func() {
		defer recoverPanic()
		defer func() {
			if w.file != nil {
				fmt.Fprint(w.file, FormatLogRecord(w.trailer, &LogRecord{Created: time.Now()}))
				w.file.Close()
			}
		}()

		for {
			select {
			case <-w.rot:
				if err := w.intRotate(); err != nil {
					fmt.Fprintf(os.Stderr, "FileLogWriter(%q): %s\n", w.filename, err)
					return
				}
			case rec, ok := <-w.rec:
				if !ok {
					return
				}
				now := time.Now()
				if (w.maxlines > 0 && w.maxlines_curlines >= w.maxlines) ||
					(w.maxsize > 0 && w.maxsize_cursize >= w.maxsize) ||
					(w.daily && now.Day() != w.daily_opendate) {
					if err := w.intRotate(); err != nil {
						fmt.Fprintf(os.Stderr, "FileLogWriter(%q): %s\n", w.filename, err)
						return
					}
				}

				// Sanitize newlines
				if w.sanitize {
					rec.Message = strings.Replace(rec.Message, "\n", "\\n", -1)
				}

				// Perform the write
				n, err := fmt.Fprint(w.file, FormatLogRecord(w.format, rec))
				if err != nil {
					fmt.Fprintf(os.Stderr, "FileLogWriter(%q): %s\n", w.filename, err)
					return
				}

				// Update the counts
				w.maxlines_curlines++
				w.maxsize_cursize += n
			}
		}
	}()

	return w
}

// Request that the logs rotate
func (w *FileLogWriter) Rotate() {
	w.rot <- true
}

// If this is called in a threaded context, it MUST be synchronized
func (w *FileLogWriter) intRotate() error {
	// Close any log file that may be open
	if w.file != nil {
		fmt.Fprint(w.file, FormatLogRecord(w.trailer, &LogRecord{Created: time.Now()}))
		w.file.Close()
	}
	// If we are keeping log files, move it to the next available number
	if w.rotate || w.rotateOnStart {
		info, err := os.Stat(w.filename)
		// _, err = os.Lstat(w.filename)

		if err == nil { // file exists
			// Find the next available number
			modifiedtime := info.ModTime()
			w.daily_opendate = modifiedtime.Day()
			num := 1
			fname := ""
			if w.daily && time.Now().Day() != w.daily_opendate {
				modifieddate := modifiedtime.Format("2006-01-02")
				// for ; err == nil && num <= w.maxbackup; num++ {
				// 	fname = w.filename + fmt.Sprintf(".%s.%03d", yesterday, num)
				// 	_, err = os.Lstat(fname)
				// }
				// if err == nil {
				// 	return fmt.Errorf("Rotate: Cannot find free log number to rename %s\n", w.filename)
				// }
				fname = w.filename + fmt.Sprintf(".%s", modifieddate)
				w.file.Close()
				// Rename the file to its newfound home
				err = os.Rename(w.filename, fname)
				if err != nil {
					return fmt.Errorf("Rotate: %s\n", err)
				}

				err = w.RemoveOldDailyLogs(false)
				if err != nil {
					return fmt.Errorf("Rotate: %s\n", err)
				}

			} else if !w.daily {
				num = w.maxbackup - 1
				for ; num >= 1; num-- {
					fname = w.filename + fmt.Sprintf(".%d", num)
					nfname := w.filename + fmt.Sprintf(".%d", num+1)
					_, err = os.Lstat(fname)
					if err == nil {
						os.Rename(fname, nfname)
					}
				}
				w.file.Close()
				// Rename the file to its newfound home
				err = os.Rename(w.filename, fname)
				// return error if the last file checked still existed
				if err != nil {
					return fmt.Errorf("Rotate: %s\n", err)
				}
			}

		}
	}

	// Open the log file
	fd, err := os.OpenFile(w.filename, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0660)
	if err != nil {
		return err
	}
	w.file = fd

	now := time.Now()
	fmt.Fprint(w.file, FormatLogRecord(w.header, &LogRecord{Created: now}))

	// Set the daily open date to the current date
	w.daily_opendate = now.Day()

	// initialize rotation values
	w.maxlines_curlines = 0
	w.maxsize_cursize = 0

	return nil
}

// Set the logging format (chainable).  Must be called before the first log
// message is written.
func (w *FileLogWriter) SetFormat(format string) *FileLogWriter {
	w.format = format
	return w
}

// Set the logfile header and footer (chainable).  Must be called before the first log
// message is written.  These are formatted similar to the FormatLogRecord (e.g.
// you can use %D and %T in your header/footer for date and time).
func (w *FileLogWriter) SetHeadFoot(head, foot string) *FileLogWriter {
	w.header, w.trailer = head, foot
	if w.maxlines_curlines == 0 {
		fmt.Fprint(w.file, FormatLogRecord(w.header, &LogRecord{Created: time.Now()}))
	}
	return w
}

// Set rotate at linecount (chainable). Must be called before the first log
// message is written.
func (w *FileLogWriter) SetRotateLines(maxlines int) *FileLogWriter {
	//fmt.Fprintf(os.Stderr, "FileLogWriter.SetRotateLines: %v\n", maxlines)
	w.maxlines = maxlines
	return w
}

// Set rotate at size (chainable). Must be called before the first log message
// is written.
func (w *FileLogWriter) SetRotateSize(maxsize int) *FileLogWriter {
	//fmt.Fprintf(os.Stderr, "FileLogWriter.SetRotateSize: %v\n", maxsize)
	w.maxsize = maxsize
	return w
}

// Set rotate daily (chainable). Must be called before the first log message is
// written.
func (w *FileLogWriter) SetRotateDaily(daily bool) *FileLogWriter {
	//fmt.Fprintf(os.Stderr, "FileLogWriter.SetRotateDaily: %v\n", daily)
	w.daily = daily
	return w
}

func (w *FileLogWriter) SetMaxDays(maxdays int) *FileLogWriter {
	w.maxdays = maxdays
	return w
}

// Set max backup files. Must be called before the first log message
// is written.
func (w *FileLogWriter) SetRotateMaxBackup(maxbackup int) *FileLogWriter {
	w.maxbackup = maxbackup
	return w
}

// SetRotate changes whether or not the old logs are kept. (chainable) Must be
// called before the first log message is written.  If rotate is false, the
// files are overwritten; otherwise, they are rotated to another file before the
// new log is opened.
func (w *FileLogWriter) SetRotate(rotate bool) *FileLogWriter {
	//fmt.Fprintf(os.Stderr, "FileLogWriter.SetRotate: %v\n", rotate)
	w.rotate = rotate
	return w
}

// SetSanitize changes whether or not the sanitization of newline characters takes
// place. This is to prevent log injection, although at some point the sanitization
// of other non-printable characters might be valueable just to prevent binary
// data from mucking up the logs.
func (w *FileLogWriter) SetSanitize(sanitize bool) *FileLogWriter {
	w.sanitize = sanitize
	return w
}

// NewXMLLogWriter is a utility method for creating a FileLogWriter set up to
// output XML record log messages instead of line-based ones.
func NewXMLLogWriter(fname string, rotate bool, daily bool, maxsize int, maxlines int) *FileLogWriter {
	return NewFileLogWriter(fname, rotate, daily, maxsize, maxlines).SetFormat(
		`	<record level="%L">
		<timestamp>%D %T</timestamp>
		<source>%S</source>
		<message>%M</message>
	</record>`).SetHeadFoot("<log created=\"%D %T\">", "</log>")
}
