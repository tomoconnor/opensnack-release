// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"opensnack/internal/resource"

	"gorm.io/gorm"
)

const service = "ec2"

// Store types.
const (
	tVpc      = "vpc"
	tSubnet   = "subnet"
	tRTB      = "route_table"
	tIGW      = "internet_gateway"
	tNAT      = "nat_gateway"
	tEIP      = "address"
	tSG       = "security_group"
	tNACL     = "network_acl"
	tENI      = "network_interface"
	tInstance = "instance"
	tVolume   = "volume"
	tKeyPair  = "key_pair"
	tEndpoint = "vpc_endpoint"
	// tDeleted marks a default-VPC resource the user deleted, so the
	// generated copy stops appearing.
	tDeleted = "deleted_default"
)

// object is what every stored EC2 resource implements, so tagging and
// filtering work the same way for all of them.
type object interface {
	objectID() string
	tagList() *[]Tag
	// filterValues maps Describe* filter names to the resource's values.
	// tag:, tag-key and tag-value are handled generically.
	filterValues() map[string][]string
}

// kind describes a taggable resource type.
type kind struct {
	typ     string // store type
	resType string // DescribeTags resource-type
	new     func() object
}

// kindsByPrefix maps an ID prefix ("vpc" in vpc-0abc) to its kind.
var kindsByPrefix = map[string]kind{
	"vpc":      {tVpc, "vpc", func() object { return &Vpc{} }},
	"subnet":   {tSubnet, "subnet", func() object { return &Subnet{} }},
	"rtb":      {tRTB, "route-table", func() object { return &RouteTable{} }},
	"igw":      {tIGW, "internet-gateway", func() object { return &InternetGateway{} }},
	"nat":      {tNAT, "natgateway", func() object { return &NatGateway{} }},
	"eipalloc": {tEIP, "elastic-ip", func() object { return &Address{} }},
	"sg":       {tSG, "security-group", func() object { return &SecurityGroup{} }},
	"acl":      {tNACL, "network-acl", func() object { return &NetworkAcl{} }},
	"eni":      {tENI, "network-interface", func() object { return &NetworkInterface{} }},
	"i":        {tInstance, "instance", func() object { return &Instance{} }},
	"vol":      {tVolume, "volume", func() object { return &Volume{} }},
	"key":      {tKeyPair, "key-pair", func() object { return &KeyPair{} }},
	"vpce":     {tEndpoint, "vpc-endpoint", func() object { return &VpcEndpoint{} }},
}

func kindOf(id string) (kind, bool) {
	prefix, _, ok := strings.Cut(id, "-")
	if !ok {
		return kind{}, false
	}
	k, ok := kindsByPrefix[prefix]
	return k, ok
}

// newID returns an AWS-style ID: prefix, dash, 17 hex digits.
func newID(prefix string) string {
	b := make([]byte, 9)
	rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)[:17]
}

// stableID derives an ID from a seed, for the generated default VPC.
func stableID(prefix string, seed ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(seed, "\x00")))
	return prefix + "-" + hex.EncodeToString(sum[:])[:17]
}

// get loads one resource into out. It reports whether it exists, looking at
// the generated default VPC resources when the store has no such record.
func (c *call) get(typ, id string, out object) (bool, error) {
	if id == "" {
		return false, nil
	}
	res, err := c.h.Store.Get(id, service, typ, c.ns)
	if err == nil {
		return true, json.Unmarshal(res.Attributes, out)
	}
	if !isNotFound(err) {
		return false, err
	}
	if d, ok := c.defaults()[typ][id]; ok && !c.deletedDefault(id) {
		b, _ := json.Marshal(d)
		return true, json.Unmarshal(b, out)
	}
	return false, nil
}

// all lists every resource of a type, generated defaults included.
func all[T any, P interface {
	*T
	object
}](c *call, typ string) ([]P, error) {
	objs, err := c.list(typ, func() object { return P(new(T)) })
	out := make([]P, len(objs))
	for i, o := range objs {
		out[i] = o.(P)
	}
	return out, err
}

// list is all without the type parameter, for code that walks every kind.
func (c *call) list(typ string, newObj func() object) ([]object, error) {
	rows, err := c.h.Store.List(service, typ, c.ns)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []object
	for _, row := range rows {
		v := newObj()
		if err := json.Unmarshal(row.Attributes, v); err != nil {
			continue
		}
		seen[row.ID] = true
		out = append(out, v)
	}
	for id, d := range c.defaults()[typ] {
		if seen[id] || c.deletedDefault(id) {
			continue
		}
		b, _ := json.Marshal(d)
		v := newObj()
		json.Unmarshal(b, v)
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].objectID() < out[j].objectID() })
	return out, nil
}

// put creates or replaces a resource.
func (c *call) put(typ string, v object) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	res := &resource.Resource{ID: v.objectID(), Namespace: c.ns, Service: service, Type: typ, Attributes: b}
	if _, err := c.h.Store.Get(res.ID, service, typ, c.ns); err == nil {
		return c.h.Store.Update(res)
	}
	return c.h.Store.Create(res)
}

func (c *call) remove(typ, id string) error {
	if err := c.h.Store.Delete(id, service, typ, c.ns); err != nil {
		return err
	}
	if _, ok := c.defaults()[typ][id]; ok {
		return c.h.Store.Create(&resource.Resource{ID: id, Namespace: c.ns, Service: service, Type: tDeleted, Attributes: []byte("{}")})
	}
	return nil
}

func (c *call) deletedDefault(id string) bool {
	if c.deleted == nil {
		c.deleted = map[string]bool{}
		rows, _ := c.h.Store.List(service, tDeleted, c.ns)
		for _, r := range rows {
			c.deleted[r.ID] = true
		}
	}
	return c.deleted[id]
}

func isNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

// describe applies the usual Describe* semantics: explicit IDs must all
// exist (else notFoundCode), and filters narrow the result.
func describe[T any, P interface {
	*T
	object
}](c *call, typ string, ids []string, notFoundCode string) ([]P, error) {
	items, err := all[T, P](c, typ)
	if err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		byID := map[string]P{}
		for _, it := range items {
			byID[it.objectID()] = it
		}
		var picked []P
		for _, id := range ids {
			it, ok := byID[id]
			if !ok {
				return nil, notFound(notFoundCode, id)
			}
			picked = append(picked, it)
		}
		items = picked
	}
	fs := c.p.filters()
	var out []P
	for _, it := range items {
		ok, err := matches(fs, it)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, it)
		}
	}
	return out, nil
}

// must loads a resource or returns the not-found error.
func must[T any, P interface {
	*T
	object
}](c *call, typ, id, notFoundCode string) (P, error) {
	v := P(new(T))
	ok, err := c.get(typ, id, v)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notFound(notFoundCode, id)
	}
	return v, nil
}
