// Package tz resolves the server time zone from the most authoritative
// available source, first valid wins (adapted from the freshmen verifier
// gateway's resolveServerTz):
//
//  1. TZ env var
//  2. /etc/localtime — the bind-mounted host zone. The IANA name is
//     recovered from the symlink target (Debian/Alpine hosts), or, when
//     the mount is a regular file (macOS Docker Desktop), by byte-matching
//     against the tz database shipped in the image
//  3. /etc/timezone — the plain-text IANA zone written by Debian/Ubuntu
//     tzdata. NOTE: deliberately not ranked above /etc/localtime because
//     macOS Docker Desktop synthesizes this file with the VM default
//     (Etc/UTC) instead of the host zone
//  4. system default, then UTC
//
// The choice and its source are logged once so a silent-UTC
// misconfiguration is visible.
package tz

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	// Embed the tz database so TZ names resolve even in a container
	// without the tzdata package installed.
	_ "time/tzdata"

	"github.com/sirupsen/logrus"
)

const (
	localtimePath = "/etc/localtime"
	timezonePath  = "/etc/timezone"
	zoneInfoDir   = "/usr/share/zoneinfo"
)

var (
	once     sync.Once
	location *time.Location
	source   string
)

// ResolveLocal returns the local zone and where it came from, applying the
// result to time.Local. Safe for concurrent use; resolved once.
func ResolveLocal() (*time.Location, string) {
	once.Do(resolve)
	return location, source
}

func resolve() {
	for _, c := range candidates() {
		if c.name == "" {
			continue
		}
		loc, err := time.LoadLocation(c.name)
		if err != nil {
			continue
		}
		location, source = loc, c.src
		time.Local = loc
		logrus.Infof("timezone resolved: %s (via %s)", c.name, c.src)
		return
	}

	// Fall back to whatever Go already loaded, then UTC.
	location, source = time.Local, "system default"
	logrus.Infof("timezone resolved: %s (via %s)", location.String(), source)
}

// candidate is one possible zone name with a human-readable source label.
type candidate struct {
	name string
	src  string
}

// candidates builds the zone resolution list in priority order.
func candidates() []candidate {
	var ret []candidate
	if env := strings.TrimSpace(os.Getenv("TZ")); env != "" {
		ret = append(ret, candidate{env, "TZ env"})
	}
	if name := sysZoneName(); name != "" {
		ret = append(ret, candidate{name, localtimePath + " symlink"})
	}
	if name := matchLocaltimeFile(); name != "" {
		ret = append(ret, candidate{name, localtimePath + " content"})
	}
	if data, err := os.ReadFile(timezonePath); err == nil {
		if fileTz := strings.TrimSpace(string(data)); fileTz != "" {
			ret = append(ret, candidate{fileTz, timezonePath})
		}
	}
	return ret
}

// sysZoneName recovers an IANA zone name from the /etc/localtime symlink,
// e.g. /usr/share/zoneinfo/Asia/Shanghai -> Asia/Shanghai. Returns "" when
// /etc/localtime is absent or a regular file (macOS Docker Desktop mounts
// it as a regular file).
func sysZoneName() string {
	target, err := os.Readlink(localtimePath)
	if err != nil {
		return ""
	}
	// Handle relative symlink targets.
	if !strings.HasPrefix(target, "/") {
		target = filepath.Join("/etc", target)
	}
	i := strings.Index(target, "zoneinfo/")
	if i < 0 {
		return ""
	}
	name := strings.TrimPrefix(target[i:], "zoneinfo/")
	if name == "" || strings.Contains(name, "..") {
		return ""
	}
	return name
}

// matchLocaltimeFile byte-compares a regular-file /etc/localtime against
// the zone database in the image and returns a matching zone name. Zone
// aliases share content; the alphabetically first non-duplicate name wins,
// which is deterministic and offset-equivalent.
func matchLocaltimeFile() string {
	st, err := os.Stat(localtimePath)
	if err != nil || !st.Mode().IsRegular() {
		return ""
	}
	data, err := os.ReadFile(localtimePath)
	if err != nil || len(data) == 0 {
		return ""
	}

	var matches []string
	_ = filepath.WalkDir(zoneInfoDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil // keep walking on per-entry errors
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(data)) {
			return nil
		}
		fileData, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if bytes.Equal(fileData, data) {
			name, _ := filepath.Rel(zoneInfoDir, path)
			if isCanonicalZone(name) {
				matches = append(matches, name)
			}
		}
		return nil
	})
	if len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return matches[0]
}

// isCanonicalZone filters out tz database duplicate trees and metadata
// files so the byte-match picks a real zone name.
func isCanonicalZone(name string) bool {
	if strings.HasPrefix(name, "posix/") || strings.HasPrefix(name, "right/") {
		return false
	}
	// Skip tzdata metadata files sometimes shipped alongside zones.
	switch name {
	case "localtime", "posixrules", "iso3166.tab", "zone.tab", "zone1970.tab", "tzdata.zi", "leapseconds":
		return false
	}
	return true
}
