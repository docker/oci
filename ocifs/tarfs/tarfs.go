// Package tarfs builds an fs.FS over a decompressed tar stream backed by an
// io.ReaderAt. The filesystem is immutable after construction; all read paths
// are safe for concurrent use.
package tarfs

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"path"
	"sort"
	"strings"
	"time"
)

// ErrSymlinkLoop is returned (wrapped in *fs.PathError) when symlink resolution
// in Open exceeds the per-call hop budget of 255 hops.
var ErrSymlinkLoop = errors.New("tarfs: too many levels of symbolic links")

const (
	maxSymlinkHops  = 256
	maxHardlinkHops = 8
)

// Header holds the subset of tar entry metadata that tarfs requires. It is
// JSON-serializable with stable round-trip semantics; ModTime is stored as
// Unix nanoseconds rather than time.Time to avoid timezone drift.
type Header struct {
	Typeflag byte        `json:"typeflag"`
	Name     string      `json:"name"`
	Linkname string      `json:"linkname,omitempty"`
	Size     int64       `json:"size"`
	Mode     fs.FileMode `json:"mode"`
	Uid      int         `json:"uid,omitempty"`
	Gid      int         `json:"gid,omitempty"`
	Uname    string      `json:"uname,omitempty"`
	Gname    string      `json:"gname,omitempty"`
	ModTime  int64       `json:"modtime,omitempty"`
}

// Entry pairs a Header with its byte offset in the decompressed tar stream
// and a normalized Filename (no leading ./ or /, no trailing /).
type Entry struct {
	Header   Header `json:"header"`
	Offset   int64  `json:"offset"`
	Filename string `json:"filename"`
}

// FS is a read-only fs.FS over a tar archive accessed via io.ReaderAt.
type FS struct {
	ra    io.ReaderAt
	files []Entry
	index map[string]int
	dirs  map[string][]fs.DirEntry
}

// New scans the tar archive in ra (length size; pass -1 if unknown) and
// returns a ready-to-use FS. Implicit ancestor directories are synthesized.
func New(ra io.ReaderAt, size int64) (*FS, error) {
	if size < 0 {
		size = math.MaxInt64
	}
	sr := io.NewSectionReader(ra, 0, size)
	entries, err := scan(sr)
	if err != nil {
		return nil, err
	}
	return NewFromEntries(ra, entries)
}

// Index scans the tar stream r and returns the raw entry list. Implicit
// ancestor directories are NOT synthesized; that is the responsibility of
// NewFromEntries when constructing a live FS.
func Index(r io.Reader) ([]Entry, error) {
	return scan(r)
}

// NewFromEntries constructs a live FS from a pre-built entry slice and a
// random-access decompressed reader. Synthesizes implicit ancestor
// directories. Whiteout entries (.wh. prefix, .wh..wh..opq) remain in the
// path index (so Open succeeds) but are excluded from ReadDir results.
// Entries whose Filename contains a ".." component are silently dropped to
// prevent TarSlip/ZipSlip path traversal.
func NewFromEntries(ra io.ReaderAt, entries []Entry) (*FS, error) {
	safe := entries[:0:0]
	for _, e := range entries {
		if !containsDotDot(e.Filename) {
			safe = append(safe, e)
		}
	}
	entries = safe

	files := make([]Entry, len(entries))
	copy(files, entries)

	idx := make(map[string]int, len(files))
	for i, e := range files {
		idx[e.Filename] = i
	}

	synth := synthesizeAncestors(files, idx)
	if len(synth) > 0 {
		base := len(files)
		files = append(files, synth...)
		for i, e := range synth {
			idx[e.Filename] = base + i
		}
	}

	dirs := buildDirs(files, idx)

	return &FS{ra: ra, files: files, index: idx, dirs: dirs}, nil
}

// Entries returns a copy of the underlying entry slice. Synthetic ancestor
// directory entries created during construction are excluded; whiteouts and
// hardlinks are included.
func (f *FS) Entries() []Entry {
	out := make([]Entry, 0, len(f.files))
	for _, e := range f.files {
		if e.synthetic() {
			continue
		}
		out = append(out, e)
	}
	return out
}

func (e Entry) synthetic() bool {
	// Synthetic ancestor directories are minted with a sentinel header
	// (Mode=fs.ModeDir|0755, Typeflag=tar.TypeDir, all other fields zero,
	// Offset=0, Size=0). The cleanest way to tag them without bloating
	// Entry is by Header.Name being empty: real entries always have a
	// non-empty Header.Name, while synthesized ones leave it blank.
	return e.Header.Name == "" && e.Header.Typeflag == tar.TypeDir
}

func scan(r io.Reader) ([]Entry, error) {
	cr := &countingReader{r: r}
	tr := tar.NewReader(cr)
	var entries []Entry
	for {
		th, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tarfs: read tar header: %w", err)
		}
		offset := cr.n
		mode := th.FileInfo().Mode()
		filename := normalize(th.Name)
		// Skip entries with ".." components to prevent TarSlip path traversal.
		if containsDotDot(filename) {
			continue
		}
		entries = append(entries, Entry{
			Header: Header{
				Typeflag: th.Typeflag,
				Name:     th.Name,
				Linkname: th.Linkname,
				Size:     th.Size,
				Mode:     mode,
				Uid:      th.Uid,
				Gid:      th.Gid,
				Uname:    th.Uname,
				Gname:    th.Gname,
				ModTime:  th.ModTime.UnixNano(),
			},
			Offset:   offset,
			Filename: filename,
		})
	}
	return entries, nil
}

// containsDotDot reports whether any component of the normalized path is "..".
// Such entries are rejected to prevent TarSlip/ZipSlip path traversal attacks.
func containsDotDot(p string) bool {
	for _, c := range strings.Split(p, "/") {
		if c == ".." {
			return true
		}
	}
	return false
}

// countingReader wraps an io.Reader to track total bytes consumed so the
// scan loop can record each entry's data offset.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// normalize converts a tar entry name to the canonical FS key form: strip
// leading "./" or "/", strip trailing "/".
func normalize(name string) string {
	for strings.HasPrefix(name, "./") {
		name = name[2:]
	}
	for strings.HasPrefix(name, "/") {
		name = name[1:]
	}
	for strings.HasSuffix(name, "/") {
		name = name[:len(name)-1]
	}
	if name == "." {
		name = ""
	}
	return name
}

// isWhiteout reports whether the path's base is an OCI whiteout marker.
func isWhiteout(filename string) bool {
	base := path.Base(filename)
	return base == ".wh..wh..opq" || strings.HasPrefix(base, ".wh.")
}

// synthesizeAncestors returns synthetic directory entries for any ancestor
// path implied by entries' filenames but missing from idx.
func synthesizeAncestors(entries []Entry, idx map[string]int) []Entry {
	seen := make(map[string]bool)
	var out []Entry
	for _, e := range entries {
		if e.Filename == "" {
			continue
		}
		parts := strings.Split(e.Filename, "/")
		for i := 1; i < len(parts); i++ {
			anc := strings.Join(parts[:i], "/")
			if anc == "" {
				continue
			}
			if _, ok := idx[anc]; ok {
				continue
			}
			if seen[anc] {
				continue
			}
			seen[anc] = true
			out = append(out, Entry{
				Header: Header{
					Typeflag: tar.TypeDir,
					Mode:     fs.ModeDir | 0o755,
				},
				Filename: anc,
			})
		}
	}
	return out
}

// buildDirs constructs the directory listing map. Whiteouts are excluded.
// Each slice is sorted by Name() ascending. Hardlink entries reference the
// resolved target so their DirEntry.Info reports the real Size/Mode.
func buildDirs(entries []Entry, idx map[string]int) map[string][]fs.DirEntry {
	dirs := make(map[string][]fs.DirEntry)
	dirs[""] = nil
	for _, e := range entries {
		if e.Header.Mode.IsDir() {
			if _, ok := dirs[e.Filename]; !ok {
				dirs[e.Filename] = nil
			}
		}
	}
	for i := range entries {
		e := entries[i]
		if e.Filename == "" {
			continue
		}
		if isWhiteout(e.Filename) {
			continue
		}
		parent, base := splitParent(e.Filename)
		target := &entries[i]
		if target.Header.Typeflag == tar.TypeLink {
			if resolved, ok := resolveHardlink(entries, idx, target); ok {
				target = resolved
			}
		}
		dirs[parent] = append(dirs[parent], &dirEntry{
			name:  base,
			entry: target,
		})
	}
	for k, v := range dirs {
		sort.Slice(v, func(i, j int) bool { return v[i].Name() < v[j].Name() })
		dirs[k] = v
	}
	return dirs
}

func resolveHardlink(entries []Entry, idx map[string]int, e *Entry) (*Entry, bool) {
	cur := e
	for hop := 0; hop < maxHardlinkHops && cur.Header.Typeflag == tar.TypeLink; hop++ {
		i, ok := idx[normalize(cur.Header.Linkname)]
		if !ok {
			return nil, false
		}
		cur = &entries[i]
	}
	if cur.Header.Typeflag == tar.TypeLink {
		return nil, false
	}
	return cur, true
}

func splitParent(p string) (parent, base string) {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return "", p
	}
	return p[:i], p[i+1:]
}

// Open implements fs.FS. Symlinks are followed up to maxSymlinkHops-1
// hops; hardlinks up to maxHardlinkHops hops.
func (f *FS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if name == "." {
		return f.openDir("", name), nil
	}

	hops := maxSymlinkHops
	components := splitPath(name)
	stack := make([]string, 0, len(components))
	var current *Entry

	for len(components) > 0 {
		c := components[0]
		components = components[1:]
		switch c {
		case ".":
			continue
		case "..":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			current = nil
			continue
		}
		stack = append(stack, c)
		resolved := strings.Join(stack, "/")
		i, ok := f.index[resolved]
		if !ok {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
		}
		current = &f.files[i]

		if current.Header.Mode&fs.ModeSymlink != 0 {
			if current.Header.Linkname == "" {
				return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
			}
			hops--
			if hops == 0 {
				return nil, &fs.PathError{Op: "open", Path: name, Err: ErrSymlinkLoop}
			}
			target := current.Header.Linkname
			var rebuilt []string
			if path.IsAbs(target) {
				rebuilt = append(rebuilt, splitPath(target)...)
			} else {
				rebuilt = append(rebuilt, stack[:len(stack)-1]...)
				rebuilt = append(rebuilt, splitPath(target)...)
			}
			rebuilt = append(rebuilt, components...)
			components = rebuilt
			stack = stack[:0]
			current = nil
			continue
		}

		if current.Header.Mode.IsDir() {
			continue
		}

		// non-directory, non-symlink — must be the final component
		if len(components) > 0 {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
		}
	}

	if len(stack) == 0 {
		return f.openDir("", name), nil
	}

	resolved := strings.Join(stack, "/")
	if current == nil {
		i, ok := f.index[resolved]
		if !ok {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
		}
		current = &f.files[i]
	}

	if current.Header.Mode.IsDir() {
		return f.openDir(resolved, name), nil
	}

	target, ok := resolveHardlink(f.files, f.index, current)
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return f.openFile(target, name), nil
}

func (f *FS) openFile(e *Entry, openArg string) fs.File {
	return &file{
		entry:   e,
		openArg: openArg,
		sr:      io.NewSectionReader(f.ra, e.Offset, e.Header.Size),
	}
}

func (f *FS) openDir(resolved, openArg string) fs.File {
	return &dirFile{
		fs:       f,
		resolved: resolved,
		openArg:  openArg,
		entries:  f.dirs[resolved],
	}
}

// ReadDir implements fs.ReadDirFS. Calls Open and forwards to the directory
// handle's ReadDir(-1) so symlinks naming directories are followed.
func (f *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	file, err := f.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	rdf, ok := file.(fs.ReadDirFile)
	if !ok {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	return rdf.ReadDir(-1)
}

// Stat returns metadata for name with symlinks followed.
func (f *FS) Stat(name string) (fs.FileInfo, error) {
	file, err := f.Open(name)
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			pe.Op = "stat"
		}
		return nil, err
	}
	defer file.Close()
	return file.Stat()
}

// Lstat returns metadata for name without following a final-component symlink.
// Intermediate components are still resolved through symlinks.
func (f *FS) Lstat(name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "lstat", Path: name, Err: fs.ErrInvalid}
	}
	if name == "." {
		return rootFileInfo(name), nil
	}

	parent, base := splitParent(name)
	var dirPath string
	if parent == "" {
		dirPath = ""
	} else {
		// Resolve the parent through Open's traversal so intermediate
		// symlinks are followed; then assemble the final-component lookup.
		fi, err := f.Stat(parent)
		if err != nil {
			pe := &fs.PathError{Op: "lstat", Path: name, Err: fs.ErrNotExist}
			if errors.Is(err, fs.ErrInvalid) {
				pe.Err = fs.ErrInvalid
			}
			return nil, pe
		}
		if !fi.IsDir() {
			return nil, &fs.PathError{Op: "lstat", Path: name, Err: fs.ErrNotExist}
		}
		var rdErr error
		dirPath, rdErr = f.resolveDir(parent)
		if rdErr != nil {
			return nil, &fs.PathError{Op: "lstat", Path: name, Err: rdErr}
		}
	}

	var lookup string
	if dirPath == "" {
		lookup = base
	} else {
		lookup = dirPath + "/" + base
	}
	i, ok := f.index[lookup]
	if !ok {
		return nil, &fs.PathError{Op: "lstat", Path: name, Err: fs.ErrNotExist}
	}
	return entryFileInfo(&f.files[i], base), nil
}

// resolveDir runs Open's traversal but returns the canonical directory path
// instead of a file handle. Used by Lstat for parent resolution. Caller
// guarantees name is a valid directory. Returns ErrSymlinkLoop if the hop
// budget is exhausted while resolving intermediate symlinks.
func (f *FS) resolveDir(name string) (string, error) {
	hops := maxSymlinkHops
	components := splitPath(name)
	stack := make([]string, 0, len(components))
	for len(components) > 0 {
		c := components[0]
		components = components[1:]
		switch c {
		case ".":
			continue
		case "..":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			continue
		}
		stack = append(stack, c)
		resolved := strings.Join(stack, "/")
		i, ok := f.index[resolved]
		if !ok {
			return resolved, nil
		}
		cur := &f.files[i]
		if cur.Header.Mode&fs.ModeSymlink != 0 && cur.Header.Linkname != "" {
			hops--
			if hops == 0 {
				return "", ErrSymlinkLoop
			}
			target := cur.Header.Linkname
			var rebuilt []string
			if path.IsAbs(target) {
				rebuilt = append(rebuilt, splitPath(target)...)
			} else {
				rebuilt = append(rebuilt, stack[:len(stack)-1]...)
				rebuilt = append(rebuilt, splitPath(target)...)
			}
			rebuilt = append(rebuilt, components...)
			components = rebuilt
			stack = stack[:0]
			continue
		}
	}
	return strings.Join(stack, "/"), nil
}

// splitPath strips any leading "/" then splits on "/" filtering empty parts.
func splitPath(p string) []string {
	for strings.HasPrefix(p, "/") {
		p = p[1:]
	}
	if p == "" {
		return nil
	}
	parts := strings.Split(p, "/")
	out := parts[:0]
	for _, c := range parts {
		if c == "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// dirEntry implements fs.DirEntry backed by an Entry pointer.
type dirEntry struct {
	name  string
	entry *Entry
}

func (d *dirEntry) Name() string { return d.name }
func (d *dirEntry) IsDir() bool  { return d.entry.Header.Mode.IsDir() }
func (d *dirEntry) Type() fs.FileMode {
	return d.entry.Header.Mode.Type()
}
func (d *dirEntry) Info() (fs.FileInfo, error) {
	return entryFileInfo(d.entry, d.name), nil
}

// fileInfo implements fs.FileInfo built from a Header.
type fileInfo struct {
	name string
	h    Header
}

func (fi *fileInfo) Name() string       { return fi.name }
func (fi *fileInfo) Size() int64        { return fi.h.Size }
func (fi *fileInfo) Mode() fs.FileMode  { return fi.h.Mode }
func (fi *fileInfo) ModTime() time.Time { return time.Unix(0, fi.h.ModTime).UTC() }
func (fi *fileInfo) IsDir() bool        { return fi.h.Mode.IsDir() }
func (fi *fileInfo) Sys() any           { return nil }

func entryFileInfo(e *Entry, name string) fs.FileInfo {
	return &fileInfo{name: name, h: e.Header}
}

func rootFileInfo(openArg string) fs.FileInfo {
	return &fileInfo{
		name: path.Base(openArg),
		h:    Header{Typeflag: tar.TypeDir, Mode: fs.ModeDir | 0o755},
	}
}

// Compile-time interface checks.
var (
	_ fs.FS        = (*FS)(nil)
	_ fs.ReadDirFS = (*FS)(nil)
	_ fs.StatFS    = (*FS)(nil)
)
