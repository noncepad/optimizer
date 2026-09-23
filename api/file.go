package api

import (
	"context"
	"io"
)

type FileStore interface {
	Read(ctx context.Context, path string) (io.ReadCloser, error)
	Write(ctx context.Context, path string) (io.WriteCloser, error)
}
