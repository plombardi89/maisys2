package system

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// KeepLogs is how many runs' logs OpenLog keeps, counting the new one.
const KeepLogs = 20

// logTimeFormat names log files after the time a run started, in UTC, so
// they sort by name in the order they were made. Windows file names can't
// have colons.
const logTimeFormat = "2006-01-02T15-04-05Z"

// logName matches the names OpenLog gives log files, with the run's start
// time and, for a second run in the same second, a number.
var logName = regexp.MustCompile(`^(\d{4}-\d\d-\d\dT\d\d-\d\d-\d\dZ)(?:-(\d+))?\.log$`)

// Log is one run's log file. It's safe for concurrent use.
type Log struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

// OpenLog creates the log file for a run that started at now in dir
// (creating dir, readable only by the user, if needed), writes header at
// the top, and deletes the oldest logs in dir so only the newest KeepLogs
// remain. A log that can't be deleted is noted in the new log rather than
// stopping the run.
func OpenLog(dir string, now time.Time, header string) (*Log, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("couldn't create the logs folder: %w", err)
	}
	base := now.UTC().Format(logTimeFormat)
	var f *os.File
	for n := 1; f == nil; n++ {
		name := base + ".log"
		if n > 1 {
			name = fmt.Sprintf("%s-%d.log", base, n)
		}
		var err error
		f, err = os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil && (!errors.Is(err, fs.ErrExist) || n >= 100) {
			return nil, fmt.Errorf("couldn't create a log file: %w", err)
		}
	}
	l := &Log{f: f, path: f.Name()}

	if header != "" {
		if !strings.HasSuffix(header, "\n") {
			header += "\n"
		}
		if _, err := l.Write([]byte(header + "\n")); err != nil {
			_ = l.Close()
			return nil, fmt.Errorf("couldn't write the log file: %w", err)
		}
	}
	if err := pruneLogs(dir, filepath.Base(l.path), KeepLogs); err != nil {
		_, _ = fmt.Fprintf(l, "couldn't delete old logs: %v\n\n", err)
	}
	return l, nil
}

// Path returns the log file's path.
func (l *Log) Path() string {
	return l.path
}

// Write appends p to the log.
func (l *Log) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Write(p)
}

// Close closes the log file.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

// pruneLogs deletes the oldest log files in dir, keeping the newest keep,
// and always current, the run's own. Other files are left alone.
func pruneLogs(dir, current string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type logFile struct {
		name, time string
		n          int
	}
	var logs []logFile
	for _, e := range entries {
		m := logName.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() || e.Name() == current {
			continue
		}
		n := 1
		if m[2] != "" {
			if n, err = strconv.Atoi(m[2]); err != nil {
				continue
			}
		}
		logs = append(logs, logFile{name: e.Name(), time: m[1], n: n})
	}
	keep-- // current is one of the logs kept
	if len(logs) <= keep {
		return nil
	}
	slices.SortFunc(logs, func(a, b logFile) int {
		return cmp.Or(strings.Compare(a.time, b.time), cmp.Compare(a.n, b.n))
	})
	var errs []error
	for _, lf := range logs[:len(logs)-keep] {
		if err := os.Remove(filepath.Join(dir, lf.name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
