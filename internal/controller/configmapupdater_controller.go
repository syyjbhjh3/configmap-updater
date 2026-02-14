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

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubernetes "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	opsv1alpha1 "github.com/syyjbhjh3/configmap-updater/api/v1alpha1"
)

// ConfigMapUpdaterReconciler reconciles a ConfigMapUpdater object
type ConfigMapUpdaterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=ops.ops.example.io,resources=configmapupdaters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ops.ops.example.io,resources=configmapupdaters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ops.ops.example.io,resources=configmapupdaters/finalizers,verbs=update
// +kubebuilder:rbac:groups=ops.ops.example.io,resources=destinationclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="apps",resources=deployments,verbs=get;list;watch;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the ConfigMapUpdater object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.1/pkg/reconcile
func (r *ConfigMapUpdaterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithValues("configMapUpdater", req.NamespacedName.String())
	start := time.Now()

	var updater opsv1alpha1.ConfigMapUpdater
	if err := r.Get(ctx, req.NamespacedName, &updater); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	interval := 5 * time.Minute
	if updater.Spec.Interval.Duration > 0 {
		interval = updater.Spec.Interval.Duration
	}
	if interval < time.Minute {
		interval = time.Minute
	}

	if updater.Spec.DestinationClusterRef.Name == "" {
		return r.failStatus(ctx, &updater, "InvalidSpec", "destinationClusterRef.name is required", interval)
	}
	if updater.Spec.Source.Namespace == "" || updater.Spec.Source.Name == "" {
		return r.failStatus(ctx, &updater, "InvalidSpec", "source namespace/name is required", interval)
	}
	if updater.Spec.Target.Namespace == "" || updater.Spec.Target.Name == "" {
		return r.failStatus(ctx, &updater, "InvalidSpec", "target namespace/name is required", interval)
	}

	destKey := types.NamespacedName{Namespace: updater.Namespace, Name: updater.Spec.DestinationClusterRef.Name}
	var dest opsv1alpha1.DestinationCluster
	if err := r.Get(ctx, destKey, &dest); err != nil {
		return r.failStatus(ctx, &updater, "DestinationClusterNotFound", err.Error(), interval)
	}
	if updater.Spec.Interval.Duration == 0 && dest.Spec.PollInterval.Duration > 0 {
		interval = dest.Spec.PollInterval.Duration
	}

	if dest.Spec.KubeconfigSecretRef.Name == "" || dest.Spec.KubeconfigSecretRef.Key == "" {
		return r.failStatus(ctx, &updater, "InvalidDestinationCluster", "kubeconfigSecretRef.name/key is required", interval)
	}

	var secret corev1.Secret
	secretKey := types.NamespacedName{Namespace: updater.Namespace, Name: dest.Spec.KubeconfigSecretRef.Name}
	if err := r.Get(ctx, secretKey, &secret); err != nil {
		return r.failStatus(ctx, &updater, "KubeconfigSecretNotFound", err.Error(), interval)
	}

	kubeconfigBytes, ok := secret.Data[dest.Spec.KubeconfigSecretRef.Key]
	if !ok || len(kubeconfigBytes) == 0 {
		return r.failStatus(ctx, &updater, "KubeconfigMissing", "referenced kubeconfig key not found in secret", interval)
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
	if err != nil {
		return r.failStatus(ctx, &updater, "KubeconfigParseError", err.Error(), interval)
	}

	remoteClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return r.failStatus(ctx, &updater, "RemoteClientInitError", err.Error(), interval)
	}

	// Remote cluster is read-only: only GET source ConfigMap.
	sourceCM, err := remoteClient.CoreV1().ConfigMaps(updater.Spec.Source.Namespace).Get(ctx, updater.Spec.Source.Name, metav1.GetOptions{})
	if err != nil {
		return r.failStatus(ctx, &updater, "SourceConfigMapReadError", err.Error(), interval)
	}
	nowValidated := metav1.Now()
	dest.Status.LastValidatedTime = &nowValidated
	_ = r.Status().Update(ctx, &dest)


	ignoreKeys := toIgnoreKeySet(updater.Spec.IgnoreKeys)
	sourceDataForCompare, sourceBinaryForCompare := filteredConfigMap(sourceCM.Data, sourceCM.BinaryData, ignoreKeys)
	sourceHash, err := hashConfigMap(sourceDataForCompare, sourceBinaryForCompare)
	if err != nil {
		return r.failStatus(ctx, &updater, "SourceHashError", err.Error(), interval)
	}

	targetKey := types.NamespacedName{Namespace: updater.Spec.Target.Namespace, Name: updater.Spec.Target.Name}
	var targetCM corev1.ConfigMap
	targetExists := true
	if err := r.Get(ctx, targetKey, &targetCM); err != nil {
		if !apierrors.IsNotFound(err) {
			return r.failStatus(ctx, &updater, "TargetConfigMapReadError", err.Error(), interval)
		}
		targetExists = false
	}

	changed := true
	action := "noop"
	if targetExists {
		targetDataForCompare, targetBinaryForCompare := filteredConfigMap(targetCM.Data, targetCM.BinaryData, ignoreKeys)
		targetHash, hashErr := hashConfigMap(targetDataForCompare, targetBinaryForCompare)
		if hashErr != nil {
			return r.failStatus(ctx, &updater, "TargetHashError", hashErr.Error(), interval)
		}
		changed = targetHash != sourceHash
		log.Info("configmap compare", "sourceHash", sourceHash, "targetHash", targetHash, "changed", changed)
	} else {
		log.Info("target configmap not found, creating", "target", targetKey.String())
	}

	if changed {
		action = "updated-configmap"
		if !targetExists {
			targetCM = corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      updater.Spec.Target.Name,
					Namespace: updater.Spec.Target.Namespace,
				},
			}
		}
		if targetCM.Labels == nil {
			targetCM.Labels = map[string]string{}
		}
		targetCM.Labels["app.kubernetes.io/managed-by"] = "configmap-updater"
		targetCM.Labels["configmap-updater.ops.example.io/policy"] = updater.Name
		targetDataForSync := copyStringMap(sourceDataForCompare)
		targetBinaryForSync := copyByteMap(sourceBinaryForCompare)

		if targetExists {
			for key, value := range targetCM.Data {
				if _, ignored := ignoreKeys[key]; ignored {
					targetDataForSync[key] = value
				}
			}
			for key, value := range copyByteMap(targetCM.BinaryData) {
				if _, ignored := ignoreKeys[key]; ignored {
					targetBinaryForSync[key] = value
				}
			}
		}

		targetCM.Data = targetDataForSync
		targetCM.BinaryData = targetBinaryForSync

		if targetExists {
			if err := r.Update(ctx, &targetCM); err != nil {
				return r.failStatus(ctx, &updater, "TargetConfigMapUpdateError", err.Error(), interval)
			}
		} else {
			if err := r.Create(ctx, &targetCM); err != nil {
				return r.failStatus(ctx, &updater, "TargetConfigMapCreateError", err.Error(), interval)
			}
		}

		if updater.Spec.RestartOnChange {
			restarted := false
			for _, depRef := range updater.Spec.RestartTargets {
				var dep appsv1.Deployment
				key := types.NamespacedName{Namespace: depRef.Namespace, Name: depRef.Name}
				if err := r.Get(ctx, key, &dep); err != nil {
					log.Error(err, "restart target deployment fetch failed", "deployment", key.String())
					continue
				}
				base := dep.DeepCopy()
				if dep.Spec.Template.Annotations == nil {
					dep.Spec.Template.Annotations = map[string]string{}
				}
				dep.Spec.Template.Annotations["configmap-updater.ops.example.io/restartedAt"] = time.Now().UTC().Format(time.RFC3339)
				dep.Spec.Template.Annotations["configmap-updater.ops.example.io/sourceHash"] = sourceHash
				if err := r.Patch(ctx, &dep, client.MergeFrom(base)); err != nil {
					log.Error(err, "deployment restart patch failed", "deployment", key.String())
				} else {
					log.Info("deployment restart patch applied", "deployment", key.String())
					restarted = true
				}
			}
			if restarted {
				action = "updated-configmap-and-restarted"
			}
		}
	} else {
		log.Info("target already up-to-date; skip update and restart")
	}

	now := metav1.Now()
	updater.Status.ObservedGeneration = updater.Generation
	updater.Status.LastSyncTime = &now
	updater.Status.LastSourceHash = sourceHash
	updater.Status.LastError = ""
	updater.Status.LastAction = action
	setCondition(&updater.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		Message:            fmt.Sprintf("reconciled (changed=%t)", changed),
		LastTransitionTime: now,
		ObservedGeneration: updater.Generation,
	})
	setCondition(&updater.Status.Conditions, metav1.Condition{
		Type:               "Degraded",
		Status:             metav1.ConditionFalse,
		Reason:             "Healthy",
		Message:            "no degradation",
		LastTransitionTime: now,
		ObservedGeneration: updater.Generation,
	})
	if err := r.Status().Update(ctx, &updater); err != nil {
		log.Error(err, "status update failed")
		return ctrl.Result{RequeueAfter: interval}, nil
	}

	requeueAfter := withJitter(interval, updater.Namespace+"/"+updater.Name)
	log.Info("reconcile complete", "changed", changed, "duration", time.Since(start).String(), "nextRequeue", requeueAfter.String())
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

func toIgnoreKeySet(ignoreKeys []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ignoreKeys))
	for _, key := range ignoreKeys {
		set[key] = struct{}{}
	}
	return set
}

func filteredConfigMap(data map[string]string, binaryData map[string][]byte, ignoreKeys map[string]struct{}) (map[string]string, map[string][]byte) {
	filteredData := copyStringMap(data)
	filteredBinary := copyByteMap(binaryData)

	for key := range ignoreKeys {
		delete(filteredData, key)
		delete(filteredBinary, key)
	}

	return filteredData, filteredBinary
}

// SetupWithManager sets up the controller with the Manager.
func (r *ConfigMapUpdaterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&opsv1alpha1.ConfigMapUpdater{}).
		Named("configmapupdater").
		Complete(r)
}

func (r *ConfigMapUpdaterReconciler) failStatus(
	ctx context.Context,
	updater *opsv1alpha1.ConfigMapUpdater,
	reason, message string,
	interval time.Duration,
) (ctrl.Result, error) {
	now := metav1.Now()
	updater.Status.ObservedGeneration = updater.Generation
	updater.Status.LastSyncTime = &now
	updater.Status.LastError = fmt.Sprintf("%s: %s", reason, message)
	updater.Status.LastAction = "error"
	setCondition(&updater.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
		ObservedGeneration: updater.Generation,
	})
	setCondition(&updater.Status.Conditions, metav1.Condition{
		Type:               "Degraded",
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
		ObservedGeneration: updater.Generation,
	})
	_ = r.Status().Update(ctx, updater)
	return ctrl.Result{RequeueAfter: withJitter(interval, updater.Namespace+"/"+updater.Name)}, nil
}

func hashConfigMap(data map[string]string, binaryData map[string][]byte) (string, error) {
	payload := struct {
		Data       map[string]string `json:"data"`
		BinaryData map[string][]byte `json:"binaryData"`
	}{
		Data:       data,
		BinaryData: binaryData,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func copyStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyByteMap(in map[string][]byte) map[string][]byte {
	if in == nil {
		return nil
	}
	out := make(map[string][]byte, len(in))
	for k, v := range in {
		buf := make([]byte, len(v))
		copy(buf, v)
		out[k] = buf
	}
	return out
}

func setCondition(conditions *[]metav1.Condition, cond metav1.Condition) {
	apimeta.SetStatusCondition(conditions, cond)
}

func withJitter(base time.Duration, key string) time.Duration {
	if base <= 0 {
		base = 5 * time.Minute
	}
	maxJitter := base / 10
	if maxJitter > 30*time.Second {
		maxJitter = 30 * time.Second
	}
	if maxJitter <= 0 {
		return base
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	j := time.Duration(h.Sum32() % uint32(maxJitter))
	return base + j
}
