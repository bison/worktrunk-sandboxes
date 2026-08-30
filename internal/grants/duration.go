package grants

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseDuration parses a grant duration: "0" for no expiry, Nd for days,
// and anything time.ParseDuration accepts (90s, 45m, 2h, 1h30m).
func ParseDuration(s string) (time.Duration, error) {
	if s == "0" {
		return 0, nil
	}
	if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil && strings.HasSuffix(s, "d") {
		if n <= 0 {
			return 0, fmt.Errorf("bad duration %q (use Ns/Nm/Nh/Nd or 0)", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("bad duration %q (use Ns/Nm/Nh/Nd or 0)", s)
	}
	return d, nil
}

// FormatDuration renders a duration compactly (1h30m, 45m, 90s, 2d).
func FormatDuration(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	}
	s := d.Round(time.Second).String()
	s = strings.TrimSuffix(s, "0s")
	s = strings.TrimSuffix(s, "0m")
	if s == "" {
		s = "0s"
	}
	return s
}

// FormatExpiry renders a state-row expiry for display: "never", "OVERDUE",
// or time remaining like 1h05m / 12m.
func FormatExpiry(expiry int64, now time.Time) string {
	if expiry == 0 {
		return "never"
	}
	left := expiry - now.Unix()
	if left <= 0 {
		return "OVERDUE"
	}
	m := left / 60
	if m >= 60 {
		return fmt.Sprintf("%dh%02dm", m/60, m%60)
	}
	return fmt.Sprintf("%dm", m)
}
