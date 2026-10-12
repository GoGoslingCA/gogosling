package preview

import (
	"context"
	"path"
	"strconv"
	"strings"
)

func parseOpenDocument(ctx context.Context, in Input) (Document, error) {
	p, e := openPackage(ctx, in)
	if e != nil {
		return Document{}, e
	}
	root, e := p.xml("content.xml")
	if e != nil {
		return Document{}, e
	}
	if root.name != "document-content" {
		return Document{}, Error("corrupt")
	}
	body := root.child("body")
	out := Document{Warnings: []string{"content_layout"}}
	ext := strings.ToLower(path.Ext(in.Name))
	switch ext {
	case ".ods", ".ots":
		out.Kind = "sheet"
		cells := 0
		for _, table := range body.child("spreadsheet").children {
			if table.name != "table" {
				continue
			}
			if len(out.Sheets) >= 50 {
				out.Warnings = append(out.Warnings, "truncated")
				break
			}
			rows := [][]string{}
			stop := false
			for _, row := range table.all("table-row") {
				values := []string{}
				for _, cell := range row.children {
					if cell.name != "table-cell" && cell.name != "covered-table-cell" {
						continue
					}
					count := repeat(cell.attrs["number-columns-repeated"])
					if count > MaxColumns-len(values) {
						count = MaxColumns - len(values)
						stop = true
					}
					parts := []string{}
					for _, para := range cell.all("p") {
						parts = append(parts, para.content())
					}
					value := strings.Join(parts, "\n")
					if value == "" {
						value = cell.attrs["value"]
					}
					for i := 0; i < count; i++ {
						values = append(values, value)
					}
				}
				count := repeat(row.attrs["number-rows-repeated"])
				for i := 0; i < count; i++ {
					if ctx.Err() != nil {
						return Document{}, ctx.Err()
					}
					if len(rows) >= 2000 || cells+len(values)+1 > MaxCells {
						stop = true
						break
					}
					rows = append(rows, values)
					cells += len(values) + 1
				}
				if stop {
					out.Warnings = append(out.Warnings, "truncated")
					break
				}
			}
			out.Sheets = append(out.Sheets, Sheet{Name: table.attrs["name"], Rows: rows})
		}
	case ".odp", ".otp":
		out.Kind = "slides"
		for _, page := range body.child("presentation").children {
			if page.name != "page" {
				continue
			}
			if len(out.Slides) >= MaxPages {
				out.Warnings = append(out.Warnings, "truncated")
				break
			}
			out.Slides = append(out.Slides, Slide{Name: page.attrs["name"], Blocks: odfBlocks(page, p)})
		}
	default:
		out.Kind = "document"
		out.Blocks = odfBlocks(body.child("text"), p)
	}
	return out, nil
}
func repeat(s string) int {
	n, e := strconv.Atoi(s)
	if e != nil || n < 1 {
		return 1
	}
	return n
}
func odfBlocks(root *xmlNode, p *officePackage) []Block {
	blocks := []Block{}
	var walk func(*xmlNode)
	walk = func(n *xmlNode) {
		switch n.name {
		case "p", "h":
			kind := "paragraph"
			level := 0
			if n.name == "h" {
				kind = "heading"
				level, _ = strconv.Atoi(n.attrs["outline-level"])
			}
			blocks = append(blocks, Block{Kind: kind, Level: level, Text: n.content()})
			return
		case "image":
			if url := p.image(n.attrs["href"]); url != "" {
				blocks = append(blocks, Block{Kind: "image", Image: url})
			}
			return
		}
		for _, c := range n.children {
			walk(c)
		}
	}
	walk(root)
	return blocks
}
