package tarfs

import (
	"io"
	"io/fs"
	"path"
)

// Compile-time interface checks.
var (
	_ fs.File        = (*file)(nil)
	_ io.ReaderAt    = (*file)(nil)
	_ io.Seeker      = (*file)(nil)
	_ io.WriterTo    = (*file)(nil)
	_ fs.ReadDirFile = (*dirFile)(nil)
)

// file is the fs.File returned for regular and hardlink-resolved entries.
// Implements io.WriterTo by forwarding to the underlying SectionReader so
// io.Copy avoids allocating a 32 KB buffer.
type file struct {
	entry   *Entry
	openArg string
	sr      *io.SectionReader
}

func (f *file) Read(p []byte) (int, error)              { return f.sr.Read(p) }
func (f *file) ReadAt(p []byte, off int64) (int, error) { return f.sr.ReadAt(p, off) }
func (f *file) Seek(off int64, whence int) (int64, error) {
	return f.sr.Seek(off, whence)
}

// WriteTo allows io.Copy to bypass its 32 KB staging buffer. We stream
// directly from the backing io.ReaderAt into w in 64 KB chunks, advancing
// the SectionReader's cursor so subsequent Reads continue from the right
// position.
func (f *file) WriteTo(w io.Writer) (int64, error) {
	pos, err := f.sr.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	remaining := f.entry.Header.Size - pos
	if remaining <= 0 {
		return 0, nil
	}
	const chunk = 64 * 1024
	buf := make([]byte, chunk)
	var written int64
	for remaining > 0 {
		want := int64(len(buf))
		if want > remaining {
			want = remaining
		}
		n, rerr := f.sr.Read(buf[:want])
		if n > 0 {
			wn, werr := w.Write(buf[:n])
			written += int64(wn)
			if werr != nil {
				return written, werr
			}
			if wn != n {
				return written, io.ErrShortWrite
			}
			remaining -= int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return written, rerr
		}
	}
	return written, nil
}
func (f *file) Close() error { return nil }
func (f *file) Stat() (fs.FileInfo, error) {
	return entryFileInfo(f.entry, path.Base(f.openArg)), nil
}

// dirFile is the fs.ReadDirFile returned for directory entries. Its cursor
// makes it not safe for concurrent use; callers needing parallel iteration
// must Open separately.
type dirFile struct {
	fs       *FS
	resolved string
	openArg  string
	entries  []fs.DirEntry
	cursor   int
}

func (d *dirFile) Read(p []byte) (int, error) {
	pathForError := d.resolved
	if pathForError == "" {
		pathForError = "."
	}
	return 0, &fs.PathError{Op: "read", Path: pathForError, Err: fs.ErrInvalid}
}

func (d *dirFile) Close() error { return nil }

func (d *dirFile) Stat() (fs.FileInfo, error) {
	if d.resolved == "" {
		return rootFileInfo(d.openArg), nil
	}
	if i, ok := d.fs.index[d.resolved]; ok {
		return entryFileInfo(&d.fs.files[i], path.Base(d.openArg)), nil
	}
	return rootFileInfo(d.openArg), nil
}

func (d *dirFile) ReadDir(n int) ([]fs.DirEntry, error) {
	remaining := len(d.entries) - d.cursor
	if n <= 0 {
		out := make([]fs.DirEntry, remaining)
		copy(out, d.entries[d.cursor:])
		d.cursor = len(d.entries)
		return out, nil
	}
	if remaining == 0 {
		return nil, io.EOF
	}
	take := n
	if take > remaining {
		take = remaining
	}
	out := make([]fs.DirEntry, take)
	copy(out, d.entries[d.cursor:d.cursor+take])
	d.cursor += take
	if take < n {
		return out, io.EOF
	}
	return out, nil
}
