package app

import (
	"errors"

	"github.com/alternayte/sayso/internal/runner"
)

// KubernetesRunner builds the Kubernetes runner from the in-cluster
// service account. SAYSO_AGENT_IMAGE names the default agent image.
func KubernetesRunner(cfg *Config) (*runner.Kubernetes, error) {
	if cfg.K8sNamespace == "" {
		return nil, errors.New("SAYSO_K8S_NAMESPACE is required for the kubernetes runner")
	}
	c, err := runner.InClusterKubeClient()
	if err != nil {
		return nil, err
	}
	k := &runner.Kubernetes{Client: c, Namespace: cfg.K8sNamespace, Image: cfg.AgentImage, Resources: cfg.K8sResources}
	// A wrong quantity fails at start, not at the first round.
	if _, err := k.Job(runner.Job{RunID: "check", Image: "check"}); err != nil {
		return nil, err
	}
	return k, nil
}
