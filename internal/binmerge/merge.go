package binmerge

// The bytes. Both directions are a plain sequential copy — the interesting
// part is the cleanup: a merge or split that fails halfway leaves a bin that
// looks like a dump but isn't, so anything already written is removed before
// the error goes up. That includes a Ctrl-C, which is why the copy loops check
// the context between chunks rather than trusting the process to die tidily.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const copyChunkSize = 1 << 20

// copyN copies n bytes, checking for cancellation between chunks. A short read
// means the source didn't hold what the cue claimed.
func copyN(ctx context.Context, dst io.Writer, src io.Reader, n int64, what string) error {
	buf := make([]byte, copyChunkSize)
	for n > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk := int64(copyChunkSize)
		if n < chunk {
			chunk = n
		}
		read, err := io.CopyBuffer(dst, io.LimitReader(src, chunk), buf)
		if err != nil {
			return err
		}
		if read == 0 {
			return errors.New(what)
		}
		n -= read
	}
	return nil
}

// copyAll copies to EOF, checking for cancellation between chunks.
func copyAll(ctx context.Context, dst io.Writer, src io.Reader) error {
	buf := make([]byte, copyChunkSize)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := io.CopyBuffer(dst, io.LimitReader(src, copyChunkSize), buf)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
	}
}

// mergeFiles concatenates the bins in cue order into one.
func mergeFiles(ctx context.Context, outPath string, files []BinFile, log *logger) (err error) {
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer func() {
		cerr := out.Close()
		if err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(outPath) // don't leave a partial bin behind
		}
	}()

	for _, f := range files {
		log.debugf("Appending %s", filepath.Base(f.Path))
		// Assigned, not declared: a := here would shadow the named err and the
		// deferred cleanup would see nil and leave the partial bin on disk.
		var src *os.File
		src, err = os.Open(f.Path)
		if err != nil {
			return err
		}
		err = copyAll(ctx, out, src)
		src.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// splitOutput is one track and the file its sectors go to.
type splitOutput struct {
	Track Track
	Path  string
}

// splitFile cuts the merged bin back into one file per track. On any failure —
// including a cancelled context — every file this call created is removed.
func splitFile(ctx context.Context, merged BinFile, outputs []splitOutput, blocksize int, log *logger) (err error) {
	var written []string
	defer func() {
		if err != nil {
			for _, p := range written {
				os.Remove(p) // don't leave partial bins behind
			}
		}
	}()

	src, err := os.Open(merged.Path)
	if err != nil {
		return err
	}
	defer src.Close()

	for _, o := range outputs {
		if _, err = src.Seek(int64(o.Track.Start())*int64(blocksize), io.SeekStart); err != nil {
			return err
		}
		log.debugf("Writing %s (%d sectors)", filepath.Base(o.Path), o.Track.Sectors)
		written = append(written, o.Path)
		var out *os.File // assigned, not declared — see mergeFiles
		out, err = os.Create(o.Path)
		if err != nil {
			return err
		}
		err = copyN(ctx, out, src, int64(o.Track.Sectors)*int64(blocksize),
			fmt.Sprintf("Unexpected end of file in %s while writing %s",
				filepath.Base(merged.Path), filepath.Base(o.Path)))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}
