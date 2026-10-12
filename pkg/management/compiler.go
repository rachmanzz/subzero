package management

import (
	"context"
)

// Compiler defines the pluggable contract for compiling source code into WebAssembly.
type Compiler interface {
	// Name returns the identifier of the compiler adapter (e.g. "go", "tinygo")
	Name() string

	// Compile transforms Go source code into WebAssembly bytecode.
	Compile(ctx context.Context, sourceCode []byte, opts CompileOptions) (*CompileResult, error)
}
