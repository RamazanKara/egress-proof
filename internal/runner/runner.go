package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/RamazanKara/egress-proof/internal/probe"
	"github.com/RamazanKara/egress-proof/internal/report"
	"github.com/RamazanKara/egress-proof/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

const runAnnotation = "egress-proof.io/run"
const trafficGate = "egress-proof.io/traffic-disabled"

func Run(ctx context.Context, client typedcorev1.CoreV1Interface, r *report.Report) {
	ns, err := client.Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		r.Errors = append(r.Errors, fmt.Sprintf("cluster identity: %v", err))
		return
	}
	r.Cluster.KubeSystemUID = string(ns.UID)
	pods, err := client.Pods(r.Spec.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: r.Spec.PodSelector, FieldSelector: "status.phase=Running",
	})
	if err != nil {
		r.Errors = append(r.Errors, fmt.Sprintf("select target pods: %v", err))
		return
	}
	slices.SortFunc(pods.Items, func(a, b corev1.Pod) int { return strings.Compare(a.Name, b.Name) })
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil || pod.Annotations[runAnnotation] != "" {
			continue
		}
		if ctx.Err() != nil {
			r.Errors = append(r.Errors, ctx.Err().Error())
			return
		}
		r.Targets = append(r.Targets, report.Target{
			Namespace: pod.Namespace, Pod: pod.Name, UID: string(pod.UID), Labels: maps.Clone(pod.Labels),
			ServiceAccount: pod.Spec.ServiceAccountName, Node: pod.Spec.NodeName,
		})
		target := &r.Targets[len(r.Targets)-1]
		observations, err := runPod(ctx, client, &pod, r.Spec.Checks(), r.ProbeImage, target, &r.Errors)
		for i, check := range r.Spec.Checks() {
			var obs probe.Observation
			if err != nil {
				now := time.Now().UTC()
				obs = probe.Observation{Destination: check.Destination, StartedAt: now, FinishedAt: now, Outcome: "error", Error: err.Error(), Stages: []probe.Stage{}}
			} else {
				obs = observations[i]
			}
			r.Checks = append(r.Checks, report.Check{
				Target: pod.Namespace + "/" + pod.Name, Expected: check.Expected, Verdict: probe.Verdict(check.Expected, obs), Observation: obs,
			})
		}
	}
	if len(r.Targets) == 0 {
		r.Errors = append(r.Errors, "no running target pods matched the namespace and podSelector")
	}
}

func probePod(source *corev1.Pod, checks []spec.Check, image string) *corev1.Pod {
	id := strings.ToLower(rand.Text())
	destinations := make([]string, len(checks))
	for i, check := range checks {
		destinations[i] = check.Destination
	}
	data, _ := json.Marshal(destinations)
	no, yes := false, true
	zero, uid := int64(0), int64(65532)
	lifetime := int64((2*time.Minute + time.Duration(len(checks))*probe.Timeout + 5*time.Second) / time.Second)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "egress-proof-" + id, Namespace: source.Namespace, Labels: maps.Clone(source.Labels),
			Annotations: map[string]string{runAnnotation: id},
			// A controller owner prevents ReplicaSets from adopting this identically labelled pod.
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: source.Name, UID: source.UID, Controller: &yes, BlockOwnerDeletion: &no}},
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: source.Spec.ServiceAccountName, AutomountServiceAccountToken: &no,
			NodeName: source.Spec.NodeName, Tolerations: source.Spec.Tolerations, ImagePullSecrets: source.Spec.ImagePullSecrets,
			DNSPolicy: source.Spec.DNSPolicy, DNSConfig: source.Spec.DNSConfig.DeepCopy(), HostAliases: source.Spec.HostAliases,
			RestartPolicy: corev1.RestartPolicyNever, ActiveDeadlineSeconds: &lifetime, TerminationGracePeriodSeconds: &zero,
			EnableServiceLinks: &no,
			// The gate stays unset, so normal Services do not send application traffic here.
			ReadinessGates: []corev1.PodReadinessGate{{ConditionType: trafficGate}},
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot: &yes, RunAsUser: &uid,
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{{
				Name: "probe", Image: image, ImagePullPolicy: corev1.PullIfNotPresent,
				Env: []corev1.EnvVar{{Name: "EGRESS_PROOF_DESTINATIONS", Value: string(data)}},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes,
					Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("16Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
				},
			}},
		},
	}
}

func runPod(ctx context.Context, client typedcorev1.CoreV1Interface, source *corev1.Pod, checks []spec.Check, image string, target *report.Target, runErrors *[]string) ([]probe.Observation, error) {
	if source.Spec.HostNetwork || source.Spec.OS != nil && source.Spec.OS.Name != corev1.Linux {
		return nil, fmt.Errorf("target %s requires a Linux pod network; hostNetwork and Windows pods are unsupported", source.Name)
	}
	pod := probePod(source, checks, image)
	target.ProbePod = pod.Name
	// Use a fresh context even on interruption; identify the pod before deleting it.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := cleanup(cleanupCtx, client, pod); err != nil {
			*runErrors = append(*runErrors, fmt.Sprintf("cleanup %s/%s: %v", pod.Namespace, pod.Name, err))
		} else {
			target.CleanedUp = true
		}
	}()
	created, err := client.Pods(pod.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create probe: %w", err)
	}
	target.ProbeUID = string(created.UID)
	pod.UID = created.UID
	if !maps.Equal(created.Labels, source.Labels) || created.Spec.ServiceAccountName != source.Spec.ServiceAccountName || created.Spec.HostNetwork ||
		created.Annotations[runAnnotation] != pod.Annotations[runAnnotation] ||
		len(created.Spec.Containers) != 1 || len(created.Spec.InitContainers) != 0 || created.Spec.Containers[0].Image != image {
		return nil, fmt.Errorf("admission changed probe identity or injected containers; cannot attest this target")
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, time.Duration(*pod.Spec.ActiveDeadlineSeconds)*time.Second)
	defer cancel()
	var lastState string
	err = wait.PollUntilContextCancel(deadlineCtx, 500*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		current, err := client.Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		lastState = string(current.Status.Phase) + ": " + current.Status.Reason + " " + current.Status.Message
		for _, status := range current.Status.ContainerStatuses {
			if status.Name != "probe" {
				continue
			}
			target.ProbeImageID = status.ImageID
			if t := status.State.Terminated; t != nil {
				if t.ExitCode != 0 {
					return false, fmt.Errorf("probe exited %d: %s %s", t.ExitCode, t.Reason, t.Message)
				}
				return true, nil
			}
			if w := status.State.Waiting; w != nil {
				lastState = w.Reason + ": " + w.Message
				if w.Reason == "ErrImagePull" || w.Reason == "ImagePullBackOff" || w.Reason == "CreateContainerConfigError" {
					return false, fmt.Errorf("probe startup: %s", lastState)
				}
			}
		}
		if current.Status.Phase == corev1.PodFailed {
			return false, fmt.Errorf("probe failed: %s", lastState)
		}
		return false, nil
	})
	if err != nil {
		return nil, fmt.Errorf("wait for probe (%s): %w", lastState, err)
	}
	limit := int64(4 << 20)
	data, err := client.Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: "probe", LimitBytes: &limit}).DoRaw(deadlineCtx)
	if err != nil {
		return nil, fmt.Errorf("read probe logs: %w", err)
	}
	return decodeObservations(data, checks)
}

func decodeObservations(data []byte, checks []spec.Check) ([]probe.Observation, error) {
	var observations []probe.Observation
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&observations); err != nil {
		return nil, fmt.Errorf("decode probe evidence: %w", err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || len(observations) != len(checks) {
		return nil, fmt.Errorf("probe evidence is incomplete or contains extra output")
	}
	for i, obs := range observations {
		if obs.Destination != checks[i].Destination || obs.StartedAt.IsZero() || obs.FinishedAt.Before(obs.StartedAt) ||
			(len(obs.Stages) == 0 && obs.Outcome != "error") ||
			(obs.Outcome != "reachable" && obs.Outcome != "unreachable" && obs.Outcome != "error") {
			return nil, fmt.Errorf("invalid probe evidence for check %d", i+1)
		}
	}
	return observations, nil
}

func cleanup(ctx context.Context, client typedcorev1.CoreV1Interface, pod *corev1.Pod) error {
	pods := client.Pods(pod.Namespace)
	current, err := pods.Get(ctx, pod.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if pod.UID != "" && current.UID != pod.UID || pod.UID == "" && current.Annotations[runAnnotation] != pod.Annotations[runAnnotation] {
		return fmt.Errorf("refusing to delete a pod belonging to another run")
	}
	zero := int64(0)
	deletedUID := current.UID
	err = pods.Delete(ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero, Preconditions: &metav1.Preconditions{UID: &deletedUID}})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		current, err := pods.Get(ctx, pod.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return current.UID != deletedUID, nil
	})
}
