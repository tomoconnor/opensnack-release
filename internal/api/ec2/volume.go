// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

import (
	"slices"
)

var volumeActions = map[string]action{
	"CreateVolume":    createVolume,
	"DeleteVolume":    deleteVolume,
	"DescribeVolumes": describeVolumes,
	"AttachVolume":    attachVolume,
	"DetachVolume":    detachVolume,
	"ModifyVolume":    modifyVolume,
}

func (v *Volume) objectID() string { return v.VolumeId }
func (v *Volume) tagList() *[]Tag  { return &v.Tags }
func (v *Volume) filterValues() map[string][]string {
	m := map[string][]string{
		"volume-id": {v.VolumeId}, "size": {i2s(v.Size)}, "snapshot-id": nonEmpty(v.SnapshotId),
		"availability-zone": {v.AvailabilityZone}, "status": {v.Status}, "volume-type": {v.VolumeType},
		"encrypted": {b2s(v.Encrypted)}, "create-time": {v.CreateTime}, "multi-attach-enabled": {b2s(v.MultiAttachEnabled)},
		"attachment.instance-id": nil, "attachment.device": nil, "attachment.status": nil,
		"attachment.delete-on-termination": nil, "attachment.attach-time": nil,
	}
	for _, a := range v.Attachments {
		m["attachment.instance-id"] = append(m["attachment.instance-id"], a.InstanceId)
		m["attachment.device"] = append(m["attachment.device"], a.Device)
		m["attachment.status"] = append(m["attachment.status"], a.Status)
		m["attachment.delete-on-termination"] = append(m["attachment.delete-on-termination"], b2s(a.DeleteOnTermination))
		m["attachment.attach-time"] = append(m["attachment.attach-time"], a.AttachTime)
	}
	return m
}

func (c *call) volume(id string) (*Volume, error) {
	return must[Volume](c, tVolume, id, "InvalidVolume.NotFound")
}

func createVolume(c *call) (any, error) {
	z, err := c.pickZone(c.p.get("AvailabilityZone"), c.p.get("AvailabilityZoneId"))
	if err != nil {
		return nil, err
	}
	if c.p.get("AvailabilityZone") == "" && c.p.get("AvailabilityZoneId") == "" {
		return nil, missing("AvailabilityZone")
	}
	size := c.p.integer("Size", 0)
	if size == 0 && c.p.get("SnapshotId") == "" {
		return nil, missing("Size")
	}
	volType := c.p.get("VolumeType")
	if volType == "" {
		volType = "gp2"
	}
	if !slices.Contains([]string{"gp2", "gp3", "io1", "io2", "st1", "sc1", "standard"}, volType) {
		return nil, invalidParam("VolumeType", volType)
	}
	if size < 1 || size > 16384 {
		return nil, errorf("InvalidParameterValue", "Volume of %dGiB is outside the allowed range for %s", size, volType)
	}
	tags, err := c.tagSpec("volume")
	if err != nil {
		return nil, err
	}
	v := newVolume(z, size, volType, c.p.optInt("Iops"), c.p.optInt("Throughput"), c.p.boolean("Encrypted"), c.p.get("KmsKeyId"), c.p.get("SnapshotId"))
	v.MultiAttachEnabled = c.p.boolean("MultiAttachEnabled")
	v.Tags = tags
	if err := c.put(tVolume, v); err != nil {
		return nil, err
	}
	return v, nil
}

func describeVolumes(c *call) (any, error) {
	vols, err := describe[Volume](c, tVolume, c.p.list("VolumeId"), "InvalidVolume.NotFound")
	if err != nil {
		return nil, err
	}
	return struct {
		Vols []*Volume `xml:"volumeSet>item"`
	}{vols}, nil
}

func deleteVolume(c *call) (any, error) {
	v, err := c.volume(c.p.get("VolumeId"))
	if err != nil {
		return nil, err
	}
	if len(v.Attachments) > 0 {
		return nil, errorf("VolumeInUse", "Volume %s is currently attached to %s", v.VolumeId, v.Attachments[0].InstanceId)
	}
	return nil, c.remove(tVolume, v.VolumeId)
}

func attachVolume(c *call) (any, error) {
	v, err := c.volume(c.p.get("VolumeId"))
	if err != nil {
		return nil, err
	}
	inst, err := c.instance(c.p.get("InstanceId"))
	if err != nil {
		return nil, err
	}
	device := c.p.get("Device")
	if device == "" {
		return nil, missing("Device")
	}
	if len(v.Attachments) > 0 && !v.MultiAttachEnabled {
		return nil, errorf("VolumeInUse", "%s is already attached to an instance", v.VolumeId)
	}
	if v.AvailabilityZone != inst.AvailabilityZone {
		return nil, errorf("InvalidVolume.ZoneMismatch", "The volume '%s' is not in the same availability zone as instance '%s'", v.VolumeId, inst.InstanceId)
	}
	if slices.ContainsFunc(inst.Volumes, func(iv instanceVolume) bool { return iv.DeviceName == device }) {
		return nil, errorf("InvalidParameterValue", "Invalid value '%s' for unixDevice. Attachment point %s is already in use", device, device)
	}
	a := VolAttach{VolumeId: v.VolumeId, InstanceId: inst.InstanceId, Device: device, Status: "attached", AttachTime: now()}
	v.Attachments = append(v.Attachments, a)
	v.Status = "in-use"
	inst.Volumes = append(inst.Volumes, instanceVolume{v.VolumeId, device, false, a.AttachTime})
	if err := c.put(tVolume, v); err != nil {
		return nil, err
	}
	if err := c.put(tInstance, inst); err != nil {
		return nil, err
	}
	a.Status = "attaching"
	return a, nil
}

func detachVolume(c *call) (any, error) {
	v, err := c.volume(c.p.get("VolumeId"))
	if err != nil {
		return nil, err
	}
	instID := c.p.get("InstanceId")
	i := slices.IndexFunc(v.Attachments, func(a VolAttach) bool { return instID == "" || a.InstanceId == instID })
	if i < 0 {
		return nil, errorf("IncorrectState", "Volume '%s' is in the 'available' state.", v.VolumeId)
	}
	a := v.Attachments[i]
	if inst, err := c.instance(a.InstanceId); err == nil {
		if inst.RootDeviceName == a.Device && inst.State != stateStopped {
			return nil, errorf("OperationNotPermitted", "Unable to detach root volume '%s' from instance '%s'", v.VolumeId, inst.InstanceId)
		}
		inst.Volumes = slices.DeleteFunc(inst.Volumes, func(iv instanceVolume) bool { return iv.VolumeId == v.VolumeId })
		if err := c.put(tInstance, inst); err != nil {
			return nil, err
		}
	}
	v.Attachments = slices.Delete(v.Attachments, i, i+1)
	if len(v.Attachments) == 0 {
		v.Status = "available"
	}
	if err := c.put(tVolume, v); err != nil {
		return nil, err
	}
	a.Status = "detaching"
	return a, nil
}

func modifyVolume(c *call) (any, error) {
	v, err := c.volume(c.p.get("VolumeId"))
	if err != nil {
		return nil, err
	}
	type mod struct {
		VolumeId          string `xml:"volumeId"`
		ModificationState string `xml:"modificationState"`
		TargetSize        int    `xml:"targetSize"`
		TargetVolumeType  string `xml:"targetVolumeType"`
		OriginalSize      int    `xml:"originalSize"`
		OriginalType      string `xml:"originalVolumeType"`
		Progress          int    `xml:"progress"`
		StartTime         string `xml:"startTime"`
	}
	m := mod{VolumeId: v.VolumeId, ModificationState: "completed", OriginalSize: v.Size, OriginalType: v.VolumeType, Progress: 100, StartTime: now()}
	if s := c.p.integer("Size", 0); s > 0 {
		if s < v.Size {
			return nil, errorf("InvalidParameterValue", "New size cannot be smaller than existing size")
		}
		v.Size = s
	}
	if t := c.p.get("VolumeType"); t != "" {
		v.VolumeType = t
	}
	nv := newVolume(azInfo{Name: v.AvailabilityZone, ID: v.AvailabilityZoneId}, v.Size, v.VolumeType, c.p.optInt("Iops"), c.p.optInt("Throughput"), v.Encrypted, v.KmsKeyId, v.SnapshotId)
	if c.p.optInt("Iops") == nil && v.VolumeType == "gp3" && v.Iops != nil {
		nv.Iops = v.Iops
	}
	if c.p.optInt("Throughput") == nil && v.VolumeType == "gp3" && v.Throughput != nil {
		nv.Throughput = v.Throughput
	}
	v.Iops, v.Throughput = nv.Iops, nv.Throughput
	m.TargetSize, m.TargetVolumeType = v.Size, v.VolumeType
	if err := c.put(tVolume, v); err != nil {
		return nil, err
	}
	return struct {
		M mod `xml:"volumeModification"`
	}{m}, nil
}
