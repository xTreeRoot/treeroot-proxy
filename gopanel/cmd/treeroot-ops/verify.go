package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

func verifyBackup(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	gz, e := gzip.NewReader(f)
	if e != nil {
		return e
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	dir, e := os.MkdirTemp(filepath.Dir(path), ".verify-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	actual := map[string]string{}
	var manifest struct {
		Hashes map[string]string `json:"sha256"`
	}
	seen := map[string]bool{}
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		if h.Typeflag != tar.TypeReg || filepath.IsAbs(h.Name) || strings.Contains("/"+h.Name+"/", "/../") || seen[h.Name] || h.Size > 500<<20 {
			return errors.New("unsafe backup member")
		}
		seen[h.Name] = true
		if h.Name == "manifest.json" {
			if e = json.NewDecoder(io.LimitReader(tr, 4<<20)).Decode(&manifest); e != nil {
				return e
			}
			continue
		}
		hasher := sha256.New()
		var writer io.Writer = hasher
		var db *os.File
		if h.Name == "data/panel.sqlite3" {
			db, e = os.OpenFile(filepath.Join(dir, "panel.sqlite3"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				return e
			}
			writer = io.MultiWriter(writer, db)
		}
		_, e = io.Copy(writer, tr)
		if db != nil {
			db.Close()
		}
		if e != nil {
			return e
		}
		actual[h.Name] = hex.EncodeToString(hasher.Sum(nil))
	}
	if !reflect.DeepEqual(actual, manifest.Hashes) {
		return errors.New("backup file manifest mismatch")
	}
	return checkDB(filepath.Join(dir, "panel.sqlite3"))
}
