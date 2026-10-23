//go:build linux

package platform

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// clockTicks is USER_HZ, which is 100 on every Linux ABI Go targets.
const clockTicks = 100

func describePID(pid int) (ProcDetails, bool) {
	base := readProcInfo(pid)
	if base == nil {
		return ProcDetails{}, false
	}
	d := ProcDetails{
		PID:     pid,
		PPID:    base.PPID,
		Command: base.Command,
		Cmdline: base.Cmdline,
		Cwd:     base.Cwd,
	}

	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		// Indices are relative to the field after comm (state = 0).
		f := parseStatFieldsAfterComm(data)
		if len(f) > 21 {
			utime, _ := strconv.ParseUint(f[11], 10, 64)
			stime, _ := strconv.ParseUint(f[12], 10, 64)
			d.CPUSeconds = float64(utime+stime) / clockTicks
			if n, err := strconv.Atoi(f[17]); err == nil {
				d.Threads = n
			}
			if start, err := strconv.ParseUint(f[19], 10, 64); err == nil {
				if boot, ok := bootTime(); ok {
					d.StartTime = boot.Add(time.Duration(start) * time.Second / clockTicks)
					d.Uptime = time.Since(d.StartTime).Round(time.Second)
				}
			}
			if rss, err := strconv.ParseInt(f[21], 10, 64); err == nil && rss > 0 {
				d.RSSBytes = uint64(rss) * uint64(os.Getpagesize())
			}
		}
	}

	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid)); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if rest, ok := strings.CutPrefix(line, "Uid:"); ok {
				if fields := strings.Fields(rest); len(fields) > 0 {
					d.User = fields[0]
				}
				break
			}
		}
	}
	return d, true
}

// bootTime reads the system boot time from /proc/stat's btime line.
func bootTime() (time.Time, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "btime "); ok {
			if sec, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64); err == nil {
				return time.Unix(sec, 0), true
			}
		}
	}
	return time.Time{}, false
}
