package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// RunLabel labels the Job and the Pod of an agent run.
const RunLabel = "sayso/run-id"

// Resources are the requests and limits of an agent Job (settings).
type Resources struct {
	CPURequest, CPULimit       string
	MemoryRequest, MemoryLimit string
}

// Kubernetes runs each agent run as a Job in one namespace (§8.2).
type Kubernetes struct {
	Client    kubernetes.Interface
	Namespace string
	Image     string
	Resources Resources
	// Poll is the interval of the status checks.
	Poll time.Duration
}

// JobGrace is added to the run timeout for activeDeadlineSeconds.
const JobGrace = 5 * time.Minute

// Job returns the Job of an agent run: the agent image, emptyDir volumes for
// /work, HOME and /tmp, no service account token, non-root and a read-only
// root filesystem (SI-09).
func (k *Kubernetes) Job(j Job) (*batchv1.Job, error) {
	image := j.Image
	if image == "" {
		image = k.Image
	}
	if image == "" {
		return nil, errors.New("kubernetes runner: no agent image")
	}
	res := corev1.ResourceRequirements{Requests: corev1.ResourceList{}, Limits: corev1.ResourceList{}}
	for _, r := range []struct {
		list corev1.ResourceList
		name corev1.ResourceName
		val  string
	}{
		{res.Requests, corev1.ResourceCPU, k.Resources.CPURequest}, {res.Limits, corev1.ResourceCPU, k.Resources.CPULimit},
		{res.Requests, corev1.ResourceMemory, k.Resources.MemoryRequest}, {res.Limits, corev1.ResourceMemory, k.Resources.MemoryLimit},
	} {
		if r.val == "" {
			continue
		}
		q, err := resource.ParseQuantity(r.val)
		if err != nil {
			return nil, fmt.Errorf("kubernetes runner: %s %q: %w", r.name, r.val, err)
		}
		r.list[r.name] = q
	}
	no, yes := false, true
	uid := int64(10001)
	deadline := int64((j.Timeout + JobGrace).Seconds())
	ttl, backoff := int32(600), int32(0)
	labels := map[string]string{RunLabel: j.RunID, "app.kubernetes.io/name": "sayso-agent", "app.kubernetes.io/component": "agent-run"}
	empty := func(name string) corev1.Volume {
		return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}
	}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "sayso-run-" + strings.ReplaceAll(j.RunID, "-", ""), Namespace: k.Namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			ActiveDeadlineSeconds:   &deadline,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: &no,
					EnableServiceLinks:           &no,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: &yes, RunAsUser: &uid, RunAsGroup: &uid, FSGroup: &uid,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:            "agent",
						Image:           image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Args:            []string{"agent"},
						Env: []corev1.EnvVar{
							{Name: "SAYSO_API_URL", Value: j.APIURL},
							{Name: "SAYSO_RUN_TOKEN", Value: j.Token},
							{Name: "SAYSO_RUN_ID", Value: j.RunID},
							{Name: "HOME", Value: "/home/sayso"},
						},
						WorkingDir: "/work",
						Resources:  res,
						SecurityContext: &corev1.SecurityContext{
							RunAsNonRoot: &yes, ReadOnlyRootFilesystem: &yes, AllowPrivilegeEscalation: &no,
							Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						VolumeMounts: []corev1.VolumeMount{{Name: "work", MountPath: "/work"}, {Name: "home", MountPath: "/home/sayso"}, {Name: "tmp", MountPath: "/tmp"}},
					}},
					Volumes: []corev1.Volume{empty("work"), empty("home"), empty("tmp")},
				},
			},
		},
	}, nil
}

type kubeJob struct {
	k       *Kubernetes
	name    string
	runID   string
	stopped bool
}

// Start creates the Job.
func (k *Kubernetes) Start(ctx context.Context, j Job) (Handle, error) {
	job, err := k.Job(j)
	if err != nil {
		return nil, err
	}
	created, err := k.Client.BatchV1().Jobs(k.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create Job: %w", err)
	}
	return &kubeJob{k: k, name: created.Name, runID: j.RunID}, nil
}

func (h *kubeJob) Ref() string { return h.k.Namespace + "/" + h.name }

// Wait watches the Job and its Pod until the run ends and reports the exit
// reason: completed, failed, deadline, evicted or image_pull_failed.
func (h *kubeJob) Wait(ctx context.Context) (string, error) {
	poll := h.k.Poll
	if poll == 0 {
		poll = 2 * time.Second
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		if reason, done, err := h.status(ctx); err != nil || done {
			if done && reason == ExitImagePullFailed {
				_ = h.Stop(context.WithoutCancel(ctx))
			}
			return reason, err
		}
		select {
		case <-ctx.Done():
			_ = h.Stop(context.WithoutCancel(ctx))
			return ExitCancelled, ctx.Err()
		case <-t.C:
		}
	}
}

var pullFailures = map[string]bool{"ErrImagePull": true, "ImagePullBackOff": true, "InvalidImageName": true, "ErrImageNeverPull": true}

func (h *kubeJob) status(ctx context.Context) (string, bool, error) {
	job, err := h.k.Client.BatchV1().Jobs(h.k.Namespace).Get(ctx, h.name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if h.stopped {
			return ExitCancelled, true, nil
		}
		return ExitFailed, true, errors.New("the Job was deleted")
	}
	if err != nil {
		return "", false, err
	}
	pods, err := h.k.Client.CoreV1().Pods(h.k.Namespace).List(ctx, metav1.ListOptions{LabelSelector: RunLabel + "=" + h.runID})
	if err != nil {
		return "", false, err
	}
	for _, p := range pods.Items {
		if p.Status.Reason == "Evicted" {
			return ExitEvicted, true, nil
		}
		for _, cs := range p.Status.ContainerStatuses {
			if w := cs.State.Waiting; w != nil && pullFailures[w.Reason] {
				return ExitImagePullFailed, true, nil
			}
		}
	}
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return ExitCompleted, true, nil
		case batchv1.JobFailed:
			if c.Reason == "DeadlineExceeded" {
				return ExitDeadline, true, nil
			}
			return ExitFailed, true, nil
		}
	}
	return "", false, nil
}

// Stop deletes the Job and its Pod.
func (h *kubeJob) Stop(ctx context.Context) error {
	h.stopped = true
	bg := metav1.DeletePropagationBackground
	err := h.k.Client.BatchV1().Jobs(h.k.Namespace).Delete(ctx, h.name, metav1.DeleteOptions{PropagationPolicy: &bg})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// Permissions lists the verbs the worker needs in the runner namespace (§18.2).
var Permissions = []struct{ Resource, Subresource, Verb string }{
	{"jobs", "", "create"}, {"jobs", "", "get"}, {"jobs", "", "list"}, {"jobs", "", "watch"}, {"jobs", "", "delete"},
	{"pods", "", "get"}, {"pods", "", "list"}, {"pods", "", "watch"}, {"pods", "", "delete"}, {"pods", "log", "get"},
}

// CheckPermissions asks the API server whether the worker may run Jobs.
func (k *Kubernetes) CheckPermissions(ctx context.Context) error {
	var missing []string
	for _, p := range Permissions {
		group := ""
		if p.Resource == "jobs" {
			group = "batch"
		}
		r, err := k.Client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{
			Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authv1.ResourceAttributes{
				Namespace: k.Namespace, Group: group, Resource: p.Resource, Subresource: p.Subresource, Verb: p.Verb,
			}},
		}, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		if !r.Status.Allowed {
			name := p.Resource
			if p.Subresource != "" {
				name += "/" + p.Subresource
			}
			missing = append(missing, p.Verb+" "+name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the worker may not %s in namespace %s", strings.Join(missing, ", "), k.Namespace)
	}
	return nil
}
