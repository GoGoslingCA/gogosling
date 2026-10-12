package preview

import (
	"context"
	"strconv"
	"strings"
)

func parseSheets(ctx context.Context, in Input) (Document, error) {
	p, e := openPackage(ctx, in)
	if e != nil {
		return Document{}, e
	}
	book, e := p.xml("xl/workbook.xml")
	if e != nil {
		return Document{}, e
	}
	if book.name != "workbook" {
		return Document{}, Error("corrupt")
	}
	shared := []string{}
	if p.files["xl/sharedStrings.xml"] != nil {
		ss, e := p.xml("xl/sharedStrings.xml")
		if e != nil {
			return Document{}, e
		}
		for _, item := range ss.children {
			if item.name == "si" {
				shared = append(shared, item.texts())
			}
		}
	}
	refs := p.relationships("xl/workbook.xml")
	sheets := []Sheet{}
	warnings := []string{"cached_values"}
	allocated := 0
	for _, ref := range book.child("sheets").children {
		if ref.name != "sheet" {
			continue
		}
		if len(sheets) >= 50 {
			warnings = append(warnings, "truncated")
			break
		}
		node, e := p.xml(refs[ref.attrs["relId"]])
		if e != nil {
			return Document{}, e
		}
		if node.name != "worksheet" {
			return Document{}, Error("corrupt")
		}
		rows := [][]string{}
		truncated := false
		for _, row := range node.child("sheetData").children {
			if row.name != "row" {
				continue
			}
			if ctx.Err() != nil {
				return Document{}, ctx.Err()
			}
			index, _ := strconv.Atoi(row.attrs["r"])
			if index <= 0 {
				index = len(rows) + 1
			}
			if index > 2000 {
				truncated = true
				continue
			}
			for len(rows) < index {
				if allocated >= MaxCells {
					truncated = true
					break
				}
				rows = append(rows, []string{})
				allocated++
			}
			if len(rows) < index {
				break
			}
			for _, cell := range row.children {
				if cell.name != "c" {
					continue
				}
				col := columnIndex(cell.attrs["r"])
				if col < 0 {
					col = len(rows[index-1])
				}
				if col >= MaxColumns {
					truncated = true
					continue
				}
				needed := col + 1 - len(rows[index-1])
				if needed > 0 {
					if allocated+needed > MaxCells {
						truncated = true
						continue
					}
					rows[index-1] = append(rows[index-1], make([]string, needed)...)
					allocated += needed
				}
				value := cell.child("v").content()
				switch cell.attrs["t"] {
				case "s":
					i, e := strconv.Atoi(value)
					if e != nil || i < 0 || i >= len(shared) {
						return Document{}, Error("corrupt")
					}
					value = shared[i]
				case "inlineStr":
					value = cell.child("is").texts()
				case "b":
					if value == "1" {
						value = "TRUE"
					} else {
						value = "FALSE"
					}
				}
				if value == "" && cell.child("f").name != "" {
					value = "=" + cell.child("f").content()
				}
				rows[index-1][col] = value
			}
		}
		if truncated {
			warnings = append(warnings, "truncated")
		}
		sheets = append(sheets, Sheet{Name: ref.attrs["name"], Rows: rows})
	}
	return Document{Sheets: sheets, Warnings: warnings}, nil
}
func columnIndex(ref string) int {
	col := 0
	for _, c := range strings.ToUpper(ref) {
		if c < 'A' || c > 'Z' {
			break
		}
		col = col*26 + int(c-'A') + 1
		if col > MaxColumns {
			return MaxColumns
		}
	}
	return col - 1
}
