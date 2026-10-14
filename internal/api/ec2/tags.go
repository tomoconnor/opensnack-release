// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

import (
	"slices"
	"sort"
	"strings"
)

var tagActions = map[string]action{
	"CreateTags":   createTags,
	"DeleteTags":   deleteTags,
	"DescribeTags": describeTags,
}

const maxTags = 50

// validateTags checks keys the way EC2 does and returns the tags with later
// duplicates winning.
func validateTags(tags []Tag) ([]Tag, error) {
	var out []Tag
	for _, t := range tags {
		switch {
		case t.Key == "":
			return nil, errorf("InvalidParameterValue", "Tag key cannot be empty")
		case len(t.Key) > 128:
			return nil, errorf("InvalidParameterValue", "Tag key exceeds the maximum length of 128 characters")
		case len(t.Value) > 256:
			return nil, errorf("InvalidParameterValue", "Tag value exceeds the maximum length of 256 characters")
		case strings.HasPrefix(strings.ToLower(t.Key), "aws:"):
			return nil, errorf("InvalidParameterValue", "Tag keys starting with 'aws:' are reserved for internal use")
		}
		out = mergeTags(out, []Tag{t})
	}
	return out, nil
}

// mergeTags sets each of add on base, replacing values of existing keys.
func mergeTags(base, add []Tag) []Tag {
	out := slices.Clone(base)
	for _, t := range add {
		if i := slices.IndexFunc(out, func(o Tag) bool { return o.Key == t.Key }); i >= 0 {
			out[i].Value = t.Value
		} else {
			out = append(out, t)
		}
	}
	return out
}

// tagSpec returns the validated tags for resourceType from TagSpecification.N.
func (c *call) tagSpec(resourceType string) ([]Tag, error) {
	tags, err := validateTags(c.p.tagSpecs(resourceType))
	if err == nil && len(tags) > maxTags {
		err = errorf("TagLimitExceeded", "The maximum number of tags per resource is %d", maxTags)
	}
	return tags, err
}

// taggable finds a resource by ID for CreateTags and DeleteTags, and returns
// a function that saves it.
func (c *call) taggable(id string) (*[]Tag, func() error, error) {
	if strings.HasPrefix(id, "sgr-") {
		sg, i, err := c.findRule(id)
		if err != nil {
			return nil, nil, err
		}
		return &sg.Rules[i].Tags, func() error { return c.put(tSG, sg) }, nil
	}
	k, ok := kindOf(id)
	if !ok {
		return nil, nil, errorf("InvalidID", "The ID '%s' is not valid", id)
	}
	o := k.new()
	found, err := c.get(k.typ, id, o)
	if err != nil {
		return nil, nil, err
	}
	if !found {
		return nil, nil, notFound(notFoundCodeFor(k.typ), id)
	}
	return o.tagList(), func() error { return c.put(k.typ, o) }, nil
}

func notFoundCodeFor(typ string) string {
	switch typ {
	case tVpc:
		return "InvalidVpcID.NotFound"
	case tSubnet:
		return "InvalidSubnetID.NotFound"
	case tRTB:
		return "InvalidRouteTableID.NotFound"
	case tIGW:
		return "InvalidInternetGatewayID.NotFound"
	case tNAT:
		return "NatGatewayNotFound"
	case tEIP:
		return "InvalidAllocationID.NotFound"
	case tSG:
		return "InvalidGroup.NotFound"
	case tNACL:
		return "InvalidNetworkAclID.NotFound"
	case tENI:
		return "InvalidNetworkInterfaceID.NotFound"
	case tInstance:
		return "InvalidInstanceID.NotFound"
	case tVolume:
		return "InvalidVolume.NotFound"
	case tKeyPair:
		return "InvalidKeyPair.NotFound"
	case tEndpoint:
		return "InvalidVpcEndpointId.NotFound"
	}
	return "InvalidID"
}

func createTags(c *call) (any, error) {
	ids := c.p.list("ResourceId")
	if len(ids) == 0 {
		return nil, missing("ResourceId")
	}
	tags, err := validateTags(c.p.tags("Tag"))
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		list, save, err := c.taggable(id)
		if err != nil {
			return nil, err
		}
		merged := mergeTags(*list, tags)
		if len(merged) > maxTags {
			return nil, errorf("TagLimitExceeded", "The maximum number of tags per resource is %d", maxTags)
		}
		*list = merged
		if err := save(); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func deleteTags(c *call) (any, error) {
	ids := c.p.list("ResourceId")
	if len(ids) == 0 {
		return nil, missing("ResourceId")
	}
	spec := c.p.sub("Tag")
	for _, id := range ids {
		list, save, err := c.taggable(id)
		if err != nil {
			return nil, err
		}
		if len(spec) == 0 {
			*list = nil
		} else {
			*list = slices.DeleteFunc(*list, func(t Tag) bool {
				for _, s := range spec {
					if s.get("Key") == t.Key && (!s.has("Value") || s.get("Value") == t.Value) {
						return true
					}
				}
				return false
			})
		}
		if err := save(); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

type TagDescription struct {
	ResourceId   string `xml:"resourceId"`
	ResourceType string `xml:"resourceType"`
	Key          string `xml:"key"`
	Value        string `xml:"value"`
}

// allTags lists every tag in the namespace and region.
func (c *call) allTags() ([]TagDescription, error) {
	var out []TagDescription
	for _, k := range kindsByPrefix {
		objs, err := c.list(k.typ, k.new)
		if err != nil {
			return nil, err
		}
		for _, o := range objs {
			for _, t := range *o.tagList() {
				out = append(out, TagDescription{o.objectID(), k.resType, t.Key, t.Value})
			}
			if sg, ok := o.(*SecurityGroup); ok {
				for _, r := range sg.Rules {
					for _, t := range r.Tags {
						out = append(out, TagDescription{r.SecurityGroupRuleId, "security-group-rule", t.Key, t.Value})
					}
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ResourceId != out[j].ResourceId {
			return out[i].ResourceId < out[j].ResourceId
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

func describeTags(c *call) (any, error) {
	tags, err := c.allTags()
	if err != nil {
		return nil, err
	}
	fs := c.p.filters()
	var out []TagDescription
	for _, t := range tags {
		vals := map[string][]string{
			"resource-id": {t.ResourceId}, "resource-type": {t.ResourceType}, "key": {t.Key}, "value": {t.Value},
		}
		ok := true
		for _, f := range fs {
			var have []string
			if k, isTag := strings.CutPrefix(f.name, "tag:"); isTag {
				if k == t.Key {
					have = []string{t.Value}
				}
			} else if have, ok = vals[f.name]; !ok {
				return nil, errorf("InvalidParameterValue", "The filter '%s' is invalid", f.name)
			}
			if !anyMatch(f.values, have) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, t)
		}
	}
	return struct {
		Tags []TagDescription `xml:"tagSet>item"`
	}{out}, nil
}
