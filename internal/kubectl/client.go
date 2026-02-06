package kubectl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"kui/internal/types"
)

type kList[T any] struct {
	Items []T `json:"items"`
}

type kNamespace struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
}

type kMeta struct {
	Name string `json:"name"`
}

type kWorkload struct {
	Metadata kMeta `json:"metadata"`
	Spec     struct {
		Selector struct {
			MatchLabels map[string]string `json:"matchLabels"`
		} `json:"selector"`
	} `json:"spec"`
}

type kPod struct {
	Metadata kMeta `json:"metadata"`
	Spec     struct {
		Containers []struct {
			Name string `json:"name"`
		} `json:"containers"`
	} `json:"spec"`
}

func Run(args ...string) ([]byte, []byte, error) {
	cmd := exec.Command("kubectl", args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	e := cmd.Run()
	return out.Bytes(), errb.Bytes(), e
}

func GetNamespaces() ([]string, error) {
	out, errb, err := Run("get", "ns", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("kubectl get ns: %w: %s", err, strings.TrimSpace(string(errb)))
	}
	var parsed kList[kNamespace]
	if e := json.Unmarshal(out, &parsed); e != nil {
		return nil, e
	}
	var res []string
	for _, it := range parsed.Items {
		res = append(res, it.Metadata.Name)
	}
	sort.Strings(res)
	return res, nil
}

func GetNamesInNS(namespace string, kind types.ResType) ([]string, error) {
	args := []string{"-n", namespace, "get", string(kind), "-o", "json"}
	out, errb, err := Run(args...)
	if err != nil {
		return nil, fmt.Errorf("kubectl get %s: %w: %s", kind, err, strings.TrimSpace(string(errb)))
	}
	var parsed struct {
		Items []struct {
			Metadata kMeta `json:"metadata"`
		} `json:"items"`
	}
	if e := json.Unmarshal(out, &parsed); e != nil {
		return nil, e
	}
	var res []string
	for _, it := range parsed.Items {
		res = append(res, it.Metadata.Name)
	}
	sort.Strings(res)
	return res, nil
}

func GetSelectorForWorkload(namespace string, kind types.ResType, name string) (string, error) {
	if kind != types.RtDeployment && kind != types.RtStatefulSet {
		return "", errors.New("selector only supported for deployment/statefulset")
	}
	out, errb, err := Run("-n", namespace, "get", string(kind), name, "-o", "json")
	if err != nil {
		return "", fmt.Errorf("kubectl get %s/%s: %w: %s", kind, name, err, strings.TrimSpace(string(errb)))
	}
	var wl kWorkload
	if e := json.Unmarshal(out, &wl); e != nil {
		return "", e
	}
	if len(wl.Spec.Selector.MatchLabels) == 0 {
		return "", errors.New("matchLabels empty; kan ikke auto-finne pods")
	}
	keys := make([]string, 0, len(wl.Spec.Selector.MatchLabels))
	for k := range wl.Spec.Selector.MatchLabels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, wl.Spec.Selector.MatchLabels[k]))
	}
	return strings.Join(parts, ","), nil
}

func GetPodsBySelector(namespace, selector string) ([]string, error) {
	out, errb, err := Run("-n", namespace, "get", "pods", "-l", selector, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("kubectl get pods -l %q: %w: %s", selector, err, strings.TrimSpace(string(errb)))
	}
	var parsed kList[kPod]
	if e := json.Unmarshal(out, &parsed); e != nil {
		return nil, e
	}
	var res []string
	for _, it := range parsed.Items {
		res = append(res, it.Metadata.Name)
	}
	sort.Strings(res)
	return res, nil
}

func GetContainers(namespace, pod string) ([]string, error) {
	out, errb, err := Run("-n", namespace, "get", "pod", pod, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("kubectl get pod/%s: %w: %s", pod, err, strings.TrimSpace(string(errb)))
	}
	var p kPod
	if e := json.Unmarshal(out, &p); e != nil {
		return nil, e
	}
	var res []string
	for _, c := range p.Spec.Containers {
		res = append(res, c.Name)
	}
	sort.Strings(res)
	return res, nil
}

func GetPodSecurityPolicy(namespace string) (string, error) {
	args := []string{"get", "namespace", namespace, "-o", "jsonpath={.metadata.labels.pod-security\\.kubernetes\\.io/enforce}"}
	out, _, err := Run(args...)
	if err != nil {
		return "", err
	}
	policy := strings.TrimSpace(string(out))
	if policy == "" {
		return "", nil
	}
	return policy, nil
}

func SetPodSecurityPolicy(namespace, policy string) error {
	if policy == "" {
		args := []string{"label", "namespace", namespace, "pod-security.kubernetes.io/enforce-"}
		_, stderr, err := Run(args...)
		if err != nil {
			return fmt.Errorf("failed to remove PodSecurity policy label: %w (stderr: %s)", err, string(stderr))
		}
		return nil
	}

	args := []string{"label", "namespace", namespace, fmt.Sprintf("pod-security.kubernetes.io/enforce=%s", policy), "--overwrite"}
	_, stderr, err := Run(args...)
	if err != nil {
		return fmt.Errorf("failed to set PodSecurity policy: %w (stderr: %s)", err, string(stderr))
	}
	return nil
}

func CreateDebugContainer(namespace, pod, targetContainer string) (string, string, error) {
	debugName := fmt.Sprintf("kcmd-debug-%d", time.Now().Unix())

	ephemeralContainer := map[string]any{
		"name":                debugName,
		"image":               "busybox:latest",
		"targetContainerName": targetContainer,
		"command":             []string{"sleep", "3600"},
		"securityContext": map[string]any{
			"allowPrivilegeEscalation": false,
			"runAsUser":                0,
			"capabilities": map[string]any{
				"drop": []string{"ALL"},
				"add":  []string{"SYS_ADMIN", "SYS_CHROOT", "SYS_PTRACE"},
			},
			"seccompProfile": map[string]any{
				"type": "RuntimeDefault",
			},
		},
	}

	getPodCmd := []string{"get", "pod", pod, "-n", namespace, "-o", "json"}
	podJSON, _, err := Run(getPodCmd...)
	if err != nil {
		return "", "", fmt.Errorf("failed to get pod: %w", err)
	}

	var podSpec map[string]any
	if err := json.Unmarshal(podJSON, &podSpec); err != nil {
		return "", "", fmt.Errorf("failed to parse pod spec: %w", err)
	}

	spec := podSpec["spec"].(map[string]any)
	ephemeralContainers, ok := spec["ephemeralContainers"].([]any)
	if !ok {
		ephemeralContainers = []any{}
	}
	ephemeralContainers = append(ephemeralContainers, ephemeralContainer)
	spec["ephemeralContainers"] = ephemeralContainers

	patchedSpec, err := json.Marshal(podSpec)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal patched spec: %w", err)
	}

	patchCmd := []string{
		"replace", "--raw",
		fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/ephemeralcontainers", namespace, pod),
		"-f", "-",
	}

	cmd := exec.Command("kubectl", patchCmd...)
	cmd.Stdin = bytes.NewReader(patchedSpec)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("failed to create ephemeral container: %w (stderr: %s)", err, stderr.String())
	}

	// Wait for container to be running
	for range 30 {
		time.Sleep(500 * time.Millisecond)
		checkCmd := []string{"get", "pod", pod, "-n", namespace, "-o", "jsonpath={.status.ephemeralContainerStatuses[?(@.name==\"" + debugName + "\")].state.running}"}
		out, _, err := Run(checkCmd...)
		if err == nil && len(out) > 0 && string(out) != "map[]" {
			// Also verify we can exec into it
			testCmd := []string{"-n", namespace, "exec", pod, "-c", debugName, "--", "echo", "ready"}
			if _, _, execErr := Run(testCmd...); execErr == nil {
				break
			}
		}
	}

	pid := "1"

	testNsenterCmd := []string{
		"-n", namespace, "exec", pod,
		"-c", debugName,
		"--",
		"sh", "-c",
		fmt.Sprintf("nsenter -t %s -m -u -i -p -- pwd 2>&1", pid),
	}
	nsenterOut, _, nsenterErr := Run(testNsenterCmd...)

	if nsenterErr == nil && strings.TrimSpace(string(nsenterOut)) != "" {
		return debugName, fmt.Sprintf("NSENTER:%s", pid), nil
	}

	targetRoot := fmt.Sprintf("/proc/%s/root", pid)
	return debugName, targetRoot, nil
}

func ExecInPod(namespace, pod, container, cmdline, currentDir string) (string, string, error) {
	fullCmd := cmdline
	if currentDir != "" {
		fullCmd = fmt.Sprintf("cd %s && %s", currentDir, cmdline)
	}
	out, errb, err := Run("-n", namespace, "exec", pod, "-c", container, "--", "sh", "-lc", fullCmd)
	return string(out), string(errb), err
}

func ExecInDebugContainer(namespace, pod, debugContainer, targetRoot, cmdline, currentDir string) (string, string, error) {
	if _, ok := strings.CutPrefix(targetRoot, "NSENTER:"); ok {
		pid := strings.TrimPrefix(targetRoot, "NSENTER:")

		targetCmd := cmdline
		if currentDir != "" && currentDir != "~" {
			targetCmd = fmt.Sprintf("cd %s && %s", currentDir, cmdline)
		}

		escapedCmd := strings.ReplaceAll(targetCmd, "'", "'\"'\"'")
		fullCmd := fmt.Sprintf("nsenter -t %s -m -u -i -p -- sh -c '%s'", pid, escapedCmd)

		out, errb, err := Run("-n", namespace, "exec", pod, "-c", debugContainer, "--", "sh", "-c", fullCmd)
		return string(out), string(errb), err
	}

	var fullCmd string
	if currentDir != "" && currentDir != "~" {
		fullCmd = fmt.Sprintf("cd %s%s && %s", targetRoot, currentDir, cmdline)
	} else {
		fullCmd = fmt.Sprintf("cd %s && %s", targetRoot, cmdline)
	}

	out, errb, err := Run("-n", namespace, "exec", pod, "-c", debugContainer, "--", "sh", "-c", fullCmd)
	return string(out), string(errb), err
}

// WaitForDebugContainerReady waits for an ephemeral debug container to be ready
func WaitForDebugContainerReady(namespace, pod, debugContainer string) error {
	maxRetries := 10

	for range maxRetries {
		time.Sleep(500 * time.Millisecond)

		// Check container status
		podJSON, _, err := Run("get", "pod", pod, "-n", namespace, "-o", "json")
		if err != nil {
			return fmt.Errorf("failed to get pod status: %w", err)
		}

		var podStatus map[string]any
		if err := json.Unmarshal(podJSON, &podStatus); err != nil {
			return fmt.Errorf("failed to parse pod status: %w", err)
		}

		// Check ephemeralContainerStatuses
		status := podStatus["status"].(map[string]any)
		ephStatuses, ok := status["ephemeralContainerStatuses"].([]any)
		if ok {
			for _, s := range ephStatuses {
				st := s.(map[string]any)
				if st["name"] == debugContainer {
					// Check if ready
					if ready, ok := st["ready"].(bool); ok && ready {
						return nil
					}

					// Check for errors
					if state, ok := st["state"].(map[string]any); ok {
						if waiting, ok := state["waiting"].(map[string]any); ok {
							reason := waiting["reason"].(string)
							message := waiting["message"].(string)
							if reason == "CreateContainerConfigError" {
								return fmt.Errorf("debug container failed to start: %s\n\nThis namespace likely has additional policy enforcement (Kyverno/OPA) preventing root containers.\nSuggested workaround: temporarily disable the policy or use a non-root debug image", message)
							}
						}
					}
				}
			}
		}

		// Try to execute a simple command
		_, _, execErr := Run("-n", namespace, "exec", pod, "-c", debugContainer, "--", "echo", "ready")
		if execErr == nil {
			return nil
		}
	}

	return errors.New("debug container did not become ready in time")
}
