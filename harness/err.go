package harness

import "errors"

var (
	ErrCompileNonzeroExit       = errors.New("cargo build exited non-zero")
	ErrLintNonzeroExit          = errors.New("cargo clippy exited non-zero")
	ErrTestNonzeroExit          = errors.New("cargo test exited non-zero")
	ErrFmtNonzeroExit           = errors.New("cargo fmt exited non-zero")
	ErrMachineAllocationTimeout = errors.New("timeout on machine allocation")
)
