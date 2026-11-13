package app

import (
	"errors"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/alternayte/sayso/internal/runner"
)

// KubernetesRunner builds the Kubernetes runner from the in-cluster
// service account. SAYSO_AGENT_IMAGE names the default agent image.
func KubernetesRunner(cfg *Config) (*runner.Kubernetes, error) {
	if cfg.K8sNamespace == "" {
		return nil, errors.New("SAYSO_K8S_NAMESPACE is required for the kubernetes runner")
	}
	rc, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	c, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, err
	}
	return &runner.Kubernetes{Client: c, Namespace: cfg.K8sNamespace, Image: cfg.AgentImage}, nil
}
