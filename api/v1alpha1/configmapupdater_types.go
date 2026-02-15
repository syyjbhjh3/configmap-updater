/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type ConfigMapRef struct {
	// +kubebuilder:validation:MinLength=1
	// +required
	Namespace string `json:"namespace"`
	// +kubebuilder:validation:MinLength=1
	// +required
	Name string `json:"name"`
}

type DeploymentRef struct {
	// +kubebuilder:validation:MinLength=1
	// +required
	Namespace string `json:"namespace"`
	// +kubebuilder:validation:MinLength=1
	// +required
	Name string `json:"name"`
}

type GitSyncSpec struct {
	// +required
	// Repo is HTTPS/SSH URL of git repository to update.
	Repo string `json:"repo"`

	// +optional
	// Branch is target branch. Defaults to "main".
	Branch string `json:"branch,omitempty"`

	// +required
	// FilePath is path in repository to update (e.g. app/values.yaml).
	FilePath string `json:"filePath"`

	// +optional
	// SecretRef references secret that has git credentials.
	// Used when pushing commit to remote git.
	SecretRef corev1.LocalObjectReference `json:"secretRef,omitempty"`
}

type ArgoCDSyncSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +required
	Name string `json:"name"`

	// Namespace where the ArgoCD Application exists.
	// +kubebuilder:default:="argocd"
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Poll interval while waiting for ArgoCD Sync.
	// +kubebuilder:default:="10s"
	// +optional
	PollInterval metav1.Duration `json:"pollInterval,omitempty"`

	// Maximum wait time for ArgoCD Sync completion.
	// +kubebuilder:default:="3m"
	// +optional
	Timeout metav1.Duration `json:"timeout,omitempty"`

	// If true, wait for health status to be Healthy in addition to synced.
	// +kubebuilder:default:=false
	// +optional
	RequireHealthy bool `json:"requireHealthy,omitempty"`
}

// ConfigMapUpdaterSpec defines the desired state of ConfigMapUpdater
type ConfigMapUpdaterSpec struct {
	// destinationClusterRef references DestinationCluster used as source cluster.
	// +required
	DestinationClusterRef corev1.LocalObjectReference `json:"destinationClusterRef"`

	// source defines source ConfigMap identity in destination cluster.
	// +required
	Source ConfigMapRef `json:"source"`

	// target defines target ConfigMap identity in local cluster.
	// +required
	Target ConfigMapRef `json:"target"`

	// git defines git sync settings.
	// +optional
	Git *GitSyncSpec `json:"git,omitempty"`

	// interval overrides reconcile interval for this policy.
	// +kubebuilder:default:="5m"
	// +optional
	Interval metav1.Duration `json:"interval,omitempty"`

	// restartOnChange controls deployment restart when target ConfigMap changes.
	// +kubebuilder:default:=true
	// +optional
	RestartOnChange bool `json:"restartOnChange,omitempty"`

	// restartTargets lists deployments to restart on change.
	// +optional
	RestartTargets []DeploymentRef `json:"restartTargets,omitempty"`

	// ignoreKeys excludes keys from change detection and sync.
	// +optional
	IgnoreKeys []string `json:"ignoreKeys,omitempty"`

	// argocdSync controls optional waiting for ArgoCD Application sync completion
	// before restarting target deployments.
	// +optional
	ArgocdSync *ArgoCDSyncSpec `json:"argocdSync,omitempty"`
}

// ConfigMapUpdaterStatus defines the observed state of ConfigMapUpdater.
type ConfigMapUpdaterStatus struct {
	// observedGeneration tracks the last processed generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// lastSyncTime records last successful sync attempt.
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// lastSourceHash stores source ConfigMap content hash.
	// +optional
	LastSourceHash string `json:"lastSourceHash,omitempty"`

	// lastError stores the latest reconcile error summary.
	// +optional
	LastError string `json:"lastError,omitempty"`

	// lastAction describes the latest reconcile action.
	// +optional
	LastAction string `json:"lastAction,omitempty"`

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the ConfigMapUpdater resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=".spec.source.name"
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=".spec.target.name"
// +kubebuilder:printcolumn:name="LastSync",type=date,JSONPath=".status.lastSyncTime"
// +kubebuilder:printcolumn:name="LastError",type=string,JSONPath=".status.lastError"

// ConfigMapUpdater is the Schema for the configmapupdaters API
type ConfigMapUpdater struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of ConfigMapUpdater
	// +required
	Spec ConfigMapUpdaterSpec `json:"spec"`

	// status defines the observed state of ConfigMapUpdater
	// +optional
	Status ConfigMapUpdaterStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ConfigMapUpdaterList contains a list of ConfigMapUpdater
type ConfigMapUpdaterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ConfigMapUpdater `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ConfigMapUpdater{}, &ConfigMapUpdaterList{})
}
