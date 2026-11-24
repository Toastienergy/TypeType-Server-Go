package portability

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path"
	"sort"
	"strings"
)

// Input is PortabilityInput: the uploaded file, its first 64 KiB, and its zip inventory when it is an archive.
type Input struct {
	Path        string
	Filename    string
	ContentType string
	Size        int64
	Probe       []byte
	Archive     *Inventory
}

type ArchiveEntry struct {
	Name                     string
	Compressed, ExpandedSize int64
}

type Inventory struct {
	Entries  []ArchiveEntry
	Expanded int64
	names    map[string]bool
}

func (i *Inventory) Has(name string) bool { return i != nil && i.names[name] }

// NewInput is PortabilityInputFactory with PortabilityArchiveInspector's limits.
func NewInput(p, filename, contentType string) (*Input, error) {
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if st.Size() < 1 || st.Size() > MaxUploadBytes {
		return nil, invalid("Backup file size is outside the allowed range")
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	probe, _ := io.ReadAll(io.LimitReader(f, ProbeBytes))
	f.Close()
	in := &Input{Path: p, Filename: filename, ContentType: contentType, Size: st.Size(), Probe: probe}
	if len(probe) >= 4 && probe[0] == 0x50 && probe[1] == 0x4b {
		inv, err := inspectArchive(p)
		if err != nil {
			return nil, err
		}
		in.Archive = inv
	}
	return in, nil
}

func inspectArchive(p string) (*Inventory, error) {
	z, err := zip.OpenReader(p)
	if err != nil {
		return nil, invalid("Backup archive is unreadable")
	}
	defer z.Close()
	inv := &Inventory{names: map[string]bool{}}
	count := 0
	for _, f := range z.File {
		if count >= MaxArchiveEntries {
			return nil, invalid("Archive contains too many entries")
		}
		count++
		name := f.Name
		clean := path.Clean(strings.ReplaceAll(name, "\\", "/"))
		if strings.TrimSpace(name) == "" || strings.ContainsRune(name, 0) {
			return nil, invalid("Archive entry has an invalid name")
		}
		if strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
			return nil, invalid("Archive entry escapes its root")
		}
		if f.FileInfo().IsDir() {
			continue
		}
		expanded, compressed := int64(f.UncompressedSize64), int64(f.CompressedSize64)
		if expanded < 0 || expanded > MaxArchiveEntryBytes {
			return nil, invalid("Archive entry is too large")
		}
		inv.Expanded += expanded
		if inv.Expanded > MaxArchiveExpandedBytes {
			return nil, invalid("Archive expands beyond the limit")
		}
		if expanded > 0 && compressed == 0 {
			return nil, invalid("Archive entry has an invalid compression size")
		}
		if expanded > 0 && compressed > 0 && expanded/compressed > MaxCompressionRatio {
			return nil, invalid("Archive entry exceeds the compression ratio limit")
		}
		inv.Entries = append(inv.Entries, ArchiveEntry{name, compressed, expanded})
		inv.names[name] = true
	}
	return inv, nil
}

// OpenEntry reads one archive entry, never more than its declared size.
func (in *Input) OpenEntry(name string) ([]byte, error) {
	z, err := zip.OpenReader(in.Path)
	if err != nil {
		return nil, err
	}
	defer z.Close()
	for _, f := range z.File {
		if f.Name == name {
			r, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer r.Close()
			return io.ReadAll(io.LimitReader(r, MaxArchiveEntryBytes))
		}
	}
	return nil, os.ErrNotExist
}

// ProbeText is the probe without a byte-order mark, trimmed, for format sniffing.
func (in *Input) ProbeText() string {
	return strings.TrimSpace(string(bytes.TrimPrefix(in.Probe, []byte("\xef\xbb\xbf"))))
}

type Detection struct {
	Format        Format
	FormatVersion *string
	Confidence    int
	Evidence      string
}

type Fidelity string

const (
	Complete Fidelity = "complete"
	Partial  Fidelity = "partial"
)

type Capability struct {
	Category   Category `json:"category"`
	Directions []string `json:"directions"`
	Fidelity   Fidelity `json:"fidelity"`
}

type Descriptor struct {
	Format           Format       `json:"format"`
	AdapterVersion   int          `json:"adapterVersion"`
	Capabilities     []Capability `json:"capabilities"`
	DefaultExtension string       `json:"defaultExtension"`
	ContentType      string       `json:"contentType"`
}

// Adapter reads and writes one format.
type Adapter interface {
	Descriptor() Descriptor
	AutoDetect() bool
	Detect(*Input) *Detection
	Decode(*Input, Sink) error
	Encode(src Source, w io.Writer, categories []Category) error
}

func both(c Category, f Fidelity) Capability {
	return Capability{c, []string{"import", "export"}, f}
}

func importOnly(c Category, f Fidelity) Capability {
	return Capability{c, []string{"import"}, f}
}

func exportable(d Descriptor, c Category) bool {
	for _, cap := range d.Capabilities {
		if cap.Category == c {
			for _, dir := range cap.Directions {
				if dir == "export" {
					return true
				}
			}
		}
	}
	return false
}

// Registry is PortabilityRegistry: detection picks the most confident adapter and refuses a guess closer than
// ten points to the next one.
type Registry struct{ adapters []Adapter }

func (r *Registry) Descriptors() []Descriptor {
	out := make([]Descriptor, len(r.adapters))
	for i, a := range r.adapters {
		out[i] = a.Descriptor()
	}
	return out
}

func (r *Registry) Adapter(f Format) (Adapter, error) {
	for _, a := range r.adapters {
		if a.Descriptor().Format == f {
			return a, nil
		}
	}
	return nil, unsupportedFormat(f)
}

type match struct {
	a Adapter
	d *Detection
}

func (r *Registry) Detect(in *Input, hint Format) (Adapter, *Detection, error) {
	if hint != "" {
		a, err := r.Adapter(hint)
		if err != nil {
			return nil, nil, err
		}
		d := a.Detect(in)
		if d == nil {
			return nil, nil, mismatch(hint)
		}
		return a, d, nil
	}
	var ms []match
	for _, a := range r.adapters {
		if !a.AutoDetect() {
			continue
		}
		if d := a.Detect(in); d != nil {
			ms = append(ms, match{a, d})
		}
	}
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].d.Confidence > ms[j].d.Confidence })
	if len(ms) == 0 {
		return nil, nil, errUnknownFormat
	}
	if len(ms) > 1 && ms[0].d.Confidence-ms[1].d.Confidence < 10 {
		return nil, nil, contract("portability_format_ambiguous", "Backup matches both "+string(ms[0].d.Format)+" and "+string(ms[1].d.Format))
	}
	return ms[0].a, ms[0].d, nil
}
