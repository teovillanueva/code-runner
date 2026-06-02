//go:build tools

// Package tools pins build-time and indirect dependencies that are not yet
// imported by production code but are required by later Phase 2 plans. This
// ensures go.mod carry-forward for Docker SDK, Pusher SDK, and testify without
// requiring a placeholder import in production packages.
package tools

import (
	_ "github.com/docker/docker/client"
	_ "github.com/pusher/pusher-http-go/v5"
	_ "github.com/stretchr/testify/assert"
)
