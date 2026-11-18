package runner

import (
	"context"
	"encoding/json"
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

// Scheduling places the agent Pods on nodes: tolerations for tainted
// nodes and a node selector (settings).
type Scheduling struct {
	Tolerations  []KubeToleration
	NodeSelector map[string]string
}

// ParseTolerations reads a JSON array of Kubernetes tolerations.
func ParseTolerations(s string) ([]KubeToleration, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var ts []KubeToleration
	if err := strictJSON(s, &ts); err != nil {
		return nil, fmt.Errorf("not a JSON array of tolerations: %w", err)
	}
	for i, t := range ts {
		if err := checkToleration(t); err != nil {
			return nil, fmt.Errorf("toleration %d: %w", i, err)
		}
	}
	return ts, nil
}

// ParseNodeSelector reads a JSON object of node label names and values.
func ParseNodeSelector(s string) (map[string]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var m map[string]string
	if err := strictJSON(s, &m); err != nil {
		return nil, fmt.Errorf("not a JSON object of string labels: %w", err)
	}
	for k := range m {
		if k == "" {
			return nil, errors.New("a label name is empty")
		}
	}
	return m, nil
}

func strictJSON(s string, v any) error {
	d := json.NewDecoder(strings.NewReader(s))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.More() {
		return errors.New("text after the JSON value")
	}
	return nil
}

func checkToleration(t KubeToleration) error {
	switch t.Operator {
	case "", "Equal":
		if t.Key == "" {
			return errors.New("operator Equal needs a key")
		}
	case "Exists":
		if t.Value != "" {
			return errors.New("operator Exists takes no value")
		}
	default:
		return fmt.Errorf("operator %q is not Equal or Exists", t.Operator)
	}
	switch t.Effect {
	case "", "NoSchedule", "PreferNoSchedule", "NoExecute":
	default:
		return fmt.Errorf("effect %q is not NoSchedule, PreferNoSchedule or NoExecute", t.Effect)
	}
	if t.TolerationSeconds != nil && t.Effect != "NoExecute" {
		return errors.New("tolerationSeconds needs effect NoExecute")
	}
	return nil
}

// Kubernetes runs each agent run as a Job in one namespace (§8.2).
type Kubernetes struct {
	Client     *KubeClient
	Namespace  string
	Image      string
	Resources  Resources
	Scheduling Scheduling
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
	for i, t := range k.Scheduling.Tolerations {
		if err := checkToleration(t); err != nil {
			return nil, fmt.Errorf("kubernetes runner: toleration %d: %w", i, err)
		}
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
					Volumes:      []KubeEmptyDirVolume{{Name: "work"}, {Name: "home"}, {Name: "tmp"}},
					Tolerations:  k.Scheduling.Tolerations,
					NodeSelector: k.Scheduling.NodeSelector,
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
	// scheduleTimeout is Job.ScheduleTimeout.
	scheduleTimeout time.Duration
	// unscheduled is the scheduler message while the Pod has no node, and
	// unscheduledAt is when Wait first saw it.
	unscheduled   string
	unscheduledAt time.Time
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
	return &kubeJob{k: k, name: created.Metadata.Name, runID: j.RunID, scheduleTimeout: j.ScheduleTimeout}, nil
}

func (h *kubeJob) Ref() string { return h.k.Namespace + "/" + h.name }

// Wait watches the Job and its Pod until the run ends and reports the exit
// reason: completed, failed, deadline, evicted, image_pull_failed or
// unschedulable.
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
			if reason == ExitImagePullFailed || reason == ExitUnschedulable {
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
		for _, c := range p.Status.Conditions {
			if c.Type != "PodScheduled" {
				continue
			}
			if c.Status == "True" {
				h.unscheduled = ""
			} else if c.Reason == "Unschedulable" {
				if h.unscheduled == "" {
					h.unscheduledAt = time.Now()
				}
				h.unscheduled = c.Message
			}
		}
	}
	if h.unscheduled != "" && h.scheduleTimeout > 0 && time.Since(h.unscheduledAt) >= h.scheduleTimeout {
		return ExitUnschedulable, true, fmt.Errorf("the agent Pod found no node in %s: %s%s", h.scheduleTimeout, h.unscheduled, schedulingHint)
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
				if h.unscheduled != "" {
					return ExitDeadline, true, fmt.Errorf("the agent Pod found no node: %s%s", h.unscheduled, schedulingHint)
				}
				return ExitDeadline, true, nil
			}
			return ExitFailed, true, nil
		}
	}
	return "", false, nil
}

const schedulingHint = "; set SAYSO_K8S_TOLERATIONS or SAYSO_K8S_NODE_SELECTOR for the agent Pods"

// UnscheduledFor is how long an agent Pod may wait for a node before
// Unscheduled reports it.
const UnscheduledFor = time.Minute

// Unscheduled returns an error when an agent Pod in the namespace has waited
// for a node for longer than UnscheduledFor.
func (k *Kubernetes) Unscheduled(ctx context.Context) error {
	var pods kubePodList
	if err := k.Client.do(ctx, http.MethodGet, "/api/v1/namespaces/"+url.PathEscape(k.Namespace)+"/pods",
		url.Values{"labelSelector": {"app.kubernetes.io/component=agent-run"}}, nil, &pods); err != nil {
		return err
	}
	for _, p := range pods.Items {
		if p.Status.Phase != "Pending" {
			continue
		}
		for _, c := range p.Status.Conditions {
			if c.Type == "PodScheduled" && c.Status == "False" && c.Reason == "Unschedulable" && time.Since(c.LastTransitionTime) > UnscheduledFor {
				return fmt.Errorf("agent Pod %s has no node since %s: %s%s", p.Metadata.Name, c.LastTransitionTime.UTC().Format(time.RFC3339), c.Message, schedulingHint)
			}
		}
	}
	return nil
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
