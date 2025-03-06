package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var (
	// Metric for counting pods per namespace
	podsPerNamespace = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pods_per_namespace",
			Help: "Number of pods running in each namespace",
		},
		[]string{"namespace"},
	)

	// Metric for counting containers per image
	containerImageCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "container_image_count",
			Help: "Number of containers running each image",
		},
		[]string{"image"},
	)

	// Metric for counting containers by base image type
	containerBaseImageType = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "container_base_image_type",
			Help: "Number of containers running each base image type based on /etc/os-release",
		},
		[]string{"type"},
	)
)

// Client wraps the Kubernetes clientset and logger
type Client struct {
	clientset *kubernetes.Clientset
	logger    *slog.Logger
}

func main() {
	// Use structured logging (introduced in Go 1.21, improved in later releases)
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// Set up Kubernetes in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		logger.Error("Failed to load in-cluster config", "error", err)
		os.Exit(1)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		logger.Error("Failed to create Kubernetes client", "error", err)
		os.Exit(1)
	}

	client := &Client{clientset: clientset, logger: logger}

	// Register Prometheus metrics
	prometheus.MustRegister(podsPerNamespace, containerImageCount, containerBaseImageType)

	// Start metrics server in a goroutine
	go func() {
		http.Handle("/metrics", promhttp.Handler())
		logger.Info("Starting metrics server on :8080")
		if err := http.ListenAndServe(":8080", nil); err != nil {
			logger.Error("Metrics server failed", "error", err)
			os.Exit(1)
		}
	}()

	// Run discovery loop
	logger.Info("Starting workload discovery")
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	ctx := context.Background()
	for range ticker.C {
		client.updateMetrics(ctx)
	}
}

func (c *Client) updateMetrics(ctx context.Context) {
	// List all pods in the cluster
	pods, err := c.clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		c.logger.Warn("Failed to list pods", "error", err)
		return
	}

	// Reset metrics to avoid stale data
	podsPerNamespace.Reset()
	containerImageCount.Reset()
	containerBaseImageType.Reset()

	// Use maps to aggregate data
	namespaceMap := make(map[string]float64)
	imageMap := make(map[string]float64)
	baseImageTypeMap := make(map[string]float64)

	for _, pod := range pods.Items {
		namespaceMap[pod.Namespace]++

		for _, container := range pod.Spec.Containers {
			imageMap[container.Image]++
			baseType := c.getBaseImageType(ctx, pod.Namespace, pod.Name, container.Name)
			baseImageTypeMap[baseType]++
		}
	}

	// Update Prometheus metrics with range loops (Go 1.22+ style)
	for ns, count := range namespaceMap {
		podsPerNamespace.WithLabelValues(ns).Set(count)
	}
	for img, count := range imageMap {
		containerImageCount.WithLabelValues(img).Set(count)
	}
	for baseType, count := range baseImageTypeMap {
		containerBaseImageType.WithLabelValues(baseType).Set(count)
	}

	c.logger.Info("Updated metrics", "pod_count", len(pods.Items))
}

// getBaseImageType executes 'cat /etc/os-release' in the container
func (c *Client) getBaseImageType(ctx context.Context, namespace, podName, containerName string) string {
	exec, err := c.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(podName).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName,
			Command:   []string{"cat", "/etc/os-release"},
			Stdin:     false,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, metav1.ParameterCodec).
		Stream(ctx)

	if err != nil {
		c.logger.Debug("Exec failed", "namespace", namespace, "pod", podName, "container", containerName, "error", err)
		return "unknown"
	}
	defer exec.Close()

	var output bytes.Buffer
	if _, err := output.ReadFrom(exec); err != nil {
		c.logger.Debug("Failed to read exec output", "namespace", namespace, "pod", podName, "container", containerName, "error", err)
		return "unknown"
	}

	return parseOsRelease(output.String())
}

// parseOsRelease extracts the base image type from /etc/os-release
func parseOsRelease(content string) string {
	for _, line := range strings.Split(content, "\n") {
		lowerLine := strings.ToLower(line)
		if strings.HasPrefix(lowerLine, "id=") {
			id := strings.Trim(strings.TrimPrefix(lowerLine, "id="), `"`)
			switch id {
			case "debian":
				return "debian"
			case "ubuntu":
				return "ubuntu"
			case "alpine":
				return "alpine"
			case "centos", "rhel", "fedora":
				return id
			default:
				return "other"
			}
		}
	}
	return "unknown"
}