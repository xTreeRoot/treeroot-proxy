package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestReleaseRejectsTraversalLinksDuplicateAndMissingMembers(t *testing.T) {
	for _, kind := range []string{"traversal", "symlink", "duplicate", "missing"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "package.tar.gz")
			f, e := os.Create(path)
			if e != nil {
				t.Fatal(e)
			}
			gz := gzip.NewWriter(f)
			tw := tar.NewWriter(gz)
			h := &tar.Header{Name: "treeroot-go", Typeflag: tar.TypeReg, Mode: 0755, Size: 0}
			switch kind {
			case "traversal":
				h.Name = "../treeroot-go"
			case "symlink":
				h.Typeflag = tar.TypeSymlink
				h.Linkname = "/etc/passwd"
			}
			if kind != "missing" {
				tw.WriteHeader(h)
			}
			if kind == "duplicate" {
				tw.WriteHeader(h)
			}
			tw.Close()
			gz.Close()
			f.Close()
			out := filepath.Join(dir, "out")
			os.Mkdir(out, 0700)
			if e = unpack(path, out, map[string]bool{"treeroot-go": true}); e == nil {
				t.Fatal("unsafe or incomplete package accepted")
			}
		})
	}
}
func TestBackupChecksEveryFileAndSQLite(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "panel.sqlite3")
	db, e := openDB(dbPath, "rwc")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("CREATE TABLE keep(id INTEGER PRIMARY KEY,value TEXT);INSERT INTO keep VALUES(1,'preserved')"); e != nil {
		t.Fatal(e)
	}
	db.Close()
	sum, e := digest(dbPath)
	if e != nil {
		t.Fatal(e)
	}
	manifest := filepath.Join(dir, "manifest.json")
	if e = writeJSON(manifest, map[string]any{"sha256": map[string]string{"data/panel.sqlite3": sum}}); e != nil {
		t.Fatal(e)
	}
	archive := filepath.Join(dir, "backup.tar.gz")
	if e = packFiles(archive, map[string]string{"data/panel.sqlite3": dbPath, "manifest.json": manifest}); e != nil {
		t.Fatal(e)
	}
	if e = verifyBackup(archive); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(dbPath, []byte("not a database"), 0600); e != nil {
		t.Fatal(e)
	}
	sum, _ = digest(dbPath)
	writeJSON(manifest, map[string]any{"sha256": map[string]string{"data/panel.sqlite3": sum}})
	broken := filepath.Join(dir, "broken.tar.gz")
	if e = packFiles(broken, map[string]string{"data/panel.sqlite3": dbPath, "manifest.json": manifest}); e != nil {
		t.Fatal(e)
	}
	if e = verifyBackup(broken); e == nil {
		t.Fatal("matching hashes hid corrupted SQLite")
	}
}
