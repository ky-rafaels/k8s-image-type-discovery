package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"log"
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
		[]string{"base_type"},
	)

	// Metric for counting FIPS-compliant containers
	containersFipsCompliant = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "containers_fips_compliant",
			Help: "Total number of containers running in FIPS-compliant mode",
		},
	)
)

func main() {
	// Register Prometheus metrics
	prometheus.MustRegister(podsPerNamespace)
	prometheus.MustRegister(containerImageCount)
	prometheus.MustRegister(containerBaseImageType)
	prometheus.MustRegister(containersFipsCompliant)

	// Set up Kubernetes in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("Failed to load in-cluster config: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Failed to create Kubernetes client: %v", err)
	}

	// Start metrics server
	go func() {
		http.Handle("/metrics", promhttp.Handler())
		log.Println("Starting metrics server on :8080")
		log.Fatal(http.ListenAndServe(":8080", nil))
	}()

	// Periodically discover workloads and update metrics
	log.Println("Starting workload discovery...")
	for {
		updateMetrics(clientset, config)
		time.Sleep(30 * time.Second) // Refresh every 30 seconds
	}
}

func updateMetrics(clientset *kubernetes.Clientset, config *rest.Config) {
	// List all pods in the cluster
	pods, err := clientset.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		log.Printf("Failed to list pods: %v", err)
		return
	}

	// Reset metrics to avoid stale data
	podsPerNamespace.Reset()
	containerImageCount.Reset()
	containerBaseImageType.Reset()
	containersFipsCompliant.Set(0)

	// Track pod counts, image counts, base image types, and FIPS compliance
	namespaceMap := make(map[string]float64)
	imageMap := make(map[string]float64)
	baseTypeMap := make(map[string]float64)
	fipsCount := 0.0

	for _, pod := range pods.Items {
		// Increment namespace count
		namespaceMap[pod.Namespace]++

		for _, container := range pod.Spec.Containers {
			// Increment image count
			imageMap[container.Image]++

			// Determine base image type and FIPS compliance
			baseType, isFips := getContainerDetails(clientset, config, pod, container)
			baseTypeMap[baseType]++
			if isFips {
				fipsCount++
			}
		}
	}

	// Update Prometheus metrics
	for ns, count := range namespaceMap {
		podsPerNamespace.WithLabelValues(ns).Set(count)
	}
	for img, count := range imageMap {
		containerImageCount.WithLabelValues(img).Set(count)
	}
	for baseType, count := range baseTypeMap {
		containerBaseImageType.WithLabelValues(baseType).Set(count)
	}
	containersFipsCompliant.Set(fipsCount)

	log.Printf("Updated metrics for %d pods, %.0f FIPS-compliant containers", len(pods.Items), fipsCount)
}

// getContainerDetails executes a command to read /etc/os-release and determines base type and FIPS status
func getContainerDetails(clientset *kubernetes.Clientset, config *rest.Config, pod corev1.Pod, container corev1.Container) (baseType string, isFips bool) {
	// Command to read /etc/os-release
	cmd := []string{"cat", "/etc/os-release"}
	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(pod.Namespace).
		Name(pod.Name).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container.Name,
			Command:   cmd,
			Stdin:     false,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, metav1.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		log.Printf("Failed to create executor for pod %s/%s: %v", pod.Namespace, pod.Name, err)
		return "Unknown", false
	}

	// Execute the command and capture output
	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(context.TODO(), remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		log.Printf("Failed to exec in pod %s/%s container %s: %v, stderr: %s", pod.Namespace, pod.Name, container.Name, err, stderr.String())
		// Fallback to image name heuristic for FIPS
		return "Unknown", isFipsFromImageName(container.Image)
	}

	// Parse /etc/os-release for base type and FIPS status
	osRelease := stdout.String()
	baseType = parseOsRelease(osRelease)
	isFips = isFipsCompliant(osRelease, container.Image)
	return baseType, isFips
}

// parseOsRelease extracts the base image type from /etc/os-release content
func parseOsRelease(content string) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		lowerLine := strings.ToLower(line)
		if strings.HasPrefix(lowerLine, "id=") || strings.HasPrefix(lowerLine, "name=") {
			value := strings.TrimPrefix(lowerLine, "id=")
			value = strings.TrimPrefix(value, "name=")
			value = strings.Trim(value, "\"")

			switch {
			case strings.Contains(value, "debian"):
				return "Debian"
			case strings.Contains(value, "alpine"):
				return "Alpine"
			case strings.Contains(value, "centos") || strings.Contains(value, "rhel") || strings.Contains(value, "ubi"):
				return "RHEL"
			case strings.Contains(value, "wolfi"):
				return "Chainguard"
			default:
				return "Other"
			}
		}
	}
	return "Unknown"
}

// isFipsCompliant checks if the container is FIPS-compliant based on /etc/os-release or image name
func isFipsCompliant(osRelease, image string) bool {
	lowerOsRelease := strings.ToLower(osRelease)
	// Check /etc/os-release for FIPS indicators
	if strings.Contains(lowerOsRelease, "fips_mode=yes") || strings.Contains(lowerOsRelease, "fips=1") {
		return true
	}

	// Fallback to image name heuristic if /etc/os-release doesn’t confirm FIPS
	return isFipsFromImageName(image)
}

// isFipsFromImageName checks for FIPS indicators in the image name as a fallback
func isFipsFromImageName(image string) bool {
	lowerImage := strings.ToLower(image)
	fipsIndicators := []string{
		"fips",
		"ubi8-fips",       // Red Hat UBI FIPS variant
		"debian:fips",     // Hypothetical Debian FIPS
		"chainguard:fips", // Hypothetical Chainguard FIPS
	}
	for _, indicator := range fipsIndicators {
		if strings.Contains(lowerImage, indicator) {
			return true
		}
	}
	return false
}