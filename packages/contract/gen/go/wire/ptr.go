package wire

// Ptr returns a pointer to v. It is a tiny convenience for constructing the
// optional pointer fields the JSON-Schema codegen emits (e.g. FileInput.Content,
// FileInput.Ref, *int / *string fields). It is NOT generated — `pnpm contract`
// only rewrites wire.gen.go, so this helper is stable across regeneration and
// `make contract-check` (which diffs only generated artifacts) ignores it.
//
// Usage: wire.FileInput{Name: "main.py", Content: wire.Ptr("print(1)")}.
func Ptr[T any](v T) *T { return &v }
