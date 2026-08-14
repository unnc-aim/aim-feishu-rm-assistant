package tz

import (
	"os"
	"testing"
	"time"
)

func TestCandidatesPreferTZEnv(t *testing.T) {
	t.Setenv("TZ", "Asia/Tokyo")
	cands := candidates()
	if len(cands) == 0 || cands[0].name != "Asia/Tokyo" || cands[0].src != "TZ env" {
		t.Fatalf("top candidate = %+v, want Asia/Tokyo via TZ env", cands)
	}
}

func TestCandidatesResolveToValidZone(t *testing.T) {
	t.Setenv("TZ", "")
	for _, c := range candidates() {
		loc, err := time.LoadLocation(c.name)
		if err != nil {
			t.Errorf("candidate %q (%s) is not a valid zone: %v", c.name, c.src, err)
			continue
		}
		t.Logf("resolved via %s: %s", c.src, loc)
		return
	}
	t.Error("no candidate zone resolved on this host")
}

func TestSysZoneName(t *testing.T) {
	// On a host where /etc/localtime is a regular file (macOS Docker
	// Desktop mounts it as one) the symlink lookup must return "", not an
	// error path. Lstat: the path itself, not the symlink target.
	info, err := os.Lstat(localtimePath)
	switch {
	case err != nil:
		t.Skip("no /etc/localtime on this host")
	case info.Mode().IsRegular():
		if got := sysZoneName(); got != "" {
			t.Errorf("sysZoneName on regular file = %q, want empty", got)
		}
	default:
		if got := sysZoneName(); got == "" {
			t.Log("symlinked /etc/localtime but no zoneinfo path recovered")
		} else {
			t.Logf("symlink resolves to %s", got)
		}
	}
}

func TestIsCanonicalZone(t *testing.T) {
	for _, ok := range []string{"Asia/Shanghai", "UTC", "Etc/UTC", "America/New_York"} {
		if !isCanonicalZone(ok) {
			t.Errorf("isCanonicalZone(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"posix/Asia/Shanghai", "right/Europe/Berlin", "zone.tab", "leapseconds"} {
		if isCanonicalZone(bad) {
			t.Errorf("isCanonicalZone(%q) = true, want false", bad)
		}
	}
}
