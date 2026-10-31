//go:build tools

// Package main pins the code generators this module runs, so `go mod tidy` keeps them in go.sum.
package main

import (
	_ "github.com/99designs/gqlgen"
)
