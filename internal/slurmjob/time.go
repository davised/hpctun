// Package slurmjob finds, starts, waits for and stops the one Slurm job
// behind `hpctun code`, by running short scripts on the gateway over ssh.
package slurmjob

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var digits = regexp.MustCompile(`^[0-9]+$`)

// ParseTime parses a Slurm --time value: minutes, MM:SS, HH:MM:SS, D-HH,
// D-HH:MM or D-HH:MM:SS. It rejects UNLIMITED and zero: hpctun code needs a
// finite limit to compute the job's deadline.
func ParseTime(s string) (time.Duration, error) {
	bad := fmt.Errorf("%q is not a Slurm time limit: use minutes, MM:SS, HH:MM:SS, D-HH, D-HH:MM or D-HH:MM:SS", s)
	num := func(p string) (int, bool) {
		if !digits.MatchString(p) {
			return 0, false
		}
		n, err := strconv.Atoi(p)
		return n, err == nil
	}
	days, rest, hasDays := 0, s, false
	if d, r, ok := strings.Cut(s, "-"); ok {
		n, ok := num(d)
		if !ok {
			return 0, bad
		}
		days, rest, hasDays = n, r, true
	}
	parts := strings.Split(rest, ":")
	nums := make([]int, len(parts))
	for i, p := range parts {
		n, ok := num(p)
		if !ok {
			return 0, bad
		}
		nums[i] = n
	}
	var h, m, sec int
	switch {
	case hasDays && len(nums) == 1:
		h = nums[0]
	case hasDays && len(nums) == 2:
		h, m = nums[0], nums[1]
	case len(nums) == 3:
		h, m, sec = nums[0], nums[1], nums[2]
	case !hasDays && len(nums) == 1:
		m = nums[0]
	case !hasDays && len(nums) == 2:
		m, sec = nums[0], nums[1]
	default:
		return 0, bad
	}
	d := time.Duration(days)*24*time.Hour + time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(sec)*time.Second
	if d <= 0 {
		return 0, bad
	}
	return d, nil
}

// Deadline is the sbatch --deadline value for a job with time limit limit
// that must start within startWithin: Slurm removes a pending job once it
// can no longer finish by then. Whole minutes, rounded up.
func Deadline(limit, startWithin time.Duration) string {
	m := (limit + startWithin + time.Minute - 1) / time.Minute
	return fmt.Sprintf("now+%dminutes", m)
}
