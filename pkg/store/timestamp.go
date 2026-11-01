package store

import (
	"fmt"
	"strings"
	"time"
)

const snapshotAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
const snapshotEpoch int64 = 1735689600
const snapshotTicks int64 = 1 << 30

// EncodeSnapshotTimestamp implements FL-RFD-0008's six-character, three-second timestamp.
func EncodeSnapshotTimestamp(t time.Time) (string, error) {
	if t.Before(time.Unix(snapshotEpoch, 0)) || !t.Before(time.Unix(snapshotEpoch+snapshotTicks*3, 0)) {
		return "", fmt.Errorf("snapshot timestamp outside RFD-0008 range")
	}
	seconds := t.Unix() - snapshotEpoch
	tick := seconds / 3
	var out [6]byte
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = snapshotAlphabet[tick&31]
		tick >>= 5
	}
	return string(out[:]), nil
}

// DecodeSnapshotTimestamp returns the beginning of the encoded three-second interval.
func DecodeSnapshotTimestamp(s string) (time.Time, error) {
	if len(s) != 6 {
		return time.Time{}, fmt.Errorf("snapshot timestamp must have six characters")
	}
	var tick int64
	for _, c := range strings.ToUpper(s) {
		switch c {
		case 'O':
			c = '0'
		case 'I', 'L':
			c = '1'
		}
		n := strings.IndexRune(snapshotAlphabet, c)
		if n < 0 {
			return time.Time{}, fmt.Errorf("invalid snapshot timestamp character %q", c)
		}
		tick = tick*32 + int64(n)
	}
	return time.Unix(snapshotEpoch+tick*3, 0).UTC(), nil
}

func AutomatedSnapshotName(t time.Time) (string, error) {
	stamp, err := EncodeSnapshotTimestamp(t)
	if err != nil {
		return "", err
	}
	return "fl-" + stamp, nil
}

// IsAutomatedSnapshot excludes legacy and manually named snapshots from automatic pruning.
func IsAutomatedSnapshot(name string) bool {
	if len(name) != 9 || !strings.HasPrefix(name, "fl-") {
		return false
	}
	for _, c := range name[3:] {
		if !strings.ContainsRune(snapshotAlphabet, c) {
			return false
		}
	}
	return true
}
