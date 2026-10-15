// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"archive/zip"
	"bufio"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Export is an opened Apple Health export: the zip the Health app writes, or
// an export.xml already extracted on disk (its sibling folders
// workout-routes/ and electrocardiograms/ are then read from next to it).
type Export struct {
	zr      *zip.ReadCloser
	xml     *zip.File            // export.xml inside the zip
	files   map[string]*zip.File // zip entries by path relative to the export root
	xmlPath string               // export.xml on disk, when not a zip
	root    string               // the export root on disk, when not a zip
}

// ErrNoExportXML reports a zip without an export.xml.
var ErrNoExportXML = errors.New("no export.xml in the archive: is this an Apple Health export?")

// Open opens an export.zip or an export.xml.
func Open(name string) (*Export, error) {
	// Checked first: reading a folder as a zip fails with the system's own
	// "is a directory", which says nothing about what to pass instead.
	if st, err := os.Stat(name); err == nil && st.IsDir() {
		return nil, fmt.Errorf("%s is a folder: pass export.zip or export.xml", name)
	}
	zr, err := zip.OpenReader(name)
	if err == nil {
		e := &Export{zr: zr, files: map[string]*zip.File{}}
		// The export root is the folder holding export.xml (normally
		// apple_health_export/). export_cda.xml is a different document.
		root := ""
		for _, f := range zr.File {
			if path.Base(f.Name) == "export.xml" && (e.xml == nil || len(f.Name) < len(e.xml.Name)) {
				e.xml = f
				root = strings.TrimSuffix(f.Name, "export.xml")
			}
		}
		if e.xml == nil {
			_ = zr.Close() // read-only archive: ErrNoExportXML is the error that matters
			return nil, ErrNoExportXML
		}
		for _, f := range zr.File {
			if strings.HasPrefix(f.Name, root) {
				e.files[strings.TrimPrefix(f.Name, root)] = f
			}
		}
		return e, nil
	}
	if !errors.Is(err, zip.ErrFormat) {
		return nil, err
	}
	return &Export{xmlPath: name, root: filepath.Dir(name)}, nil
}

// Close releases the archive.
func (e *Export) Close() error {
	if e.zr != nil {
		return e.zr.Close()
	}
	return nil
}

// XMLSize is the uncompressed size of export.xml in bytes.
func (e *Export) XMLSize() (int64, error) {
	if e.xml != nil {
		return int64(e.xml.UncompressedSize64), nil
	}
	st, err := os.Stat(e.xmlPath)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

func (e *Export) openXML() (io.ReadCloser, error) {
	if e.xml != nil {
		return e.xml.Open()
	}
	return os.Open(e.xmlPath)
}

// openFile opens a file of the export by its path relative to the export
// root, as a FileReference names it ("/workout-routes/route_x.gpx").
func (e *Export) openFile(rel string) (io.ReadCloser, error) {
	rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
	if e.zr != nil {
		f, ok := e.files[rel]
		if !ok {
			return nil, os.ErrNotExist
		}
		return f.Open()
	}
	return os.Open(filepath.Join(e.root, filepath.FromSlash(rel)))
}

// ecgFiles lists the electrocardiogram CSV files, sorted, relative to the
// export root.
func (e *Export) ecgFiles() ([]string, error) {
	var out []string
	if e.zr != nil {
		for rel := range e.files {
			if path.Dir(rel) == "electrocardiograms" && strings.EqualFold(path.Ext(rel), ".csv") {
				out = append(out, rel)
			}
		}
	} else {
		entries, err := os.ReadDir(filepath.Join(e.root, "electrocardiograms"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, d := range entries {
			if !d.IsDir() && strings.EqualFold(filepath.Ext(d.Name()), ".csv") {
				out = append(out, "electrocardiograms/"+d.Name())
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// ExportDate reads the ExportDate element at the top of export.xml, as
// written ("2026-07-15 09:00:00 +0200"). It reads only the head of the file.
func (e *Export) ExportDate() (string, error) {
	r, err := e.openXML()
	if err != nil {
		return "", err
	}
	defer r.Close()
	dec := xml.NewDecoder(bufio.NewReader(r))
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("read export.xml: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "ExportDate":
			return attr(se, "value"), nil
		case "HealthData", "Me":
			continue
		default:
			return "", nil // past the header: no ExportDate in this file
		}
	}
}

func attr(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}
