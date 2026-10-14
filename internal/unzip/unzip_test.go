// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package unzip

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func archive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

func TestExtract(t *testing.T) {
	dir := t.TempDir()
	if err := Extract(archive(t, map[string]string{"app.py": "x", "pkg/mod.py": "y"}), dir, 1<<20); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "pkg", "mod.py")); err != nil || string(b) != "y" {
		t.Fatalf("pkg/mod.py: %q %v", b, err)
	}
	for name, files := range map[string]map[string]string{
		"escape":   {"../evil.py": "x"},
		"absolute": {"/etc/evil": "x"},
	} {
		if err := Extract(archive(t, files), t.TempDir(), 1<<20); err == nil || !strings.Contains(err.Error(), "illegal path") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := Extract(archive(t, map[string]string{"big": strings.Repeat("a", 2048)}), t.TempDir(), 1024); err == nil {
		t.Error("an archive over the size limit was extracted")
	}
	if err := Extract([]byte("not a zip"), t.TempDir(), 1024); err == nil {
		t.Error("garbage was extracted")
	}
}
