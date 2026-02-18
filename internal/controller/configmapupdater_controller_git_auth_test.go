package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	opsv1alpha1 "github.com/syyjbhjh3/configmap-updater/api/v1alpha1"
)

func TestBuildGitCommandEnvNoSecretRef(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := opsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add opsv1alpha1 scheme: %v", err)
	}
	reconciler := &ConfigMapUpdaterReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
		Scheme: scheme,
	}

	updater := &opsv1alpha1.ConfigMapUpdater{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "updater",
		},
		Spec: opsv1alpha1.ConfigMapUpdaterSpec{
			Git: &opsv1alpha1.GitSyncSpec{
				Repo:     "https://github.com/example/repo.git",
				FilePath: "apps/configmap.yaml",
			},
		},
	}

	env, cleanup, err := reconciler.buildGitCommandEnv(context.Background(), updater, updater.Spec.Git)
	defer cleanup()
	if err != nil {
		t.Fatalf("buildGitCommandEnv returned error: %v", err)
	}
	if len(env) != 0 {
		t.Fatalf("expected empty env for empty secretRef, got: %#v", env)
	}
}

func TestBuildGitCommandEnvWithHTTPToken(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := opsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add opsv1alpha1 scheme: %v", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "git-auth",
		},
		Data: map[string][]byte{
			gitSecretTokenKey: []byte("token-value"),
		},
	}
	reconciler := &ConfigMapUpdaterReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build(),
		Scheme: scheme,
	}
	updater := &opsv1alpha1.ConfigMapUpdater{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "updater",
		},
		Spec: opsv1alpha1.ConfigMapUpdaterSpec{
			Git: &opsv1alpha1.GitSyncSpec{
				Repo:      "https://github.com/example/repo.git",
				FilePath:  "apps/configmap.yaml",
				SecretRef: corev1.LocalObjectReference{Name: "git-auth"},
			},
		},
	}

	env, cleanup, err := reconciler.buildGitCommandEnv(context.Background(), updater, updater.Spec.Git)
	defer cleanup()
	if err != nil {
		t.Fatalf("buildGitCommandEnv returned error: %v", err)
	}

	envMap := envToMap(env)
	if envMap["GIT_HTTP_USERNAME"] != "x-access-token" {
		t.Fatalf("expected default username x-access-token, got %q", envMap["GIT_HTTP_USERNAME"])
	}
	if envMap["GIT_HTTP_PASSWORD"] != "token-value" {
		t.Fatalf("expected token to be used as password")
	}
	if envMap["GIT_ASKPASS"] == "" {
		t.Fatalf("expected GIT_ASKPASS to be set")
	}
}

func TestBuildGitCommandEnvWithSSHKey(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := opsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add opsv1alpha1 scheme: %v", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "git-auth",
		},
		Data: map[string][]byte{
			gitSecretSSHPrivateKeyKey: []byte("dummy-private-key"),
			gitSecretKnownHostsKey:    []byte("github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI"),
		},
	}
	reconciler := &ConfigMapUpdaterReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build(),
		Scheme: scheme,
	}
	updater := &opsv1alpha1.ConfigMapUpdater{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "updater",
		},
		Spec: opsv1alpha1.ConfigMapUpdaterSpec{
			Git: &opsv1alpha1.GitSyncSpec{
				Repo:      "git@github.com:example/repo.git",
				FilePath:  "apps/configmap.yaml",
				SecretRef: corev1.LocalObjectReference{Name: "git-auth"},
			},
		},
	}

	env, cleanup, err := reconciler.buildGitCommandEnv(context.Background(), updater, updater.Spec.Git)
	defer cleanup()
	if err != nil {
		t.Fatalf("buildGitCommandEnv returned error: %v", err)
	}

	envMap := envToMap(env)
	sshCommand := envMap["GIT_SSH_COMMAND"]
	if sshCommand == "" {
		t.Fatalf("expected GIT_SSH_COMMAND to be set")
	}
	if !strings.Contains(sshCommand, "UserKnownHostsFile") {
		t.Fatalf("expected GIT_SSH_COMMAND to include known_hosts option, got %q", sshCommand)
	}
}

func TestBuildGitCommandEnvUnsupportedSecretData(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := opsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add opsv1alpha1 scheme: %v", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "git-auth",
		},
		Data: map[string][]byte{
			"unexpected": []byte("value"),
		},
	}
	reconciler := &ConfigMapUpdaterReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build(),
		Scheme: scheme,
	}
	updater := &opsv1alpha1.ConfigMapUpdater{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "updater",
		},
		Spec: opsv1alpha1.ConfigMapUpdaterSpec{
			Git: &opsv1alpha1.GitSyncSpec{
				Repo:      "https://github.com/example/repo.git",
				FilePath:  "apps/configmap.yaml",
				SecretRef: corev1.LocalObjectReference{Name: "git-auth"},
			},
		},
	}

	env, cleanup, err := reconciler.buildGitCommandEnv(context.Background(), updater, updater.Spec.Git)
	defer cleanup()
	if err == nil {
		t.Fatalf("expected error for unsupported secret data, env=%#v", env)
	}
}

func envToMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, item := range env {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) == 2 {
			out[parts[0]] = parts[1]
		}
	}
	return out
}
