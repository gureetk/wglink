//go:build !unix

package state

// Lock is a no-op where flock is unavailable.
func Lock(dir string) (func(), error) { return func() {}, nil }
