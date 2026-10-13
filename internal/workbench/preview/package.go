package preview

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"io"
	"net/http"
	"path"
	"strings"
)

type officePackage struct {
	files map[string]*zip.File
	total int64
	ctx   context.Context
}

func openPackage(ctx context.Context, in Input) (*officePackage, error) {
	zr, err := zip.NewReader(in.Reader, in.Size)
	if err != nil {
		return nil, Error("corrupt")
	}
	if len(zr.File) > MaxEntries {
		return nil, Error("too_large")
	}
	p := &officePackage{files: map[string]*zip.File{}, ctx: ctx}
	var expanded uint64
	for _, f := range zr.File {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if f.Flags&1 != 0 {
			return nil, Error("encrypted")
		}
		if !safeMember(f.Name) || p.files[f.Name] != nil {
			return nil, Error("corrupt")
		}
		if f.UncompressedSize64 > MaxExpanded || expanded > MaxExpanded-f.UncompressedSize64 {
			return nil, Error("too_large")
		}
		expanded += f.UncompressedSize64
		p.files[f.Name] = f
	}
	return p, nil
}
func safeMember(name string) bool {
	return name != "" && !strings.ContainsAny(name, "\\\x00") && !strings.HasPrefix(name, "/") && !strings.Contains(name, ":") && path.Clean(strings.TrimSuffix(name, "/")) == strings.TrimSuffix(name, "/") && name != ".." && !strings.HasPrefix(name, "../")
}
func (p *officePackage) read(name string) ([]byte, error) {
	if p.ctx.Err() != nil {
		return nil, p.ctx.Err()
	}
	f := p.files[name]
	if f == nil {
		return nil, Error("corrupt")
	}
	if f.UncompressedSize64 > MaxPart {
		return nil, Error("too_large")
	}
	r, err := f.Open()
	if err != nil {
		return nil, Error("corrupt")
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(packageReader{ctx: p.ctx, reader: r}, MaxPart+1))
	p.total += int64(len(data))
	if p.ctx.Err() != nil {
		return nil, p.ctx.Err()
	}
	if len(data) > MaxPart || p.total > MaxExpanded {
		return nil, Error("too_large")
	}
	if err != nil {
		return nil, Error("corrupt")
	}
	return data, nil
}

// packageReader 在解压产生新数据时检查取消，不等待整个 XML 成员解压完成。
type packageReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r packageReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

// xmlNode 只用于最多8MiB的包成员；限制节点数/嵌套层数避免极端输入。
type xmlNode struct {
	name     string
	attrs    map[string]string
	text     string
	children []*xmlNode
}

func parseXML(ctx context.Context, data []byte) (*xmlNode, error) {
	root := &xmlNode{}
	stack := []*xmlNode{root}
	d := xml.NewDecoder(bytes.NewReader(data))
	count := 0
	tokens := 0
	for {
		// 解压结束后的 CPU 遍历也必须响应关闭/切换文件，及时归还唯一解析槽。
		if tokens%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		tokens++
		t, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, Error("corrupt")
		}
		switch v := t.(type) {
		case xml.StartElement:
			count++
			if count > 100000 || len(stack) > 80 {
				return nil, Error("too_large")
			}
			n := &xmlNode{name: v.Name.Local}
			if len(v.Attr) > 0 {
				n.attrs = make(map[string]string, len(v.Attr))
			}
			for _, a := range v.Attr {
				n.attrs[a.Name.Local] = a.Value
				if a.Name.Local == "id" && (strings.Contains(a.Name.Space, "/relationships") || a.Name.Space == "r") {
					n.attrs["relId"] = a.Value
				}
			}
			parent := stack[len(stack)-1]
			parent.children = append(parent.children, n)
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) < 2 {
				return nil, Error("corrupt")
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) == 1 {
				if strings.TrimSpace(string(v)) != "" {
					return nil, Error("corrupt")
				}
				continue
			}
			count++
			if count > 100000 {
				return nil, Error("too_large")
			}
			if len(v) > 0 {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, &xmlNode{name: "#text", text: string(v)})
			}
		case xml.Directive:
			return nil, Error("corrupt")
		}
	}
	if len(stack) != 1 || len(root.children) != 1 {
		return nil, Error("corrupt")
	}
	return root.children[0], nil
}
func (p *officePackage) xml(name string) (*xmlNode, error) {
	b, e := p.read(name)
	if e != nil {
		return nil, e
	}
	return parseXML(p.ctx, b)
}
func (n *xmlNode) child(name string) *xmlNode {
	for _, c := range n.children {
		if c.name == name {
			return c
		}
	}
	return &xmlNode{}
}
func (n *xmlNode) all(name string) []*xmlNode {
	var out []*xmlNode
	var walk func(*xmlNode)
	walk = func(v *xmlNode) {
		if v.name == name {
			out = append(out, v)
		}
		for _, c := range v.children {
			walk(c)
		}
	}
	walk(n)
	return out
}
func (n *xmlNode) content() string {
	var b strings.Builder
	var walk func(*xmlNode)
	walk = func(v *xmlNode) {
		b.WriteString(v.text)
		for _, c := range v.children {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}
func (n *xmlNode) texts() string {
	var b strings.Builder
	for _, t := range n.all("t") {
		b.WriteString(t.content())
	}
	return b.String()
}
func (p *officePackage) relationships(part string) map[string]string {
	relPath := path.Join(path.Dir(part), "_rels", path.Base(part)+".rels")
	out := map[string]string{}
	if p.files[relPath] == nil {
		return out
	}
	node, err := p.xml(relPath)
	if err != nil {
		return out
	}
	for _, r := range node.children {
		if r.name != "Relationship" || r.attrs["TargetMode"] == "External" {
			continue
		}
		rawTarget := r.attrs["Target"]
		target := path.Clean(path.Join(path.Dir(part), rawTarget))
		if strings.HasPrefix(rawTarget, "/") {
			target = strings.TrimPrefix(path.Clean(rawTarget), "/")
		}
		if safeMember(target) {
			out[r.attrs["Id"]] = target
		}
	}
	return out
}
func (p *officePackage) image(name string) string {
	if name == "" || p.files[name] == nil || p.files[name].UncompressedSize64 > 2<<20 {
		return ""
	}
	raw, err := p.read(name)
	if err != nil {
		return ""
	}
	mime := http.DetectContentType(raw)
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return ""
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw)
}
