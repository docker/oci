package ocifs

import (
	"archive/tar"
	"io"
	"io/fs"
	"log/slog"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/docker/oci/ocifs/tarfs"
)

// overlayEntry is the bottom-of-stack record stored in overlayIndex for a
// resolved path. layerIdx selects the layer whose decompRA backs file
// content; entry holds the resolved tarfs.Entry (for hardlinks the target
// entry, never the link entry itself).
type overlayEntry struct {
	layerIdx int
	entry    tarfs.Entry
}

// overlayIndex maps a normalized path (no leading "/" or "./") to its
// resolved overlay entry. The root directory is NEVER stored here — it is
// synthesized lazily by Open/Stat/ReadDir.
type overlayIndex map[string]*overlayEntry

// overlayDirEntry is the concrete fs.DirEntry stored in overlayDirs.
type overlayDirEntry struct {
	name  string
	entry tarfs.Entry
}

func (d *overlayDirEntry) Name() string      { return d.name }
func (d *overlayDirEntry) IsDir() bool       { return d.entry.Header.Mode.IsDir() }
func (d *overlayDirEntry) Type() fs.FileMode { return d.entry.Header.Mode.Type() }
func (d *overlayDirEntry) Info() (fs.FileInfo, error) {
	return &overlayFileInfo{name: d.name, header: d.entry.Header}, nil
}

// overlayDirs maps a directory path to its sorted child fs.DirEntry slice.
type overlayDirs map[string][]*overlayDirEntry

// overlayFileInfo is the fs.FileInfo built from an overlay entry's header.
type overlayFileInfo struct {
	name   string
	header tarfs.Header
}

func (fi *overlayFileInfo) Name() string       { return fi.name }
func (fi *overlayFileInfo) Size() int64        { return fi.header.Size }
func (fi *overlayFileInfo) Mode() fs.FileMode  { return fi.header.Mode }
func (fi *overlayFileInfo) ModTime() time.Time { return time.Unix(0, fi.header.ModTime).UTC() }
func (fi *overlayFileInfo) IsDir() bool        { return fi.header.Mode.IsDir() }
func (fi *overlayFileInfo) Sys() any           { return nil }

// rootHeader is the synthesized header for the root directory.
func rootHeader() tarfs.Header {
	return tarfs.Header{
		Typeflag: tar.TypeDir,
		Mode:     fs.ModeDir | 0o755,
	}
}

// rootFileInfo returns the fs.FileInfo for the root, named by the original
// argument the caller passed to Open/Stat/ReadDir.
func rootFileInfo(openArg string) fs.FileInfo {
	return &overlayFileInfo{name: path.Base(openArg), header: rootHeader()}
}

// splitPath strips any leading "/" then splits on "/", filtering empty
// components. "/" maps to an empty slice.
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

// splitParentBase splits a normalized path into (parent, base) where
// parent is "" (NOT ".") for root-level entries.
func splitParentBase(p string) (parent, base string) {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return "", p
	}
	return p[:i], p[i+1:]
}

// buildOverlay processes layer tar entries bottom-to-top and produces the
// final overlayIndex/overlayDirs maps. layerEntries[i] must hold the
// entries from layer i (bottom = 0, top = len-1).
func buildOverlay(layerEntries [][]tarfs.Entry) (overlayIndex, overlayDirs) {
	idx := make(overlayIndex)
	dirs := make(overlayDirs)

	for layerIdx, entries := range layerEntries {
		applyLayer(layerIdx, entries, idx, dirs)
	}

	// Sort each directory listing in lexical order (deferred for O(F log F)).
	for k, v := range dirs {
		sort.Slice(v, func(i, j int) bool { return v[i].Name() < v[j].Name() })
		dirs[k] = v
	}
	return idx, dirs
}

// applyLayer applies one layer's entries to the (idx, dirs) maps.
func applyLayer(layerIdx int, entries []tarfs.Entry, idx overlayIndex, dirs overlayDirs) {
	for _, entry := range entries {
		p := entry.Filename
		if p == "" {
			continue
		}
		parent, base := splitParentBase(p)

		// 1. Opaque whiteout
		if base == whiteoutOpaque {
			if parent == "" {
				clearOverlay(idx, dirs)
			} else {
				removePrefix(idx, parent+"/")
				removeDirsPrefix(dirs, parent+"/")
				delete(dirs, parent)
			}
			continue
		}

		// 2. Regular whiteout
		if strings.HasPrefix(base, whiteoutPrefix) {
			deletedName := whiteoutTarget(base)
			// Reject empty, ".", "..", or names that contain a slash —
			// any of these would allow path traversal outside the parent
			// directory.
			if deletedName == "" || deletedName == "." || deletedName == ".." || strings.Contains(deletedName, "/") {
				continue
			}
			var target string
			if parent == "" {
				target = deletedName
			} else {
				target = parent + "/" + deletedName
			}
			wasDir := false
			if cur, ok := idx[target]; ok && cur != nil {
				wasDir = cur.entry.Header.Mode.IsDir()
			}
			delete(idx, target)
			removeFromDirList(dirs, parent, deletedName)
			if wasDir {
				removePrefix(idx, target+"/")
				removeDirsPrefix(dirs, target+"/")
				delete(dirs, target)
			}
			continue
		}

		// 3. Directory
		if entry.Header.Typeflag == tar.TypeDir || entry.Header.Mode.IsDir() {
			synthesizeParents(p, layerIdx, idx, dirs)
			idx[p] = &overlayEntry{layerIdx: layerIdx, entry: entry}
			if _, ok := dirs[p]; !ok {
				dirs[p] = nil
			}
			if p != "" {
				upsertDirEntry(dirs, parent, &overlayDirEntry{name: base, entry: entry})
			}
			continue
		}

		// 4. File, symlink, or hardlink
		var resolved tarfs.Entry
		var resolvedLayer int
		if entry.Header.Typeflag == tar.TypeLink {
			target := resolveHardlink(entry.Header.Linkname, idx)
			if target == nil {
				slog.Default().Warn("ocifs: hardlink target absent",
					"path", p,
					"linkname", entry.Header.Linkname,
				)
				continue
			}
			resolved = target.entry
			resolvedLayer = target.layerIdx
		} else {
			resolved = entry
			resolvedLayer = layerIdx
		}

		idx[p] = &overlayEntry{layerIdx: resolvedLayer, entry: resolved}
		synthesizeParents(p, layerIdx, idx, dirs)
		upsertDirEntry(dirs, parent, &overlayDirEntry{name: base, entry: resolved})
	}
}

// synthesizeParents ensures that each strict ancestor of path is present in
// idx and its parent's dirs entry as a directory. Insert-if-absent only —
// it must never overwrite real metadata. The root ("") is never touched;
// it is synthesized at call time.
func synthesizeParents(p string, layerIdx int, idx overlayIndex, dirs overlayDirs) {
	parts := strings.Split(p, "/")
	if len(parts) <= 1 {
		return
	}
	for i := 1; i < len(parts); i++ {
		ancestor := strings.Join(parts[:i], "/")
		if ancestor == "" {
			continue
		}
		ancParent, ancBase := splitParentBase(ancestor)
		if _, ok := idx[ancestor]; !ok {
			synth := tarfs.Entry{
				Header: tarfs.Header{
					Typeflag: tar.TypeDir,
					Mode:     fs.ModeDir | 0o755,
				},
				Filename: ancestor,
			}
			idx[ancestor] = &overlayEntry{layerIdx: layerIdx, entry: synth}
			if _, ok := dirs[ancestor]; !ok {
				dirs[ancestor] = nil
			}
			insertIfAbsentDirEntry(dirs, ancParent, &overlayDirEntry{name: ancBase, entry: synth})
		} else {
			// Real entry exists; do not overwrite. Ensure its dirs slot
			// exists (it should from a prior real upsert, but defensive).
			if _, ok := dirs[ancestor]; !ok {
				dirs[ancestor] = nil
			}
		}
	}
}

// resolveHardlink looks up linkname in the overlay index, normalizing
// "./"/"/" prefixes first. Returns nil if absent.
func resolveHardlink(linkname string, idx overlayIndex) *overlayEntry {
	for strings.HasPrefix(linkname, "./") {
		linkname = linkname[2:]
	}
	for strings.HasPrefix(linkname, "/") {
		linkname = linkname[1:]
	}
	for strings.HasSuffix(linkname, "/") {
		linkname = linkname[:len(linkname)-1]
	}
	if linkname == "" || linkname == "." {
		return nil
	}
	return idx[linkname]
}

// upsertDirEntry inserts e into dirs[parent], replacing any existing entry
// with the same Name().
func upsertDirEntry(dirs overlayDirs, parent string, e *overlayDirEntry) {
	cur := dirs[parent]
	for i, existing := range cur {
		if existing.Name() == e.Name() {
			cur[i] = e
			dirs[parent] = cur
			return
		}
	}
	dirs[parent] = append(cur, e)
}

// insertIfAbsentDirEntry inserts e into dirs[parent] only if no entry with
// the same Name() is already present.
func insertIfAbsentDirEntry(dirs overlayDirs, parent string, e *overlayDirEntry) {
	cur := dirs[parent]
	for _, existing := range cur {
		if existing.Name() == e.Name() {
			return
		}
	}
	dirs[parent] = append(cur, e)
}

// removeFromDirList removes any entry with name == base from dirs[parent].
func removeFromDirList(dirs overlayDirs, parent, base string) {
	cur, ok := dirs[parent]
	if !ok {
		return
	}
	out := cur[:0]
	for _, e := range cur {
		if e.Name() == base {
			continue
		}
		out = append(out, e)
	}
	dirs[parent] = out
}

// clearOverlay erases every key from idx and dirs.
func clearOverlay(idx overlayIndex, dirs overlayDirs) {
	for k := range idx {
		delete(idx, k)
	}
	for k := range dirs {
		delete(dirs, k)
	}
}

// removePrefix deletes every key in idx that starts with the given prefix.
func removePrefix(idx overlayIndex, prefix string) {
	for k := range idx {
		if strings.HasPrefix(k, prefix) {
			delete(idx, k)
		}
	}
}

// removeDirsPrefix deletes every key in dirs that starts with the given prefix.
func removeDirsPrefix(dirs overlayDirs, prefix string) {
	for k := range dirs {
		if strings.HasPrefix(k, prefix) {
			delete(dirs, k)
		}
	}
}

// resolvePathResult bundles the outcome of component-wise traversal.
type resolvePathResult struct {
	resolvedPath string        // canonical normalized path; "" for root
	entry        *overlayEntry // overlayIndex[resolvedPath] when non-root, may be nil for root
	isRoot       bool          // true when resolvedPath == ""
}

// resolvePath performs the component-wise overlay traversal described in
// the design (Open steps 0–3). When followFinalSymlink is false, a
// final-component symlink is returned without dereferencing — used by
// Lstat. Errors are returned as *fs.PathError with the supplied op.
func (f *FS) resolvePath(name, op string, followFinalSymlink bool) (resolvePathResult, error) {
	if name == "." {
		return resolvePathResult{isRoot: true}, nil
	}
	if !fs.ValidPath(name) {
		return resolvePathResult{}, &fs.PathError{Op: op, Path: name, Err: fs.ErrInvalid}
	}

	components := splitPath(name)
	stack := make([]string, 0, len(components))
	hops := 256
	var current *overlayEntry
	resolvedPath := ""

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
			resolvedPath = strings.Join(stack, "/")
			current = nil
			continue
		}

		stack = append(stack, c)
		resolvedPath = strings.Join(stack, "/")
		current = f.index[resolvedPath]
		if current == nil {
			return resolvePathResult{}, &fs.PathError{Op: op, Path: name, Err: fs.ErrNotExist}
		}

		isSymlink := current.entry.Header.Mode&fs.ModeSymlink != 0
		isFinal := len(components) == 0
		if isSymlink && (!isFinal || followFinalSymlink) {
			if current.entry.Header.Linkname == "" {
				return resolvePathResult{}, &fs.PathError{Op: op, Path: name, Err: fs.ErrNotExist}
			}
			hops--
			if hops == 0 {
				return resolvePathResult{}, &fs.PathError{Op: op, Path: name, Err: ErrSymlinkLoop}
			}
			target := current.entry.Header.Linkname
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
			resolvedPath = ""
			continue
		}

		if current.entry.Header.Mode.IsDir() {
			continue
		}

		// Non-directory, non-symlink (or final symlink with !followFinalSymlink).
		if !isFinal {
			return resolvePathResult{}, &fs.PathError{Op: op, Path: name, Err: fs.ErrNotExist}
		}
	}

	if len(stack) == 0 {
		return resolvePathResult{isRoot: true}, nil
	}
	if current == nil {
		current = f.index[resolvedPath]
		if current == nil {
			return resolvePathResult{}, &fs.PathError{Op: op, Path: name, Err: fs.ErrNotExist}
		}
	}
	return resolvePathResult{resolvedPath: resolvedPath, entry: current}, nil
}

// Open implements fs.FS. See the design document for the full algorithm.
func (f *FS) Open(name string) (fs.File, error) {
	if f.closed.Load() {
		return nil, &fs.PathError{Op: "open", Path: name, Err: ErrFSClosed}
	}
	res, err := f.resolvePath(name, "open", true)
	if err != nil {
		return nil, err
	}
	if res.isRoot {
		return f.openDir("", name), nil
	}
	if res.entry.entry.Header.Mode.IsDir() {
		return f.openDir(res.resolvedPath, name), nil
	}
	return f.openFile(res.entry, name), nil
}

func (f *FS) openFile(oe *overlayEntry, openArg string) fs.File {
	layer := &f.layers[oe.layerIdx]
	sr := io.NewSectionReader(layer.decompRA, oe.entry.Offset, oe.entry.Header.Size)
	return &overlayFile{
		entry:   oe.entry,
		openArg: openArg,
		sr:      sr,
	}
}

func (f *FS) openDir(resolvedPath, openArg string) fs.File {
	return &overlayDirFile{
		fs:           f,
		resolvedPath: resolvedPath,
		openArg:      openArg,
		entries:      f.dirs[resolvedPath],
	}
}

// ReadDir implements fs.ReadDirFS.
func (f *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	if f.closed.Load() {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: ErrFSClosed}
	}
	res, err := f.resolvePath(name, "readdir", true)
	if err != nil {
		return nil, err
	}
	if !res.isRoot {
		if res.entry == nil {
			return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
		}
		if !res.entry.entry.Header.Mode.IsDir() {
			return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
		}
	}
	raw := f.dirs[res.resolvedPath]
	out := make([]fs.DirEntry, len(raw))
	for i, e := range raw {
		out[i] = e
	}
	return out, nil
}

// Stat implements fs.StatFS. Symlinks at the final component are followed.
func (f *FS) Stat(name string) (fs.FileInfo, error) {
	if f.closed.Load() {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: ErrFSClosed}
	}
	res, err := f.resolvePath(name, "stat", true)
	if err != nil {
		return nil, err
	}
	if res.isRoot {
		return rootFileInfo(name), nil
	}
	return &overlayFileInfo{name: path.Base(name), header: res.entry.entry.Header}, nil
}

// Lstat returns metadata for the named entry without following a final
// symlink.
func (f *FS) Lstat(name string) (fs.FileInfo, error) {
	if f.closed.Load() {
		return nil, &fs.PathError{Op: "lstat", Path: name, Err: ErrFSClosed}
	}
	res, err := f.resolvePath(name, "lstat", false)
	if err != nil {
		return nil, err
	}
	if res.isRoot {
		return rootFileInfo(name), nil
	}
	return &overlayFileInfo{name: path.Base(name), header: res.entry.entry.Header}, nil
}

// ReadFile opens name, reads its entire contents, and closes it. Returns
// *fs.PathError wrapping ErrFSClosed if the FS has been closed.
func (f *FS) ReadFile(name string) ([]byte, error) {
	if f.closed.Load() {
		return nil, &fs.PathError{Op: "readfile", Path: name, Err: ErrFSClosed}
	}
	file, err := f.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

// overlayFile is the fs.File returned for regular and hardlink-resolved entries.
type overlayFile struct {
	entry   tarfs.Entry
	openArg string
	sr      *io.SectionReader
}

func (f *overlayFile) Read(p []byte) (int, error)              { return f.sr.Read(p) }
func (f *overlayFile) ReadAt(p []byte, off int64) (int, error) { return f.sr.ReadAt(p, off) }
func (f *overlayFile) Seek(off int64, whence int) (int64, error) {
	return f.sr.Seek(off, whence)
}

// WriteTo allows io.Copy to bypass its 32 KB staging buffer. We stream
// directly from the SectionReader in 64 KB chunks rather than relying on
// io.Copy's internal staging buffer.
func (f *overlayFile) WriteTo(w io.Writer) (int64, error) {
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

func (f *overlayFile) Close() error { return nil }
func (f *overlayFile) Stat() (fs.FileInfo, error) {
	return &overlayFileInfo{name: path.Base(f.openArg), header: f.entry.Header}, nil
}

// overlayDirFile is the fs.ReadDirFile returned for directory entries.
type overlayDirFile struct {
	fs           *FS
	resolvedPath string
	openArg      string
	entries      []*overlayDirEntry
	cursor       int
}

func (d *overlayDirFile) Read([]byte) (int, error) {
	pathForError := d.resolvedPath
	if pathForError == "" {
		pathForError = "."
	}
	return 0, &fs.PathError{Op: "read", Path: pathForError, Err: fs.ErrInvalid}
}

func (d *overlayDirFile) Close() error { return nil }

func (d *overlayDirFile) Stat() (fs.FileInfo, error) {
	if d.resolvedPath == "" {
		return rootFileInfo(d.openArg), nil
	}
	if oe := d.fs.index[d.resolvedPath]; oe != nil {
		return &overlayFileInfo{name: path.Base(d.openArg), header: oe.entry.Header}, nil
	}
	return rootFileInfo(d.openArg), nil
}

func (d *overlayDirFile) ReadDir(n int) ([]fs.DirEntry, error) {
	remaining := len(d.entries) - d.cursor
	if n <= 0 {
		out := make([]fs.DirEntry, remaining)
		for i, e := range d.entries[d.cursor:] {
			out[i] = e
		}
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
	for i, e := range d.entries[d.cursor : d.cursor+take] {
		out[i] = e
	}
	d.cursor += take
	if take < n {
		return out, io.EOF
	}
	return out, nil
}

// Compile-time interface checks.
var (
	_ fs.FS         = (*FS)(nil)
	_ fs.ReadDirFS  = (*FS)(nil)
	_ fs.StatFS     = (*FS)(nil)
	_ fs.ReadFileFS = (*FS)(nil)

	_ fs.File        = (*overlayFile)(nil)
	_ io.ReaderAt    = (*overlayFile)(nil)
	_ io.Seeker      = (*overlayFile)(nil)
	_ io.WriterTo    = (*overlayFile)(nil)
	_ fs.ReadDirFile = (*overlayDirFile)(nil)
	_ fs.DirEntry    = (*overlayDirEntry)(nil)
	_ fs.FileInfo    = (*overlayFileInfo)(nil)
)
