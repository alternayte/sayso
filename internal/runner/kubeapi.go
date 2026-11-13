package runner

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// KubeClient calls the few Kubernetes API paths the runner needs: Jobs,
// Pods and the access review, as JSON. The generated clientset and API
// types would add most of the image size (NFR-003).
type KubeClient struct {
	http *http.Client
	host string
}

// NewKubeClient returns a client for an API server URL with an HTTP client
// that already authenticates, for example one built from a kubeconfig.
func NewKubeClient(host string, hc *http.Client) *KubeClient {
	return &KubeClient{http: hc, host: strings.TrimRight(host, "/")}
}

// Service account files of a Pod.
const (
	saTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	saCAFile    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
)

// InClusterKubeClient uses the service account of the Pod. It reads the
// token again each minute, because the kubelet rotates bound tokens.
func InClusterKubeClient() (*KubeClient, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("kubernetes runner: not in a cluster (KUBERNETES_SERVICE_HOST is not set)")
	}
	ca, err := os.ReadFile(saCAFile)
	if err != nil {
		return nil, fmt.Errorf("kubernetes runner: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("kubernetes runner: the service account CA has no certificate")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil // the API server is inside the cluster
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	tok := &tokenTransport{file: saTokenFile, base: tr}
	if _, err := tok.token(); err != nil {
		return nil, err
	}
	return NewKubeClient("https://"+net.JoinHostPort(host, port), &http.Client{Transport: tok, Timeout: 30 * time.Second}), nil
}

type tokenTransport struct {
	file string
	base http.RoundTripper
	mu   sync.Mutex
	tok  string
	read time.Time
}

func (t *tokenTransport) token() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tok != "" && time.Since(t.read) < time.Minute {
		return t.tok, nil
	}
	b, err := os.ReadFile(t.file)
	if err != nil {
		return "", fmt.Errorf("kubernetes runner: service account token: %w", err)
	}
	t.tok, t.read = strings.TrimSpace(string(b)), time.Now()
	return t.tok, nil
}

func (t *tokenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tok, err := t.token()
	if err != nil {
		return nil, err
	}
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+tok)
	return t.base.RoundTrip(r)
}

// KubeError is a failed API call with the Status of the API server.
type KubeError struct {
	Code   int
	Reason string
	Msg    string
}

func (e *KubeError) Error() string {
	return fmt.Sprintf("kubernetes: %d %s: %s", e.Code, e.Reason, e.Msg)
}

// IsNotFound reports whether err is a 404 from the API server.
func IsNotFound(err error) bool {
	var k *KubeError
	return errors.As(err, &k) && k.Code == http.StatusNotFound
}

func (c *KubeClient) do(ctx context.Context, method, path string, q url.Values, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	u := c.host + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var st struct {
			Message string `json:"message"`
			Reason  string `json:"reason"`
		}
		_ = json.Unmarshal(b, &st)
		if st.Message == "" {
			st.Message = strings.TrimSpace(string(b))
		}
		return &KubeError{Code: resp.StatusCode, Reason: st.Reason, Msg: st.Message}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

func jobsPath(ns string) string { return "/apis/batch/v1/namespaces/" + url.PathEscape(ns) + "/jobs" }

// The parts of the Kubernetes API objects that the runner writes or reads.

type kMeta struct {
	Name      string            `json:"name,omitempty"`
	Namespace string            `json:"namespace,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// KubeJob is a batch/v1 Job.
type KubeJob struct {
	APIVersion string        `json:"apiVersion"`
	Kind       string        `json:"kind"`
	Metadata   kMeta         `json:"metadata"`
	Spec       KubeJobSpec   `json:"spec"`
	Status     KubeJobStatus `json:"status,omitzero"`
}

type KubeJobSpec struct {
	BackoffLimit            int32           `json:"backoffLimit"`
	ActiveDeadlineSeconds   int64           `json:"activeDeadlineSeconds"`
	TTLSecondsAfterFinished int32           `json:"ttlSecondsAfterFinished"`
	Template                KubePodTemplate `json:"template"`
}

type KubePodTemplate struct {
	Metadata kMeta       `json:"metadata"`
	Spec     KubePodSpec `json:"spec"`
}

type KubePodSpec struct {
	RestartPolicy                string               `json:"restartPolicy"`
	AutomountServiceAccountToken bool                 `json:"automountServiceAccountToken"`
	EnableServiceLinks           bool                 `json:"enableServiceLinks"`
	SecurityContext              KubePodSecurity      `json:"securityContext"`
	Containers                   []KubeContainer      `json:"containers"`
	Volumes                      []KubeEmptyDirVolume `json:"volumes"`
}

type KubePodSecurity struct {
	RunAsNonRoot   bool              `json:"runAsNonRoot"`
	RunAsUser      int64             `json:"runAsUser"`
	RunAsGroup     int64             `json:"runAsGroup"`
	FSGroup        int64             `json:"fsGroup"`
	SeccompProfile map[string]string `json:"seccompProfile"`
}

type KubeContainer struct {
	Name            string                `json:"name"`
	Image           string                `json:"image"`
	ImagePullPolicy string                `json:"imagePullPolicy"`
	Args            []string              `json:"args"`
	Env             []KubeEnv             `json:"env"`
	WorkingDir      string                `json:"workingDir"`
	Resources       KubeResources         `json:"resources"`
	SecurityContext KubeContainerSecurity `json:"securityContext"`
	VolumeMounts    []KubeMount           `json:"volumeMounts"`
}

type KubeEnv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type KubeResources struct {
	Requests map[string]string `json:"requests,omitempty"`
	Limits   map[string]string `json:"limits,omitempty"`
}

type KubeContainerSecurity struct {
	RunAsNonRoot             bool                `json:"runAsNonRoot"`
	ReadOnlyRootFilesystem   bool                `json:"readOnlyRootFilesystem"`
	AllowPrivilegeEscalation bool                `json:"allowPrivilegeEscalation"`
	Capabilities             map[string][]string `json:"capabilities"`
}

type KubeMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
}

type KubeEmptyDirVolume struct {
	Name     string   `json:"name"`
	EmptyDir struct{} `json:"emptyDir"`
}

type KubeJobStatus struct {
	Conditions []KubeCondition `json:"conditions,omitempty"`
}

type KubeCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type kubePodList struct {
	Items []struct {
		Status struct {
			Reason            string `json:"reason"`
			ContainerStatuses []struct {
				State struct {
					Waiting *struct {
						Reason string `json:"reason"`
					} `json:"waiting"`
				} `json:"state"`
			} `json:"containerStatuses"`
		} `json:"status"`
	} `json:"items"`
}
