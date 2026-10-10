package api

import (
	"context"
	"io"
	"io/fs"

	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/noncepad/catmsg"
	sgo "github.com/solana-foundation/solana-go/v2"
)

type FilesystemWrapper interface {
	// List file info in a directory
	LsInfo(ctx context.Context, req *filesystem.LsInfoRequest) ([]filesystem.FileInfo, error)

	// Read file content, supports line offset and limit
	Read(ctx context.Context, req *filesystem.ReadRequest) (*filesystem.FileContent, error)

	// batch write file content
	BatchRead(ctx context.Context, listPath []*ReadHeader) []*io.PipeReader

	// Search for matching content in files
	GrepRaw(ctx context.Context, req *filesystem.GrepRequest) ([]filesystem.GrepMatch, error)

	// Match files by glob pattern
	GlobInfo(ctx context.Context, req *filesystem.GlobInfoRequest) ([]filesystem.FileInfo, error)

	// Write file content
	Write(ctx context.Context, req *filesystem.WriteRequest) error

	// batch write file content
	BatchWrite(ctx context.Context, listWrite []*WriteHeader) error

	// Edit file content (string replacement)
	Edit(ctx context.Context, req *filesystem.EditRequest) error
}
type ReadHeader struct {
	Path   string
	Offset int
	Limit  int
}
type WriteHeader struct {
	Path   string
	Size   int
	Reader io.ReadCloser
}

type BotCoder interface {
	// Compile the wasm binary. The startingFiles get copied into the base directory of the coding directory in the container compiling the code.
	Compiler(ctx context.Context, startingFiles fs.FS) (BotCompiler, error)
}

type LogResult struct {
	LogC   <-chan string
	ErrorC <-chan error
	// BinaryC receives one CompileBinary per file named in Compile's own
	// binaryPath argument, as the server streams it back inline with the
	// log output -- nil for Lint/Format/Test, which never produce binary
	// output. Closed once the underlying stream ends, at the same moment
	// ErrorC receives its final value.
	BinaryC <-chan CompileBinary
}

// CompileBinary carries one exported file's bytes, named by the
// crate-relative path requested via Compile's binaryPath argument --
// mirrors machinev1.CompileResponse_Binary's own Path/Data fields
// one-for-one.
type CompileBinary struct {
	Path string
	Data []byte
}

type BotCompiler interface {
	// FileSystem returns a live view onto the coding container's own
	// filesystem -- every Read/Write/Edit/LsInfo/GrepRaw/GlobInfo call
	// reads or mutates that container directly, not any host-side copy,
	// so an LLM editing through it is editing the same files Compile
	// builds from.
	FileSystem(ctx context.Context) (FilesystemWrapper, error)

	// Compile the code
	Compile(ctx context.Context, binaryPath []string) LogResult

	// Lint the code
	Lint(ctx context.Context) LogResult

	// Format the code in place. paths, when non-empty, are crate-relative
	// file paths passed directly to `rustfmt` (not `cargo fmt -- ...`,
	// which does not actually scope to the given paths - see the real
	// implementation's own doc comment) so only those files are
	// reformatted; an empty paths formats the whole crate (plain
	// `cargo fmt`).
	Format(ctx context.Context, paths []string) error

	// Test the code
	Test(ctx context.Context) LogResult

	// WARNING: generic exec
	Exec(ctx context.Context, command string, args []string, mEnv map[string]string) (stdin io.WriteCloser, stdout io.ReadCloser, stderr io.ReadCloser)
}

type BotRuntime interface {
	Status() RuntimeStatusType
	// Deploy uploads the wasm image at pathToWasm and waits for it to
	// complete its handshake (or ctx to end). args becomes the
	// deployed process's own argv on the validator runtime; env is
	// forwarded to it as environment variables -- env["MODE"] in
	// particular selects which behavior the image runs (e.g. "marketwatchv1"),
	// so it must be set for the deployed bot to do anything meaningful.
	Deploy(ctx context.Context, pathToWasm string, args []string, env map[string]string) (Bot, error)
	BotList() []Bot
}

// BotRuntimeManager manages endpoints to Catscope WASM runtimes via Bot Solpipe market place.
type BotRuntimeManager interface {
	// Connect to a remote Bot runtime
	Connect(ctx context.Context, pipelineID sgo.PublicKey) (BotRuntime, error)
}

type Bot struct {
	Hash  sgo.Hash
	SendC chan<- catmsg.FixedPair
	RecvC <-chan catmsg.FixedPair
}

type RuntimeStatusType = uint8

const (
	RuntimeStatusTypeOffline   RuntimeStatusType = 0
	RuntimeStatusTypeNonbidder RuntimeStatusType = 1
	RuntimeStatusTypeBidder    RuntimeStatusType = 2
)
