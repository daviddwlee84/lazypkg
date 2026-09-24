package domain

import "time"

// QueryEvent transfers immutable batches. A provider event replaces that
// provider's rows; done carries the final aggregate, including partial coverage.
type QueryEvent struct {
	Stage    string
	Manager  string
	Snapshot Snapshot
	Cached   bool
	Elapsed  time.Duration
	Err      error
}
