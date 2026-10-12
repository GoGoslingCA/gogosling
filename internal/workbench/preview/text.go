package preview

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/csv"
	"io"
	"path"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

func parseText(ctx context.Context, in Input) (Document, error) {
	if in.Size > MaxText {
		return Document{}, Error("too_large")
	}
	raw, err := io.ReadAll(io.NewSectionReader(in.Reader, 0, in.Size))
	if err != nil {
		return Document{}, Error("corrupt")
	}
	var text string
	if len(raw) >= 2 && (bytes.Equal(raw[:2], []byte{0xff, 0xfe}) || bytes.Equal(raw[:2], []byte{0xfe, 0xff})) {
		if len(raw)%2 != 0 {
			return Document{}, Error("encoding")
		}
		var order binary.ByteOrder = binary.LittleEndian
		if raw[0] == 0xfe {
			order = binary.BigEndian
		}
		units := make([]uint16, (len(raw)-2)/2)
		for i := range units {
			units[i] = order.Uint16(raw[2+i*2:])
		}
		text = string(utf16.Decode(units))
	} else {
		raw = bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})
		if !utf8.Valid(raw) || bytes.ContainsRune(raw, 0) {
			return Document{}, Error("encoding")
		}
		text = string(raw)
	}
	return Document{Text: text}, nil
}
func parseDelimited(ctx context.Context, in Input) (Document, error) {
	doc, err := parseText(ctx, in)
	if err != nil {
		return doc, err
	}
	r := csv.NewReader(strings.NewReader(doc.Text))
	r.FieldsPerRecord = -1
	if strings.EqualFold(path.Ext(in.Name), ".tsv") {
		r.Comma = '\t'
	}
	rows := [][]string{}
	cells := 0
	warnings := []string{}
	for {
		if ctx.Err() != nil {
			return Document{}, ctx.Err()
		}
		row, e := r.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			return Document{}, Error("corrupt")
		}
		if len(row) > MaxColumns || cells+len(row) > MaxCells {
			warnings = append(warnings, "truncated")
			break
		}
		rows = append(rows, row)
		cells += len(row)
	}
	return Document{Sheets: []Sheet{{Name: in.Name, Rows: rows}}, Warnings: warnings}, nil
}
