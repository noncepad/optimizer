// Package localdev implements api.BotCoder/api.BotCompiler/
// api.FilesystemWrapper directly against the local filesystem and local
// subprocesses, instead of dialing a real machinev1/builderv1 gRPC
// server backed by a Firecracker guest over SFTP (see harness's own
// compiler.go/fs.go) - for fast local iteration against this package's
// own interfaces without needing that infrastructure running at all.
//
// There is no VM-level isolation here: "the machine" is just a scratch
// directory on whatever machine runs this code, and Exec/Compile/Lint/
// Format/Test run as ordinary local subprocesses. Treat it as a
// developer-trusted convenience, not a sandbox.
package localdev

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"git.noncepad.com/pkg/optimizer/api"
	"github.com/bmatcuk/doublestar/v4"
	adkfs "github.com/cloudwego/eino/adk/filesystem"
)

// localFileSystem implements api.FilesystemWrapper directly against a
// real local directory (root) - the same method set spawner.
// LLMFileSystem (gitlab.noncepad.com/eflam/qemu-pool) implements over
// SFTP, adapted to plain os/path-filepath calls since there is no
// network session to go over.
//
// No attempt is made to stop a path from escaping root (e.g. via "../"):
// unlike the real remote case, there is no VM providing an actual
// isolation boundary here anyway, so enforcing one only at this layer
// would be a false sense of security - see the package doc comment.
type localFileSystem struct {
	root string
}

func (fw *localFileSystem) resolve(p string) string {
	if p == "" || p == "." {
		return fw.root
	}
	return filepath.Join(fw.root, p)
}

// skipTargetOrGit mirrors spawner.skipTargetOrGit's own exclusion
// (gitlab.noncepad.com/eflam/qemu-pool) - never real source content.
func skipTargetOrGit(name string) bool {
	return name == "target" || name == ".git"
}

// LsInfo lists dir's immediate children on local disk.
func (fw *localFileSystem) LsInfo(ctx context.Context, req *adkfs.LsInfoRequest) ([]adkfs.FileInfo, error) {
	dir := req.Path
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(fw.resolve(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return []adkfs.FileInfo{}, nil
		}
		return nil, err
	}
	out := make([]adkfs.FileInfo, 0, len(entries))
	for _, e := range entries {
		if skipTargetOrGit(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, adkfs.FileInfo{
			Path:       e.Name(),
			IsDir:      e.IsDir(),
			Size:       info.Size(),
			ModifiedAt: info.ModTime().UTC().Format(time.RFC3339Nano),
		})
	}
	return out, nil
}

// Read fetches req.FilePath's whole content from local disk and applies
// InMemoryBackend's own offset/limit line-slicing to it via a throwaway
// single-file backend, reusing that logic exactly rather than
// reimplementing it - same trick spawner.LLMFileSystem.Read uses.
func (fw *localFileSystem) Read(ctx context.Context, req *adkfs.ReadRequest) (*adkfs.FileContent, error) {
	data, err := os.ReadFile(fw.resolve(req.FilePath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found: %s", req.FilePath)
		}
		return nil, err
	}
	if req.Offset <= 1 && req.Limit <= 0 {
		return &adkfs.FileContent{Content: string(data)}, nil
	}
	backend := adkfs.NewInMemoryBackend()
	if err := backend.Write(ctx, &adkfs.WriteRequest{FilePath: req.FilePath, Content: string(data)}); err != nil {
		return nil, err
	}
	return backend.Read(ctx, req)
}

// BatchRead starts one Read per entry concurrently, each delivering its
// result (or error) through its own pipe - mirroring fsysWrapper.
// BatchRead's per-file io.PipeReader contract without needing that
// version's gRPC stream-multiplexing machinery, since every read here is
// just a local file open.
func (fw *localFileSystem) BatchRead(ctx context.Context, listPath []*api.ReadHeader) []*io.PipeReader {
	out := make([]*io.PipeReader, len(listPath))
	for i, h := range listPath {
		r, w := io.Pipe()
		out[i] = r
		go func(h *api.ReadHeader, w *io.PipeWriter) {
			content, err := fw.Read(ctx, &adkfs.ReadRequest{FilePath: h.Path, Offset: h.Offset, Limit: h.Limit})
			if err != nil {
				_ = w.CloseWithError(err)
				return
			}
			_, err = w.Write([]byte(content.Content))
			_ = w.CloseWithError(err)
		}(h, w)
	}
	return out
}

// localFileEntry is one real file (never a directory) found by walkFiles.
type localFileEntry struct {
	// path is root-relative, forward-slash-separated, matching
	// spawner.LLMFileSystem.walkFiles' own SFTP-path convention.
	path string
	info os.FileInfo
}

// walkFiles walks the local tree rooted at dir (dir == "" walks the
// whole root) and returns every real file it finds, skipping target/
// and .git/ entirely without descending into either. A file or directory
// that errors mid-walk (e.g. vanished, or dir not existing at all) is
// skipped rather than failing the whole walk - the same lenient,
// best-effort posture LsInfo already takes for a path with nothing
// under it.
func (fw *localFileSystem) walkFiles(dir string) []localFileEntry {
	absRoot := fw.resolve(dir)
	var out []localFileEntry
	_ = filepath.WalkDir(absRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != absRoot && skipTargetOrGit(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(fw.root, p)
		if err != nil {
			return nil
		}
		out = append(out, localFileEntry{path: filepath.ToSlash(rel), info: info})
		return nil
	})
	return out
}

// GlobInfo matches req.Pattern (doublestar syntax, the same library
// InMemoryBackend.GlobInfo itself uses) against every file under
// req.Path - mirrors spawner.LLMFileSystem.GlobInfo's own matchPath
// computation: an absolute pattern (one starting with "/") matches
// against the whole root-relative path regardless of req.Path;
// otherwise matching is relative to req.Path.
func (fw *localFileSystem) GlobInfo(ctx context.Context, req *adkfs.GlobInfoRequest) ([]adkfs.FileInfo, error) {
	isAbsolutePattern := strings.HasPrefix(req.Pattern, "/")

	scopeDir := req.Path
	if isAbsolutePattern {
		scopeDir = ""
	}
	entries := fw.walkFiles(scopeDir)
	basePath := strings.Trim(req.Path, "/")

	var result []adkfs.FileInfo
	for _, e := range entries {
		var matchPath string
		switch {
		case isAbsolutePattern:
			matchPath = "/" + e.path
		case basePath == "":
			matchPath = e.path
		default:
			matchPath = strings.TrimPrefix(e.path, basePath+"/")
		}
		matched, err := doublestar.Match(req.Pattern, matchPath)
		if err != nil {
			return nil, fmt.Errorf("invalid glob pattern: %w", err)
		}
		if !matched {
			continue
		}
		result = append(result, adkfs.FileInfo{
			Path:       matchPath,
			IsDir:      false,
			Size:       e.info.Size(),
			ModifiedAt: e.info.ModTime().UTC().Format(time.RFC3339Nano),
		})
	}
	return result, nil
}

// GrepRaw narrows to candidate files under req.Path via walkFiles,
// pre-filters them by req.Glob (the same matchPath computation
// GlobInfo/InMemoryBackend.filterByGlob use, so a narrow glob - e.g.
// "*.rs" - skips even reading anything that can't possibly match),
// loads only the survivors' content from local disk, and delegates the
// actual Pattern/FileType/CaseInsensitive/EnableMultiline/context-line
// matching to a throwaway InMemoryBackend holding just those files -
// InMemoryBackend.GrepRaw's own logic, unmodified, just never handed
// content it has no chance of needing. Mirrors spawner.LLMFileSystem.
// GrepRaw, minus its worker-pool (a local os.ReadFile has no round trip
// worth bounding concurrency for).
func (fw *localFileSystem) GrepRaw(ctx context.Context, req *adkfs.GrepRequest) ([]adkfs.GrepMatch, error) {
	if req.Pattern == "" {
		return nil, fmt.Errorf("pattern cannot be empty")
	}

	entries := fw.walkFiles(req.Path)
	basePath := strings.Trim(req.Path, "/")

	backend := adkfs.NewInMemoryBackend()
	for _, e := range entries {
		if req.Glob != "" {
			var matchPath string
			if strings.Contains(req.Glob, "/") || strings.Contains(req.Glob, "**") {
				matchPath = e.path
				if basePath != "" {
					matchPath = strings.TrimPrefix(e.path, basePath+"/")
				}
			} else {
				matchPath = path.Base(e.path)
			}
			matched, err := doublestar.Match(req.Glob, matchPath)
			if err != nil {
				return nil, fmt.Errorf("invalid glob pattern: %w", err)
			}
			if !matched {
				continue
			}
		}
		data, err := os.ReadFile(fw.resolve(e.path))
		if err != nil {
			continue // vanished between walk and read - skip, best-effort
		}
		if err := backend.Write(ctx, &adkfs.WriteRequest{FilePath: e.path, Content: string(data)}); err != nil {
			return nil, err
		}
	}

	scoped := *req
	scoped.Path = "" // already scoped to req.Path above, via which files were even loaded
	return backend.GrepRaw(ctx, &scoped)
}

// Write creates req.FilePath's parent directories if needed and
// creates-or-truncates the file with req.Content.
func (fw *localFileSystem) Write(ctx context.Context, req *adkfs.WriteRequest) error {
	target := fw.resolve(req.FilePath)
	if dir := filepath.Dir(target); dir != fw.root {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir -p %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(target, []byte(req.Content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", req.FilePath, err)
	}
	return nil
}

// BatchWrite writes each entry in turn, failing (without writing any
// later entry) if a reader produces fewer bytes than its declared Size -
// mirrors fsysWrapper.BatchWrite's own "premature EOF" check.
func (fw *localFileSystem) BatchWrite(ctx context.Context, listFile []*api.WriteHeader) error {
	for _, f := range listFile {
		data, err := io.ReadAll(f.Reader)
		closeErr := f.Reader.Close()
		if err != nil {
			return fmt.Errorf("read %s: %w", f.Path, err)
		}
		if closeErr != nil {
			return fmt.Errorf("close reader for %s: %w", f.Path, closeErr)
		}
		if len(data) < f.Size {
			return fmt.Errorf("premature EOF: %d vs %d", len(data), f.Size)
		}
		if err := fw.Write(ctx, &adkfs.WriteRequest{FilePath: f.Path, Content: string(data)}); err != nil {
			return err
		}
	}
	return nil
}

// Edit fetches req.FilePath's current content (Read), applies the edit
// via a throwaway single-file InMemoryBackend (reusing its exact
// exact-match-or-ReplaceAll validation and error messages rather than
// reimplementing them), and writes the result back (Write) - mirrors
// spawner.LLMFileSystem.Edit exactly.
func (fw *localFileSystem) Edit(ctx context.Context, req *adkfs.EditRequest) error {
	content, err := fw.Read(ctx, &adkfs.ReadRequest{FilePath: req.FilePath})
	if err != nil {
		return err
	}

	backend := adkfs.NewInMemoryBackend()
	if err := backend.Write(ctx, &adkfs.WriteRequest{FilePath: req.FilePath, Content: content.Content}); err != nil {
		return err
	}
	if err := backend.Edit(ctx, req); err != nil {
		return err
	}
	edited, err := backend.Read(ctx, &adkfs.ReadRequest{FilePath: req.FilePath})
	if err != nil {
		return err
	}
	return fw.Write(ctx, &adkfs.WriteRequest{FilePath: req.FilePath, Content: edited.Content})
}
