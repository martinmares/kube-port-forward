package main

import (
	"io"
	"log/slog"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRegisterPodAllocatesGloballyUniquePorts(t *testing.T) {
	manager := newTestForwardManager()
	pod := testPod("pod-a", 9090, 8080, 9090)

	first, added, err := manager.registerPod(pod, target{
		namespace:    "test",
		resourceKind: resourceService,
		resourceName: "first",
		localPort:    9100,
		remotePort:   8080,
	})
	if err != nil || !added {
		t.Fatalf("register first pod: added=%v err=%v", added, err)
	}
	second, added, err := manager.registerPod(pod, target{
		namespace:    "test",
		resourceKind: resourceService,
		resourceName: "first",
		localPort:    9100,
		remotePort:   9090,
	})
	if err != nil || !added {
		t.Fatalf("register second pod: added=%v err=%v", added, err)
	}

	if first.localPort != 9100 || second.localPort != 9101 {
		t.Fatalf("local ports = %d, %d; want 9100, 9101", first.localPort, second.localPort)
	}
}

func TestRegisterPodDeduplicatesOwners(t *testing.T) {
	manager := newTestForwardManager()
	pod := testPod("pod-a", 8080)
	firstTarget := target{
		namespace:    "test",
		resourceKind: resourceService,
		resourceName: "first",
		localPort:    9100,
		remotePort:   8080,
	}
	secondTarget := firstTarget
	secondTarget.resourceName = "second"

	entry, added, err := manager.registerPod(pod, firstTarget)
	if err != nil || !added {
		t.Fatalf("register first owner: added=%v err=%v", added, err)
	}
	duplicate, added, err := manager.registerPod(pod, secondTarget)
	if err != nil {
		t.Fatalf("register second owner: %v", err)
	}
	if added || duplicate != entry {
		t.Fatal("same pod and remote port was not deduplicated")
	}
	if got := manager.snapshot()[0].resource; got != "svc/first, svc/second" {
		t.Fatalf("shared resources = %q, want %q", got, "svc/first, svc/second")
	}

	manager.removePodOwner("test", "pod-a", firstTarget)
	if entry.getStatus() == statusTerminated {
		t.Fatal("entry terminated while another resource still owns it")
	}
	manager.removePodOwner("test", "pod-a", secondTarget)
	if entry.getStatus() != statusTerminated {
		t.Fatal("entry remained active after its last owner was removed")
	}
}

func TestContainerPortsAreUniqueAndSorted(t *testing.T) {
	pod := testPod("pod-a", 9090, 8080, 9090)
	ports := containerPorts(pod)
	if len(ports) != 2 || ports[0] != 8080 || ports[1] != 9090 {
		t.Fatalf("containerPorts() = %v, want [8080 9090]", ports)
	}
}

func newTestForwardManager() *forwardManager {
	return newForwardManager(
		&config{},
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
}

func testPod(name string, ports ...int32) *corev1.Pod {
	containerPorts := make([]corev1.ContainerPort, len(ports))
	for i, port := range ports {
		containerPorts[i].ContainerPort = port
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "app",
				Ports: containerPorts,
			}},
		},
	}
}
