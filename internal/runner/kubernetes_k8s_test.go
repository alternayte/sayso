//go:build k8s

package runner_test

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/k3s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/alternayte/sayso/internal/runner"
)

const (
	k3sImage  = "rancher/k3s:v1.34.2-k3s1"
	helmImage = "alpine/helm:3.19.0"
)

type cluster struct {
	c      *k3s.K3sContainer
	client kubernetes.Interface
}

func newCluster(t *testing.T, images ...string) *cluster {
	t.Helper()
	ctx := context.Background()
	// Low eviction thresholds keep a busy Docker disk from blocking the test Pods.
	c, err := k3s.Run(ctx, k3sImage, testcontainers.WithCmdArgs(
		"--kubelet-arg=eviction-hard=imagefs.available<1%,nodefs.available<1%",
		"--kubelet-arg=image-gc-high-threshold=99", "--kubelet-arg=image-gc-low-threshold=98"))
	if err != nil {
		t.Fatalf("start k3s: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	if len(images) > 0 {
		// LoadImages saves local images; pull the ones a fresh machine lacks.
		for _, img := range images {
			if exec.Command("docker", "image", "inspect", img).Run() != nil {
				if out, err := exec.Command("docker", "pull", "-q", img).CombinedOutput(); err != nil {
					t.Fatalf("pull %s: %v %s", img, err, out)
				}
			}
		}
		if err := c.LoadImages(ctx, images...); err != nil {
			t.Fatalf("load images: %v", err)
		}
	}
	kc, err := c.GetKubeConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := clientcmd.RESTConfigFromKubeConfig(kc)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := kubernetes.NewForConfig(rc)
	if err != nil {
		t.Fatal(err)
	}
	// Pods are refused until the namespace has its default service account,
	// and the Job controller then backs off for minutes.
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if _, err := cs.CoreV1().ServiceAccounts("default").Get(ctx, "default", metav1.GetOptions{}); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the default service account did not appear")
		}
		time.Sleep(time.Second)
	}
	return &cluster{c: c, client: cs}
}

// apply runs kubectl apply inside the k3s container.
func (k *cluster) apply(t *testing.T, manifest []byte) {
	t.Helper()
	ctx := context.Background()
	if err := k.c.CopyToContainer(ctx, manifest, "/tmp/apply.yaml", 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, err := k.c.Exec(ctx, []string{"kubectl", "apply", "-f", "/tmp/apply.yaml"})
	b := new(bytes.Buffer)
	if out != nil {
		_, _ = b.ReadFrom(out)
	}
	if err != nil || code != 0 {
		t.Fatalf("kubectl apply: %d %v %s", code, err, b)
	}
}

func TestSCN_RUN_003_KubernetesJob(t *testing.T) {
	k := newCluster(t)
	ctx := context.Background()
	kr := &runner.Kubernetes{Client: k.client, Namespace: "default", Image: "sayso-agent:dev", Poll: time.Second,
		Resources: runner.Resources{CPURequest: "100m", CPULimit: "1", MemoryRequest: "128Mi", MemoryLimit: "1Gi"}}
	if err := kr.CheckPermissions(ctx); err != nil {
		t.Fatalf("permissions of the test admin: %v", err)
	}
	h, err := kr.Start(ctx, runner.Job{RunID: "0190a000-0000-7000-8000-000000000001", Token: "srt_test", APIURL: "http://sayso-api:8080", Timeout: 30 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	name := strings.TrimPrefix(h.Ref(), "default/")
	job, err := k.client.BatchV1().Jobs("default").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod := job.Spec.Template.Spec
	c := pod.Containers[0]
	switch {
	case pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken:
		t.Fatal("the Job mounts a service account token")
	case pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot || c.SecurityContext.RunAsNonRoot == nil || !*c.SecurityContext.RunAsNonRoot:
		t.Fatal("the Job may run as root")
	case c.SecurityContext.ReadOnlyRootFilesystem == nil || !*c.SecurityContext.ReadOnlyRootFilesystem:
		t.Fatal("the root filesystem is writable")
	case job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != int64((35*time.Minute).Seconds()):
		t.Fatalf("activeDeadlineSeconds %v", job.Spec.ActiveDeadlineSeconds)
	case job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 || job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished != 600:
		t.Fatal("backoffLimit or ttlSecondsAfterFinished")
	case job.Labels[runner.RunLabel] != "0190a000-0000-7000-8000-000000000001":
		t.Fatalf("labels %v", job.Labels)
	case c.Resources.Limits.Memory().String() != "1Gi" || c.Resources.Requests.Cpu().String() != "100m":
		t.Fatalf("resources %+v", c.Resources)
	}
	mounts := map[string]string{}
	for _, m := range c.VolumeMounts {
		mounts[m.MountPath] = m.Name
	}
	for _, v := range pod.Volumes {
		if v.EmptyDir == nil {
			t.Fatalf("volume %s is not emptyDir", v.Name)
		}
	}
	if mounts["/work"] == "" || mounts["/home/sayso"] == "" {
		t.Fatalf("mounts %v", mounts)
	}
	home := ""
	for _, e := range c.Env {
		if e.Name == "HOME" {
			home = e.Value
		}
	}
	if home != "/home/sayso" {
		t.Fatalf("HOME %q", home)
	}
	if err := h.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	// An image that cannot be pulled ends the run with image_pull_failed.
	h, err = kr.Start(ctx, runner.Job{RunID: "0190a000-0000-7000-8000-000000000002", Token: "srt_test", APIURL: "http://sayso-api:8080", Image: "registry.invalid/acme/missing:1", Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	reason, err := h.Wait(wctx)
	if err != nil || reason != runner.ExitImagePullFailed {
		_, out, _ := k.c.Exec(ctx, []string{"sh", "-c", `kubectl get jobs -o yaml | grep -A25 "^    status:"; kubectl get events -A --sort-by=.lastTimestamp | tail -25`})
		b := new(bytes.Buffer)
		if out != nil {
			_, _ = b.ReadFrom(out)
		}
		t.Fatalf("exit reason %q, %v\n%s", reason, err, b)
	}
	if _, err := k.client.BatchV1().Jobs("default").Get(ctx, strings.TrimPrefix(h.Ref(), "default/"), metav1.GetOptions{}); err == nil {
		// Deletion is in the background; the Job must go soon.
		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, err := k.client.BatchV1().Jobs("default").Get(ctx, strings.TrimPrefix(h.Ref(), "default/"), metav1.GetOptions{}); err != nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the failed Job was not deleted")
			}
			time.Sleep(time.Second)
		}
	}
}

// helmTemplate renders the chart with the pinned helm image.
func helmTemplate(t *testing.T, args ...string) []byte {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	chart, err := filepath.Abs(filepath.Join(filepath.Dir(file), "../../deploy/helm/sayso"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("docker", append([]string{"run", "--rm", "-v", chart + ":/chart:ro", helmImage, "template", "sayso", "/chart"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm template: %v %s", err, stderr.String())
	}
	return out
}

const backing = `
apiVersion: v1
kind: Secret
metadata: { name: sayso }
stringData:
  SAYSO_DATABASE_URL: postgresql://sayso:sayso@postgres:5432/sayso?sslmode=disable
  SAYSO_SECRET_KEY: AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=
---
apiVersion: apps/v1
kind: Deployment
metadata: { name: postgres }
spec:
  selector: { matchLabels: { app: postgres } }
  template:
    metadata: { labels: { app: postgres } }
    spec:
      containers:
        - name: postgres
          image: postgres:16
          env:
            - { name: POSTGRES_USER, value: sayso }
            - { name: POSTGRES_PASSWORD, value: sayso }
            - { name: POSTGRES_DB, value: sayso }
          ports: [{ containerPort: 5432 }]
---
apiVersion: v1
kind: Service
metadata: { name: postgres }
spec: { selector: { app: postgres }, ports: [{ port: 5432 }] }
---
apiVersion: apps/v1
kind: Deployment
metadata: { name: temporal }
spec:
  selector: { matchLabels: { app: temporal } }
  template:
    metadata: { labels: { app: temporal } }
    spec:
      containers:
        - name: temporal
          image: temporalio/temporal:1.4.1
          args: ["server", "start-dev", "--ip", "0.0.0.0", "--headless"]
          ports: [{ containerPort: 7233 }]
---
apiVersion: v1
kind: Service
metadata: { name: temporal }
spec: { selector: { app: temporal }, ports: [{ port: 7233 }] }
`

func TestSCN_CORE_003_HelmInstall(t *testing.T) {
	k := newCluster(t, "sayso:dev", "postgres:16", "temporalio/temporal:1.4.1")
	ctx := context.Background()
	k.apply(t, []byte(backing))
	k.apply(t, helmTemplate(t, "--namespace", "default", "--set", "image.repository=sayso", "--set", "image.tag=dev",
		"--set", "agentImage.repository=sayso-agent", "--set", "agentImage.tag=dev", "--set", "temporal.address=temporal:7233"))
	deadline := time.Now().Add(5 * time.Minute)
	for _, name := range []string{"sayso-api", "sayso-worker"} {
		for {
			d, err := k.client.AppsV1().Deployments("default").Get(ctx, name, metav1.GetOptions{})
			if err == nil && d.Status.ReadyReplicas == *d.Spec.Replicas && available(d) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("deployment %s is not ready: %+v; pods: %s", name, d.Status, podInfo(ctx, k.client))
			}
			time.Sleep(3 * time.Second)
		}
	}
	role, err := k.client.RbacV1().Roles("default").Get(ctx, "sayso-runner", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range role.Rules {
		for _, res := range r.Resources {
			for _, v := range r.Verbs {
				got = append(got, res+":"+v)
			}
		}
	}
	slices.Sort(got)
	// §18.2: Jobs and Pods: create, get, list, watch, delete; Pods/log: get.
	var want []string
	for _, res := range []string{"jobs", "pods"} {
		for _, v := range []string{"create", "get", "list", "watch", "delete"} {
			want = append(want, res+":"+v)
		}
	}
	want = append(want, "pods/log:get")
	slices.Sort(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("worker Role %v, want %v", got, want)
	}
	np, err := k.client.NetworkingV1().NetworkPolicies("default").Get(ctx, "sayso-agent-egress", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("NetworkPolicy: %v", err)
	}
	if np.Spec.PodSelector.MatchLabels["app.kubernetes.io/component"] != "agent-run" || len(np.Spec.Egress) < 2 {
		t.Fatalf("NetworkPolicy %+v", np.Spec)
	}
	// The worker may run agent Jobs with its service account (§18.2).
	sa, err := k.client.CoreV1().ServiceAccounts("default").Get(ctx, "sayso-worker", metav1.GetOptions{})
	if err != nil || sa == nil {
		t.Fatalf("worker service account: %v", err)
	}
}

func available(d *appsv1.Deployment) bool {
	for _, c := range d.Status.Conditions {
		if c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func podInfo(ctx context.Context, c kubernetes.Interface) string {
	pods, err := c.CoreV1().Pods("default").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err.Error()
	}
	var b strings.Builder
	for _, p := range pods.Items {
		fmt.Fprintf(&b, "%s %s %s %s", p.Name, p.Status.Phase, p.Status.Reason, p.Status.Message)
		for _, cs := range p.Status.ContainerStatuses {
			if cs.State.Waiting != nil {
				fmt.Fprintf(&b, " waiting %s %s", cs.State.Waiting.Reason, cs.State.Waiting.Message)
			}
			if cs.LastTerminationState.Terminated != nil {
				fmt.Fprintf(&b, " terminated %s", cs.LastTerminationState.Terminated.Reason)
			}
		}
		b.WriteString("; ")
	}
	return b.String()
}
