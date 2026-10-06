package output

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"time"
)

// MaxArtifacts caps the image and audio blocks one call may export.
const MaxArtifacts = 256

// Artifact is one image or audio block saved by --output-dir.
type Artifact struct {
	Index    int    `json:"index"`
	Type     string `json:"type"`
	MIMEType string `json:"mimeType"`
	Path     string `json:"path"`
	Bytes    int64  `json:"bytes"`
}

// ExportDir is an existing directory that receives exported blocks. Every
// file is created inside it through os.Root, so no name can leave it.
type ExportDir struct {
	root *os.Root
	abs  string
}

// OpenExportDir opens dir, which must be an existing directory.
func OpenExportDir(dir string) (*ExportDir, error) {
	invalid := NewError("invalid_arguments", nil)
	invalid.Message = "--output-dir must name an existing directory."
	if dir == "" {
		return nil, invalid
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, invalid
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, invalid
	}
	return &ExportDir{root: root, abs: abs}, nil
}

func (d *ExportDir) Close() error { return d.root.Close() }

type exportBlock struct {
	index          int
	kind, mimeType string
	data           []byte
}

// Export saves the result's top-level image and audio blocks as new files
// named <prefix>-<index>.<ext>. Every block is decoded before any file is
// created, and no existing file or symlink is ever opened. It returns the
// artifacts written so far alongside any export_failed error.
func (d *ExportDir) Export(result json.RawMessage, prefix string) ([]Artifact, error) {
	blocks, err := exportBlocks(result)
	if err != nil {
		return nil, err
	}
	artifacts := []Artifact{}
	for _, b := range blocks {
		name := fmt.Sprintf("%s-%d.%s", prefix, b.index, extFor(b.mimeType))
		if err = d.write(name, b.data); err != nil {
			return artifacts, NewError("export_failed", nil)
		}
		artifacts = append(artifacts, Artifact{Index: b.index, Type: b.kind, MIMEType: b.mimeType, Path: filepath.Join(d.abs, name), Bytes: int64(len(b.data))})
	}
	return artifacts, nil
}

func (d *ExportDir) write(name string, data []byte) error {
	f, err := d.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		_ = d.root.Remove(name)
	}
	return err
}

// exportBlocks decodes the exportable blocks; any shape other than an object
// with a content array has none.
func exportBlocks(result json.RawMessage) ([]exportBlock, error) {
	var top map[string]json.RawMessage
	var content []json.RawMessage
	if json.Unmarshal(result, &top) != nil || json.Unmarshal(top["content"], &content) != nil {
		return nil, nil
	}
	var blocks []exportBlock
	for i, raw := range content {
		var fields map[string]json.RawMessage
		var kind string
		if json.Unmarshal(raw, &fields) != nil || json.Unmarshal(fields["type"], &kind) != nil || kind != "image" && kind != "audio" {
			continue
		}
		if len(blocks) == MaxArtifacts {
			return nil, NewError("export_failed", nil)
		}
		var encoded, mimeType string
		_ = json.Unmarshal(fields["mimeType"], &mimeType)
		if !isString(fields["data"]) || json.Unmarshal(fields["data"], &encoded) != nil {
			return nil, NewError("export_failed", nil)
		}
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			if data, err = base64.RawStdEncoding.DecodeString(encoded); err != nil {
				return nil, NewError("export_failed", nil)
			}
		}
		blocks = append(blocks, exportBlock{index: i, kind: kind, mimeType: mimeType, data: data})
	}
	return blocks, nil
}

var extensions = map[string]string{
	"image/png": "png", "image/jpeg": "jpg", "image/gif": "gif", "image/webp": "webp", "image/svg+xml": "svg",
	"audio/wav": "wav", "audio/x-wav": "wav", "audio/mpeg": "mp3", "audio/ogg": "ogg", "audio/webm": "webm",
	"audio/flac": "flac", "audio/mp4": "m4a", "audio/aac": "m4a",
}

// extFor maps a block's MIME type to a fixed extension; the server's text
// never reaches a file name.
func extFor(mimeType string) string {
	media, _, err := mime.ParseMediaType(mimeType)
	if ext, ok := extensions[media]; err == nil && ok {
		return ext
	}
	return "bin"
}

// ExportPrefix names one call's exports: a UTC timestamp plus 24 random bits.
func ExportPrefix(now time.Time) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return "mcparcel-" + now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}
