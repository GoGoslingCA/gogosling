package preview

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"path"
	"sort"
	"strings"
)

func parseArchive(ctx context.Context, in Input) (Document, error) {
	out := Document{Entries: []Entry{}}
	if strings.EqualFold(path.Ext(in.Name), ".zip") {
		p, e := openPackage(ctx, in)
		if e != nil {
			return out, e
		}
		for name, f := range p.files {
			out.Entries = append(out.Entries, Entry{Name: name, Size: int64(f.UncompressedSize64), Directory: f.FileInfo().IsDir()})
		}
		sort.Slice(out.Entries, func(i, j int) bool { return out.Entries[i].Name < out.Entries[j].Name })
		return out, nil
	}
	var reader io.Reader = io.NewSectionReader(in.Reader, 0, in.Size)
	lower := strings.ToLower(in.Name)
	if strings.HasSuffix(lower, ".gz") || strings.HasSuffix(lower, ".tgz") {
		gz, e := gzip.NewReader(reader)
		if e != nil {
			return out, Error("corrupt")
		}
		defer gz.Close()
		reader = gz
		if !strings.HasSuffix(lower, ".tar.gz") && !strings.HasSuffix(lower, ".tgz") {
			n, e := io.Copy(io.Discard, io.LimitReader(reader, MaxExpanded+1))
			if e != nil {
				return out, Error("corrupt")
			}
			if n > MaxExpanded {
				return out, Error("too_large")
			}
			name := gz.Name
			if !safeMember(name) {
				name = strings.TrimSuffix(in.Name, path.Ext(in.Name))
			}
			out.Entries = append(out.Entries, Entry{Name: name, Size: n})
			return out, nil
		}
	}
	tr := tar.NewReader(io.LimitReader(reader, MaxExpanded+1))
	var total int64
	for {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return out, Error("corrupt")
		}
		if !safeMember(h.Name) {
			return out, Error("corrupt")
		}
		total += h.Size
		if len(out.Entries) >= MaxEntries || total > MaxExpanded {
			return out, Error("too_large")
		}
		out.Entries = append(out.Entries, Entry{Name: h.Name, Size: h.Size, Directory: h.Typeflag == tar.TypeDir})
	}
	return out, nil
}
