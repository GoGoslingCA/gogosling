package preview

import (
	"context"
	"strconv"
	"strings"
)

func parseWord(ctx context.Context, in Input) (Document, error) {
	p, e := openPackage(ctx, in)
	if e != nil {
		return Document{}, e
	}
	root, e := p.xml("word/document.xml")
	if e != nil {
		return Document{}, e
	}
	if root.name != "document" {
		return Document{}, Error("corrupt")
	}
	rels := p.relationships("word/document.xml")
	blocks := []Block{}
	for _, node := range root.child("body").children {
		if ctx.Err() != nil {
			return Document{}, ctx.Err()
		}
		switch node.name {
		case "p":
			blocks = append(blocks, wordParagraph(node, p, rels)...)
		case "tbl":
			rows := [][]string{}
			for _, r := range node.children {
				if r.name != "tr" {
					continue
				}
				row := []string{}
				for _, c := range r.children {
					if c.name == "tc" {
						parts := []string{}
						for _, para := range c.all("p") {
							parts = append(parts, para.texts())
						}
						row = append(row, strings.Join(parts, "\n"))
					}
				}
				rows = append(rows, row)
			}
			blocks = append(blocks, Block{Kind: "table", Rows: rows})
		}
		if len(blocks) > 5000 {
			return Document{}, Error("too_large")
		}
	}
	return Document{Blocks: blocks, Warnings: []string{"content_layout"}}, nil
}
func wordParagraph(n *xmlNode, p *officePackage, rels map[string]string) []Block {
	runs := []Run{}
	var body strings.Builder
	for _, r := range n.all("r") {
		text := r.texts()
		for range r.all("tab") {
			text += "\t"
		}
		for range r.all("br") {
			text += "\n"
		}
		prop := r.child("rPr")
		bold := prop.child("b")
		italic := prop.child("i")
		runs = append(runs, Run{Text: text, Bold: bold.name != "" && bold.attrs["val"] != "0", Italic: italic.name != "" && italic.attrs["val"] != "0"})
		body.WriteString(text)
	}
	block := Block{Kind: "paragraph", Text: body.String(), Runs: runs}
	style := strings.ToLower(n.child("pPr").child("pStyle").attrs["val"])
	if style == "title" {
		block.Kind = "heading"
		block.Level = 1
	}
	if style == "subtitle" {
		block.Kind = "heading"
		block.Level = 2
	}
	if strings.HasPrefix(style, "heading") {
		if level, e := strconv.Atoi(strings.TrimPrefix(style, "heading")); e == nil && level >= 1 && level <= 6 {
			block.Kind = "heading"
			block.Level = level
		}
	}
	out := []Block{block}
	for _, img := range n.all("blip") {
		if url := p.image(rels[img.attrs["embed"]]); url != "" {
			out = append(out, Block{Kind: "image", Image: url})
		}
	}
	return out
}
