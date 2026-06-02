// Package main is the minimal worker boot entrypoint for Phase 1.
// It loads all language manifests from the languages directory, logs the
// available languages, and constructs stub Runner + StdinTransport seams to
// prove the Phase 1 interfaces compile together in a single binary.
//
// No Docker, Redis, or real I/O is started here — that is Phase 2/3.
package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/teovillanueva/code-runner/internal/manifest"
	"github.com/teovillanueva/code-runner/internal/runner"
	"github.com/teovillanueva/code-runner/internal/stdintransport"
)

func main() {
	dir := os.Getenv("LANGUAGES_DIR")
	if dir == "" {
		dir = "languages"
	}

	if err := run(dir, os.Stdout); err != nil {
		slog.Error("worker boot failed", "error", err)
		os.Exit(1)
	}
}

// run loads the manifest registry from dir, logs available languages, and
// constructs the Phase-1 stub seams. It writes a human-readable summary to out.
// Factored out of main so tests can call it directly without spawning a process.
func run(dir string, out io.Writer) error {
	// Verify the languages directory exists before attempting to load manifests
	// so callers receive a clear error rather than an empty registry.
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("languages directory %q: %w", dir, err)
	}

	reg, err := manifest.Load(dir)
	if err != nil {
		return fmt.Errorf("load manifests from %q: %w", dir, err)
	}

	languages := reg.List()
	slog.Info("worker starting", "available_languages", len(languages))
	for _, info := range languages {
		slog.Info("language available",
			"language", info.Language,
			"version", info.Version,
			"aliases", info.Aliases,
			"interactive", info.Interactive,
		)
		fmt.Fprintf(out, "language: %s version: %s aliases: %v interactive: %v\n",
			info.Language, info.Version, info.Aliases, info.Interactive)
	}

	// Construct the Phase-1 stub seams to prove they compile into the binary.
	// Real implementations (DockerSocketRunner, RedisPubSubTransport) are wired
	// in Phase 2; the stubs satisfy the interfaces with zero external dependencies.
	r := runner.NewStub()
	t := stdintransport.NewStub()

	slog.Info("seams initialized",
		"runner", fmt.Sprintf("%T", r),
		"stdin_transport", fmt.Sprintf("%T", t),
	)

	return nil
}
