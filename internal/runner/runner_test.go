package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RamazanKara/egress-proof/internal/probe"
	"github.com/RamazanKara/egress-proof/internal/report"
	"github.com/RamazanKara/egress-proof/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

func sourcePod() corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "client", Namespace: "team", UID: "source-uid", Labels: map[string]string{"app": "payments", "version": "one"}},
		Spec:       corev1.PodSpec{ServiceAccountName: "payments", NodeName: "node-a", DNSPolicy: corev1.DNSClusterFirst},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func TestProbeIdentityAndLifetime(t *testing.T) {
	source := sourcePod()
	source.Spec.DNSConfig = &corev1.PodDNSConfig{Searches: []string{"internal.example"}}
	checks := []spec.Check{{Destination: "example.com", Expected: "reachable"}}
	pod := probePod(&source, checks, "probe:test")
	if pod.Namespace != source.Namespace || !maps.Equal(pod.Labels, source.Labels) || pod.Spec.ServiceAccountName != source.Spec.ServiceAccountName || pod.Spec.NodeName != source.Spec.NodeName {
		t.Fatal("workload identity was not copied")
	}
	if *pod.Spec.AutomountServiceAccountToken || pod.Spec.RestartPolicy != corev1.RestartPolicyNever || *pod.Spec.ActiveDeadlineSeconds != 130 {
		t.Fatal("probe must be credential-free and short-lived")
	}
	if len(pod.Spec.ReadinessGates) != 1 || pod.Spec.ReadinessGates[0].ConditionType != trafficGate || !*pod.OwnerReferences[0].Controller || pod.OwnerReferences[0].UID != source.UID {
		t.Fatal("probe can receive Service traffic or be adopted by a controller")
	}
	pod.Labels["app"] = "changed"
	pod.Spec.DNSConfig.Searches[0] = "changed"
	if source.Labels["app"] != "payments" || source.Spec.DNSConfig.Searches[0] != "internal.example" {
		t.Fatal("probe construction mutated source")
	}
}

func TestRunLifecycle(t *testing.T) {
	for _, mode := range []string{"success", "mismatch", "bad-logs", "startup-error", "mutated", "mutated-annotation", "mutated-node", "mutated-dns-policy", "mutated-dns-config", "mutated-host-aliases", "mutated-command", "mutated-args", "mutated-env", "mutated-env-from", "mutated-container-name", "create-error", "cancelled", "delete-error", "no-targets", "host-network"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			var created *corev1.Pod
			deletes := 0
			source := sourcePod()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := spec.Spec{Namespace: "team", PodSelector: "app=payments", Reachable: []string{"allowed:80"}, Blocked: []string{"denied:80"}}
			now := time.Now().UTC()
			observations := []probe.Observation{
				{Destination: "allowed:80", StartedAt: now, FinishedAt: now, Outcome: "reachable", Stages: []probe.Stage{{Name: "tcp", Success: true}}},
				{Destination: "denied:80", StartedAt: now, FinishedAt: now, Outcome: "unreachable", Error: "i/o timeout", Stages: []probe.Stage{{Name: "tcp", Error: "i/o timeout"}}},
			}
			if mode == "mismatch" {
				observations[1].Outcome = "reachable"
				observations[1].Stages[0].Success = true
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				respond := func(v any) { json.NewEncoder(w).Encode(v) }
				statusError := func(code int, reason metav1.StatusReason) {
					w.WriteHeader(code)
					respond(&metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Failure", Reason: reason, Code: int32(code), Message: mode})
				}
				switch {
				case req.URL.Path == "/api/v1/namespaces/kube-system":
					respond(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "cluster-uid"}})
				case req.Method == "GET" && req.URL.Path == "/api/v1/namespaces/team/pods":
					if req.URL.Query().Get("labelSelector") != s.PodSelector || req.URL.Query().Get("fieldSelector") != "status.phase=Running" {
						t.Error("target selectors not sent to API")
					}
					list := &corev1.PodList{Items: []corev1.Pod{source}}
					if mode == "no-targets" {
						list.Items = nil
					}
					if mode == "host-network" {
						list.Items[0].Spec.HostNetwork = true
					}
					respond(list)
				case req.Method == "POST":
					var pod corev1.Pod
					if err := json.NewDecoder(req.Body).Decode(&pod); err != nil {
						t.Error(err)
					}
					pod.UID = types.UID("probe-uid")
					created = &pod
					switch mode {
					case "mutated":
						pod.Labels["admission"] = "extra"
					case "mutated-annotation":
						delete(pod.Annotations, runAnnotation)
					case "mutated-node":
						pod.Spec.NodeName = "other-node"
					case "mutated-dns-policy":
						pod.Spec.DNSPolicy = corev1.DNSDefault
					case "mutated-dns-config":
						pod.Spec.DNSConfig = &corev1.PodDNSConfig{Nameservers: []string{"192.0.2.53"}}
					case "mutated-host-aliases":
						pod.Spec.HostAliases = []corev1.HostAlias{{IP: "127.0.0.1", Hostnames: []string{"allowed"}}}
					case "mutated-command":
						pod.Spec.Containers[0].Command = []string{"/other-probe"}
					case "mutated-args":
						pod.Spec.Containers[0].Args = []string{"--other"}
					case "mutated-env":
						pod.Spec.Containers[0].Env[0].Value = `["other:80"]`
					case "mutated-env-from":
						pod.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "other"}}}}
					case "mutated-container-name":
						pod.Spec.Containers[0].Name = "other"
					}
					if mode == "create-error" {
						statusError(500, metav1.StatusReasonInternalError)
						return
					}
					respond(created)
				case req.Method == "DELETE":
					var options metav1.DeleteOptions
					json.NewDecoder(req.Body).Decode(&options)
					if options.Preconditions == nil || *options.Preconditions.UID != "probe-uid" {
						t.Error("cleanup has no UID precondition")
					}
					deletes++
					if mode == "delete-error" {
						statusError(403, metav1.StatusReasonForbidden)
						return
					}
					created = nil
					respond(&metav1.Status{Status: "Success"})
				case strings.HasSuffix(req.URL.Path, "/log"):
					if req.URL.Query().Get("container") != "probe" {
						t.Error("wrong log container")
					}
					if mode == "bad-logs" {
						io.WriteString(w, "[]")
					} else {
						respond(observations)
					}
				case req.Method == "GET":
					if created == nil {
						statusError(404, metav1.StatusReasonNotFound)
						return
					}
					current := created.DeepCopy()
					current.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "probe", ImageID: "sha256:image", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}
					if mode == "startup-error" {
						current.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "missing image"}}
					}
					if mode == "cancelled" && ctx.Err() == nil {
						cancel()
					}
					respond(current)
				default:
					t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
					statusError(500, metav1.StatusReasonInternalError)
				}
			}))
			defer server.Close()
			client, err := typedcorev1.NewForConfig(&rest.Config{Host: server.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
			if err != nil {
				t.Fatal(err)
			}
			r := report.New(s, "sha256:test", "probe:test")
			Run(ctx, client, r)
			r.Finish()
			want := 2
			if mode == "success" {
				want = 0
			} else if mode == "mismatch" {
				want = 1
			}
			if r.ExitCode() != want || r.Cluster.KubeSystemUID != "cluster-uid" {
				t.Fatalf("exit %d, want %d: %+v", r.ExitCode(), want, r)
			}
			mu.Lock()
			defer mu.Unlock()
			if mode != "no-targets" && mode != "host-network" {
				if deletes != 1 || r.Targets[0].CleanedUp != (mode != "delete-error") {
					t.Fatalf("cleanup was lost: deletes=%d, targets=%+v, errors=%v", deletes, r.Targets, r.Errors)
				}
				if mode != "delete-error" && created != nil {
					t.Fatal("probe leaked")
				}
			}
		})
	}
}

func TestRejectIncompleteEvidence(t *testing.T) {
	checks := []spec.Check{{Destination: "example.com", Expected: "reachable"}}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	valid := fmt.Sprintf(`[{"destination":"example.com","startedAt":%q,"finishedAt":%q,"outcome":"reachable","stages":[{"name":"dns","success":true}]}]`, now, now)
	if _, err := decodeObservations([]byte(valid), checks); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{"[]", "null", valid + "garbage", valid + valid, strings.Replace(valid, "example.com", "other.com", 1), strings.Replace(valid, "reachable", "unknown", 1), `[{}]`} {
		if _, err := decodeObservations([]byte(data), checks); err == nil {
			t.Errorf("accepted malformed evidence %s", data)
		}
	}
}

func FuzzDecodeObservations(f *testing.F) {
	for _, seed := range []string{
		"", "[]", "null", "[{}]",
		`[{"destination":"example.com","startedAt":"2026-01-01T00:00:00Z","finishedAt":"2026-01-01T00:00:01Z","outcome":"reachable","stages":[{"name":"dns","success":true}]}]`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		checks := []spec.Check{{Destination: "example.com", Expected: "reachable"}}
		observations, err := decodeObservations(data, checks)
		if err != nil {
			return
		}
		if len(observations) != 1 || observations[0].Destination != "example.com" {
			t.Fatal("accepted incomplete or unrelated observations")
		}
		encoded, err := json.Marshal(observations)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeObservations(encoded, checks); err != nil {
			t.Fatalf("round trip rejected: %v", err)
		}
	})
}
