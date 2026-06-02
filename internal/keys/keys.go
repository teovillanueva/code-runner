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
	EventStage  = "stage"
	EventStdout = "stdout"
	EventStderr = "stderr"
	EventResult = "result"
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
