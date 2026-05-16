package tarfs

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"path"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// fixtureFile describes one entry to write into a synthetic tar archive.
type fixtureFile struct {
	Name     string
	Linkname string
	Mode     int64
	Type     byte
	Body     string
}

// buildTarBytes writes the supplied fixtures into a tar archive and returns
// the raw bytes.
func buildTarBytes(t *testing.T, files []fixtureFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		mode := f.Mode
		if mode == 0 {
			switch f.Type {
			case tar.TypeDir:
				mode = 0o755
			case tar.TypeSymlink:
				mode = 0o777
			default:
				mode = 0o644
			}
		}
		hdr := &tar.Header{
			Name:     f.Name,
			Linkname: f.Linkname,
			Mode:     mode,
			Typeflag: f.Type,
			Size:     int64(len(f.Body)),
			ModTime:  time.Unix(1700000000, 0).UTC(),
		}
		if f.Type == tar.TypeDir || f.Type == tar.TypeSymlink || f.Type == tar.TypeLink {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %q: %v", f.Name, err)
		}
		if hdr.Size > 0 {
			if _, err := tw.Write([]byte(f.Body)); err != nil {
				t.Fatalf("tar body %q: %v", f.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	return buf.Bytes()
}

// buildTar wraps buildTarBytes in a *bytes.Reader (which satisfies io.ReaderAt).
func buildTar(t *testing.T, files []fixtureFile) *bytes.Reader {
	t.Helper()
	return bytes.NewReader(buildTarBytes(t, files))
}

func basicFixture() []fixtureFile {
	return []fixtureFile{
		{Name: "etc/", Type: tar.TypeDir},
		{Name: "etc/hostname", Type: tar.TypeReg, Body: "node\n"},
		{Name: "etc/hosts", Type: tar.TypeReg, Body: "127.0.0.1\n"},
		{Name: "bin/sh", Type: tar.TypeReg, Body: "#!/bin/sh\necho hi\n"},
		{Name: "bin/ash", Type: tar.TypeLink, Linkname: "bin/sh"},
		{Name: "bin/sh-link", Type: tar.TypeSymlink, Linkname: "sh"},
		{Name: "var/log/", Type: tar.TypeDir},
		{Name: "var/log/.wh.secret", Type: tar.TypeReg, Body: ""},
	}
}

func TestNew_BasicFixture(t *testing.T) {
	ra := buildTar(t, basicFixture())
	tfs, err := New(ra, int64(ra.Len()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Run("Open regular", func(t *testing.T) {
		f, err := tfs.Open("etc/hostname")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer f.Close()
		got, err := io.ReadAll(f)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if string(got) != "node\n" {
			t.Errorf("got %q want %q", got, "node\n")
		}
	})

	t.Run("Stat name is base of openArg", func(t *testing.T) {
		f, err := tfs.Open("etc/hostname")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if fi.Name() != "hostname" {
			t.Errorf("Name() = %q want %q", fi.Name(), "hostname")
		}
	})

	t.Run("Open root", func(t *testing.T) {
		f, err := tfs.Open(".")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if !fi.IsDir() {
			t.Errorf("root should be dir")
		}
		if fi.Name() != "." {
			t.Errorf("Name() = %q want %q", fi.Name(), ".")
		}
		rdf, ok := f.(fs.ReadDirFile)
		if !ok {
			t.Fatalf("root not ReadDirFile")
		}
		entries, err := rdf.ReadDir(-1)
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		want := []string{"bin", "etc", "var"}
		if !reflect.DeepEqual(names, want) {
			t.Errorf("root entries = %v want %v", names, want)
		}
	})

	t.Run("Symlink follow", func(t *testing.T) {
		f, err := tfs.Open("bin/sh-link")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer f.Close()
		got, err := io.ReadAll(f)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if !strings.Contains(string(got), "echo hi") {
			t.Errorf("symlink content = %q", got)
		}
		fi, _ := f.Stat()
		if fi.Name() != "sh-link" {
			t.Errorf("Stat.Name() = %q want sh-link", fi.Name())
		}
	})

	t.Run("Hardlink resolved", func(t *testing.T) {
		f, err := tfs.Open("bin/ash")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer f.Close()
		got, err := io.ReadAll(f)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if !strings.Contains(string(got), "echo hi") {
			t.Errorf("hardlink content = %q (expected sh body)", got)
		}
	})

	t.Run("Whiteout invisible in ReadDir", func(t *testing.T) {
		entries, err := tfs.ReadDir("var/log")
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".wh.") {
				t.Errorf("whiteout %q leaked into ReadDir", e.Name())
			}
		}
	})

	t.Run("Whiteout openable by Open", func(t *testing.T) {
		f, err := tfs.Open("var/log/.wh.secret")
		if err != nil {
			t.Fatalf("Open whiteout: %v", err)
		}
		f.Close()
	})

	t.Run("Lstat returns symlink itself", func(t *testing.T) {
		fi, err := tfs.Lstat("bin/sh-link")
		if err != nil {
			t.Fatalf("Lstat: %v", err)
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			t.Errorf("Lstat mode = %v, want symlink bit set", fi.Mode())
		}
		if fi.Name() != "sh-link" {
			t.Errorf("Name() = %q", fi.Name())
		}
	})

	t.Run("Stat follows symlink", func(t *testing.T) {
		fi, err := tfs.Stat("bin/sh-link")
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			t.Errorf("Stat mode should not include symlink bit, got %v", fi.Mode())
		}
	})

	t.Run("Missing path is *fs.PathError", func(t *testing.T) {
		_, err := tfs.Open("does/not/exist")
		var pe *fs.PathError
		if !errors.As(err, &pe) {
			t.Fatalf("want *fs.PathError, got %T %v", err, err)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("want fs.ErrNotExist, got %v", err)
		}
	})

	t.Run("Invalid path", func(t *testing.T) {
		_, err := tfs.Open("/abs")
		if !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("want fs.ErrInvalid, got %v", err)
		}
	})
}

func TestIndex_RoundTrip(t *testing.T) {
	raw := buildTarBytes(t, basicFixture())
	ra := bytes.NewReader(raw)

	entries, err := Index(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Index: %v", err)
	}

	tfs, err := NewFromEntries(ra, entries)
	if err != nil {
		t.Fatalf("NewFromEntries: %v", err)
	}

	got, err := fs.ReadFile(tfs, "etc/hosts")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "127.0.0.1\n" {
		t.Errorf("got %q", got)
	}
}

func TestIndex_NoSyntheticDirs(t *testing.T) {
	files := []fixtureFile{
		{Name: "deep/nested/path/file.txt", Type: tar.TypeReg, Body: "x"},
	}
	raw := buildTarBytes(t, files)
	entries, err := Index(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("Index returned %d entries, want 1 (no synthesis)", len(entries))
	}
}

func TestNewFromEntries_SynthesizesAncestors(t *testing.T) {
	files := []fixtureFile{
		{Name: "deep/nested/path/file.txt", Type: tar.TypeReg, Body: "x"},
	}
	ra := buildTar(t, files)
	tfs, err := New(ra, int64(ra.Len()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, p := range []string{"deep", "deep/nested", "deep/nested/path"} {
		fi, err := tfs.Stat(p)
		if err != nil {
			t.Errorf("Stat(%q): %v", p, err)
			continue
		}
		if !fi.IsDir() {
			t.Errorf("Stat(%q) not dir", p)
		}
	}
}

func TestEntries_ExcludesSynthetic(t *testing.T) {
	files := []fixtureFile{
		{Name: "a/b/c.txt", Type: tar.TypeReg, Body: "y"},
	}
	ra := buildTar(t, files)
	tfs, err := New(ra, int64(ra.Len()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := tfs.Entries()
	if len(got) != 1 {
		t.Errorf("Entries() = %d items, want 1; got %+v", len(got), got)
	}
	if got[0].Filename != "a/b/c.txt" {
		t.Errorf("Filename = %q", got[0].Filename)
	}
}

func TestEntries_IncludesWhiteouts(t *testing.T) {
	ra := buildTar(t, basicFixture())
	tfs, err := New(ra, int64(ra.Len()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var found bool
	for _, e := range tfs.Entries() {
		if path.Base(e.Filename) == ".wh.secret" {
			found = true
		}
	}
	if !found {
		t.Errorf("Entries() missing whiteout")
	}
}

func TestSymlinkLoop(t *testing.T) {
	files := []fixtureFile{
		{Name: "a", Type: tar.TypeSymlink, Linkname: "b"},
		{Name: "b", Type: tar.TypeSymlink, Linkname: "a"},
	}
	ra := buildTar(t, files)
	tfs, err := New(ra, int64(ra.Len()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = tfs.Open("a")
	if !errors.Is(err, ErrSymlinkLoop) {
		t.Fatalf("want ErrSymlinkLoop, got %v", err)
	}
	var pe *fs.PathError
	if !errors.As(err, &pe) {
		t.Errorf("want *fs.PathError")
	}
}

func TestSymlinkAbsolute(t *testing.T) {
	files := []fixtureFile{
		{Name: "data/payload", Type: tar.TypeReg, Body: "PAYLOAD"},
		{Name: "ptr", Type: tar.TypeSymlink, Linkname: "/data/payload"},
	}
	ra := buildTar(t, files)
	tfs, err := New(ra, int64(ra.Len()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := fs.ReadFile(tfs, "ptr")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "PAYLOAD" {
		t.Errorf("got %q", got)
	}
}

func TestSymlinkRelativeWithDotDot(t *testing.T) {
	files := []fixtureFile{
		{Name: "etc/conf", Type: tar.TypeReg, Body: "K=V"},
		{Name: "links/conf", Type: tar.TypeSymlink, Linkname: "../etc/conf"},
	}
	ra := buildTar(t, files)
	tfs, err := New(ra, int64(ra.Len()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := fs.ReadFile(tfs, "links/conf")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "K=V" {
		t.Errorf("got %q", got)
	}
}

func TestHardlinkChain(t *testing.T) {
	files := []fixtureFile{
		{Name: "real", Type: tar.TypeReg, Body: "REAL"},
		{Name: "hl1", Type: tar.TypeLink, Linkname: "real"},
		{Name: "hl2", Type: tar.TypeLink, Linkname: "hl1"},
		{Name: "hl3", Type: tar.TypeLink, Linkname: "./hl2"},
	}
	ra := buildTar(t, files)
	tfs, err := New(ra, int64(ra.Len()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := fs.ReadFile(tfs, "hl3")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "REAL" {
		t.Errorf("got %q", got)
	}
}

func TestHardlinkBroken(t *testing.T) {
	files := []fixtureFile{
		{Name: "broken", Type: tar.TypeLink, Linkname: "missing"},
	}
	ra := buildTar(t, files)
	tfs, err := New(ra, int64(ra.Len()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = tfs.Open("broken")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("want fs.ErrNotExist, got %v", err)
	}
}

func TestReadDirCursorSemantics(t *testing.T) {
	files := []fixtureFile{
		{Name: "d/a", Type: tar.TypeReg, Body: "a"},
		{Name: "d/b", Type: tar.TypeReg, Body: "b"},
		{Name: "d/c", Type: tar.TypeReg, Body: "c"},
	}
	ra := buildTar(t, files)
	tfs, err := New(ra, int64(ra.Len()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Run("n<=0 returns all then empty nil", func(t *testing.T) {
		f, err := tfs.Open("d")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer f.Close()
		rdf := f.(fs.ReadDirFile)
		got, err := rdf.ReadDir(-1)
		if err != nil {
			t.Fatalf("ReadDir(-1): %v", err)
		}
		if len(got) != 3 {
			t.Errorf("first batch = %d", len(got))
		}
		got2, err := rdf.ReadDir(-1)
		if err != nil {
			t.Errorf("second ReadDir(-1) err = %v, want nil", err)
		}
		if len(got2) != 0 {
			t.Errorf("second batch len = %d, want 0", len(got2))
		}
	})

	t.Run("n>0 paginates with EOF on final batch", func(t *testing.T) {
		f, err := tfs.Open("d")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer f.Close()
		rdf := f.(fs.ReadDirFile)
		got, err := rdf.ReadDir(2)
		if err != nil {
			t.Fatalf("ReadDir(2) #1: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("got %d", len(got))
		}
		got, err = rdf.ReadDir(2)
		if !errors.Is(err, io.EOF) {
			t.Errorf("ReadDir(2) #2 err = %v, want io.EOF", err)
		}
		if len(got) != 1 {
			t.Errorf("ReadDir(2) #2 got %d", len(got))
		}
		got, err = rdf.ReadDir(2)
		if !errors.Is(err, io.EOF) {
			t.Errorf("ReadDir(2) #3 err = %v, want io.EOF", err)
		}
		if len(got) != 0 {
			t.Errorf("ReadDir(2) #3 got %d", len(got))
		}
	})

	t.Run("n>0 exact match returns nil err", func(t *testing.T) {
		f, err := tfs.Open("d")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer f.Close()
		rdf := f.(fs.ReadDirFile)
		got, err := rdf.ReadDir(3)
		if err != nil {
			t.Errorf("err = %v, want nil (exact-fit)", err)
		}
		if len(got) != 3 {
			t.Errorf("got %d", len(got))
		}
		_, err = rdf.ReadDir(1)
		if !errors.Is(err, io.EOF) {
			t.Errorf("subsequent ReadDir(1) err = %v, want io.EOF", err)
		}
	})
}

func TestDirectoryRead(t *testing.T) {
	files := []fixtureFile{{Name: "d/", Type: tar.TypeDir}}
	ra := buildTar(t, files)
	tfs, err := New(ra, int64(ra.Len()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f, err := tfs.Open("d")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	_, err = f.Read(make([]byte, 1))
	if !errors.Is(err, fs.ErrInvalid) {
		t.Errorf("Read on dir err = %v, want fs.ErrInvalid", err)
	}
}

// writerToProbe records whether WriteTo was invoked on the source.
type writerToProbe struct {
	buf bytes.Buffer
}

func (w *writerToProbe) Write(p []byte) (int, error) { return w.buf.Write(p) }

func TestFile_ImplementsWriterTo(t *testing.T) {
	files := []fixtureFile{{Name: "blob", Type: tar.TypeReg, Body: strings.Repeat("z", 1024)}}
	ra := buildTar(t, files)
	tfs, err := New(ra, int64(ra.Len()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f, err := tfs.Open("blob")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	if _, ok := f.(io.WriterTo); !ok {
		t.Fatalf("file does not implement io.WriterTo")
	}
	probe := &writerToProbe{}
	n, err := io.Copy(probe, f)
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if n != 1024 {
		t.Errorf("copied %d bytes, want 1024", n)
	}
	if probe.buf.Len() != 1024 {
		t.Errorf("probe got %d bytes", probe.buf.Len())
	}
}

func TestFstestTestFS(t *testing.T) {
	files := []fixtureFile{
		{Name: "etc/", Type: tar.TypeDir},
		{Name: "etc/conf", Type: tar.TypeReg, Body: "k=v"},
		{Name: "bin/", Type: tar.TypeDir},
		{Name: "bin/run", Type: tar.TypeReg, Body: "RUN"},
		{Name: "bin/run-link", Type: tar.TypeSymlink, Linkname: "run"},
		{Name: "bin/run-hardlink", Type: tar.TypeLink, Linkname: "bin/run"},
	}
	ra := buildTar(t, files)
	tfs, err := New(ra, int64(ra.Len()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := []string{
		"etc/conf",
		"bin/run",
		"bin/run-link",
		"bin/run-hardlink",
	}
	if err := fstest.TestFS(tfs, want...); err != nil {
		t.Fatalf("TestFS: %v", err)
	}
}

func TestSize_NegativeOne(t *testing.T) {
	ra := buildTar(t, basicFixture())
	if _, err := New(ra, -1); err != nil {
		t.Fatalf("New(size=-1): %v", err)
	}
}

func TestModeBitsPreserved(t *testing.T) {
	files := []fixtureFile{
		{Name: "exe", Type: tar.TypeReg, Mode: 0o4755, Body: "x"}, // setuid
	}
	ra := buildTar(t, files)
	tfs, err := New(ra, int64(ra.Len()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fi, err := tfs.Stat("exe")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode()&fs.ModeSetuid == 0 {
		t.Errorf("setuid bit dropped: mode = %v", fi.Mode())
	}
}
