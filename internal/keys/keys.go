// Package keys mirrors the Redis key / channel / event name conventions
// exported from packages/contract/src/index.ts.  These are wire-visible
// strings shared by the Go worker and the TS API — keep them in lockstep.
package keys

import "fmt"

// JobQueue is the Redis list the API LPUSHes jobs onto and the worker BRPOPs
// from.  Matches keys.jobQueue in index.ts.
const JobQueue = "jobs:queue"

// Event name constants emitted on the private-run-<jobId> soketi channel.
// Match the `events` object in index.ts.
const (
	EventStage    = "stage"
	EventStdout   = "stdout"
	EventStderr   = "stderr"
	EventResult   = "result"
	EventArtifact = "artifact"
	// EventCompileOutput is the live interleaved build log of the compile stage
	// (compiled languages only). Kept separate from EventStdout/EventStderr so
	// the client can render a dedicated real-time build panel. Matches
	// events.compileOutput in index.ts.
	EventCompileOutput = "compile_output"
)

// JobStatusKey returns the Redis key holding the JSON-encoded JobStatus for
// a job.  Matches keys.jobStatus(id) in index.ts.
func JobStatusKey(jobID string) string {
	return fmt.Sprintf("job:%s:status", jobID)
}

// JobSpecKey returns the Redis key holding the JSON-encoded JobSpec for a job.
// Matches keys.jobSpec(id) in index.ts.
func JobSpecKey(jobID string) string {
	return fmt.Sprintf("job:%s:spec", jobID)
}

// JobOutputKey returns the Redis key holding the JSON-encoded RunResult
// (collected output) for a job.  Written with a TTL by the worker at teardown.
// Matches keys.jobOutput(id) in index.ts.
func JobOutputKey(jobID string) string {
	return fmt.Sprintf("job:%s:output", jobID)
}

// ChannelForJob returns the soketi channel name for a job.
// Matches channelForJob(id) in index.ts.
func ChannelForJob(jobID string) string {
	return fmt.Sprintf("private-run-%s", jobID)
}

// StdinChannel returns the Redis pub/sub channel for stdin delivery to a job.
// Matches stdinChannel(id) in index.ts.
func StdinChannel(jobID string) string {
	return fmt.Sprintf("stdin:%s", jobID)
}

// ControlChannel returns the Redis pub/sub channel for lifecycle control of a
// job.  Matches controlChannel(id) in index.ts.
func ControlChannel(jobID string) string {
	return fmt.Sprintf("ctrl:%s", jobID)
}

// ── Worker-internal keys (reaper-consumed, not API-readable) ──────────────────

// WorkerHeartbeatKey returns the Redis key for a worker's heartbeat.  The key
// is set with a TTL by the worker on each heartbeat interval; it disappears
// automatically after the worker stops.  The reaper (plan 05-02) checks for
// workers whose heartbeat key has expired.
//
// Format: worker:<id>:heartbeat
func WorkerHeartbeatKey(workerID string) string {
	return fmt.Sprintf("worker:%s:heartbeat", workerID)
}

// WorkerJobsKey returns the Redis key for the set of jobIDs currently owned by
// a worker.  The worker adds a jobID on Create and removes it in teardown.  The
// reaper reads this set to recover orphaned jobs when a worker dies.
//
// Format: worker:<id>:jobs
func WorkerJobsKey(workerID string) string {
	return fmt.Sprintf("worker:%s:jobs", workerID)
}

// CapacityFree is a best-effort global counter of free sandbox slots across all
// workers.  Workers INCR/DECR it on slot acquire/release.  This is a secondary
// capacity signal; the authoritative admission gate is the queue depth (LLEN
// jobs:queue, used in plan 05-03).  Counter drift is acceptable — it must not
// be used for hard admission decisions.
const CapacityFree = "capacity:free"
