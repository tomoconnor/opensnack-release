// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package dynamodb

import (
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strings"

	"opensnack/internal/resource"
	"opensnack/internal/util"

	"github.com/google/uuid"
)

// Global tables (version 2019.11.21). Each replica is an ordinary table of
// the same name in another region of the same namespace, and every copy
// lists the regions of the others in Replicas. Writes are applied to every
// copy before the call returns, so replication is immediate; settings the
// replicas share (key schema, indexes, capacity) are copied on UpdateTable.

const globalTableVersion = "2019.11.21"

type ReplicationGroupUpdate struct {
	Create *struct {
		RegionName         string `json:"RegionName"`
		KMSMasterKeyId     string `json:"KMSMasterKeyId,omitempty"`
		TableClassOverride string `json:"TableClassOverride,omitempty"`
	} `json:"Create,omitempty"`
	Update *struct {
		RegionName         string `json:"RegionName"`
		KMSMasterKeyId     string `json:"KMSMasterKeyId,omitempty"`
		TableClassOverride string `json:"TableClassOverride,omitempty"`
	} `json:"Update,omitempty"`
	Delete *struct {
		RegionName string `json:"RegionName"`
	} `json:"Delete,omitempty"`
}

type ReplicaDescription struct {
	RegionName               string             `json:"RegionName"`
	ReplicaStatus            string             `json:"ReplicaStatus"`
	KMSMasterKeyId           string             `json:"KMSMasterKeyId,omitempty"`
	ReplicaTableClassSummary *TableClassSummary `json:"ReplicaTableClassSummary,omitempty"`
}

func replicaScope(ns, region string) string {
	base, _ := resource.SplitScope(ns)
	return resource.JoinScope(base, region)
}

// replicaDescriptions renders the other copies for DescribeTable.
func (h *Handler) replicaDescriptions(ns string, t *tableRecord) []ReplicaDescription {
	var out []ReplicaDescription
	for _, region := range t.Replicas {
		d := ReplicaDescription{RegionName: region, ReplicaStatus: "ACTIVE"}
		if rt, _, err := h.loadTable(replicaScope(ns, region), t.Name); err == nil {
			d.ReplicaTableClassSummary = &TableClassSummary{TableClass: rt.TableClass}
			if rt.SSE != nil {
				d.KMSMasterKeyId = rt.SSE.KMSMasterKeyArn
			}
		}
		out = append(out, d)
	}
	return out
}

// replicaUpdates applies UpdateTable's ReplicaUpdates to t (already loaded
// from ns), saving every copy it changes. t is saved by the caller.
func (h *Handler) replicaUpdates(r *http.Request, ns string, t *tableRecord, ups []ReplicationGroupUpdate) error {
	_, own := resource.SplitScope(ns)
	for _, u := range ups {
		switch {
		case u.Create != nil:
			if err := h.createReplica(r, ns, own, t, u.Create.RegionName, u.Create.KMSMasterKeyId, u.Create.TableClassOverride); err != nil {
				return err
			}
		case u.Update != nil:
			region := u.Update.RegionName
			if region == own {
				if u.Update.TableClassOverride != "" {
					t.TableClass = u.Update.TableClassOverride
				}
				continue
			}
			if !slices.Contains(t.Replicas, region) {
				return notAReplica()
			}
			rt, res, err := h.loadTable(replicaScope(ns, region), t.Name)
			if err != nil {
				return err
			}
			if u.Update.TableClassOverride != "" {
				rt.TableClass = u.Update.TableClassOverride
			}
			if k := u.Update.KMSMasterKeyId; k != "" {
				rt.SSE = kmsSSE(region, util.AccountID(r), k)
			}
			if err := h.saveTable(res, rt); err != nil {
				return err
			}
		case u.Delete != nil:
			region := u.Delete.RegionName
			if region == own || !slices.Contains(t.Replicas, region) {
				return notAReplica()
			}
			rns := replicaScope(ns, region)
			rt, _, err := h.loadTable(rns, t.Name)
			if err != nil {
				return err
			}
			if rt.DeletionProtection {
				return validationErr("Resource cannot be deleted as it is currently protected against deletion. Disable deletion protection first.")
			}
			if err := h.dropTable(rns, rt); err != nil {
				return err
			}
			t.Replicas = slices.DeleteFunc(t.Replicas, func(s string) bool { return s == region })
			if err := h.forgetRegion(ns, t, region); err != nil {
				return err
			}
		default:
			return validationErr("One or more parameter values were invalid: ReplicationGroupUpdate must have exactly one of Create, Update or Delete")
		}
	}
	return nil
}

func notAReplica() error {
	return validationErr("Update global table operation failed because one or more replicas were not part of the global table.")
}

func kmsSSE(region, account, key string) *SSEDescription {
	if !strings.HasPrefix(key, "arn:") {
		key = "arn:aws:kms:" + region + ":" + account + ":key/" + key
	}
	return &SSEDescription{Status: "ENABLED", SSEType: "KMS", KMSMasterKeyArn: key}
}

func (h *Handler) createReplica(r *http.Request, ns, own string, t *tableRecord, region, kmsKey, class string) error {
	if region == "" {
		return validationErr("1 validation error detected: Value null at 'replicaUpdates.member.create.regionName' failed to satisfy constraint: Member must not be null")
	}
	rns := replicaScope(ns, region)
	if region == own || slices.Contains(t.Replicas, region) {
		return validationErr("Failed to create a the new replica of table with name: '" + t.Name + "' because one or more replicas already existed as tables.")
	}
	if _, err := h.Store.Get(t.Name, service, typeTable, rns); err == nil {
		return validationErr("Failed to create a the new replica of table with name: '" + t.Name + "' because one or more replicas already existed as tables.")
	}
	// Replication runs on the table's stream, which DynamoDB turns on itself.
	switch {
	case t.Stream == nil || !t.Stream.StreamEnabled:
		t.Stream = &StreamSpecification{StreamEnabled: true, StreamViewType: "NEW_AND_OLD_IMAGES"}
		t.StreamLabel = streamLabel()
	case t.Stream.StreamViewType != "NEW_AND_OLD_IMAGES":
		return validationErr("Table stream must be enabled with NEW_AND_OLD_IMAGES to create a replica")
	}

	rt := *t
	rt.ID = uuid.New().String()
	rt.Arn = "arn:aws:dynamodb:" + region + ":" + util.AccountID(r) + ":table/" + t.Name
	rt.CreatedAt = nowSeconds()
	rt.StreamLabel = streamLabel()
	rt.Tags = nil
	rt.DeletionProtection = false
	rt.PITR, rt.PITRDays = false, 0
	rt.SSE = nil
	if kmsKey != "" {
		rt.SSE = kmsSSE(region, util.AccountID(r), kmsKey)
	}
	if class != "" {
		rt.TableClass = class
	}
	rt.Replicas = append([]string{own}, t.Replicas...)
	sort.Strings(rt.Replicas)
	b, _ := json.Marshal(&rt)
	if err := h.Store.Create(&resource.Resource{
		ID: t.Name, Namespace: rns, Service: service, Type: typeTable, Attributes: b,
	}); err != nil {
		return err
	}
	for _, it := range h.tableItems(ns, t.Name) {
		if err := h.storeItem(rns, &rt, it, false); err != nil {
			return err
		}
	}

	// The other replicas learn about the new one; t is saved by the caller.
	for _, other := range t.Replicas {
		ot, res, err := h.loadTable(replicaScope(ns, other), t.Name)
		if err != nil {
			continue
		}
		ot.Replicas = append(ot.Replicas, region)
		sort.Strings(ot.Replicas)
		if err := h.saveTable(res, ot); err != nil {
			return err
		}
	}
	t.Replicas = append(t.Replicas, region)
	sort.Strings(t.Replicas)
	return nil
}

// forgetRegion removes region from the Replicas of every copy of t other
// than the one in ns.
func (h *Handler) forgetRegion(ns string, t *tableRecord, region string) error {
	for _, other := range t.Replicas {
		if other == region {
			continue
		}
		ot, res, err := h.loadTable(replicaScope(ns, other), t.Name)
		if err != nil {
			continue
		}
		ot.Replicas = slices.DeleteFunc(ot.Replicas, func(s string) bool { return s == region })
		if err := h.saveTable(res, ot); err != nil {
			return err
		}
	}
	return nil
}

// syncReplicas copies the settings a global table's replicas share from t
// to every other copy.
func (h *Handler) syncReplicas(ns string, t *tableRecord) error {
	for _, region := range t.Replicas {
		rt, res, err := h.loadTable(replicaScope(ns, region), t.Name)
		if err != nil {
			continue
		}
		rt.AttributeDefinitions = t.AttributeDefinitions
		rt.GSIs = t.GSIs
		rt.BillingMode, rt.LastPayPerRequest = t.BillingMode, t.LastPayPerRequest
		rt.Throughput, rt.OnDemand, rt.Warm = t.Throughput, t.OnDemand, t.Warm
		rt.TTL = t.TTL
		if err := h.saveTable(res, rt); err != nil {
			return err
		}
	}
	return nil
}

// replicate applies a write that has just been made in ns to the table's
// other replicas.
func (h *Handler) replicate(ns string, w *preparedWrite) error {
	for _, region := range w.t.Replicas {
		rns := replicaScope(ns, region)
		if w.newItem == nil {
			if err := h.removeItem(rns, w.t, w.key); err != nil {
				return err
			}
			continue
		}
		_, err := h.Store.Get(itemID(w.t, w.key), service, typeItem, rns)
		if err := h.storeItem(rns, w.t, w.newItem, err == nil); err != nil {
			return err
		}
	}
	return nil
}
