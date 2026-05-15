package main

import (
	"context"
	"fmt"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type clusterInfo struct {
	ContextName string `json:"contextName"`
	ClusterName string `json:"clusterName"`
	ServerURL   string `json:"serverURL"`
	Namespace   string `json:"namespace"`
}

type k8sClient struct {
	clientset *kubernetes.Clientset
	config    *rest.Config
	info      clusterInfo
}

// currentContextNamespace returns the namespace set in the current kubeconfig context.
func currentContextNamespace() (string, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules, &clientcmd.ConfigOverrides{},
	)
	ns, _, err := kubeConfig.Namespace()
	return ns, err
}

func newK8sClient(namespace string) (*k8sClient, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	rawConfig, err := kubeConfig.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	restConfig, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to build REST config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create k8s client: %w", err)
	}

	currentContext := rawConfig.CurrentContext
	ctx := rawConfig.Contexts[currentContext]
	clusterName := ""
	serverURL := restConfig.Host
	if ctx != nil {
		clusterName = ctx.Cluster
	}

	return &k8sClient{
		clientset: clientset,
		config:    restConfig,
		info: clusterInfo{
			ContextName: currentContext,
			ClusterName: clusterName,
			ServerURL:   serverURL,
			Namespace:   namespace,
		},
	}, nil
}

// resolveServicePorts returns containerPorts from pods behind a service.
// This reflects what the POD actually listens on, not what the Service exposes.
func (c *k8sClient) resolveServicePorts(ctx context.Context, namespace, name string) ([]int, error) {
	selector, err := c.resolveService(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	pods, err := c.listPods(ctx, namespace, selector)
	if err != nil {
		return nil, err
	}
	if len(pods) == 0 {
		return nil, fmt.Errorf("no running pods found for service %q", name)
	}
	// collect unique containerPorts from first pod
	seen := map[int]bool{}
	var ports []int
	for _, container := range pods[0].Spec.Containers {
		for _, p := range container.Ports {
			port := int(p.ContainerPort)
			if !seen[port] {
				seen[port] = true
				ports = append(ports, port)
			}
		}
	}
	if len(ports) == 0 {
		return nil, fmt.Errorf("no containerPorts found in pods of service %q", name)
	}
	return ports, nil
}

// resolveService returns the label selector for a service.
func (c *k8sClient) resolveService(ctx context.Context, namespace, name string) (labels.Selector, error) {
	svc, err := c.clientset.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("service %q not found in namespace %q: %w", name, namespace, err)
	}
	if len(svc.Spec.Selector) == 0 {
		return nil, fmt.Errorf("service %q has no pod selector", name)
	}
	return labels.SelectorFromSet(svc.Spec.Selector), nil
}

// listPods returns all running pods matching the selector.
func (c *k8sClient) listPods(ctx context.Context, namespace string, selector labels.Selector) ([]corev1.Pod, error) {
	list, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		return nil, err
	}
	var running []corev1.Pod
	for _, p := range list.Items {
		if p.Status.Phase == corev1.PodRunning {
			running = append(running, p)
		}
	}
	return running, nil
}

// watchPods streams pod add/delete events for the given selector.
func (c *k8sClient) watchPods(ctx context.Context, namespace string, selector labels.Selector) (<-chan podEvent, error) {
	watcher, err := c.clientset.CoreV1().Pods(namespace).Watch(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		return nil, err
	}

	ch := make(chan podEvent, 16)
	go func() {
		defer close(ch)
		defer watcher.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-watcher.ResultChan():
				if !ok {
					return
				}
				pod, ok := ev.Object.(*corev1.Pod)
				if !ok {
					continue
				}
				switch ev.Type {
				case watch.Added, watch.Modified:
					if pod.Status.Phase == corev1.PodRunning {
						select {
						case ch <- podEvent{kind: podAdded, pod: pod}:
						case <-ctx.Done():
							return
						}
					}
				case watch.Deleted:
					select {
					case ch <- podEvent{kind: podDeleted, pod: pod}:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()

	return ch, nil
}

type podEventKind int

const (
	podAdded podEventKind = iota
	podDeleted
)

type podEvent struct {
	kind podEventKind
	pod  *corev1.Pod
}

// getPod returns a single pod by name.
func (c *k8sClient) getPod(ctx context.Context, namespace, name string, logger *slog.Logger) (*corev1.Pod, error) {
	pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("pod %q not found in namespace %q: %w", name, namespace, err)
	}
	if pod.Status.Phase != corev1.PodRunning {
		logger.Warn("pod is not running", "pod", name, "phase", pod.Status.Phase)
	}
	return pod, nil
}
