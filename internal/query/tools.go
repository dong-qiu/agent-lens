//go:build tools

// Package-level tool dependencies. Kept under the `tools` build tag so
// they are tracked by `go mod tidy` but excluded from normal builds.
package query

import _ "github.com/99designs/gqlgen"
