// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cloudfront

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
)

// node is a generic XML element. CloudFront configs are large and change
// often, so they are kept as trees and written back as they came, rather
// than modelled field by field. Namespaces are dropped on the way in; the
// response writer puts the CloudFront one back on the root.
type node struct {
	Name     string
	Text     string
	Children []*node
}

func parseXML(b []byte) (*node, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	var stack []*node
	var root *node
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &node{Name: t.Name.Local}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.Children = append(p.Children, n)
			} else if root == nil {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].Text += string(t)
			}
		}
	}
	if root == nil {
		return nil, errors.New("empty document")
	}
	root.trim()
	return root, nil
}

// trim drops the whitespace between child elements.
func (n *node) trim() {
	if len(n.Children) > 0 {
		n.Text = ""
	}
	for _, c := range n.Children {
		c.trim()
	}
}

func elem(name string, children ...*node) *node {
	return &node{Name: name, Children: children}
}

func leaf(name, text string) *node {
	return &node{Name: name, Text: text}
}

func (n *node) clone() *node {
	if n == nil {
		return nil
	}
	c := &node{Name: n.Name, Text: n.Text}
	for _, ch := range n.Children {
		c.Children = append(c.Children, ch.clone())
	}
	return c
}

func (n *node) child(name string) *node {
	if n == nil {
		return nil
	}
	for _, c := range n.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// path walks "A/B/C" from n.
func (n *node) path(p string) *node {
	for _, name := range strings.Split(p, "/") {
		n = n.child(name)
		if n == nil {
			return nil
		}
	}
	return n
}

func (n *node) text(p string) string {
	if c := n.path(p); c != nil {
		return strings.TrimSpace(c.Text)
	}
	return ""
}

func (n *node) bool(p string) bool { return n.text(p) == "true" }

func (n *node) int64(p string) (int64, bool) {
	v, err := strconv.ParseInt(n.text(p), 10, 64)
	return v, err == nil
}

// items returns the children of p/Items.
func (n *node) items(p string) []*node {
	if c := n.path(p + "/Items"); c != nil {
		return c.Children
	}
	return nil
}

// set replaces (or adds) a leaf child.
func (n *node) set(name, text string) {
	if c := n.child(name); c != nil {
		c.Text, c.Children = text, nil
		return
	}
	n.Children = append(n.Children, leaf(name, text))
}

// ensure adds a child if there is none with that name.
func (n *node) ensure(c *node) *node {
	if have := n.child(c.Name); have != nil {
		return have
	}
	n.Children = append(n.Children, c)
	return c
}

func (n *node) remove(name string) {
	out := n.Children[:0]
	for _, c := range n.Children {
		if c.Name != name {
			out = append(out, c)
		}
	}
	n.Children = out
}

const xmlns = "http://cloudfront.amazonaws.com/doc/2020-05-31/"

// render writes the document with the CloudFront namespace on the root.
func (n *node) render() []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	n.write(&b, true)
	return b.Bytes()
}

func (n *node) write(b *bytes.Buffer, root bool) {
	b.WriteByte('<')
	b.WriteString(n.Name)
	if root {
		b.WriteString(` xmlns="` + xmlns + `"`)
	}
	if len(n.Children) == 0 && n.Text == "" {
		b.WriteString("/>")
		return
	}
	b.WriteByte('>')
	if len(n.Children) == 0 {
		xml.EscapeText(b, []byte(n.Text))
	}
	for _, c := range n.Children {
		c.write(b, false)
	}
	b.WriteString("</")
	b.WriteString(n.Name)
	b.WriteByte('>')
}

// String is the element without the XML declaration, for storage.
func (n *node) String() string {
	var b bytes.Buffer
	n.write(&b, false)
	return b.String()
}

// list builds the {Quantity, Items} shape CloudFront uses for collections.
func list(name string, items ...*node) *node {
	n := elem(name, leaf("Quantity", strconv.Itoa(len(items))))
	if len(items) > 0 {
		n.Children = append(n.Children, elem("Items", items...))
	}
	return n
}
