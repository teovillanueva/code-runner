package runner

import (
	"encoding/base64"
	"fmt"
	"path"
	"strings"

	"github.com/teovillanueva/code-runner/packages/contract/gen/go/wire"
)

// decodeFileContent returns the raw bytes of a FileInput, interpreting
// f.Content according to f.Encoding.
//
//   - "" (absent) or "utf8" → the content string is taken verbatim as bytes
//     (back-compat: every existing text caller is unaffected).
//   - "base64"               → the content string is base64-decoded; a clear
//     error is returned on malformed input.
//
// The worker never trusts the API's validation, so this is the single decode
// path used by both the DockerSocketRunner and the ZygoteRunner relay.
func decodeFileContent(f wire.FileInput) ([]byte, error) {
	switch f.Encoding {
	case "", wire.FileInputEncodingUtf8:
		return []byte(f.Content), nil
	case wire.FileInputEncodingBase64:
		raw, err := base64.StdEncoding.DecodeString(f.Content)
		if err != nil {
			return nil, fmt.Errorf("file %q: invalid base64 content: %w", f.Name, err)
		}
		return raw, nil
	default:
		return nil, fmt.Errorf("file %q: unknown encoding %q (want utf8 or base64)", f.Name, f.Encoding)
	}
}

// sanitizeWorkspacePath converts a caller-supplied wire file name into a safe
// relative path anchored at the workspace root. Subdirectories are preserved
// (e.g. "data/input.csv"); absolute paths and any ".." traversal are rejected
// rather than silently collapsed, because the threat model is host-escape-only
// and a path that tries to escape is a signal, not noise.
//
// Wire paths are always forward-slash, so this uses the `path` package (not
// `filepath`) to stay platform-independent regardless of the worker's OS.
func sanitizeWorkspacePath(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("file name is empty")
	}
	if path.IsAbs(name) {
		return "", fmt.Errorf("file name %q is absolute; only relative workspace paths are allowed", name)
	}
	// Reject any ".." segment outright rather than silently collapsing it.
	// path.Clean("/"+"../x") would fold to "/x" (inside the root), but the
	// threat model treats an attempted traversal as a signal, not noise: the
	// worker never trusts the path, so a "../" anywhere is an error.
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return "", fmt.Errorf("file name %q contains a '..' traversal segment", name)
		}
	}
	// Clean folds redundant "." and "//" while preserving legitimate subdirs.
	clean := path.Clean("/" + name)
	rel := strings.TrimPrefix(clean, "/")
	if rel == "" || rel == "." {
		return "", fmt.Errorf("file name %q does not resolve to a file inside the workspace", name)
	}
	return rel, nil
}
