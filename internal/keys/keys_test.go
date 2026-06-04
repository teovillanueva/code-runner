package keys_test

import (
	"testing"

	"github.com/teovillanueva/code-runner/internal/keys"
)

func TestJobQueue(t *testing.T) {
	if keys.JobQueue != "jobs:queue" {
		t.Errorf("JobQueue = %q; want %q", keys.JobQueue, "jobs:queue")
	}
}

func TestJobStatusKey(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		{"abc", "job:abc:status"},
		{"123", "job:123:status"},
		{"job-xyz", "job:job-xyz:status"},
	}
	for _, tt := range tests {
		got := keys.JobStatusKey(tt.id)
		if got != tt.want {
			t.Errorf("JobStatusKey(%q) = %q; want %q", tt.id, got, tt.want)
		}
	}
}

func TestJobSpecKey(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		{"abc", "job:abc:spec"},
		{"123", "job:123:spec"},
	}
	for _, tt := range tests {
		got := keys.JobSpecKey(tt.id)
		if got != tt.want {
			t.Errorf("JobSpecKey(%q) = %q; want %q", tt.id, got, tt.want)
		}
	}
}

func TestChannelForJob(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		{"abc", "private-run-abc"},
		{"job-999", "private-run-job-999"},
	}
	for _, tt := range tests {
		got := keys.ChannelForJob(tt.id)
		if got != tt.want {
			t.Errorf("ChannelForJob(%q) = %q; want %q", tt.id, got, tt.want)
		}
	}
}

func TestStdinChannel(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		{"abc", "stdin:abc"},
		{"job-999", "stdin:job-999"},
	}
	for _, tt := range tests {
		got := keys.StdinChannel(tt.id)
		if got != tt.want {
			t.Errorf("StdinChannel(%q) = %q; want %q", tt.id, got, tt.want)
		}
	}
}

func TestControlChannel(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		{"abc", "ctrl:abc"},
		{"job-999", "ctrl:job-999"},
	}
	for _, tt := range tests {
		got := keys.ControlChannel(tt.id)
		if got != tt.want {
			t.Errorf("ControlChannel(%q) = %q; want %q", tt.id, got, tt.want)
		}
	}
}

func TestEventConstants(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"EventStage", keys.EventStage, "stage"},
		{"EventStdout", keys.EventStdout, "stdout"},
		{"EventStderr", keys.EventStderr, "stderr"},
		{"EventResult", keys.EventResult, "result"},
		{"EventArtifact", keys.EventArtifact, "artifact"},
		{"EventCompileOutput", keys.EventCompileOutput, "compile_output"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %q; want %q", tt.name, tt.got, tt.want)
		}
	}
}
