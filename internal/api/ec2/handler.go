// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ec2

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"

	"github.com/google/uuid"
)

const (
	APIVersion      = "2016-11-15"
	ec2Region       = "us-east-1"
	ec2Account      = "000000000000"
	defaultVpcId    = "vpc-00000000"
	defaultSubnetId = "subnet-00000000"
)

// TODO: Implement ModifyInstanceAttribute call

type Handler struct {
	Store resource.Store
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store}
}

// ensureInstanceFields ensures all required fields are populated for Terraform compatibility
// This is critical - Terraform blindly indexes [0] on these slices without checking length
func ensureInstanceFields(instance Instance, privateIp string, privateDns string, now time.Time) Instance {
	// Ensure root device fields (required by Terraform)
	if instance.RootDeviceType == "" {
		instance.RootDeviceType = "ebs"
	}
	if instance.RootDeviceName == "" {
		instance.RootDeviceName = "/dev/xvda"
	}

	// Ensure private IP/DNS for network interfaces
	if privateIp == "" {
		privateIp = instance.PrivateIpAddress
		if privateIp == "" {
			privateIp = "10.0.0.10" // Default fallback
		}
	}
	if privateDns == "" {
		privateDns = instance.PrivateDnsName
		if privateDns == "" {
			privateDns = "ip-10-0-0-10.ec2.internal" // Default fallback
		}
	}

	// Ensure block device mappings - ALWAYS must have at least one item
	// Check both nil slice and empty slice cases
	if instance.BlockDeviceMappings.Items == nil || len(instance.BlockDeviceMappings.Items) == 0 {
		instance.BlockDeviceMappings = BlockDeviceMappingSet{
			Items: []BlockDeviceMapping{
				{
					DeviceName: "/dev/xvda",
					Ebs: EbsBlockDevice{
						VolumeId:            "vol-00000000",
						Status:              "attached",
						AttachTime:          now.Format(time.RFC3339),
						DeleteOnTermination: true,
					},
				},
			},
		}
	}

	// Ensure VPC fields
	if instance.VpcId == "" {
		instance.VpcId = defaultVpcId
	}
	if instance.SubnetId == "" {
		instance.SubnetId = defaultSubnetId
	}

	// Ensure network interfaces - ALWAYS must have at least one item
	if instance.NetworkInterfaces.Items == nil || len(instance.NetworkInterfaces.Items) == 0 {
		// Ensure private IP matches instance-level IP
		if privateIp == "" {
			privateIp = instance.PrivateIpAddress
			if privateIp == "" {
				privateIp = "10.0.0.10"
			}
		}
		instance.NetworkInterfaces = NetworkInterfaceSet{
			Items: []NetworkInterface{
				{
					NetworkInterfaceId: "eni-00000000",
					SubnetId:           instance.SubnetId,
					VpcId:              instance.VpcId,
					OwnerId:            ec2Account,
					Status:             "in-use",
					MacAddress:         "02:00:00:00:00:00",
					PrivateIpAddress:   privateIp,
					SourceDestCheck:    true,
					PrivateIpAddressesSet: PrivateIpAddressesSet{
						Items: []PrivateIpAddressInfo{
							{
								PrivateIpAddress: privateIp,
								Primary:          true,
							},
						},
					},
					Groups: GroupSet{
						Items: []SecurityGroup{
							{
								GroupId:   "sg-00000000",
								GroupName: "default",
							},
						},
					},
					Attachment: NetworkAttachment{
						AttachmentId:        "eni-attach-00000000",
						DeviceIndex:         0,
						Status:              "attached",
						AttachTime:          now.Format(time.RFC3339),
						DeleteOnTermination: true,
					},
				},
			},
		}
	}

	// Normalize SourceDestCheck for existing network interfaces
	// AWS default is true, so we normalize false (from old code) to true
	if len(instance.NetworkInterfaces.Items) > 0 {
		for i := range instance.NetworkInterfaces.Items {
			// Normalize to true (AWS default) - old instances had false stored
			instance.NetworkInterfaces.Items[i].SourceDestCheck = true
		}
	}

	// Ensure security groups - ALWAYS must have at least one item
	if instance.SecurityGroups.Items == nil || len(instance.SecurityGroups.Items) == 0 {
		instance.SecurityGroups = SecurityGroupSet{
			Items: []SecurityGroup{
				{
					GroupId:   "sg-00000000",
					GroupName: "default",
				},
			},
		}
	}

	// Ensure metadata options (required by Terraform provider v5.x+)
	if instance.MetadataOptions.HttpTokens == "" {
		instance.MetadataOptions = MetadataOptions{
			HttpTokens:              "optional",
			HttpPutResponseHopLimit: 1,
			HttpEndpoint:            "enabled",
		}
	}

	// Ensure private IP/DNS are set on the instance itself
	// Must match the primary IP in networkInterfaceSet
	if instance.PrivateIpAddress == "" {
		instance.PrivateIpAddress = privateIp
	}
	if instance.PrivateDnsName == "" {
		if instance.PrivateIpAddress != "" {
			instance.PrivateDnsName = "ip-" + strings.ReplaceAll(instance.PrivateIpAddress, ".", "-") + ".ec2.internal"
		} else {
			instance.PrivateDnsName = privateDns
		}
	}

	// Ensure instance-level VPC fields match network interface
	if instance.VpcId == "" {
		if len(instance.NetworkInterfaces.Items) > 0 {
			instance.VpcId = instance.NetworkInterfaces.Items[0].VpcId
		} else {
			instance.VpcId = defaultVpcId
		}
	}
	if instance.SubnetId == "" {
		if len(instance.NetworkInterfaces.Items) > 0 {
			instance.SubnetId = instance.NetworkInterfaces.Items[0].SubnetId
		} else {
			instance.SubnetId = defaultSubnetId
		}
	}

	return instance
}

// Dispatch handles EC2 Query API requests
func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	action := r.FormValue("Action")
	if action == "" {
		action = r.URL.Query().Get("Action")
	}

	switch action {
	case "RunInstances":
		h.RunInstances(w, r)
	case "DescribeInstances":
		h.DescribeInstances(w, r)
	case "TerminateInstances":
		h.TerminateInstances(w, r)
	case "CreateVolume":
		h.CreateVolume(w, r)
	case "DescribeVolumes":
		h.DescribeVolumes(w, r)
	case "DeleteVolume":
		h.DeleteVolume(w, r)
	case "AttachVolume":
		h.AttachVolume(w, r)
	case "DetachVolume":
		h.DetachVolume(w, r)
	case "DescribeInstanceTypes":
		h.DescribeInstanceTypes(w, r)
	case "DescribeTags":
		h.DescribeTags(w, r)
	case "DescribeVpcs":
		h.DescribeVpcs(w, r)
	case "DescribeInstanceAttribute":
		h.DescribeInstanceAttribute(w, r)
	case "ModifyInstanceAttribute":
		h.ModifyInstanceAttribute(w, r)
	default:
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"InvalidAction",
			"Unknown EC2 Action",
			action,
		)
	}
}

// RunInstances creates new EC2 instances
func (h *Handler) RunInstances(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)

	imageId := r.FormValue("ImageId")
	if imageId == "" {
		imageId = r.URL.Query().Get("ImageId")
	}
	if imageId == "" {
		awsresponses.WriteErrorXML(w, http.StatusBadRequest, "MissingParameter", "ImageId is required", "")
		return
	}

	instanceType := r.FormValue("InstanceType")
	if instanceType == "" {
		instanceType = r.URL.Query().Get("InstanceType")
	}
	if instanceType == "" {
		instanceType = "t3.micro"
	}

	// Hard rule: Terraform expects exactly 1 instance unless explicitly asked otherwise
	// Ignore MaxCount/MinCount complexity for now - always return 1 instance
	count := 1

	availabilityZone := r.FormValue("Placement.AvailabilityZone")
	if availabilityZone == "" {
		availabilityZone = r.URL.Query().Get("Placement.AvailabilityZone")
	}
	if availabilityZone == "" {
		availabilityZone = "us-east-1a"
	}

	now := time.Now().UTC()
	reservationId := "r-" + strings.ReplaceAll(uuid.New().String(), "-", "")[:17]

	instances := make([]Instance, 0, count)

	for i := 0; i < count; i++ {
		instanceId := "i-" + strings.ReplaceAll(uuid.New().String(), "-", "")[:17]

		// Generate stable private IP based on instance index for consistency
		ipOctet := 10 + (i % 245) // Use 10.0.0.10-10.0.0.255 range
		privateIp := "10.0.0." + strconv.Itoa(ipOctet)
		privateDns := "ip-" + strings.ReplaceAll(privateIp, ".", "-") + ".ec2.internal"

		instance := Instance{
			InstanceId:   instanceId,
			ImageId:      imageId,
			InstanceType: instanceType,
			InstanceState: InstanceState{
				Code: 16, // running
				Name: "running",
			},
			PrivateIpAddress: privateIp,
			PrivateDnsName:   privateDns,
			SubnetId:         defaultSubnetId,
			VpcId:            defaultVpcId,
			RootDeviceType:   "ebs",
			RootDeviceName:   "/dev/xvda",
			LaunchTime:       now.Format(time.RFC3339),
			Placement: Placement{
				AvailabilityZone: availabilityZone,
			},
			BlockDeviceMappings: BlockDeviceMappingSet{
				Items: []BlockDeviceMapping{
					{
						DeviceName: "/dev/xvda",
						Ebs: EbsBlockDevice{
							VolumeId:            "vol-00000000",
							Status:              "attached",
							AttachTime:          now.Format(time.RFC3339),
							DeleteOnTermination: true,
						},
					},
				},
			},
			NetworkInterfaces: NetworkInterfaceSet{
				Items: []NetworkInterface{
					{
						NetworkInterfaceId: "eni-00000000",
						SubnetId:           defaultSubnetId,
						VpcId:              defaultVpcId,
						OwnerId:            ec2Account,
						Status:             "in-use",
						MacAddress:         "02:00:00:00:00:00",
						PrivateIpAddress:   privateIp,
						SourceDestCheck:    true,
						PrivateIpAddressesSet: PrivateIpAddressesSet{
							Items: []PrivateIpAddressInfo{
								{
									PrivateIpAddress: privateIp,
									Primary:          true,
								},
							},
						},
						Groups: GroupSet{
							Items: []SecurityGroup{
								{
									GroupId:   "sg-00000000",
									GroupName: "default",
								},
							},
						},
						Attachment: NetworkAttachment{
							AttachmentId:        "eni-attach-00000000",
							DeviceIndex:         0,
							Status:              "attached",
							AttachTime:          now.Format(time.RFC3339),
							DeleteOnTermination: true,
						},
					},
				},
			},
			SecurityGroups: SecurityGroupSet{
				Items: []SecurityGroup{
					{
						GroupId:   "sg-00000000",
						GroupName: "default",
					},
				},
			},
			MetadataOptions: MetadataOptions{
				HttpTokens:              "optional",
				HttpPutResponseHopLimit: 1,
				HttpEndpoint:            "enabled",
			},
		}

		instances = append(instances, instance)

		// Store instance
		entry := map[string]any{
			"instance":       instance,
			"reservation_id": reservationId,
			"created_at":     now,
		}

		buf, _ := json.Marshal(entry)
		res := &resource.Resource{
			ID:         instanceId,
			Namespace:  ns,
			Service:    "ec2",
			Type:       "instance",
			Attributes: buf,
		}

		h.Store.Create(res)
	}

	resp := RunInstancesResponse{
		RequestId:     awsresponses.NextRequestID(),
		ReservationId: reservationId,
		OwnerId:       ec2Account,
		GroupSet: GroupSet{
			Items: []SecurityGroup{
				{
					GroupId:   "sg-00000000",
					GroupName: "default",
				},
			},
		},
		Instances: instances,
	}

	awsresponses.WriteXML(w, resp)
}

// DescribeInstances describes EC2 instances
func (h *Handler) DescribeInstances(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)

	instanceIds := r.Form["InstanceId"]
	if len(instanceIds) == 0 {
		instanceIds = r.URL.Query()["InstanceId"]
	}

	type instanceWithReservation struct {
		instance      Instance
		reservationId string
	}

	var instancesWithRes []instanceWithReservation

	if len(instanceIds) > 0 {
		// Describe specific instances
		for _, instanceId := range instanceIds {
			res, err := h.Store.Get(instanceId, "ec2", "instance", ns)
			if err != nil {
				continue
			}

			var entry map[string]any
			if err := json.Unmarshal(res.Attributes, &entry); err != nil {
				continue
			}

			instanceBytes, _ := json.Marshal(entry["instance"])
			var instance Instance
			json.Unmarshal(instanceBytes, &instance)

			// Ensure all required fields are populated
			instance = ensureInstanceFields(instance, instance.PrivateIpAddress, instance.PrivateDnsName, time.Now().UTC())

			// Persist normalized values back to storage to prevent drift
			entry["instance"] = instance
			buf, _ := json.Marshal(entry)
			res.Attributes = buf
			h.Store.Update(res)

			reservationId, _ := entry["reservation_id"].(string)
			if reservationId == "" {
				// Fallback: generate one (shouldn't happen if RunInstances stored it)
				reservationId = "r-" + strings.ReplaceAll(uuid.New().String(), "-", "")[:17]
			}

			instancesWithRes = append(instancesWithRes, instanceWithReservation{
				instance:      instance,
				reservationId: reservationId,
			})
		}
	} else {
		// List all instances
		allInstances, err := h.Store.List("ec2", "instance", ns)
		if err == nil {
			for _, instRes := range allInstances {
				var entry map[string]any
				if err := json.Unmarshal(instRes.Attributes, &entry); err != nil {
					continue
				}

				instanceBytes, _ := json.Marshal(entry["instance"])
				var instance Instance
				json.Unmarshal(instanceBytes, &instance)

				// Ensure all required fields are populated
				instance = ensureInstanceFields(instance, instance.PrivateIpAddress, instance.PrivateDnsName, time.Now().UTC())

				// Persist normalized values back to storage to prevent drift
				entry["instance"] = instance
				buf, _ := json.Marshal(entry)
				instRes.Attributes = buf
				h.Store.Update(&instRes)

				reservationId, _ := entry["reservation_id"].(string)
				if reservationId == "" {
					// Fallback: generate one (shouldn't happen if RunInstances stored it)
					reservationId = "r-" + strings.ReplaceAll(uuid.New().String(), "-", "")[:17]
				}

				instancesWithRes = append(instancesWithRes, instanceWithReservation{
					instance:      instance,
					reservationId: reservationId,
				})
			}
		}
	}

	// Group instances by reservation - reuse stored reservation_id
	reservationMap := make(map[string][]Instance)

	for _, instWithRes := range instancesWithRes {
		reservationMap[instWithRes.reservationId] = append(reservationMap[instWithRes.reservationId], instWithRes.instance)
	}

	// Convert map to reservations list
	reservations := make([]Reservation, 0, len(reservationMap))
	for reservationId, instList := range reservationMap {
		// Get ownerId and groupSet from first instance (they should be consistent)
		var ownerId string = ec2Account
		var groupSet GroupSet
		if len(instList) > 0 {
			// Use security groups from the first instance
			if len(instList[0].SecurityGroups.Items) > 0 {
				groupSet = GroupSet{
					Items: instList[0].SecurityGroups.Items,
				}
			} else {
				// Fallback to default security group
				groupSet = GroupSet{
					Items: []SecurityGroup{
						{
							GroupId:   "sg-00000000",
							GroupName: "default",
						},
					},
				}
			}
		} else {
			// Default groupSet if no instances
			groupSet = GroupSet{
				Items: []SecurityGroup{
					{
						GroupId:   "sg-00000000",
						GroupName: "default",
					},
				},
			}
		}
		reservations = append(reservations, Reservation{
			ReservationId: reservationId,
			OwnerId:       ownerId,
			GroupSet:      groupSet,
			Instances:     instList,
		})
	}

	resp := DescribeInstancesResponse{
		RequestId:    awsresponses.NextRequestID(),
		Reservations: reservations,
	}

	awsresponses.WriteXML(w, resp)
}

// TerminateInstances terminates EC2 instances
func (h *Handler) TerminateInstances(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)

	instanceIds := r.Form["InstanceId"]
	if len(instanceIds) == 0 {
		instanceIds = r.URL.Query()["InstanceId"]
	}

	changes := make([]InstanceStateChange, 0, len(instanceIds))

	for _, instanceId := range instanceIds {
		res, err := h.Store.Get(instanceId, "ec2", "instance", ns)
		if err != nil {
			continue
		}

		var entry map[string]any
		json.Unmarshal(res.Attributes, &entry)

		instanceBytes, _ := json.Marshal(entry["instance"])
		var instance Instance
		json.Unmarshal(instanceBytes, &instance)

		// Ensure all required fields are populated before updating
		instance = ensureInstanceFields(instance, instance.PrivateIpAddress, instance.PrivateDnsName, time.Now().UTC())

		previousState := instance.InstanceState

		// Update instance state to terminated
		instance.InstanceState = InstanceState{
			Code: 48, // terminated
			Name: "terminated",
		}

		entry["instance"] = instance
		buf, _ := json.Marshal(entry)
		res.Attributes = buf
		h.Store.Update(res)

		changes = append(changes, InstanceStateChange{
			InstanceId:    instanceId,
			CurrentState:  instance.InstanceState,
			PreviousState: previousState,
		})
	}

	resp := TerminateInstancesResponse{
		TerminateInstancesResult: TerminateInstancesResult{
			Instances: changes,
		},
		ResponseMetadata: ResponseMetadata{
			RequestId: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, resp)
}

// CreateVolume creates a new EBS volume
func (h *Handler) CreateVolume(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)

	size := 1
	if s := r.FormValue("Size"); s != "" {
		if parsed, err := strconv.Atoi(s); err == nil {
			size = parsed
		}
	} else if s := r.URL.Query().Get("Size"); s != "" {
		if parsed, err := strconv.Atoi(s); err == nil {
			size = parsed
		}
	}

	volumeType := r.FormValue("VolumeType")
	if volumeType == "" {
		volumeType = r.URL.Query().Get("VolumeType")
	}
	if volumeType == "" {
		volumeType = "gp3"
	}

	availabilityZone := r.FormValue("AvailabilityZone")
	if availabilityZone == "" {
		availabilityZone = r.URL.Query().Get("AvailabilityZone")
	}
	if availabilityZone == "" {
		availabilityZone = "us-east-1a"
	}

	volumeId := "vol-" + strings.ReplaceAll(uuid.New().String(), "-", "")[:17]
	now := time.Now().UTC()

	// Store volume (use Volume struct for storage)
	volume := Volume{
		VolumeId:         volumeId,
		Size:             size,
		VolumeType:       volumeType,
		State:            "available",
		AvailabilityZone: availabilityZone,
		CreateTime:       now.Format(time.RFC3339),
	}

	entry := map[string]any{
		"volume":     volume,
		"created_at": now,
	}

	buf, _ := json.Marshal(entry)
	res := &resource.Resource{
		ID:         volumeId,
		Namespace:  ns,
		Service:    "ec2",
		Type:       "volume",
		Attributes: buf,
	}

	if err := h.Store.Create(res); err != nil {
		awsresponses.WriteErrorXML(w, http.StatusInternalServerError, "InternalFailure", "Failed to create volume", volumeId)
		return
	}

	resp := CreateVolumeResponse{
		VolumeId:         volumeId,
		Size:             size,
		VolumeType:       volumeType,
		State:            "available",
		AvailabilityZone: availabilityZone,
		CreateTime:       now.Format(time.RFC3339),
		ResponseMetadata: ResponseMetadata{
			RequestId: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, resp)
}

// DescribeVolumes describes EBS volumes
func (h *Handler) DescribeVolumes(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)

	volumeIds := r.Form["VolumeId"]
	if len(volumeIds) == 0 {
		volumeIds = r.URL.Query()["VolumeId"]
	}

	// If still empty, manually check for numbered parameters (VolumeId.1, VolumeId.2, etc.)
	if len(volumeIds) == 0 {
		for i := 1; i <= 100; i++ {
			key := "VolumeId." + strconv.Itoa(i)
			value := r.FormValue(key)
			if value == "" {
				value = r.URL.Query().Get(key)
			}
			if value != "" {
				volumeIds = append(volumeIds, value)
			} else {
				// Stop at first missing number
				break
			}
		}
	}

	// Filter out empty volume IDs
	filteredVolumeIds := make([]string, 0, len(volumeIds))
	for _, vid := range volumeIds {
		if vid != "" {
			filteredVolumeIds = append(filteredVolumeIds, vid)
		}
	}

	var volumes []Volume

	if len(filteredVolumeIds) > 0 {
		// Describe specific volumes
		for _, volumeId := range filteredVolumeIds {
			res, err := h.Store.Get(volumeId, "ec2", "volume", ns)
			if err != nil {
				continue
			}

			var entry map[string]any
			if err := json.Unmarshal(res.Attributes, &entry); err != nil {
				continue
			}

			volumeBytes, _ := json.Marshal(entry["volume"])
			var volume Volume
			json.Unmarshal(volumeBytes, &volume)

			// CRITICAL: Ensure Attachments is nil (not empty slice) when no attachments
			// This makes XML omit the element entirely due to omitempty tag
			volume.Attachments = nil

			// Load attachments - only add if there are actual attachments
			attachments, _ := h.Store.List("ec2", "volume_attachment", ns)
			for _, attRes := range attachments {
				var attEntry map[string]any
				if err := json.Unmarshal(attRes.Attributes, &attEntry); err != nil {
					continue
				}

				if attEntry["volume_id"].(string) == volumeId {
					attBytes, _ := json.Marshal(attEntry["attachment"])
					var attachment VolumeAttachment
					json.Unmarshal(attBytes, &attachment)

					// Only include attached attachments (not detached)
					if attachment.State == "attached" {
						if volume.Attachments == nil {
							volume.Attachments = make([]VolumeAttachment, 0)
						}
						volume.Attachments = append(volume.Attachments, attachment)
					}
				}
			}

			volumes = append(volumes, volume)
		}
	} else {
		// List all volumes
		allVolumes, err := h.Store.List("ec2", "volume", ns)
		if err == nil {
			for _, volRes := range allVolumes {
				var entry map[string]any
				if err := json.Unmarshal(volRes.Attributes, &entry); err != nil {
					continue
				}

				volumeBytes, _ := json.Marshal(entry["volume"])
				var volume Volume
				json.Unmarshal(volumeBytes, &volume)

				// CRITICAL: Ensure Attachments is nil (not empty slice) when no attachments
				volume.Attachments = nil

				// Load attachments - only add if there are actual attachments
				attachments, _ := h.Store.List("ec2", "volume_attachment", ns)
				for _, attRes := range attachments {
					var attEntry map[string]any
					if err := json.Unmarshal(attRes.Attributes, &attEntry); err != nil {
						continue
					}

					if attEntry["volume_id"].(string) == volume.VolumeId {
						attBytes, _ := json.Marshal(attEntry["attachment"])
						var attachment VolumeAttachment
						json.Unmarshal(attBytes, &attachment)

						// Only include attached attachments (not detached)
						if attachment.State == "attached" {
							if volume.Attachments == nil {
								volume.Attachments = make([]VolumeAttachment, 0)
							}
							volume.Attachments = append(volume.Attachments, attachment)
						}
					}
				}

				volumes = append(volumes, volume)
			}
		}
	}

	resp := DescribeVolumesResponse{
		RequestId: awsresponses.NextRequestID(),
		Volumes:   volumes,
	}

	awsresponses.WriteXML(w, resp)
}

// DeleteVolume deletes an EBS volume
func (h *Handler) DeleteVolume(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)

	volumeId := r.FormValue("VolumeId")
	if volumeId == "" {
		volumeId = r.URL.Query().Get("VolumeId")
	}

	if volumeId == "" {
		awsresponses.WriteErrorXML(w, http.StatusBadRequest, "MissingParameter", "VolumeId is required", "")
		return
	}

	// Check if volume is attached
	attachments, _ := h.Store.List("ec2", "volume_attachment", ns)
	for _, attRes := range attachments {
		var attEntry map[string]any
		if err := json.Unmarshal(attRes.Attributes, &attEntry); err != nil {
			continue
		}

		if attEntry["volume_id"].(string) == volumeId {
			attBytes, _ := json.Marshal(attEntry["attachment"])
			var attachment VolumeAttachment
			json.Unmarshal(attBytes, &attachment)
			if attachment.State == "attached" {
				awsresponses.WriteErrorXML(w, http.StatusBadRequest, "VolumeInUse", "Volume is attached", volumeId)
				return
			}
		}
	}

	h.Store.Delete(volumeId, "ec2", "volume", ns)

	resp := DeleteVolumeResponse{
		ResponseMetadata: ResponseMetadata{
			RequestId: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, resp)
}

// AttachVolume attaches an EBS volume to an instance
func (h *Handler) AttachVolume(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)

	volumeId := r.FormValue("VolumeId")
	if volumeId == "" {
		volumeId = r.URL.Query().Get("VolumeId")
	}

	instanceId := r.FormValue("InstanceId")
	if instanceId == "" {
		instanceId = r.URL.Query().Get("InstanceId")
	}

	device := r.FormValue("Device")
	if device == "" {
		device = r.URL.Query().Get("Device")
	}

	if volumeId == "" || instanceId == "" || device == "" {
		awsresponses.WriteErrorXML(w, http.StatusBadRequest, "MissingParameter", "VolumeId, InstanceId, and Device are required", "")
		return
	}

	// Verify volume exists
	_, err := h.Store.Get(volumeId, "ec2", "volume", ns)
	if err != nil {
		awsresponses.WriteErrorXML(w, http.StatusNotFound, "InvalidVolume.NotFound", "Volume not found", volumeId)
		return
	}

	// Verify instance exists
	_, err = h.Store.Get(instanceId, "ec2", "instance", ns)
	if err != nil {
		awsresponses.WriteErrorXML(w, http.StatusNotFound, "InvalidInstanceID.NotFound", "Instance not found", instanceId)
		return
	}

	now := time.Now().UTC()
	attachmentId := "vol-attach-" + strings.ReplaceAll(uuid.New().String(), "-", "")[:17]

	attachment := VolumeAttachment{
		VolumeId:   volumeId,
		InstanceId: instanceId,
		Device:     device,
		State:      "attached",
		AttachTime: now.Format(time.RFC3339),
	}

	// Store attachment
	entry := map[string]any{
		"volume_id":   volumeId,
		"instance_id": instanceId,
		"attachment":  attachment,
		"created_at":  now,
	}

	buf, _ := json.Marshal(entry)
	res := &resource.Resource{
		ID:         attachmentId,
		Namespace:  ns,
		Service:    "ec2",
		Type:       "volume_attachment",
		Attributes: buf,
	}

	if err := h.Store.Create(res); err != nil {
		awsresponses.WriteErrorXML(w, http.StatusInternalServerError, "InternalFailure", "Failed to attach volume", volumeId)
		return
	}

	resp := AttachVolumeResponse{
		AttachVolumeResult: AttachVolumeResult{
			Attachment: attachment,
		},
		ResponseMetadata: ResponseMetadata{
			RequestId: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, resp)
}

// DetachVolume detaches an EBS volume from an instance
func (h *Handler) DetachVolume(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)

	volumeId := r.FormValue("VolumeId")
	if volumeId == "" {
		volumeId = r.URL.Query().Get("VolumeId")
	}

	instanceId := r.FormValue("InstanceId")
	if instanceId == "" {
		instanceId = r.URL.Query().Get("InstanceId")
	}

	device := r.FormValue("Device")
	if device == "" {
		device = r.URL.Query().Get("Device")
	}

	if volumeId == "" {
		awsresponses.WriteErrorXML(w, http.StatusBadRequest, "MissingParameter", "VolumeId is required", "")
		return
	}

	// Find attachment
	attachments, _ := h.Store.List("ec2", "volume_attachment", ns)
	var attachmentRes *resource.Resource
	var attachment VolumeAttachment

	for _, attRes := range attachments {
		var attEntry map[string]any
		if err := json.Unmarshal(attRes.Attributes, &attEntry); err != nil {
			continue
		}

		if attEntry["volume_id"].(string) == volumeId {
			if instanceId != "" && attEntry["instance_id"].(string) != instanceId {
				continue
			}
			if device != "" {
				attBytes, _ := json.Marshal(attEntry["attachment"])
				var att VolumeAttachment
				json.Unmarshal(attBytes, &att)
				if att.Device != device {
					continue
				}
			}

			attachmentRes = &attRes
			attBytes, _ := json.Marshal(attEntry["attachment"])
			json.Unmarshal(attBytes, &attachment)
			break
		}
	}

	if attachmentRes == nil {
		awsresponses.WriteErrorXML(w, http.StatusNotFound, "InvalidAttachment.NotFound", "Attachment not found", volumeId)
		return
	}

	// Update attachment state
	attachment.State = "detached"

	var attEntry map[string]any
	json.Unmarshal(attachmentRes.Attributes, &attEntry)
	attEntry["attachment"] = attachment
	buf, _ := json.Marshal(attEntry)
	attachmentRes.Attributes = buf
	h.Store.Update(attachmentRes)

	resp := DetachVolumeResponse{
		DetachVolumeResult: DetachVolumeResult{
			Attachment: attachment,
		},
		ResponseMetadata: ResponseMetadata{
			RequestId: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, resp)
}

// DescribeInstanceTypes describes EC2 instance types
func (h *Handler) DescribeInstanceTypes(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	// Get requested instance types from form (handles InstanceType.1, InstanceType.2, etc.)
	requestedTypes := r.Form["InstanceType"]
	if len(requestedTypes) == 0 {
		// Also check URL query parameters
		requestedTypes = r.URL.Query()["InstanceType"]
	}

	// If still empty, manually check for numbered parameters (InstanceType.1, InstanceType.2, etc.)
	if len(requestedTypes) == 0 {
		for i := 1; i <= 100; i++ {
			key := "InstanceType." + strconv.Itoa(i)
			value := r.FormValue(key)
			if value == "" {
				value = r.URL.Query().Get(key)
			}
			if value != "" {
				requestedTypes = append(requestedTypes, value)
			} else {
				// Stop at first missing number
				break
			}
		}
	}

	// Filter out empty strings
	filteredTypes := make([]string, 0, len(requestedTypes))
	for _, t := range requestedTypes {
		if t != "" {
			filteredTypes = append(filteredTypes, t)
		}
	}
	requestedTypes = filteredTypes

	// Instance type specifications (minimal info for Terraform validation)
	instanceTypeSpecs := map[string]struct {
		vcpus     int
		memoryMiB int
	}{
		"t3.micro":    {vcpus: 2, memoryMiB: 1024},
		"t3.small":    {vcpus: 2, memoryMiB: 2048},
		"t3.medium":   {vcpus: 2, memoryMiB: 4096},
		"t3.large":    {vcpus: 2, memoryMiB: 8192},
		"t3.xlarge":   {vcpus: 4, memoryMiB: 16384},
		"t3.2xlarge":  {vcpus: 8, memoryMiB: 32768},
		"t3a.micro":   {vcpus: 2, memoryMiB: 1024},
		"t3a.small":   {vcpus: 2, memoryMiB: 2048},
		"t3a.medium":  {vcpus: 2, memoryMiB: 4096},
		"t3a.large":   {vcpus: 2, memoryMiB: 8192},
		"t3a.xlarge":  {vcpus: 4, memoryMiB: 16384},
		"t3a.2xlarge": {vcpus: 8, memoryMiB: 32768},
		"m5.large":    {vcpus: 2, memoryMiB: 8192},
		"m5.xlarge":   {vcpus: 4, memoryMiB: 16384},
		"m5.2xlarge":  {vcpus: 8, memoryMiB: 32768},
		"m5.4xlarge":  {vcpus: 16, memoryMiB: 65536},
		"c5.large":    {vcpus: 2, memoryMiB: 4096},
		"c5.xlarge":   {vcpus: 4, memoryMiB: 8192},
		"c5.2xlarge":  {vcpus: 8, memoryMiB: 16384},
		"c5.4xlarge":  {vcpus: 16, memoryMiB: 32768},
	}

	instanceTypes := make([]InstanceTypeInfo, 0)

	// Only return the specifically requested instance types
	// If none requested, return empty (Terraform always requests specific types)
	for _, instanceType := range requestedTypes {
		if spec, ok := instanceTypeSpecs[instanceType]; ok {
			instanceTypes = append(instanceTypes, InstanceTypeInfo{
				InstanceType: instanceType,
				VcpuInfo: VcpuInfo{
					DefaultVcpus: spec.vcpus,
				},
				MemoryInfo: MemoryInfo{
					SizeInMiB: spec.memoryMiB,
				},
			})
		}
	}

	resp := DescribeInstanceTypesResponse{
		RequestId:     awsresponses.NextRequestID(),
		InstanceTypes: instanceTypes,
	}

	awsresponses.WriteXML(w, resp)
}

// DescribeTags describes tags for EC2 resources
func (h *Handler) DescribeTags(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	// Parse filters - Filter.1.Name=key, Filter.1.Value.1=value, Filter.2.Name=resource-id, Filter.2.Value.1=id
	var resourceIds []string
	var resourceTypes []string
	var tagKeys []string

	// Parse filters manually
	for i := 1; i <= 100; i++ {
		filterNameKey := "Filter." + strconv.Itoa(i) + ".Name"
		filterName := r.FormValue(filterNameKey)
		if filterName == "" {
			filterName = r.URL.Query().Get(filterNameKey)
		}
		if filterName == "" {
			break
		}

		// Get filter values
		for j := 1; j <= 100; j++ {
			valueKey := "Filter." + strconv.Itoa(i) + ".Value." + strconv.Itoa(j)
			value := r.FormValue(valueKey)
			if value == "" {
				value = r.URL.Query().Get(valueKey)
			}
			if value == "" {
				break
			}

			switch filterName {
			case "resource-id":
				resourceIds = append(resourceIds, value)
			case "resource-type":
				resourceTypes = append(resourceTypes, value)
			case "key":
				tagKeys = append(tagKeys, value)
			}
		}
	}

	// For now, return empty tag set - tags are optional and Terraform can handle empty results
	// In a real implementation, we'd query stored tags based on resourceIds/resourceTypes
	resp := DescribeTagsResponse{
		RequestId: awsresponses.NextRequestID(),
		TagSet: TagSet{
			Items: []Tag{},
		},
	}

	awsresponses.WriteXML(w, resp)
}

// DescribeVpcs describes VPCs
func (h *Handler) DescribeVpcs(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	// Get requested VPC IDs
	vpcIds := r.Form["VpcId"]
	if len(vpcIds) == 0 {
		vpcIds = r.URL.Query()["VpcId"]
	}

	// If still empty, manually check for numbered parameters (VpcId.1, VpcId.2, etc.)
	if len(vpcIds) == 0 {
		for i := 1; i <= 100; i++ {
			key := "VpcId." + strconv.Itoa(i)
			value := r.FormValue(key)
			if value == "" {
				value = r.URL.Query().Get(key)
			}
			if value != "" {
				vpcIds = append(vpcIds, value)
			} else {
				break
			}
		}
	}

	// Filter out empty VPC IDs
	filteredVpcIds := make([]string, 0, len(vpcIds))
	for _, vid := range vpcIds {
		if vid != "" {
			filteredVpcIds = append(filteredVpcIds, vid)
		}
	}

	var vpcs []Vpc

	if len(filteredVpcIds) > 0 {
		// Return requested VPCs (even if they don't exist, return them with default values)
		for _, vpcId := range filteredVpcIds {
			vpcs = append(vpcs, Vpc{
				VpcId:           vpcId,
				OwnerId:         ec2Account,
				CidrBlock:       "10.0.0.0/16",
				InstanceTenancy: "default",
				IsDefault:       vpcId == defaultVpcId,
				State:           "available",
			})
		}
	} else {
		// Return default VPC if no specific VPCs requested
		vpcs = append(vpcs, Vpc{
			VpcId:           defaultVpcId,
			OwnerId:         ec2Account,
			CidrBlock:       "10.0.0.0/16",
			InstanceTenancy: "default",
			IsDefault:       true,
			State:           "available",
		})
	}

	resp := DescribeVpcsResponse{
		RequestId: awsresponses.NextRequestID(),
		Vpcs:      vpcs,
	}

	awsresponses.WriteXML(w, resp)
}

// DescribeInstanceAttribute describes a specific instance attribute
func (h *Handler) DescribeInstanceAttribute(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)

	instanceId := r.FormValue("InstanceId")
	if instanceId == "" {
		instanceId = r.URL.Query().Get("InstanceId")
	}

	attribute := r.FormValue("Attribute")
	if attribute == "" {
		attribute = r.URL.Query().Get("Attribute")
	}

	if instanceId == "" || attribute == "" {
		awsresponses.WriteErrorXML(w, http.StatusBadRequest, "MissingParameter", "InstanceId and Attribute are required", "")
		return
	}

	// Verify instance exists
	_, err := h.Store.Get(instanceId, "ec2", "instance", ns)
	if err != nil {
		awsresponses.WriteErrorXML(w, http.StatusBadRequest, "InvalidInstanceID.NotFound", "The instance ID '"+instanceId+"' does not exist", instanceId)
		return
	}

	// Build response based on attribute type
	// Use struct-based approach for consistency with other handlers
	resp := DescribeInstanceAttributeResponse{
		RequestId:  awsresponses.NextRequestID(),
		InstanceId: instanceId,
	}

	switch attribute {
	case "instanceInitiatedShutdownBehavior":
		resp.InstanceInitiatedShutdownBehavior = &AttributeValue{
			Value: "stop",
		}
	case "disableApiStop":
		resp.DisableApiStop = &AttributeValue{
			Value: "false",
		}
	case "disableApiTermination":
		resp.DisableApiTermination = &AttributeValue{
			Value: "false",
		}
	default:
		// For unknown attributes, return basic response (no attribute set)
	}

	awsresponses.WriteXML(w, resp)
}

// ModifyInstanceAttribute modifies an EC2 instance attribute
func (h *Handler) ModifyInstanceAttribute(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)

	instanceId := r.FormValue("InstanceId")
	if instanceId == "" {
		instanceId = r.URL.Query().Get("InstanceId")
	}

	if instanceId == "" {
		awsresponses.WriteErrorXML(w, http.StatusBadRequest, "MissingParameter", "InstanceId is required", "")
		return
	}

	// Verify instance exists
	res, err := h.Store.Get(instanceId, "ec2", "instance", ns)
	if err != nil {
		awsresponses.WriteErrorXML(w, http.StatusBadRequest, "InvalidInstanceID.NotFound", "The instance ID '"+instanceId+"' does not exist", instanceId)
		return
	}

	var entry map[string]any
	if err := json.Unmarshal(res.Attributes, &entry); err != nil {
		awsresponses.WriteErrorXML(w, http.StatusInternalServerError, "InternalFailure", "Failed to decode instance metadata", "")
		return
	}

	instanceBytes, _ := json.Marshal(entry["instance"])
	var instance Instance
	json.Unmarshal(instanceBytes, &instance)

	// Ensure all required fields are populated before updating
	instance = ensureInstanceFields(instance, instance.PrivateIpAddress, instance.PrivateDnsName, time.Now().UTC())

	// Handle different attribute modifications
	// Handle disableApiStop
	if disableApiStop := r.FormValue("DisableApiStop.Value"); disableApiStop != "" {
		// Store the value (though we don't expose it in DescribeInstanceAttribute yet)
		// For now, just acknowledge the change
	} else if disableApiStop := r.URL.Query().Get("DisableApiStop.Value"); disableApiStop != "" {
		// Same for query parameter
	}

	// Handle disableApiTermination
	if disableApiTermination := r.FormValue("DisableApiTermination.Value"); disableApiTermination != "" {
		// Store the value (though we don't expose it in DescribeInstanceAttribute yet)
		// For now, just acknowledge the change
	} else if disableApiTermination := r.URL.Query().Get("DisableApiTermination.Value"); disableApiTermination != "" {
		// Same for query parameter
	}

	// Handle instanceInitiatedShutdownBehavior
	if shutdownBehavior := r.FormValue("InstanceInitiatedShutdownBehavior.Value"); shutdownBehavior != "" {
		// Store the value (though we don't expose it in DescribeInstanceAttribute yet)
		// For now, just acknowledge the change
	} else if shutdownBehavior := r.URL.Query().Get("InstanceInitiatedShutdownBehavior.Value"); shutdownBehavior != "" {
		// Same for query parameter
	}

	// Update instance in store
	entry["instance"] = instance
	buf, _ := json.Marshal(entry)
	res.Attributes = buf
	h.Store.Update(res)

	resp := ModifyInstanceAttributeResponse{
		Return: true,
		ResponseMetadata: ResponseMetadata{
			RequestId: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, resp)
}
