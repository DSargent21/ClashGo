package main

import (
	"encoding/json"
	"os"
	"time"

	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/rs/zerolog/log"
)

// sessionRecord is one bot run: when the session became live and when it
// stopped. The loot/hour estimator divides a session's stolen loot by this
// span, so boot settling, search, training and teardown are all wall clock.
//
// Records are only written on a clean stop; a crash or a kill leaves no
// record, and the estimator falls back to first-to-last raid for that history.
type sessionRecord struct {
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
}

// sessionHistoryLimit keeps sessions.json small; hundreds of old runs add
// nothing to a rate that only reads the latest one.
const sessionHistoryLimit = 100

// appendSessionRecord persists one completed run. Failures are logged, not
// returned: losing a session record only downgrades the rate to the raid-span
// fallback, it must never block stopping the bot.
func appendSessionRecord(started, ended time.Time) {
	if started.IsZero() || !ended.After(started) {
		return
	}
	records := loadSessionRecords()
	records = append(records, sessionRecord{
		// Nano keeps sub-second runs distinct; a Start/Stop pair inside
		// one second must still be a valid, non-zero session.
		StartedAt: started.Format(time.RFC3339Nano),
		EndedAt:   ended.Format(time.RFC3339Nano),
	})
	if len(records) > sessionHistoryLimit {
		records = records[len(records)-sessionHistoryLimit:]
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		log.Error().Err(err).Msg("failed to encode sessions.json")
		return
	}
	if err := paths.WriteFileAtomic(paths.ResolveConfig("sessions.json"), data, 0o644); err != nil {
		log.Error().Err(err).Msg("failed to write sessions.json")
	}
}

// loadSessionRecords returns the persisted runs, oldest first. A missing or
// unreadable file is "no session timestamps", which selects the estimator's
// raid-span fallback.
func loadSessionRecords() []sessionRecord {
	data, err := os.ReadFile(paths.ResolveConfig("sessions.json"))
	if err != nil {
		return nil
	}
	var records []sessionRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil
	}
	return records
}

// parse returns the record's bounds, false when either side is missing or the
// span is empty (an interrupted write or a hand-edited file).
func (r sessionRecord) parse() (time.Time, time.Time, bool) {
	started, err := time.Parse(time.RFC3339, r.StartedAt)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	ended, err := time.Parse(time.RFC3339, r.EndedAt)
	if err != nil || !ended.After(started) {
		return time.Time{}, time.Time{}, false
	}
	return started, ended, true
}
