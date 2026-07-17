package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// KubernetesRuntime implements Runtime using kubectl CLI.
// This avoids pulling in the heavyweight k8s.io/client-go dependency while
// still supporting Kubernetes as a first-class deployment target.
type KubernetesRuntime struct {
	kubeContext   string // --context flag (empty = default)
	kubeNamespace string // --namespace flag (empty = "default")
	kubeConfig    string // --kubeconfig flag (empty = default)
	logger        *zap.Logger

	// execCmd is an optional test hook that overrides kubectl command execution.
	// When nil, execCommand falls back to exec.CommandContext.
	// Signature: func(ctx, args, stdin) (output, error)
	execCmd func(ctx context.Context, args []string, stdin []byte) (string, error)
}

// NewKubernetesRuntime creates a new Kubernetes runtime backed by kubectl.
func NewKubernetesRuntime(kubeContext, kubeNamespace, kubeConfig string, logger *zap.Logger) *KubernetesRuntime {
	if kubeNamespace == "" {
		kubeNamespace = "default"
	}
	return &KubernetesRuntime{
		kubeContext:   kubeContext,
		kubeNamespace: kubeNamespace,
		kubeConfig:    kubeConfig,
		logger:        logger,
	}
}

func (k *KubernetesRuntime) Type() domain.RuntimeType {
	return domain.RuntimeTypeK8s
}

// kubePod is a subset of the Kubernetes Pod JSON structure.
type kubePod struct {
	Metadata struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		Containers []struct {
			Name    string   `json:"name"`
			Image   string   `json:"image"`
			Command []string `json:"command,omitempty"`
			Env     []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"env,omitempty"`
			Ports []struct {
				ContainerPort int32  `json:"containerPort"`
				Protocol      string `json:"protocol,omitempty"`
			} `json:"ports,omitempty"`
		} `json:"containers"`
		RestartPolicy string `json:"restartPolicy,omitempty"`
	} `json:"spec"`
	Status struct {
		Phase             string `json:"phase"`
		ContainerStatuses []struct {
			ContainerID string `json:"containerID"`
			Image       string `json:"image"`
			ImageID     string `json:"imageID"`
			Ready       bool   `json:"ready"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

type kubePodList struct {
	Items []kubePod `json:"items"`
}

// Observe queries Kubernetes for pods with the bahia.service label.
func (k *KubernetesRuntime) Observe(ctx context.Context, serviceID, envID uuid.UUID, serviceName string) (*domain.RuntimeObservation, error) {
	args := k.baseArgs("get", "pods",
		"-l", fmt.Sprintf("bahia.service=%s", serviceName),
		"-o", "json",
	)

	output, err := k.runCommand(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("kubectl get pods: %w", err)
	}

	var podList kubePodList
	if err := json.Unmarshal([]byte(output), &podList); err != nil {
		return nil, fmt.Errorf("parsing pod list: %w", err)
	}

	if len(podList.Items) == 0 {
		return &domain.RuntimeObservation{
			ServiceID:     serviceID,
			EnvironmentID: envID,
			HealthStatus:  domain.HealthStatusStopped,
			Source:        "kubernetes",
			ObservedAt:    time.Now().UTC(),
		}, nil
	}

	pod := podList.Items[0]
	health := mapK8sPhase(pod.Status.Phase)

	var digest, image, containerID string
	if len(pod.Status.ContainerStatuses) > 0 {
		cs := pod.Status.ContainerStatuses[0]
		digest = extractDigest(cs.ImageID)
		image = cs.Image
		containerID = cs.ContainerID
		if cs.Ready {
			health = domain.HealthStatusHealthy
		}
	}
	if image == "" && len(pod.Spec.Containers) > 0 {
		image = pod.Spec.Containers[0].Image
	}

	return &domain.RuntimeObservation{
		ServiceID:           serviceID,
		EnvironmentID:       envID,
		ObservedImageDigest: digest,
		ObservedImageRepo:   image,
		ObservedContainerID: containerID,
		ObservedHost:        k.kubeNamespace,
		HealthStatus:        health,
		Source:              "kubernetes",
		ObservedAt:          time.Now().UTC(),
	}, nil
}

// Deploy creates or updates a Kubernetes deployment for the service.
// Uses `kubectl set image` for existing deployments or `kubectl create deployment` for new ones.
func (k *KubernetesRuntime) Deploy(ctx context.Context, serviceName, image string, opts DeployOptions) error {
	// Check if deployment already exists.
	checkArgs := k.baseArgs("get", "deployment", serviceName, "--ignore-not-found", "-o", "name")
	existing, err := k.runCommand(ctx, checkArgs...)
	if err != nil {
		return fmt.Errorf("checking deployment: %w", err)
	}

	if strings.TrimSpace(existing) != "" {
		// Update existing deployment image.
		setArgs := k.baseArgs("set", "image",
			fmt.Sprintf("deployment/%s", serviceName),
			fmt.Sprintf("%s=%s", serviceName, image),
		)
		if _, err := k.runCommand(ctx, setArgs...); err != nil {
			return fmt.Errorf("kubectl set image: %w", err)
		}
	} else {
		// Create new deployment.
		createArgs := k.baseArgs("create", "deployment", serviceName,
			"--image", image,
		)
		if _, err := k.runCommand(ctx, createArgs...); err != nil {
			return fmt.Errorf("kubectl create deployment: %w", err)
		}
	}

	// Apply labels for Bahia tracking.
	labelArgs := k.baseArgs("label", "deployment", serviceName,
		fmt.Sprintf("bahia.service=%s", serviceName),
		"--overwrite",
	)
	if _, err := k.runCommand(ctx, labelArgs...); err != nil {
		return fmt.Errorf("applying bahia label: %w", err)
	}

	// Set environment variables if provided.
	if len(opts.Environment) > 0 {
		envPairs := make([]string, 0, len(opts.Environment))
		for key, val := range opts.Environment {
			envPairs = append(envPairs, fmt.Sprintf("%s=%s", key, val))
		}
		envArgs := k.baseArgs("set", "env",
			fmt.Sprintf("deployment/%s", serviceName),
		)
		envArgs = append(envArgs, envPairs...)
		if _, err := k.runCommand(ctx, envArgs...); err != nil {
			return fmt.Errorf("setting deployment environment: %w", err)
		}
	}

	// Restart rollout to pick up changes.
	restartArgs := k.baseArgs("rollout", "restart",
		fmt.Sprintf("deployment/%s", serviceName),
	)
	if _, err := k.runCommand(ctx, restartArgs...); err != nil {
		return fmt.Errorf("restarting deployment rollout: %w", err)
	}

	k.logger.Info("kubernetes deployment updated",
		zap.String("service", serviceName),
		zap.String("image", image),
		zap.String("namespace", k.kubeNamespace),
	)
	return nil
}

// Undeploy deletes the Kubernetes deployment for a service.
func (k *KubernetesRuntime) Undeploy(ctx context.Context, serviceName string) error {
	args := k.baseArgs("delete", "deployment", serviceName, "--ignore-not-found")
	if _, err := k.runCommand(ctx, args...); err != nil {
		return fmt.Errorf("kubectl delete deployment: %w", err)
	}
	return nil
}

// StreamLogs streams pod logs for a service.
func (k *KubernetesRuntime) StreamLogs(ctx context.Context, serviceName string, opts LogOptions) (<-chan LogEntry, error) {
	logArgs := []string{"logs",
		"-l", fmt.Sprintf("bahia.service=%s", serviceName),
		"--timestamps",
	}
	if opts.Follow {
		logArgs = append(logArgs, "-f")
	}
	if opts.Tail > 0 {
		logArgs = append(logArgs, fmt.Sprintf("--tail=%d", opts.Tail))
	} else {
		logArgs = append(logArgs, "--tail=100")
	}

	args := k.baseArgs(logArgs...)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("getting stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting kubectl logs: %w", err)
	}

	ch := make(chan LogEntry, 64)
	go func() {
		defer close(ch)
		defer cmd.Wait() //nolint:errcheck

		buf := make([]byte, 8192)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				for _, line := range strings.Split(string(buf[:n]), "\n") {
					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}

					entry := LogEntry{
						Timestamp: time.Now().UTC(),
						Stream:    "stdout",
						Message:   line,
					}
					// Try to parse K8s timestamp prefix: "2024-01-15T10:30:00.123Z message"
					if len(line) > 30 {
						if ts, parseErr := time.Parse(time.RFC3339Nano, line[:30]); parseErr == nil {
							entry.Timestamp = ts
							entry.Message = strings.TrimSpace(line[30:])
						}
					}

					select {
					case ch <- entry:
					case <-ctx.Done():
						return
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()

	return ch, nil
}

// baseArgs builds kubectl arguments with context, namespace, and kubeconfig flags.
func (k *KubernetesRuntime) baseArgs(subArgs ...string) []string {
	args := []string{"kubectl"}
	if k.kubeConfig != "" {
		args = append(args, "--kubeconfig", k.kubeConfig)
	}
	if k.kubeContext != "" {
		args = append(args, "--context", k.kubeContext)
	}
	if k.kubeNamespace != "" {
		args = append(args, "-n", k.kubeNamespace)
	}
	return append(args, subArgs...)
}

func (k *KubernetesRuntime) runCommand(ctx context.Context, args ...string) (string, error) {
	return k.execCommand(ctx, args, nil)
}

// execCommand is the single execution path for all kubectl invocations.
// It honours the execCmd test hook when set, and falls back to
// exec.CommandContext with optional stdin support.
func (k *KubernetesRuntime) execCommand(ctx context.Context, args []string, stdin []byte) (string, error) {
	if k.execCmd != nil {
		return k.execCmd(ctx, args, stdin)
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%s: %w (output: %s)", args[0], err, string(output))
	}
	return string(output), nil
}

func mapK8sPhase(phase string) domain.HealthStatus {
	switch strings.ToLower(phase) {
	case "running":
		return domain.HealthStatusHealthy
	case "pending":
		return domain.HealthStatusStarting
	case "succeeded":
		return domain.HealthStatusStopped
	case "failed":
		return domain.HealthStatusUnhealthy
	case "unknown":
		return domain.HealthStatusUnknown
	default:
		return domain.HealthStatusUnknown
	}
}

// Compile-time interface check.
var _ Runtime = (*KubernetesRuntime)(nil)
