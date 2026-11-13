package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"net/http"
	"net/url"
	"regexp"
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
	Client    *KubeClient
	Namespace string
	Image     string
	Resources Resources
	// Poll is the interval of the status checks.
	Poll time.Duration
}

// MaxStatusFailures is the count of status calls in a row that may fail
// before Wait gives up on a Job.
const MaxStatusFailures = 5

// JobGrace is added to the run timeout for activeDeadlineSeconds.
const JobGrace = 5 * time.Minute

// Job returns the Job of an agent run: the agent image, emptyDir volumes for
// /work, HOME and /tmp, no service account token, non-root and a read-only
// root filesystem (SI-09).
func (k *Kubernetes) Job(j Job) (*KubeJob, error) {
	image := j.Image
	if image == "" {
		image = k.Image
	}
	if image == "" {
		return nil, errors.New("kubernetes runner: no agent image")
	}
	res := KubeResources{Requests: map[string]string{}, Limits: map[string]string{}}
	for _, r := range []struct {
		list map[string]string
		name string
		val  string
	}{
		{res.Requests, "cpu", k.Resources.CPURequest}, {res.Limits, "cpu", k.Resources.CPULimit},
		{res.Requests, "memory", k.Resources.MemoryRequest}, {res.Limits, "memory", k.Resources.MemoryLimit},
	} {
		if r.val == "" {
			continue
		}
		if !quantity.MatchString(r.val) {
			return nil, fmt.Errorf("kubernetes runner: %s %q is not a Kubernetes quantity", r.name, r.val)
		}
		r.list[r.name] = r.val
	}
	uid := int64(10001)
	labels := map[string]string{RunLabel: j.RunID, "app.kubernetes.io/name": "sayso-agent", "app.kubernetes.io/component": "agent-run"}
	return &KubeJob{
		APIVersion: "batch/v1",
		Kind:       "Job",
		Metadata:   kMeta{Name: "sayso-run-" + strings.ReplaceAll(j.RunID, "-", ""), Namespace: k.Namespace, Labels: labels},
		Spec: KubeJobSpec{
			BackoffLimit:            0,
			ActiveDeadlineSeconds:   int64((j.Timeout + JobGrace).Seconds()),
			TTLSecondsAfterFinished: 600,
			Template: KubePodTemplate{
				Metadata: kMeta{Labels: labels},
				Spec: KubePodSpec{
					RestartPolicy:                "Never",
					AutomountServiceAccountToken: false,
					EnableServiceLinks:           false,
					SecurityContext: KubePodSecurity{
						RunAsNonRoot: true, RunAsUser: uid, RunAsGroup: uid, FSGroup: uid,
						SeccompProfile: map[string]string{"type": "RuntimeDefault"},
					},
					Containers: []KubeContainer{{
						Name:            "agent",
						Image:           image,
						ImagePullPolicy: "IfNotPresent",
						Args:            []string{"agent"},
						Env: []KubeEnv{
							{Name: "SAYSO_API_URL", Value: j.APIURL},
							{Name: "SAYSO_RUN_TOKEN", Value: j.Token},
							{Name: "SAYSO_RUN_ID", Value: j.RunID},
							{Name: "HOME", Value: "/home/sayso"},
						},
						WorkingDir: "/work",
						Resources:  res,
						SecurityContext: KubeContainerSecurity{
							RunAsNonRoot: true, ReadOnlyRootFilesystem: true, AllowPrivilegeEscalation: false,
							Capabilities: map[string][]string{"drop": {"ALL"}},
						},
						VolumeMounts: []KubeMount{{Name: "work", MountPath: "/work"}, {Name: "home", MountPath: "/home/sayso"}, {Name: "tmp", MountPath: "/tmp"}},
					}},
					Volumes: []KubeEmptyDirVolume{{Name: "work"}, {Name: "home"}, {Name: "tmp"}},
				},
			},
		},
	}, nil
}

// quantity is the syntax of a Kubernetes resource quantity.
var quantity = regexp.MustCompile(`^([+-]?[0-9.]+)([eEinumkKMGTP]*[-+]?[0-9]*)$`)

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
	var created KubeJob
	if err := k.Client.do(ctx, http.MethodPost, jobsPath(k.Namespace), nil, job, &created); err != nil {
		return nil, fmt.Errorf("create Job: %w", err)
	}
	return &kubeJob{k: k, name: created.Metadata.Name, runID: j.RunID}, nil
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
	// A failed status call can be a short API server outage: the run goes
	// on, and only repeated failures end it, with the Job deleted.
	fails := 0
	for {
		reason, done, err := h.status(ctx)
		switch {
		case done:
			if reason == ExitImagePullFailed {
				_ = h.Stop(context.WithoutCancel(ctx))
			}
			return reason, err
		case err != nil && ctx.Err() == nil:
			if fails++; fails >= MaxStatusFailures {
				_ = h.Stop(context.WithoutCancel(ctx))
				return ExitFailed, err
			}
		case err != nil:
		default:
			fails = 0
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
	var job KubeJob
	err := h.k.Client.do(ctx, http.MethodGet, jobsPath(h.k.Namespace)+"/"+url.PathEscape(h.name), nil, nil, &job)
	if IsNotFound(err) {
		if h.stopped {
			return ExitCancelled, true, nil
		}
		return ExitFailed, true, errors.New("the Job was deleted")
	}
	if err != nil {
		return "", false, err
	}
	var pods kubePodList
	if err := h.k.Client.do(ctx, http.MethodGet, "/api/v1/namespaces/"+url.PathEscape(h.k.Namespace)+"/pods",
		url.Values{"labelSelector": {RunLabel + "=" + h.runID}}, nil, &pods); err != nil {
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
		if c.Status != "True" {
			continue
		}
		switch c.Type {
		case "Complete":
			return ExitCompleted, true, nil
		case "Failed":
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
	err := h.k.Client.do(ctx, http.MethodDelete, jobsPath(h.k.Namespace)+"/"+url.PathEscape(h.name), nil,
		map[string]string{"kind": "DeleteOptions", "apiVersion": "v1", "propagationPolicy": "Background"}, nil)
	if IsNotFound(err) {
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
		review := map[string]any{"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectAccessReview",
			"spec": map[string]any{"resourceAttributes": map[string]string{
				"namespace": k.Namespace, "group": group, "resource": p.Resource, "subresource": p.Subresource, "verb": p.Verb,
			}}}
		var r struct {
			Status struct {
				Allowed bool `json:"allowed"`
			} `json:"status"`
		}
		if err := k.Client.do(ctx, http.MethodPost, "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews", nil, review, &r); err != nil {
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
