package alerts

import "time"

// Bands evaluates warning and critical independently from a shared numeric observation.
type Bands struct {
	Query    string
	Unit     string
	Warning  Band
	Critical Band
}

type Band struct {
	Trigger, Recovery float64
	For, RecoveryFor  time.Duration
}

func (s *State) resetTimers() {
	s.PendingSince, s.RecoverySince = time.Time{}, time.Time{}
	s.CriticalPendingSince, s.CriticalRecoverySince = time.Time{}, time.Time{}
}

func advanceBands(bands Bands, old, next State, value float64, now time.Time) State {
	warning := advanceBand(bands.Warning, old.AlertID != "", value > bands.Warning.Trigger, value < bands.Warning.Recovery,
		&next.PendingSince, &next.RecoverySince, now)
	critical := advanceBand(bands.Critical, old.AlertID != "" && old.Severity == "critical", value > bands.Critical.Trigger, value < bands.Critical.Recovery,
		&next.CriticalPendingSince, &next.CriticalRecoverySince, now)
	next.Status = "inactive"
	switch {
	case critical:
		next.Status, next.Severity = "firing", "critical"
	case warning:
		next.Status, next.Severity = "firing", "warning"
	case old.AlertID != "":
		next.Status = "resolved"
	case !next.PendingSince.IsZero() || !next.CriticalPendingSince.IsZero():
		next.Status = "pending"
	}
	return next
}

func advanceBand(band Band, firing, breached, recovered bool, pending, recovery *time.Time, now time.Time) bool {
	if firing {
		*pending = time.Time{}
		if !recovered {
			*recovery = time.Time{}
			return true
		}
		if recovery.IsZero() {
			*recovery = now
		}
		return now.Sub(*recovery) < band.RecoveryFor
	}
	*recovery = time.Time{}
	if !breached {
		*pending = time.Time{}
		return false
	}
	if pending.IsZero() {
		*pending = now
	}
	return now.Sub(*pending) >= band.For
}
