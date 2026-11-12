// Package remote will drive a remote Monty worker over the monty.v1 gRPC
// protocol. For now the module only carries the vendored protocol definition
// (proto/) and the Go code generated from it (internal/montyv1); see
// proto/UPSTREAM for the pinned upstream version.
package remote
