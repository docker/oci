package ocifs

import (
	"archive/tar"
	"io/fs"
	"reflect"
	"sort"
	"testing"

	"github.com/docker/oci/ocifs/tarfs"
)

// makeFile constructs a regular-file tar entry for testing.
func makeFile(name, body string) tarfs.Entry {
	return tarfs.Entry{
		Header: tarfs.Header{
			Typeflag: tar.TypeReg,
			Name:     name,
			Size:     int64(len(body)),
			Mode:     0o644,
		},
		Filename: name,
	}
}

// makeDir constructs a directory tar entry for testing.
func makeDir(name string) tarfs.Entry {
	return tarfs.Entry{
		Header: tarfs.Header{
			Typeflag: tar.TypeDir,
			Name:     name,
			Mode:     fs.ModeDir | 0o755,
		},
		Filename: name,
	}
}

// makeSymlink constructs a symlink tar entry for testing.
func makeSymlink(name, target string) tarfs.Entry {
	return tarfs.Entry{
		Header: tarfs.Header{
			Typeflag: tar.TypeSymlink,
			Name:     name,
			Linkname: target,
			Mode:     fs.ModeSymlink | 0o777,
		},
		Filename: name,
	}
}

// makeWhiteout constructs a whiteout tar entry for testing.
func makeWhiteout(parentDir, base string) tarfs.Entry {
	name := whiteoutPrefix + base
	if parentDir != "" {
		name = parentDir + "/" + name
	}
	return tarfs.Entry{
		Header: tarfs.Header{
			Typeflag: tar.TypeReg,
			Name:     name,
			Mode:     0o644,
		},
		Filename: name,
	}
}

// makeOpaqueWhiteout constructs an opaque whiteout marker.
func makeOpaqueWhiteout(parentDir string) tarfs.Entry {
	name := whiteoutOpaque
	if parentDir != "" {
		name = parentDir + "/" + name
	}
	return tarfs.Entry{
		Header: tarfs.Header{
			Typeflag: tar.TypeReg,
			Name:     name,
			Mode:     0o644,
		},
		Filename: name,
	}
}

// makeHardlink constructs a hardlink entry pointing to target.
func makeHardlink(name, target string) tarfs.Entry {
	return tarfs.Entry{
		Header: tarfs.Header{
			Typeflag: tar.TypeLink,
			Name:     name,
			Linkname: target,
			Mode:     0o644,
		},
		Filename: name,
	}
}

func TestBuildOverlay_SimpleUpsert(t *testing.T) {
	layers := [][]tarfs.Entry{
		{makeFile("a.txt", "lower")},
		{makeFile("a.txt", "upper")},
	}
	idx, dirs := buildOverlay(layers)

	oe, ok := idx["a.txt"]
	if !ok {
		t.Fatalf("a.txt missing from overlayIndex")
	}
	if oe.layerIdx != 1 {
		t.Errorf("a.txt layerIdx = %d, want 1", oe.layerIdx)
	}
	root := dirs[""]
	if len(root) != 1 {
		t.Fatalf("root has %d entries, want 1: %v", len(root), entryNames(root))
	}
	if root[0].Name() != "a.txt" {
		t.Errorf("root[0] = %q, want a.txt", root[0].Name())
	}
}

func TestBuildOverlay_WhiteoutDeletion(t *testing.T) {
	layers := [][]tarfs.Entry{
		{
			makeFile("a.txt", "lower"),
			makeFile("b.txt", "lower-b"),
		},
		{makeWhiteout("", "a.txt")},
	}
	idx, dirs := buildOverlay(layers)

	if _, ok := idx["a.txt"]; ok {
		t.Errorf("a.txt should be removed from overlayIndex")
	}
	if _, ok := idx["b.txt"]; !ok {
		t.Errorf("b.txt should still exist in overlayIndex")
	}
	root := dirs[""]
	if len(root) != 1 {
		t.Fatalf("root has %d entries, want 1: %v", len(root), entryNames(root))
	}
	if root[0].Name() != "b.txt" {
		t.Errorf("root[0] = %q, want b.txt", root[0].Name())
	}
}

func TestBuildOverlay_OpaqueWhiteout(t *testing.T) {
	layers := [][]tarfs.Entry{
		{
			makeDir("etc"),
			makeFile("etc/a.txt", "lower"),
			makeFile("etc/b.txt", "lower-b"),
		},
		{
			makeOpaqueWhiteout("etc"),
			makeFile("etc/c.txt", "upper-c"),
		},
	}
	idx, dirs := buildOverlay(layers)

	if _, ok := idx["etc/a.txt"]; ok {
		t.Errorf("etc/a.txt should be removed")
	}
	if _, ok := idx["etc/b.txt"]; ok {
		t.Errorf("etc/b.txt should be removed")
	}
	if _, ok := idx["etc/c.txt"]; !ok {
		t.Errorf("etc/c.txt should be present")
	}
	if _, ok := idx["etc"]; !ok {
		t.Errorf("etc directory should still exist")
	}
	etcDir := dirs["etc"]
	if len(etcDir) != 1 {
		t.Fatalf("etc has %d entries, want 1: %v", len(etcDir), entryNames(etcDir))
	}
	if etcDir[0].Name() != "c.txt" {
		t.Errorf("etc[0] = %q, want c.txt", etcDir[0].Name())
	}
}

func TestBuildOverlay_RootOpaqueWhiteout(t *testing.T) {
	layers := [][]tarfs.Entry{
		{
			makeFile("a.txt", "lower"),
			makeDir("usr"),
			makeFile("usr/lib", "lib-content"),
		},
		{
			makeOpaqueWhiteout(""),
			makeFile("new.txt", "after"),
		},
	}
	idx, dirs := buildOverlay(layers)

	if _, ok := idx["a.txt"]; ok {
		t.Errorf("a.txt should be removed by root opaque whiteout")
	}
	if _, ok := idx["usr/lib"]; ok {
		t.Errorf("usr/lib should be removed by root opaque whiteout")
	}
	if _, ok := idx["new.txt"]; !ok {
		t.Errorf("new.txt should be present after root opaque whiteout")
	}
	root := dirs[""]
	if len(root) != 1 || root[0].Name() != "new.txt" {
		t.Errorf("root entries = %v, want [new.txt]", entryNames(root))
	}
}

func TestBuildOverlay_SynthesizeParents(t *testing.T) {
	layers := [][]tarfs.Entry{
		{
			// No explicit "a" or "a/b" directory entries; they must be synthesized.
			makeFile("a/b/c.txt", "deep"),
		},
	}
	idx, dirs := buildOverlay(layers)

	if oe, ok := idx["a"]; !ok || !oe.entry.Header.Mode.IsDir() {
		t.Errorf("a should be synthesized as a directory")
	}
	if oe, ok := idx["a/b"]; !ok || !oe.entry.Header.Mode.IsDir() {
		t.Errorf("a/b should be synthesized as a directory")
	}
	if _, ok := idx["a/b/c.txt"]; !ok {
		t.Errorf("a/b/c.txt should be present")
	}

	rootChildren := entryNames(dirs[""])
	if !reflect.DeepEqual(rootChildren, []string{"a"}) {
		t.Errorf("root children = %v, want [a]", rootChildren)
	}
	aChildren := entryNames(dirs["a"])
	if !reflect.DeepEqual(aChildren, []string{"b"}) {
		t.Errorf("a children = %v, want [b]", aChildren)
	}
	abChildren := entryNames(dirs["a/b"])
	if !reflect.DeepEqual(abChildren, []string{"c.txt"}) {
		t.Errorf("a/b children = %v, want [c.txt]", abChildren)
	}
}

func TestBuildOverlay_MultiLayerMerge(t *testing.T) {
	layers := [][]tarfs.Entry{
		{
			makeDir("usr"),
			makeFile("usr/a.txt", "L0-a"),
			makeFile("usr/b.txt", "L0-b"),
		},
		{
			makeFile("usr/a.txt", "L1-a-overwritten"),
			makeFile("usr/c.txt", "L1-c"),
		},
		{
			makeFile("usr/b.txt", "L2-b-overwritten"),
		},
	}
	idx, dirs := buildOverlay(layers)

	if oe := idx["usr/a.txt"]; oe == nil || oe.layerIdx != 1 {
		t.Errorf("usr/a.txt layerIdx mismatch: %+v", oe)
	}
	if oe := idx["usr/b.txt"]; oe == nil || oe.layerIdx != 2 {
		t.Errorf("usr/b.txt layerIdx mismatch: %+v", oe)
	}
	if oe := idx["usr/c.txt"]; oe == nil || oe.layerIdx != 1 {
		t.Errorf("usr/c.txt layerIdx mismatch: %+v", oe)
	}
	usr := entryNames(dirs["usr"])
	if !reflect.DeepEqual(usr, []string{"a.txt", "b.txt", "c.txt"}) {
		t.Errorf("usr children = %v, want [a.txt b.txt c.txt]", usr)
	}
}

func TestBuildOverlay_DirectoryWhiteoutRemovesSubtree(t *testing.T) {
	layers := [][]tarfs.Entry{
		{
			makeDir("etc"),
			makeFile("etc/a.txt", "x"),
			makeDir("etc/sub"),
			makeFile("etc/sub/y.txt", "y"),
		},
		{
			// Whiteout the "etc" directory itself.
			makeWhiteout("", "etc"),
		},
	}
	idx, dirs := buildOverlay(layers)

	for _, k := range []string{"etc", "etc/a.txt", "etc/sub", "etc/sub/y.txt"} {
		if _, ok := idx[k]; ok {
			t.Errorf("idx[%q] should be removed", k)
		}
	}
	for _, k := range []string{"etc", "etc/sub"} {
		if _, ok := dirs[k]; ok {
			t.Errorf("dirs[%q] should be removed", k)
		}
	}
	root := entryNames(dirs[""])
	if len(root) != 0 {
		t.Errorf("root should be empty, got %v", root)
	}
}

func TestBuildOverlay_HardlinkResolvesToTarget(t *testing.T) {
	body := "the body"
	target := makeFile("usr/bin/ls", body)
	link := makeHardlink("bin/myls", "usr/bin/ls")
	layers := [][]tarfs.Entry{
		{target, link},
	}
	idx, dirs := buildOverlay(layers)

	oe, ok := idx["bin/myls"]
	if !ok {
		t.Fatalf("bin/myls missing")
	}
	if oe.entry.Header.Size != int64(len(body)) {
		t.Errorf("hardlink size = %d, want %d", oe.entry.Header.Size, len(body))
	}
	// The DirEntry must use the link's own name, not the target's name.
	binDir := entryNames(dirs["bin"])
	if !reflect.DeepEqual(binDir, []string{"myls"}) {
		t.Errorf("bin children = %v, want [myls]", binDir)
	}
}

func TestBuildOverlay_HardlinkWhiteoutDeletedTarget(t *testing.T) {
	layers := [][]tarfs.Entry{
		{makeFile("usr/bin/ls", "body")},
		{makeWhiteout("usr/bin", "ls")},
		{makeHardlink("bin/myls", "usr/bin/ls")},
	}
	idx, _ := buildOverlay(layers)

	if _, ok := idx["bin/myls"]; ok {
		t.Errorf("bin/myls should be skipped because target was whiteout-deleted")
	}
}

func TestBuildOverlay_CrossLayerSymlink(t *testing.T) {
	layers := [][]tarfs.Entry{
		{
			makeDir("usr"),
			makeDir("usr/lib"),
			makeFile("usr/lib/foo.so", "binary"),
		},
		{
			makeSymlink("lib", "usr/lib"),
		},
	}
	idx, _ := buildOverlay(layers)

	if oe := idx["lib"]; oe == nil || oe.entry.Header.Mode&fs.ModeSymlink == 0 {
		t.Fatalf("lib should be a symlink: %+v", oe)
	}
	// Direct lookup of "lib/foo.so" is not in idx; resolution happens at Open time.
	// Here we just confirm the components are present.
	if _, ok := idx["usr/lib/foo.so"]; !ok {
		t.Errorf("usr/lib/foo.so should be present")
	}
}

func TestBuildOverlay_DirectoriesSorted(t *testing.T) {
	layers := [][]tarfs.Entry{
		{
			makeFile("z.txt", "z"),
			makeFile("a.txt", "a"),
			makeFile("m.txt", "m"),
		},
	}
	_, dirs := buildOverlay(layers)
	got := entryNames(dirs[""])
	want := []string{"a.txt", "m.txt", "z.txt"}
	if !sort.IsSorted(sort.StringSlice(got)) {
		t.Errorf("root not sorted: %v", got)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("root children = %v, want %v", got, want)
	}
}

func entryNames(es []*overlayDirEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Name()
	}
	return out
}
