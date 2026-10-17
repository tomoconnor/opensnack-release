// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package kube is a small Kubernetes client: just enough of the core/v1
// Pod API for opensnack to run sandboxes, without client-go's dependency
// tree. The types are the subset of the API's JSON that opensnack sets or
// reads; unknown fields are ignored.
package kube

import "time"

type Pod struct {
	APIVersion string     `json:"apiVersion,omitempty"`
	Kind       string     `json:"kind,omitempty"`
	Metadata   ObjectMeta `json:"metadata"`
	Spec       PodSpec    `json:"spec"`
	Status     PodStatus  `json:"status,omitempty"`
}

type ObjectMeta struct {
	Name              string            `json:"name,omitempty"`
	Namespace         string            `json:"namespace,omitempty"`
	UID               string            `json:"uid,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
	CreationTimestamp *time.Time        `json:"creationTimestamp,omitempty"`
	DeletionTimestamp *time.Time        `json:"deletionTimestamp,omitempty"`
}

type PodSpec struct {
	RuntimeClassName              *string             `json:"runtimeClassName,omitempty"`
	ServiceAccountName            string              `json:"serviceAccountName,omitempty"`
	AutomountServiceAccountToken  *bool               `json:"automountServiceAccountToken,omitempty"`
	EnableServiceLinks            *bool               `json:"enableServiceLinks,omitempty"`
	RestartPolicy                 string              `json:"restartPolicy,omitempty"`
	ActiveDeadlineSeconds         *int64              `json:"activeDeadlineSeconds,omitempty"`
	TerminationGracePeriodSeconds *int64              `json:"terminationGracePeriodSeconds,omitempty"`
	HostNetwork                   bool                `json:"hostNetwork,omitempty"`
	HostPID                       bool                `json:"hostPID,omitempty"`
	HostIPC                       bool                `json:"hostIPC,omitempty"`
	ShareProcessNamespace         *bool               `json:"shareProcessNamespace,omitempty"`
	NodeSelector                  map[string]string   `json:"nodeSelector,omitempty"`
	SecurityContext               *PodSecurityContext `json:"securityContext,omitempty"`
	Containers                    []Container         `json:"containers"`
	Volumes                       []Volume            `json:"volumes,omitempty"`
}

type PodSecurityContext struct {
	RunAsNonRoot   *bool           `json:"runAsNonRoot,omitempty"`
	RunAsUser      *int64          `json:"runAsUser,omitempty"`
	RunAsGroup     *int64          `json:"runAsGroup,omitempty"`
	FSGroup        *int64          `json:"fsGroup,omitempty"`
	SeccompProfile *SeccompProfile `json:"seccompProfile,omitempty"`
}

type SeccompProfile struct {
	Type string `json:"type"`
}

type Container struct {
	Name            string               `json:"name"`
	Image           string               `json:"image"`
	ImagePullPolicy string               `json:"imagePullPolicy,omitempty"`
	Command         []string             `json:"command,omitempty"`
	Args            []string             `json:"args,omitempty"`
	Env             []EnvVar             `json:"env,omitempty"`
	Resources       ResourceRequirements `json:"resources,omitempty"`
	SecurityContext *SecurityContext     `json:"securityContext,omitempty"`
	VolumeMounts    []VolumeMount        `json:"volumeMounts,omitempty"`
}

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ResourceRequirements maps resource names ("cpu", "memory") to quantities
// ("250m", "128Mi").
type ResourceRequirements struct {
	Limits   map[string]string `json:"limits,omitempty"`
	Requests map[string]string `json:"requests,omitempty"`
}

type SecurityContext struct {
	Privileged               *bool           `json:"privileged,omitempty"`
	AllowPrivilegeEscalation *bool           `json:"allowPrivilegeEscalation,omitempty"`
	ReadOnlyRootFilesystem   *bool           `json:"readOnlyRootFilesystem,omitempty"`
	RunAsNonRoot             *bool           `json:"runAsNonRoot,omitempty"`
	RunAsUser                *int64          `json:"runAsUser,omitempty"`
	RunAsGroup               *int64          `json:"runAsGroup,omitempty"`
	Capabilities             *Capabilities   `json:"capabilities,omitempty"`
	SeccompProfile           *SeccompProfile `json:"seccompProfile,omitempty"`
}

type Capabilities struct {
	Add  []string `json:"add,omitempty"`
	Drop []string `json:"drop,omitempty"`
}

type VolumeMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
	ReadOnly  bool   `json:"readOnly,omitempty"`
}

type Volume struct {
	Name      string                 `json:"name"`
	EmptyDir  *EmptyDirVolumeSource  `json:"emptyDir,omitempty"`
	ConfigMap *ConfigMapVolumeSource `json:"configMap,omitempty"`
}

type EmptyDirVolumeSource struct {
	Medium    string `json:"medium,omitempty"`
	SizeLimit string `json:"sizeLimit,omitempty"`
}

type ConfigMapVolumeSource struct {
	Name     string `json:"name"`
	Optional *bool  `json:"optional,omitempty"`
}

type PodStatus struct {
	Phase             string            `json:"phase,omitempty"`
	Reason            string            `json:"reason,omitempty"`
	Message           string            `json:"message,omitempty"`
	PodIP             string            `json:"podIP,omitempty"`
	ContainerStatuses []ContainerStatus `json:"containerStatuses,omitempty"`
}

type ContainerStatus struct {
	Name         string         `json:"name"`
	Ready        bool           `json:"ready"`
	RestartCount int            `json:"restartCount"`
	State        ContainerState `json:"state"`
}

type ContainerState struct {
	Waiting    *ContainerStateWaiting    `json:"waiting,omitempty"`
	Running    *ContainerStateRunning    `json:"running,omitempty"`
	Terminated *ContainerStateTerminated `json:"terminated,omitempty"`
}

type ContainerStateWaiting struct {
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

type ContainerStateRunning struct {
	StartedAt *time.Time `json:"startedAt,omitempty"`
}

type ContainerStateTerminated struct {
	ExitCode int    `json:"exitCode"`
	Signal   int    `json:"signal,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Message  string `json:"message,omitempty"`
}

// Pod phases.
const (
	PodPending   = "Pending"
	PodRunning   = "Running"
	PodSucceeded = "Succeeded"
	PodFailed    = "Failed"
)

type podList struct {
	Items []Pod `json:"items"`
}
