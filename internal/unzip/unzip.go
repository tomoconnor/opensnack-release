// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package unzip extracts Lambda deployment packages safely. It has no
// dependencies outside the standard library, so the sandbox's init binary
// can use it.
package unzip

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Extract unpacks a zip into dir, refusing entries that would land
// outside dir, symlinks, and archives that unpack to more than max bytes.
func Extract(code []byte, dir string, max int64) error {
	zr, err := zip.NewReader(bytes.NewReader(code), int64(len(code)))
	if err != nil {
		return err
	}
	var total int64
	for _, f := range zr.File {
		name := filepath.Clean(filepath.FromSlash(f.Name))
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("illegal path in archive: %s", f.Name)
		}
		dst := filepath.Join(dir, name)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not supported: %s", f.Name)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		mode := f.Mode().Perm() | 0o600
		w, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			rc.Close()
			return err
		}
		n, err := io.Copy(w, io.LimitReader(rc, max-total+1))
		rc.Close()
		w.Close()
		if err != nil {
			return err
		}
		if total += n; total > max {
			return fmt.Errorf("unzipped size must be smaller than %d bytes", max)
		}
	}
	return nil
}
