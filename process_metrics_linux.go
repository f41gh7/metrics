package metrics

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
	"unsafe"
)

// See https://github.com/prometheus/procfs/blob/a4ac0826abceb44c40fc71daed2b301db498b93e/proc_stat.go#L40 .
const userHZ = 100

// See http://man7.org/linux/man-pages/man5/proc.5.html
type procStat struct {
	State       byte
	Ppid        int
	Pgrp        int
	Session     int
	TtyNr       int
	Tpgid       int
	Flags       uint
	Minflt      uint
	Cminflt     uint
	Majflt      uint
	Cmajflt     uint
	Utime       uint
	Stime       uint
	Cutime      int
	Cstime      int
	Priority    int
	Nice        int
	NumThreads  int
	ItrealValue int
	Starttime   uint64
	Vsize       uint
	Rss         int
}

func writeProcessMetrics(w io.Writer) {
	statFilepath := "/proc/self/stat"
	data, err := ioutil.ReadFile(statFilepath)
	if err != nil {
		log.Printf("ERROR: cannot open %s: %s", statFilepath, err)
		return
	}
	// Search for the end of command.
	n := bytes.LastIndex(data, []byte(") "))
	if n < 0 {
		log.Printf("ERROR: cannot find command in parentheses in %q read from %s", data, statFilepath)
		return
	}
	data = data[n+2:]

	var p procStat
	bb := bytes.NewBuffer(data)
	_, err = fmt.Fscanf(bb, "%c %d %d %d %d %d %d %d %d %d %d %d %d %d %d %d %d %d %d %d %d %d",
		&p.State, &p.Ppid, &p.Pgrp, &p.Session, &p.TtyNr, &p.Tpgid, &p.Flags, &p.Minflt, &p.Cminflt, &p.Majflt, &p.Cmajflt,
		&p.Utime, &p.Stime, &p.Cutime, &p.Cstime, &p.Priority, &p.Nice, &p.NumThreads, &p.ItrealValue, &p.Starttime, &p.Vsize, &p.Rss)
	if err != nil {
		log.Printf("ERROR: cannot parse %q read from %s: %s", data, statFilepath, err)
		return
	}
	rssPageCache, rssAnonymous, err := getRSSStats()
	if err != nil {
		log.Printf("ERROR: cannot obtain RSS page cache bytes: %s", err)
		return
	}

	// It is expensive obtaining `process_open_fds` when big number of file descriptors is opened,
	// so don't do it here.
	// See writeFDMetrics instead.

	utime := float64(p.Utime) / userHZ
	stime := float64(p.Stime) / userHZ
	fmt.Fprintf(w, "process_cpu_seconds_system_total %g\n", stime)
	fmt.Fprintf(w, "process_cpu_seconds_total %g\n", utime+stime)
	fmt.Fprintf(w, "process_cpu_seconds_user_total %g\n", utime)
	fmt.Fprintf(w, "process_major_pagefaults_total %d\n", p.Majflt)
	fmt.Fprintf(w, "process_minor_pagefaults_total %d\n", p.Minflt)
	fmt.Fprintf(w, "process_num_threads %d\n", p.NumThreads)
	fmt.Fprintf(w, "process_resident_memory_bytes %d\n", p.Rss*4096)
	fmt.Fprintf(w, "process_resident_memory_anonymous_bytes %d\n", rssAnonymous)
	fmt.Fprintf(w, "process_resident_memory_pagecache_bytes %d\n", rssPageCache)
	fmt.Fprintf(w, "process_start_time_seconds %d\n", startTimeSeconds)
	fmt.Fprintf(w, "process_virtual_memory_bytes %d\n", p.Vsize)

	writeIOMetrics(w)
}

func writeIOMetrics(w io.Writer) {
	ioFilepath := "/proc/self/io"
	data, err := ioutil.ReadFile(ioFilepath)
	if err != nil {
		log.Printf("ERROR: cannot open %q: %s", ioFilepath, err)
	}
	getInt := func(s string) int64 {
		n := strings.IndexByte(s, ' ')
		if n < 0 {
			log.Printf("ERROR: cannot find whitespace in %q at %q", s, ioFilepath)
			return 0
		}
		v, err := strconv.ParseInt(s[n+1:], 10, 64)
		if err != nil {
			log.Printf("ERROR: cannot parse %q at %q: %s", s, ioFilepath, err)
			return 0
		}
		return v
	}
	var rchar, wchar, syscr, syscw, readBytes, writeBytes int64
	lines := strings.Split(string(data), "\n")
	for _, s := range lines {
		s = strings.TrimSpace(s)
		switch {
		case strings.HasPrefix(s, "rchar: "):
			rchar = getInt(s)
		case strings.HasPrefix(s, "wchar: "):
			wchar = getInt(s)
		case strings.HasPrefix(s, "syscr: "):
			syscr = getInt(s)
		case strings.HasPrefix(s, "syscw: "):
			syscw = getInt(s)
		case strings.HasPrefix(s, "read_bytes: "):
			readBytes = getInt(s)
		case strings.HasPrefix(s, "write_bytes: "):
			writeBytes = getInt(s)
		}
	}
	fmt.Fprintf(w, "process_io_read_bytes_total %d\n", rchar)
	fmt.Fprintf(w, "process_io_written_bytes_total %d\n", wchar)
	fmt.Fprintf(w, "process_io_read_syscalls_total %d\n", syscr)
	fmt.Fprintf(w, "process_io_write_syscalls_total %d\n", syscw)
	fmt.Fprintf(w, "process_io_storage_read_bytes_total %d\n", readBytes)
	fmt.Fprintf(w, "process_io_storage_written_bytes_total %d\n", writeBytes)
}

var startTimeSeconds = time.Now().Unix()

// riteFDMetrics writes process_max_fds and process_open_fds metrics to w.
func writeFDMetrics(w io.Writer) {
	totalOpenFDs, err := getOpenFDsCount("/proc/self/fd")
	if err != nil {
		log.Printf("ERROR: cannot determine open file descriptors count: %s", err)
		return
	}
	maxOpenFDs, err := getMaxFilesLimit("/proc/self/limits")
	if err != nil {
		log.Printf("ERROR: cannot determine the limit on open file descritors: %s", err)
		return
	}
	fmt.Fprintf(w, "process_max_fds %d\n", maxOpenFDs)
	fmt.Fprintf(w, "process_open_fds %d\n", totalOpenFDs)
}

func getOpenFDsCount(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var totalOpenFDs uint64
	for {
		names, err := f.Readdirnames(512)
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("unexpected error at Readdirnames: %s", err)
		}
		totalOpenFDs += uint64(len(names))
	}
	return totalOpenFDs, nil
}

func getMaxFilesLimit(path string) (uint64, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return 0, err
	}
	lines := strings.Split(string(data), "\n")
	const prefix = "Max open files"
	for _, s := range lines {
		if !strings.HasPrefix(s, prefix) {
			continue
		}
		text := strings.TrimSpace(s[len(prefix):])
		// Extract soft limit.
		n := strings.IndexByte(text, ' ')
		if n < 0 {
			return 0, fmt.Errorf("cannot extract soft limit from %q", s)
		}
		text = text[:n]
		if text == "unlimited" {
			return 1<<64 - 1, nil
		}
		limit, err := strconv.ParseUint(text, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("cannot parse soft limit from %q: %s", s, err)
		}
		return limit, nil
	}
	return 0, fmt.Errorf("cannot find max open files limit")
}

// getRSSStats returns RSS bytes for page cache and anonymous memory.
func getRSSStats() (uint64, uint64, error) {
	filepath := "/proc/self/smaps"
	f, err := os.Open(filepath)
	if err != nil {
		return 0, 0, fmt.Errorf("cannot open %q: %w", filepath, err)
	}
	defer func() {
		_ = f.Close()
	}()
	rssPageCache, rssAnonymous, err := getRSSStatsFromSmaps(f)
	if err != nil {
		return 0, 0, fmt.Errorf("cannot read %q: %w", filepath, err)
	}
	return rssPageCache, rssAnonymous, nil
}

func getRSSStatsFromSmaps(r io.Reader) (uint64, uint64, error) {
	var pageCacheBytes, anonymousBytes uint64
	var se smapsEntry
	ses := newSmapsEntryScanner(r)
	for ses.Next(&se) {
		if se.anonymousBytes == 0 {
			pageCacheBytes += se.rssBytes
		} else {
			anonymousBytes += se.rssBytes
		}
	}
	if err := ses.Err(); err != nil {
		return 0, 0, err
	}
	return pageCacheBytes, anonymousBytes, nil
}

type smapsEntry struct {
	rssBytes       uint64
	anonymousBytes uint64
}

func (se *smapsEntry) reset() {
	se.rssBytes = 0
	se.anonymousBytes = 0
}

type smapsEntryScanner struct {
	bs  *bufio.Scanner
	err error
}

func newSmapsEntryScanner(r io.Reader) *smapsEntryScanner {
	return &smapsEntryScanner{
		bs: bufio.NewScanner(r),
	}
}

func (ses *smapsEntryScanner) Err() error {
	return ses.err
}

// nextSmapsEntry reads the next se from ses.
//
// It returns true after successful read and false on error or on the end of stream.
// ses.Err() method must be called for determining the error.
func (ses *smapsEntryScanner) Next(se *smapsEntry) bool {
	se.reset()
	if !ses.bs.Scan() {
		ses.err = ses.bs.Err()
		return false
	}
	for ses.bs.Scan() {
		line := unsafeBytesToString(ses.bs.Bytes())
		switch {
		case strings.HasPrefix(line, "VmFlags:"):
			return true
		case strings.HasPrefix(line, "Rss:"):
			n, err := getSmapsSize(line[len("Rss:"):])
			if err != nil {
				ses.err = fmt.Errorf("cannot read Rss size: %w", err)
				return false
			}
			se.rssBytes = n
		case strings.HasPrefix(line, "Anonymous:"):
			n, err := getSmapsSize(line[len("Anonymous:"):])
			if err != nil {
				ses.err = fmt.Errorf("cannot read Anonymous size: %w", err)
				return false
			}
			se.anonymousBytes = n
		}
	}
	ses.err = ses.bs.Err()
	if ses.err == nil {
		ses.err = fmt.Errorf("unexpected end of stream")
	}
	return false
}

func getSmapsSize(line string) (uint64, error) {
	line = strings.TrimSpace(line)
	if !strings.HasSuffix(line, " kB") {
		return 0, fmt.Errorf("cannot find %q suffix in %q", " kB", line)
	}
	line = line[:len(line)-len(" kB")]
	n, err := strconv.ParseUint(line, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("cannot parse %q: %w", line, err)
	}
	if n > ((1<<64)-1)/1024 {
		return 0, fmt.Errorf("too big size in %q: %d kB", line, n)
	}
	return n * 1024, nil
}

func unsafeBytesToString(b []byte) string {
	return *(*string)(unsafe.Pointer(&b))
}
