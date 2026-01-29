// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package elasticache

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"opensnack/internal/awsresponses"
	"opensnack/internal/resource"
	"opensnack/internal/util"
)

type Handler struct {
	Store resource.Store
}

func NewHandler(store resource.Store) *Handler {
	return &Handler{Store: store}
}

// Dispatch handles ElastiCache Query API requests
func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	action := r.FormValue("Action")
	if action == "" {
		action = r.URL.Query().Get("Action")
	}

	switch action {
	case "CreateCacheCluster":
		h.CreateCacheCluster(w, r)
	case "DescribeCacheClusters":
		h.DescribeCacheClusters(w, r)
	case "DeleteCacheCluster":
		h.DeleteCacheCluster(w, r)
	case "ListTagsForResource":
		h.ListTagsForResource(w, r)
	default:
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"InvalidAction",
			"Unknown ElastiCache Action",
			action,
		)
	}
}

// CreateCacheCluster creates a new cache cluster
func (h *Handler) CreateCacheCluster(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)

	cacheClusterId := r.FormValue("CacheClusterId")
	if cacheClusterId == "" {
		cacheClusterId = r.URL.Query().Get("CacheClusterId")
	}
	if cacheClusterId == "" {
		awsresponses.WriteErrorXML(w, http.StatusBadRequest, "MissingParameter", "CacheClusterId is required", "")
		return
	}

	// Check if cluster already exists
	_, err := h.Store.Get(cacheClusterId, "elasticache", "cache-cluster", ns)
	if err == nil {
		awsresponses.WriteErrorXML(w, http.StatusBadRequest, "CacheClusterAlreadyExists", "Cache cluster already exists: "+cacheClusterId, "")
		return
	}

	engine := r.FormValue("Engine")
	if engine == "" {
		engine = r.URL.Query().Get("Engine")
	}
	if engine == "" {
		engine = "redis" // Default to redis
	}

	nodeType := r.FormValue("CacheNodeType")
	if nodeType == "" {
		nodeType = r.URL.Query().Get("CacheNodeType")
	}
	if nodeType == "" {
		nodeType = "cache.t2.micro" // Default node type
	}

	numCacheNodesStr := r.FormValue("NumCacheNodes")
	if numCacheNodesStr == "" {
		numCacheNodesStr = r.URL.Query().Get("NumCacheNodes")
	}
	numCacheNodes := 1 // Default
	if numCacheNodesStr != "" {
		if parsed, err := strconv.Atoi(numCacheNodesStr); err == nil && parsed > 0 {
			numCacheNodes = parsed
		}
	}

	now := time.Now().UTC()
	requestId := awsresponses.NextRequestID()

	// Generate cache nodes
	cacheNodes := make([]CacheNode, numCacheNodes)
	for i := 0; i < numCacheNodes; i++ {
		cacheNodeId := cacheClusterId + "-000" + strconv.Itoa(i+1)
		port := 6379 // Default Redis port
		if engine == "memcached" {
			port = 11211 // Default Memcached port
		}

		cacheNodes[i] = CacheNode{
			CacheNodeId:         cacheNodeId,
			CacheNodeStatus:     "available",
			CacheNodeCreateTime: now,
			Endpoint: &Endpoint{
				Address: "127.0.0.1",
				Port:    port,
			},
			ParameterGroupStatus:     "in-sync",
			CustomerAvailabilityZone: "us-east-1a",
		}
	}

	// Generate endpoint (for Redis single node, use first node's endpoint)
	var configEndpoint *Endpoint
	if len(cacheNodes) > 0 && cacheNodes[0].Endpoint != nil {
		configEndpoint = cacheNodes[0].Endpoint
	}

	cacheCluster := CacheCluster{
		CacheClusterId:             cacheClusterId,
		ConfigurationEndpoint:      configEndpoint,
		ClientDownloadLandingPage:  "https://console.aws.amazon.com/elasticache/home",
		CacheNodeType:              nodeType,
		Engine:                     engine,
		EngineVersion:              "6.0",
		CacheClusterStatus:         "available",
		NumCacheNodes:              numCacheNodes,
		PreferredAvailabilityZone:  "us-east-1a",
		CacheClusterCreateTime:     now,
		PreferredMaintenanceWindow: "sun:05:00-sun:09:00",
		CacheSecurityGroups: CacheSecurityGroupMemberships{
			CacheSecurityGroup: []CacheSecurityGroup{
				{
					CacheSecurityGroupName: "default",
					Status:                 "active",
				},
			},
		},
		CacheParameterGroup: CacheParameterGroupStatus{
			CacheParameterGroupName: "default." + engine + "6.0",
			ParameterApplyStatus:    "in-sync",
		},
		CacheSubnetGroupName:    "default",
		CacheNodes:              CacheNodeList{CacheNode: cacheNodes},
		AutoMinorVersionUpgrade: true,
		SecurityGroups: SecurityGroupMemberships{
			Member: []SecurityGroupMember{
				{
					SecurityGroupId: "sg-00000000",
					Status:          "active",
				},
			},
		},
		SnapshotRetentionLimit:   0,
		SnapshotWindow:           "03:00-05:00",
		AuthTokenEnabled:         false,
		TransitEncryptionEnabled: false,
		AtRestEncryptionEnabled:  false,
		ARN:                      "arn:aws:elasticache:" + elasticacheRegion + ":" + elasticacheAccount + ":cluster:" + cacheClusterId,
	}

	// Store the cache cluster
	attributes := map[string]any{
		"cache_cluster": cacheCluster,
	}
	attributesBytes, _ := json.Marshal(attributes)

	res := &resource.Resource{
		ID:         cacheClusterId,
		Namespace:  ns,
		Service:    "elasticache",
		Type:       "cache-cluster",
		Attributes: attributesBytes,
	}

	err = h.Store.Create(res)
	if err != nil {
		awsresponses.WriteErrorXML(w, http.StatusInternalServerError, "InternalFailure", "Failed to create cache cluster", "")
		return
	}

	// Build response
	response := CreateCacheClusterResponse{
		Xmlns: "http://elasticache.amazonaws.com/doc/2015-02-02/",
		CreateCacheClusterResult: CreateCacheClusterResult{
			CacheCluster: cacheCluster,
		},
		ResponseMetadata: ResponseMetadata{
			RequestId: requestId,
		},
	}

	awsresponses.WriteXML(w, response)
}

// DescribeCacheClusters describes cache clusters
func (h *Handler) DescribeCacheClusters(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)

	cacheClusterIds := r.Form["CacheClusterId"]
	if len(cacheClusterIds) == 0 {
		cacheClusterIds = r.URL.Query()["CacheClusterId"]
	}

	var clusters []CacheCluster

	if len(cacheClusterIds) > 0 {
		// Describe specific clusters
		for _, clusterId := range cacheClusterIds {
			res, err := h.Store.Get(clusterId, "elasticache", "cache-cluster", ns)
			if err != nil {
				continue
			}

			var entry map[string]any
			if err := json.Unmarshal(res.Attributes, &entry); err != nil {
				continue
			}

			clusterBytes, _ := json.Marshal(entry["cache_cluster"])
			var cluster CacheCluster
			json.Unmarshal(clusterBytes, &cluster)

			clusters = append(clusters, cluster)
		}
	} else {
		// List all clusters
		allClusters, err := h.Store.List("elasticache", "cache-cluster", ns)
		if err == nil {
			for _, clusterRes := range allClusters {
				var entry map[string]any
				if err := json.Unmarshal(clusterRes.Attributes, &entry); err != nil {
					continue
				}

				clusterBytes, _ := json.Marshal(entry["cache_cluster"])
				var cluster CacheCluster
				json.Unmarshal(clusterBytes, &cluster)

				clusters = append(clusters, cluster)
			}
		}
	}

	requestId := awsresponses.NextRequestID()

	response := DescribeCacheClustersResponse{
		Xmlns: "http://elasticache.amazonaws.com/doc/2015-02-02/",
		DescribeCacheClustersResult: DescribeCacheClustersResult{
			CacheClusters: CacheClusterList{
				CacheCluster: clusters,
			},
		},
		ResponseMetadata: ResponseMetadata{
			RequestId: requestId,
		},
	}

	awsresponses.WriteXML(w, response)
}

// DeleteCacheCluster deletes a cache cluster
func (h *Handler) DeleteCacheCluster(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)

	cacheClusterId := r.FormValue("CacheClusterId")
	if cacheClusterId == "" {
		cacheClusterId = r.URL.Query().Get("CacheClusterId")
	}
	if cacheClusterId == "" {
		awsresponses.WriteErrorXML(w, http.StatusBadRequest, "MissingParameter", "CacheClusterId is required", "")
		return
	}

	// Get the cluster first
	res, err := h.Store.Get(cacheClusterId, "elasticache", "cache-cluster", ns)
	if err != nil {
		awsresponses.WriteErrorXML(w, http.StatusNotFound, "CacheClusterNotFound", "Cache cluster not found: "+cacheClusterId, "")
		return
	}

	var entry map[string]any
	if err := json.Unmarshal(res.Attributes, &entry); err != nil {
		awsresponses.WriteErrorXML(w, http.StatusInternalServerError, "InternalFailure", "Failed to read cache cluster", "")
		return
	}

	clusterBytes, _ := json.Marshal(entry["cache_cluster"])
	var cluster CacheCluster
	json.Unmarshal(clusterBytes, &cluster)

	// Update status to deleting
	cluster.CacheClusterStatus = "deleting"

	// Delete from store
	err = h.Store.Delete(cacheClusterId, "elasticache", "cache-cluster", ns)
	if err != nil {
		awsresponses.WriteErrorXML(w, http.StatusInternalServerError, "InternalFailure", "Failed to delete cache cluster", "")
		return
	}

	requestId := awsresponses.NextRequestID()

	response := DeleteCacheClusterResponse{
		Xmlns: "http://elasticache.amazonaws.com/doc/2015-02-02/",
		DeleteCacheClusterResult: DeleteCacheClusterResult{
			CacheCluster: cluster,
		},
		ResponseMetadata: ResponseMetadata{
			RequestId: requestId,
		},
	}

	awsresponses.WriteXML(w, response)
}

// ListTagsForResource lists tags for a cache cluster
func (h *Handler) ListTagsForResource(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ns := util.NamespaceFromHeader(r)

	resourceArn := r.FormValue("ResourceArn")
	if resourceArn == "" {
		resourceArn = r.URL.Query().Get("ResourceArn")
	}

	if resourceArn == "" {
		resourceArn = r.FormValue("ResourceName")
	}

	if resourceArn == "" {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"MissingParameter",
			"ResourceArn is required",
			"",
		)
		return
	}

	// Extract cluster ID from ARN
	// Format: arn:aws:elasticache:region:account:cluster:cluster-id
	parts := strings.Split(resourceArn, ":")
	if len(parts) < 7 || parts[5] != "cluster" {
		awsresponses.WriteErrorXML(
			w,
			http.StatusBadRequest,
			"InvalidParameter",
			"Invalid ResourceArn format",
			resourceArn,
		)
		return
	}
	cacheClusterId := parts[6]

	// Get cluster from store
	cluster, err := h.Store.Get(cacheClusterId, "elasticache", "cache-cluster", ns)
	if err != nil {
		// AWS returns empty tags if resource doesn't exist
		resp := ListTagsForResourceResponse{
			ListTagsForResourceResult: ListTagsForResourceResult{
				TagList: TagList{
					Tags: []Tag{},
				},
			},
			ResponseMetadata: ResponseMetadata{
				RequestId: awsresponses.NextRequestID(),
			},
		}
		awsresponses.WriteXML(w, resp)
		return
	}

	// Parse stored attributes
	var storedAttrs map[string]any
	if err := json.Unmarshal(cluster.Attributes, &storedAttrs); err != nil {
		storedAttrs = make(map[string]any)
	}

	// Extract tags from attributes
	var tags []Tag
	if tagMap, ok := storedAttrs["tags"].(map[string]any); ok {
		for k, v := range tagMap {
			var value string
			if str, ok := v.(string); ok {
				value = str
			} else {
				// Convert non-string values to string
				value = fmt.Sprintf("%v", v)
			}
			tags = append(tags, Tag{
				Key:   k,
				Value: value,
			})
		}
	}

	resp := ListTagsForResourceResponse{
		ListTagsForResourceResult: ListTagsForResourceResult{
			TagList: TagList{
				Tags: tags,
			},
		},
		ResponseMetadata: ResponseMetadata{
			RequestId: awsresponses.NextRequestID(),
		},
	}

	awsresponses.WriteXML(w, resp)
}
