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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	kubernetes "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"

	opsv1alpha1 "github.com/syyjbhjh3/configmap-updater/api/v1alpha1"
)

// ConfigMapUpdaterReconciler reconciles a ConfigMapUpdater object
type ConfigMapUpdaterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const (
	updaterByDestinationClusterKey              = ".spec.destinationClusterRef"
	updaterByGitSecretKey                       = ".spec.git.secretRef"
	destinationClusterByKubeconfigSecretNameKey = ".spec.kubeconfigSecretRef.name"
	destinationClusterByKubeconfigSecretNSKey   = ".spec.kubeconfigSecretNamespace"
	gitSecretUsernameKey                        = "username"
	gitSecretPasswordKey                        = "password"
	gitSecretTokenKey                           = "token"
	gitSecretSSHPrivateKeyKey                   = "sshPrivateKey"
	gitSecretKnownHostsKey                      = "knownHosts"
)

// +kubebuilder:rbac:groups=ops.ops.example.io,resources=configmapupdaters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ops.ops.example.io,resources=configmapupdaters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ops.ops.example.io,resources=configmapupdaters/finalizers,verbs=update
// +kubebuilder:rbac:groups=ops.ops.example.io,resources=destinationclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="apps",resources=deployments,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=argoproj.io,resources=applications,verbs=get;list;watch

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

	destNamespace := updater.Spec.DestinationClusterRef.Namespace
	if destNamespace == "" {
		destNamespace = updater.Namespace
	}
	destKey := types.NamespacedName{Namespace: destNamespace, Name: updater.Spec.DestinationClusterRef.Name}
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
	secretNamespace := dest.Spec.KubeconfigSecretNamespace
	if secretNamespace == "" {
		secretNamespace = dest.Namespace
	}
	secretKey := types.NamespacedName{Namespace: secretNamespace, Name: dest.Spec.KubeconfigSecretRef.Name}
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

	if updater.Spec.Git == nil {
		return r.failStatus(ctx, &updater, "InvalidSpec", "spec.git is required", interval)
	}

	changed := false
	action := "noop"
	commitHash, changedInGit, err := r.syncConfigMapToGit(ctx, &updater, sourceDataForCompare, sourceBinaryForCompare, ignoreKeys, sourceHash)
	if err != nil {
		return r.failStatus(ctx, &updater, "GitSyncError", err.Error(), interval)
	}
	changed = changedInGit
	if changedInGit {
		action = "updated-git"
		if commitHash != "" {
			action = "updated-git-" + commitHash[:7]
		}
	} else {
		log.Info("git target already up-to-date; skip update and commit")
	}

	if changed && updater.Spec.RestartOnChange {
		if updater.Spec.ArgocdSync != nil && updater.Spec.ArgocdSync.Name != "" {
			if err := r.waitForArgoCDSync(ctx, &updater, updater.Spec.ArgocdSync); err != nil {
				return r.failStatus(ctx, &updater, "ArgoCDSyncTimeout", err.Error(), interval)
			}
		}
		if err := r.restartDeployments(ctx, &updater); err != nil {
			return r.failStatus(ctx, &updater, "RestartTargetsError", err.Error(), interval)
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

func (r *ConfigMapUpdaterReconciler) syncConfigMapToGit(
	ctx context.Context,
	updater *opsv1alpha1.ConfigMapUpdater,
	sourceData map[string]string,
	sourceBinary map[string][]byte,
	ignoreKeys map[string]struct{},
	sourceHash string,
) (string, bool, error) {
	if updater == nil || updater.Spec.Git == nil {
		return "", false, errors.New("git sync spec is required")
	}
	spec := updater.Spec.Git
	if spec.Repo == "" || spec.FilePath == "" {
		return "", false, errors.New("git spec.repo and spec.filePath are required")
	}

	branch := spec.Branch
	if branch == "" {
		branch = "main"
	}

	gitEnv, cleanupGitAuth, err := r.buildGitCommandEnv(ctx, updater, spec)
	if err != nil {
		return "", false, err
	}
	defer cleanupGitAuth()

	repoDir := filepath.Join(os.TempDir(), "configmap-updater-git", updater.Namespace, updater.Name)
	_ = os.MkdirAll(filepath.Dir(repoDir), 0o755)

	_ = os.RemoveAll(repoDir)
	if err := r.cloneOrUpdateRepo(ctx, spec.Repo, branch, repoDir, gitEnv); err != nil {
		return "", false, err
	}

	raw, err := os.ReadFile(filepath.Join(repoDir, spec.FilePath))
	if err != nil {
		return "", false, fmt.Errorf("read target file: %w", err)
	}
	docs, err := decodeYAMLDocuments(raw)
	if err != nil {
		return "", false, fmt.Errorf("decode target yaml: %w", err)
	}

	targetNode, exists := findConfigMapNode(docs, updater.Spec.Target.Name, updater.Spec.Target.Namespace)
	var existingData map[string]string
	var existingBinary map[string][]byte
	if exists {
		existingData, existingBinary = extractConfigMapData(targetNode)
		filteredData, filteredBinary := filteredConfigMap(existingData, existingBinary, ignoreKeys)
		existingHash, err := hashConfigMap(filteredData, filteredBinary)
		if err != nil {
			return "", false, fmt.Errorf("target configmap hash error: %w", err)
		}
		if existingHash == sourceHash {
			return "", false, nil
		}
	}

	nextData := copyStringMap(sourceData)
	nextBinary := copyByteMap(sourceBinary)
	if exists {
		for key := range ignoreKeys {
			if value, ok := existingData[key]; ok {
				nextData[key] = value
			}
			if value, ok := existingBinary[key]; ok {
				nextBinary[key] = value
			}
		}
	}

	docs, targetNode = ensureConfigMapNode(targetNode, docs, updater.Spec.Target.Name, updater.Spec.Target.Namespace)
	targetNode["data"] = convertDataMapToInterface(nextData)
	targetNode["binaryData"] = convertBinaryMapToInterface(nextBinary)
	out, err := encodeYAMLDocuments(docs)
	if err != nil {
		return "", false, fmt.Errorf("marshal updated yaml: %w", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, spec.FilePath), out, 0o644); err != nil {
		return "", false, fmt.Errorf("write target file: %w", err)
	}

	changed, err := hasFileChanged(ctx, repoDir, spec.FilePath, gitEnv)
	if err != nil {
		return "", false, err
	}
	if !changed {
		return "", false, nil
	}

	commitHash, err := pushGitCommit(ctx, repoDir, spec, updater.Spec.Source.Namespace, updater.Spec.Source.Name, sourceHash, gitEnv)
	if err != nil {
		return "", false, err
	}
	return commitHash, true, nil
}

func pushGitCommit(
	ctx context.Context,
	repoDir string,
	spec *opsv1alpha1.GitSyncSpec,
	sourceNS, sourceName, sourceHash string,
	gitEnv []string,
) (string, error) {
	if out, err := runGitCommand(ctx, repoDir, gitEnv, "add", spec.FilePath); err != nil {
		return "", fmt.Errorf("git add: %s: %w", out, err)
	}

	msg := fmt.Sprintf("chore: sync configmap from %s/%s", sourceNS, sourceName)
	if sourceHash != "" {
		msg = fmt.Sprintf("%s (sourceHash=%s)", msg, sourceHash)
	}
	if out, err := runGitCommand(
		ctx,
		repoDir,
		gitEnv,
		"-c", "user.name=configmap-updater",
		"-c", "user.email=configmap-updater@local",
		"commit",
		"-m", msg,
		"--",
		spec.FilePath,
	); err != nil {
		return "", fmt.Errorf("git commit: %s: %w", out, err)
	}

	branch := spec.Branch
	if branch == "" {
		branch = "main"
	}
	if out, err := runGitCommand(ctx, repoDir, gitEnv, "push", "origin", branch); err != nil {
		return "", fmt.Errorf("git push: %s: %w", out, err)
	}
	out, err := runGitCommand(ctx, repoDir, gitEnv, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func hasFileChanged(ctx context.Context, repoDir, filePath string, gitEnv []string) (bool, error) {
	out, err := runGitCommand(ctx, repoDir, gitEnv, "status", "--porcelain", filePath)
	if err != nil {
		return false, fmt.Errorf("git status: %s: %w", out, err)
	}
	return len(out) > 0, nil
}

func (r *ConfigMapUpdaterReconciler) waitForArgoCDSync(
	ctx context.Context,
	updater *opsv1alpha1.ConfigMapUpdater,
	spec *opsv1alpha1.ArgoCDSyncSpec,
) error {
	if updater == nil || spec == nil || spec.Name == "" {
		return nil
	}

	namespace := spec.Namespace
	if namespace == "" {
		namespace = "argocd"
	}

	log := logf.FromContext(ctx).WithValues(
		"configMapUpdater", fmt.Sprintf("%s/%s", updater.Namespace, updater.Name),
		"argocdApplication", fmt.Sprintf("%s/%s", namespace, spec.Name),
	)

	pollInterval := 10 * time.Second
	if spec.PollInterval.Duration > 0 {
		pollInterval = spec.PollInterval.Duration
	}
	if pollInterval < time.Second {
		pollInterval = time.Second
	}

	timeout := 3 * time.Minute
	if spec.Timeout.Duration > 0 {
		timeout = spec.Timeout.Duration
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	appObj := &unstructured.Unstructured{}
	appObj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "argoproj.io",
		Version: "v1alpha1",
		Kind:    "Application",
	})

	for {
		err := r.Get(waitCtx, types.NamespacedName{Namespace: namespace, Name: spec.Name}, appObj)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("argocd application %s/%s not found", namespace, spec.Name)
			}
			log.Error(err, "failed to get argocd application")
		} else {
			syncStatus, _, _ := unstructured.NestedString(appObj.Object, "status", "sync", "status")
			healthStatus, _, _ := unstructured.NestedString(appObj.Object, "status", "health", "status")

			if syncStatus == "Synced" && (!spec.RequireHealthy || healthStatus == "Healthy") {
				log.Info("argocd application synced", "syncStatus", syncStatus, "healthStatus", healthStatus)
				return nil
			}

			log.Info("waiting for argocd sync", "syncStatus", syncStatus, "healthStatus", healthStatus)
		}

		select {
		case <-waitCtx.Done():
			return fmt.Errorf("timeout waiting argocd sync for %s/%s", namespace, spec.Name)
		case <-time.After(pollInterval):
		}
	}
}

func (r *ConfigMapUpdaterReconciler) restartDeployments(ctx context.Context, updater *opsv1alpha1.ConfigMapUpdater) error {
	if updater == nil {
		return nil
	}
	if len(updater.Spec.RestartTargets) == 0 {
		return nil
	}
	restartAt := time.Now().Format(time.RFC3339)
	for _, target := range updater.Spec.RestartTargets {
		var dep appsv1.Deployment
		if err := r.Get(ctx, types.NamespacedName{
			Namespace: target.Namespace,
			Name:      target.Name,
		}, &dep); err != nil {
			return err
		}
		patch := map[string]interface{}{
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{
						"annotations": map[string]string{
							"kubectl.kubernetes.io/restartedAt": restartAt,
						},
					},
				},
			},
		}
		raw, err := json.Marshal(patch)
		if err != nil {
			return err
		}
		if err := r.Patch(ctx, &dep, client.RawPatch(types.StrategicMergePatchType, raw)); err != nil {
			return err
		}
	}
	return nil
}

func decodeYAMLDocuments(raw []byte) ([]interface{}, error) {
	decoder := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 1024)
	docs := make([]interface{}, 0)
	for {
		var doc interface{}
		if err := decoder.Decode(&doc); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if doc != nil {
			docs = append(docs, doc)
		}
	}
	return docs, nil
}

func encodeYAMLDocuments(docs []interface{}) ([]byte, error) {
	if len(docs) == 0 {
		return []byte{}, nil
	}
	var out strings.Builder
	for i, doc := range docs {
		if i > 0 {
			out.WriteString("---\n")
		}
		raw, err := yaml.Marshal(doc)
		if err != nil {
			return nil, err
		}
		out.Write(raw)
		if len(raw) > 0 && raw[len(raw)-1] != '\n' {
			out.WriteByte('\n')
		}
	}
	return []byte(out.String()), nil
}

func findConfigMapNode(docs []interface{}, targetName, targetNamespace string) (map[string]interface{}, bool) {
	for _, doc := range docs {
		if node, found := findConfigMapNodeInObject(doc, targetName, targetNamespace); found {
			return node, true
		}
	}
	return nil, false
}

func findConfigMapNodeInObject(obj interface{}, targetName, targetNamespace string) (map[string]interface{}, bool) {
	switch typed := obj.(type) {
	case map[string]interface{}:
		kind, _ := typed["kind"].(string)
		metadata, _ := typed["metadata"].(map[string]interface{})
		name, _ := metadata["name"].(string)
		namespace, _ := metadata["namespace"].(string)
		if kind == "ConfigMap" && name == targetName && (namespace == "" || namespace == targetNamespace) {
			return typed, true
		}
		if nested, ok := typed["items"].([]interface{}); ok {
			if node, found := findConfigMapNodeInObject(nested, targetName, targetNamespace); found {
				return node, true
			}
		}
		for _, key := range []string{"spec", "items"} {
			if child, ok := typed[key]; ok {
				if node, found := findConfigMapNodeInObject(child, targetName, targetNamespace); found {
					return node, true
				}
			}
		}
	case []interface{}:
		for _, item := range typed {
			if node, found := findConfigMapNodeInObject(item, targetName, targetNamespace); found {
				return node, true
			}
		}
	}
	return nil, false
}

func ensureConfigMapNode(
	existing map[string]interface{},
	docs []interface{},
	targetName, targetNamespace string,
) ([]interface{}, map[string]interface{}) {
	if existing != nil {
		return docs, existing
	}
	newNode := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]interface{}{
			"name":      targetName,
			"namespace": targetNamespace,
		},
	}
	docs = append(docs, newNode)
	return docs, newNode
}

func extractConfigMapData(node map[string]interface{}) (map[string]string, map[string][]byte) {
	dataMap, err := asStringMap(node["data"])
	if err != nil {
		dataMap = map[string]string{}
	}
	binaryMap, err := asBase64Map(node["binaryData"])
	if err != nil {
		binaryMap = map[string][]byte{}
	}
	return dataMap, binaryMap
}

func asStringMap(raw interface{}) (map[string]string, error) {
	if raw == nil {
		return map[string]string{}, nil
	}
	typed, ok := raw.(map[string]interface{})
	if !ok {
		return nil, errors.New("invalid yaml map data")
	}
	result := make(map[string]string, len(typed))
	for key, value := range typed {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("invalid value for key=%s", key)
		}
		result[key] = text
	}
	return result, nil
}

func asBase64Map(raw interface{}) (map[string][]byte, error) {
	rawMap, err := asStringMap(raw)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]byte, len(rawMap))
	for key, value := range rawMap {
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return nil, fmt.Errorf("invalid base64 data for key=%s", key)
		}
		result[key] = decoded
	}
	return result, nil
}

func convertDataMapToInterface(data map[string]string) map[string]interface{} {
	if data == nil {
		return map[string]interface{}{}
	}
	result := make(map[string]interface{}, len(data))
	for key, value := range data {
		result[key] = value
	}
	return result
}

func convertBinaryMapToInterface(binary map[string][]byte) map[string]interface{} {
	if binary == nil {
		return map[string]interface{}{}
	}
	keys := make([]string, 0, len(binary))
	for key := range binary {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make(map[string]interface{}, len(binary))
	for _, key := range keys {
		result[key] = base64.StdEncoding.EncodeToString(binary[key])
	}
	return result
}

func (r *ConfigMapUpdaterReconciler) cloneOrUpdateRepo(ctx context.Context, repository, branch, repoDir string, gitEnv []string) error {
	output, err := runGitCommand(ctx, "", gitEnv, "clone", "--depth", "1", "--branch", branch, "--single-branch", repository, repoDir)
	if err != nil {
		return fmt.Errorf("git clone: %s: %w", output, err)
	}
	output, err = runGitCommand(ctx, repoDir, gitEnv, "pull", "origin", branch)
	if err != nil {
		return fmt.Errorf("git pull: %s: %w", output, err)
	}
	return nil
}

func runGitCommand(ctx context.Context, repoDir string, gitEnv []string, args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("empty git command")
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	if repoDir != "" {
		cmd.Dir = repoDir
	}
	if len(gitEnv) > 0 {
		cmd.Env = append(os.Environ(), gitEnv...)
	}
	return cmd.CombinedOutput()
}

func (r *ConfigMapUpdaterReconciler) buildGitCommandEnv(
	ctx context.Context,
	updater *opsv1alpha1.ConfigMapUpdater,
	spec *opsv1alpha1.GitSyncSpec,
) ([]string, func(), error) {
	if updater == nil || spec == nil || spec.SecretRef.Name == "" {
		return nil, func() {}, nil
	}

	secretKey := types.NamespacedName{Namespace: updater.Namespace, Name: spec.SecretRef.Name}
	var secret corev1.Secret
	if err := r.Get(ctx, secretKey, &secret); err != nil {
		return nil, func() {}, fmt.Errorf("get git credentials secret %s: %w", secretKey.String(), err)
	}

	tmpDir, err := os.MkdirTemp("", "configmap-updater-git-auth-*")
	if err != nil {
		return nil, func() {}, fmt.Errorf("create git auth tempdir: %w", err)
	}
	cleanup := func() {
		_ = os.RemoveAll(tmpDir)
	}

	env := make([]string, 0, 8)
	configured := false

	username := secretStringValue(&secret, gitSecretUsernameKey)
	password := secretStringValue(&secret, gitSecretPasswordKey)
	token := secretStringValue(&secret, gitSecretTokenKey)
	if token != "" && password == "" {
		password = token
	}
	if password != "" && username == "" {
		username = "x-access-token"
	}
	if username != "" || password != "" {
		if username == "" || password == "" {
			cleanup()
			return nil, func() {}, errors.New("git credentials secret requires both username and password (or token)")
		}
		askPassPath := filepath.Join(tmpDir, "askpass.sh")
		askPassScript := []byte("#!/bin/sh\ncase \"$1\" in\n*Username*) printf '%s\\n' \"$GIT_HTTP_USERNAME\" ;;\n*Password*) printf '%s\\n' \"$GIT_HTTP_PASSWORD\" ;;\n*) printf '\\n' ;;\nesac\n")
		if err := os.WriteFile(askPassPath, askPassScript, 0o700); err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("write git askpass script: %w", err)
		}
		env = append(env,
			"GIT_TERMINAL_PROMPT=0",
			"GIT_ASKPASS="+askPassPath,
			"GIT_HTTP_USERNAME="+username,
			"GIT_HTTP_PASSWORD="+password,
		)
		configured = true
	}

	privateKey := secretStringValue(&secret, gitSecretSSHPrivateKeyKey)
	if privateKey != "" {
		privateKeyPath := filepath.Join(tmpDir, "id_rsa")
		if err := os.WriteFile(privateKeyPath, []byte(privateKey), 0o600); err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("write git ssh private key: %w", err)
		}

		sshCommand := fmt.Sprintf("ssh -i %s -o IdentitiesOnly=yes", shellQuote(privateKeyPath))
		knownHosts := secretStringValue(&secret, gitSecretKnownHostsKey)
		if knownHosts != "" {
			knownHostsPath := filepath.Join(tmpDir, "known_hosts")
			if err := os.WriteFile(knownHostsPath, []byte(knownHosts), 0o600); err != nil {
				cleanup()
				return nil, func() {}, fmt.Errorf("write git known_hosts: %w", err)
			}
			sshCommand = fmt.Sprintf(
				"%s -o UserKnownHostsFile=%s -o StrictHostKeyChecking=yes",
				sshCommand,
				shellQuote(knownHostsPath),
			)
		} else {
			sshCommand = sshCommand + " -o StrictHostKeyChecking=accept-new"
		}

		env = append(env, "GIT_SSH_COMMAND="+sshCommand)
		configured = true
	}

	if !configured {
		cleanup()
		return nil, func() {}, fmt.Errorf(
			"git credentials secret %s does not contain supported keys (%s/%s or %s, optional %s)",
			secretKey.String(),
			gitSecretUsernameKey,
			gitSecretPasswordKey,
			gitSecretTokenKey,
			gitSecretSSHPrivateKeyKey,
		)
	}
	return env, cleanup, nil
}

func secretStringValue(secret *corev1.Secret, key string) string {
	if secret == nil || key == "" {
		return ""
	}
	value, ok := secret.Data[key]
	if !ok {
		return ""
	}
	return strings.TrimSpace(string(value))
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// SetupWithManager sets up the controller with the Manager.
func (r *ConfigMapUpdaterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &opsv1alpha1.ConfigMapUpdater{}, updaterByDestinationClusterKey, func(rawObj client.Object) []string {
		updater, ok := rawObj.(*opsv1alpha1.ConfigMapUpdater)
		if !ok {
			return nil
		}
		destNamespace := updater.Spec.DestinationClusterRef.Namespace
		if destNamespace == "" {
			destNamespace = updater.Namespace
		}
		key := namespacedObjectKey(destNamespace, updater.Spec.DestinationClusterRef.Name)
		if key == "" {
			return nil
		}
		return []string{key}
	}); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &opsv1alpha1.ConfigMapUpdater{}, updaterByGitSecretKey, func(rawObj client.Object) []string {
		updater, ok := rawObj.(*opsv1alpha1.ConfigMapUpdater)
		if !ok || updater.Spec.Git == nil || updater.Spec.Git.SecretRef.Name == "" {
			return nil
		}
		key := namespacedObjectKey(updater.Namespace, updater.Spec.Git.SecretRef.Name)
		if key == "" {
			return nil
		}
		return []string{key}
	}); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &opsv1alpha1.DestinationCluster{}, destinationClusterByKubeconfigSecretNameKey, func(rawObj client.Object) []string {
		dest, ok := rawObj.(*opsv1alpha1.DestinationCluster)
		if !ok || dest.Spec.KubeconfigSecretRef.Name == "" {
			return nil
		}
		return []string{dest.Spec.KubeconfigSecretRef.Name}
	}); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &opsv1alpha1.DestinationCluster{}, destinationClusterByKubeconfigSecretNSKey, func(rawObj client.Object) []string {
		dest, ok := rawObj.(*opsv1alpha1.DestinationCluster)
		if !ok {
			return nil
		}
		secretNamespace := dest.Spec.KubeconfigSecretNamespace
		if secretNamespace == "" {
			secretNamespace = dest.Namespace
		}
		if secretNamespace == "" {
			return nil
		}
		return []string{secretNamespace}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&opsv1alpha1.ConfigMapUpdater{}).
		Watches(&opsv1alpha1.DestinationCluster{}, handler.EnqueueRequestsFromMapFunc(r.mapDestinationClusterToUpdaters)).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.mapSecretToUpdaters)).
		Named("configmapupdater").
		Complete(r)
}

func (r *ConfigMapUpdaterReconciler) mapDestinationClusterToUpdaters(ctx context.Context, rawObj client.Object) []reconcile.Request {
	dest, ok := rawObj.(*opsv1alpha1.DestinationCluster)
	if !ok {
		return nil
	}
	destKey := namespacedObjectKey(dest.Namespace, dest.Name)
	if destKey == "" {
		return nil
	}
	var updaters opsv1alpha1.ConfigMapUpdaterList
	if err := r.List(ctx, &updaters, client.MatchingFields{updaterByDestinationClusterKey: destKey}); err != nil {
		logf.FromContext(ctx).Error(err, "failed to list ConfigMapUpdaters by destination cluster", "destinationCluster", destKey)
		return nil
	}
	return requestsForUpdaters(updaters.Items)
}

func (r *ConfigMapUpdaterReconciler) mapSecretToUpdaters(ctx context.Context, rawObj client.Object) []reconcile.Request {
	secret, ok := rawObj.(*corev1.Secret)
	if !ok {
		return nil
	}
	log := logf.FromContext(ctx)
	requestsByName := make(map[types.NamespacedName]struct{})

	var updatersByGitSecret opsv1alpha1.ConfigMapUpdaterList
	secretKey := namespacedObjectKey(secret.Namespace, secret.Name)
	if secretKey != "" {
		if err := r.List(ctx, &updatersByGitSecret, client.MatchingFields{updaterByGitSecretKey: secretKey}); err != nil {
			log.Error(err, "failed to list ConfigMapUpdaters by git secret", "secret", secretKey)
		} else {
			for _, updater := range updatersByGitSecret.Items {
				requestsByName[types.NamespacedName{Namespace: updater.Namespace, Name: updater.Name}] = struct{}{}
			}
		}
	}

	var destinationClusters opsv1alpha1.DestinationClusterList
	if err := r.List(ctx, &destinationClusters, client.MatchingFields{
		destinationClusterByKubeconfigSecretNameKey: secret.Name,
		destinationClusterByKubeconfigSecretNSKey:   secret.Namespace,
	}); err != nil {
		log.Error(err, "failed to list DestinationClusters by kubeconfig secret", "secret", secretKey)
	} else {
		for i := range destinationClusters.Items {
			dest := &destinationClusters.Items[i]
			destKey := namespacedObjectKey(dest.Namespace, dest.Name)
			if destKey == "" {
				continue
			}
			var updaters opsv1alpha1.ConfigMapUpdaterList
			if err := r.List(ctx, &updaters, client.MatchingFields{updaterByDestinationClusterKey: destKey}); err != nil {
				log.Error(err, "failed to list ConfigMapUpdaters by destination cluster", "destinationCluster", destKey)
				continue
			}
			for _, updater := range updaters.Items {
				requestsByName[types.NamespacedName{Namespace: updater.Namespace, Name: updater.Name}] = struct{}{}
			}
		}
	}

	requests := make([]reconcile.Request, 0, len(requestsByName))
	for nn := range requestsByName {
		requests = append(requests, reconcile.Request{NamespacedName: nn})
	}
	return requests
}

func requestsForUpdaters(updaters []opsv1alpha1.ConfigMapUpdater) []reconcile.Request {
	requests := make([]reconcile.Request, 0, len(updaters))
	for i := range updaters {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: updaters[i].Namespace,
				Name:      updaters[i].Name,
			},
		})
	}
	return requests
}

func namespacedObjectKey(namespace, name string) string {
	if namespace == "" || name == "" {
		return ""
	}
	return namespace + "/" + name
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
