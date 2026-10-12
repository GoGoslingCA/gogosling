package preview

import (
	"context"
	"strconv"
)

func parseSlides(ctx context.Context, in Input) (Document, error) {
	p, e := openPackage(ctx, in)
	if e != nil {
		return Document{}, e
	}
	root, e := p.xml("ppt/presentation.xml")
	if e != nil {
		return Document{}, e
	}
	if root.name != "presentation" {
		return Document{}, Error("corrupt")
	}
	rels := p.relationships("ppt/presentation.xml")
	slides := []Slide{}
	warnings := []string{"content_layout"}
	for _, ref := range root.child("sldIdLst").children {
		if ref.name != "sldId" {
			continue
		}
		if len(slides) >= MaxPages {
			warnings = append(warnings, "truncated")
			break
		}
		if ctx.Err() != nil {
			return Document{}, ctx.Err()
		}
		name := rels[ref.attrs["relId"]]
		node, e := p.xml(name)
		if e != nil {
			return Document{}, e
		}
		if node.name != "sld" {
			return Document{}, Error("corrupt")
		}
		images := p.relationships(name)
		blocks := []Block{}
		for _, shape := range node.child("cSld").child("spTree").children {
			for _, para := range shape.all("p") {
				if text := para.texts(); text != "" {
					blocks = append(blocks, Block{Kind: "paragraph", Text: text})
				}
			}
			for _, img := range shape.all("blip") {
				if url := p.image(images[img.attrs["embed"]]); url != "" {
					blocks = append(blocks, Block{Kind: "image", Image: url})
				}
			}
		}
		slides = append(slides, Slide{Name: strconv.Itoa(len(slides) + 1), Blocks: blocks})
	}
	return Document{Slides: slides, Warnings: warnings}, nil
}
