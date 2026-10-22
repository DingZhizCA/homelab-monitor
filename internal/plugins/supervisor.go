/*
Package plugins defines the process-supervision boundary for plugin runtimes.

The interface lets Agent orchestration depend on lifecycle operations instead
of process internals. It is only a boundary: it does not provide OS isolation
until a concrete implementation enforces permissions and resource limits.
*/
package plugins

import "context"

// ProcessSpec identifies a verified local artifact selected for execution.
type ProcessSpec struct {
	Manifest     Manifest
	ArtifactPath string
}

// Process is the controlled handle returned for one isolated plugin process.
type Process interface {
	// SocketPath returns the private Unix socket used for Plugin Host RPC.
	SocketPath() string
	// Stop requests graceful shutdown and enforces the caller's deadline.
	Stop(context.Context) error
}

// Supervisor owns plugin process creation, termination, and OS-level limits.
// Implementations must verify execution permissions and resource enforcement;
// merely implementing this interface does not create a security boundary.
type Supervisor interface {
	Start(context.Context, ProcessSpec) (Process, error)
}

// PluginAuthorizer decides whether a local policy approves the package and its declared capabilities.
// It is a preflight gate only; a Supervisor must still enforce capabilities at the OS boundary.
type PluginAuthorizer interface {
	AuthorizePlugin(Manifest) error
}
