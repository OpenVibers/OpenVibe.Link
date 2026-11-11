package send

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// KindFile, KindNote and KindClipboard are the three things Link Send can carry. The kind is inside the sealed
// payload, never in the relay's metadata: the receiving machine learns it only after opening the item.
const (
	KindFile      = "file"
	KindNote      = "note"
	KindClipboard = "clipboard"
)

// Content is what a sealed item carries: the name and type a person sees, then the bytes. It is sealed as one
// blob, so the relay never sees the name, the type or the kind either.
type Content struct {
	Name string `json:"name"`
	Mime string `json:"mime"`
	Kind string `json:"kind,omitempty"`
}

type Packed struct {
	Content
	Data []byte
}

func Pack(name, mime string, data []byte) []byte {
	return PackKind(name, mime, KindFile, data)
}

func PackKind(name, mime, kind string, data []byte) []byte {
	if name == "" {
		name = "received-file"
	}
	meta, _ := json.Marshal(Content{Name: name, Mime: mime, Kind: kind})
	out := make([]byte, 4+len(meta)+len(data))
	binary.BigEndian.PutUint32(out[:4], uint32(len(meta)))
	copy(out[4:], meta)
	copy(out[4+len(meta):], data)
	return out
}

func Unpack(b []byte) (Packed, error) {
	if len(b) < 4 {
		return Packed{}, errors.New("the received item is incomplete")
	}
	n := int(binary.BigEndian.Uint32(b[:4]))
	if n < 2 || n > len(b)-4 {
		return Packed{}, errors.New("the received item is incomplete")
	}
	var c Content
	if json.Unmarshal(b[4:4+n], &c) != nil {
		return Packed{}, errors.New("the received item does not say what it is")
	}
	if c.Kind == "" {
		c.Kind = KindFile
	}
	if c.Kind != KindFile && c.Kind != KindNote && c.Kind != KindClipboard {
		return Packed{}, errors.New("the received item is not something Link sends")
	}
	return Packed{Content: c, Data: b[4+n:]}, nil
}

// SafeName keeps one path segment: no folder separators, no control characters, no leading dot, and short enough
// for every filesystem Link runs on.
func SafeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' || unicode.IsControl(r) {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimLeft(name, ".")
	if name == "" {
		return "received-file"
	}
	runes := []rune(name)
	if len(runes) > 180 {
		name = string(runes[:180])
	}
	return name
}

// DownloadsDir is where received files go unless the daemon was given another folder.
func DownloadsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errNoDownloads
	}
	return filepath.Join(home, "Downloads"), nil
}

// Save writes the item into dir without replacing an existing file: a second "report.pdf" becomes "report (2).pdf".
// It returns the path it wrote.
func Save(dir, name string, data []byte) (string, error) {
	if dir == "" {
		var err error
		if dir, err = DownloadsDir(); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("the Downloads folder could not be opened")
	}
	safe := SafeName(name)
	ext := filepath.Ext(safe)
	stem := strings.TrimSuffix(safe, ext)
	for i := 0; i < 1000; i++ {
		candidate := safe
		if i > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", stem, i+1, ext)
		}
		path := filepath.Join(dir, candidate)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("the file could not be saved in Downloads")
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			_ = os.Remove(path)
			return "", fmt.Errorf("the file could not be saved in Downloads")
		}
		if err := f.Close(); err != nil {
			_ = os.Remove(path)
			return "", fmt.Errorf("the file could not be saved in Downloads")
		}
		return path, nil
	}
	return "", fmt.Errorf("there are too many files with that name in Downloads")
}

var errNoDownloads = errors.New("this computer has no Downloads folder to save into")
