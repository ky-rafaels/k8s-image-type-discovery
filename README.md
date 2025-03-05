# k8s-image-type-discovery
Dashboarding image types being used in a kubernetes cluster along with displaying other container metrics

## Setup

First deploy kube-prom-stack

```bash
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm install prometheus-stack prometheus-community/kube-prometheus-stack -n monitoring --create-namespace
```

Here’s an updated version of the Go program that adds a new Prometheus metric to determine whether each container is FIPS (Federal Information Processing Standards) compliant. It builds on the previous code by inspecting `/etc/os-release` for FIPS-related indicators and introduces a new metric, `containers_fips_compliant`, to track the count of FIPS-compliant containers. The program continues to run in Kubernetes, discovering workloads and exposing metrics on port 8080.

---

### Program Overview
- **Functionality**:
  - Queries all pods in the cluster every 30 seconds.
  - Emits four Prometheus metrics:
    - `pods_per_namespace`: Number of pods per namespace.
    - `container_image_count`: Number of containers per image.
    - `container_base_image_type`: Number of containers per base image type (from `/etc/os-release`).
    - `containers_fips_compliant`: Total number of FIPS-compliant containers.
  - Inspects `/etc/os-release` to determine base image type and FIPS compliance.
- **FIPS Detection**: Heuristic-based, checking for "fips" in `/etc/os-release` (e.g., `FIPS_MODE=yes`) or known FIPS-compliant image patterns.
- **Deployment**: Runs in Kubernetes with RBAC permissions for pod listing and exec.

---

### Go Code
```go
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
			case strings.Contains(value, "ubuntu"):
				return "Ubuntu"
			case strings.Contains(value, "alpine"):
				return "Alpine"
			case strings.Contains(value, "centos") || strings.Contains(value, "rhel") || strings.Contains(value, "ubi"):
				return "RHEL"
			case strings.Contains(value, "chainguard"):
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
```

---

### Explanation
1. **New Metric**:
   - `containers_fips_compliant`: A gauge tracking the total number of FIPS-compliant containers across the cluster.

2. **FIPS Detection**:
   - **Primary Check**: Inspects `/etc/os-release` for explicit FIPS indicators like `FIPS_MODE=yes` or `FIPS=1` (common in FIPS-enabled RHEL or UBI images).
   - **Fallback**: If `/etc/os-release` is missing or inconclusive, checks the image name for "fips" or known FIPS-compliant patterns (e.g., `ubi8-fips`).
   - **Limitations**: This is heuristic-based. True FIPS compliance might require checking kernel settings (e.g., `fips=1` in boot params) or image metadata, which isn’t feasible via exec alone.

3. **Logic Updates**:
   - `getContainerDetails`: Now returns both base image type and FIPS status by calling `parseOsRelease` and `isFipsCompliant`.
   - `updateMetrics`: Increments `fipsCount` when a container is FIPS-compliant and updates the new metric.
   - Metrics are reset each cycle to reflect the current state.

4. **Dependencies**: No new dependencies added; still uses `client-go` and `prometheus/client_golang`.

---

### Dockerfile
Unchanged from before:
```dockerfile
# Build stage
FROM golang:1.21 AS builder
WORKDIR /app
COPY . .
RUN go mod init workload-discovery && \
    go get k8s.io/client-go@v0.29.0 && \
    go get github.com/prometheus/client_golang@v1.19.0 && \
    CGO_ENABLED=0 GOOS=linux go build -o workload-discovery .

# Runtime stage
FROM alpine:3.19
COPY --from=builder /app/workload-discovery /usr/local/bin/
RUN apk add --no-cache ca-certificates
USER 1001
CMD ["workload-discovery"]
```

Build it:
```bash
docker build -t workload-discovery:latest .
```

---

### Kubernetes Deployment
The deployment and RBAC remain the same, as `pods/exec` permissions were already added:

#### Deployment YAML
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: workload-discovery
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: workload-discovery
  template:
    metadata:
      labels:
        app: workload-discovery
    spec:
      serviceAccountName: workload-discovery-sa
      containers:
      - name: workload-discovery
        image: workload-discovery:latest
        ports:
        - containerPort: 8080
        resources:
          requests:
            cpu: "100m"
            memory: "128Mi"
          limits:
            cpu: "200m"
            memory: "256Mi"
```

#### Service YAML
```yaml
apiVersion: v1
kind: Service
metadata:
  name: workload-discovery
  namespace: default
  labels:
    app: workload-discovery
spec:
  ports:
  - port: 8080
    targetPort: 8080
    protocol: TCP
  selector:
    app: workload-discovery
```

#### RBAC YAML
```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: workload-discovery-sa
  namespace: default

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: workload-discovery-role
rules:
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["list"]
- apiGroups: [""]
  resources: ["pods/exec"]
  verbs: ["create"]

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: workload-discovery-binding
subjects:
- kind: ServiceAccount
  name: workload-discovery-sa
  namespace: default
roleRef:
  kind: ClusterRole
  name: workload-discovery-role
  apiGroup: rbac.authorization.k8s.io
```

Apply these:
```bash
kubectl apply -f deployment.yaml -f service.yaml -f rbac.yaml
```

---

### Prometheus Integration
Update your Prometheus config:
```yaml
scrape_configs:
  - job_name: 'workload-discovery'
    static_configs:
      - targets: ['workload-discovery.default.svc.cluster.local:8080']
```

---

### Sample Metrics Output
After running, you might see:
```
# HELP pods_per_namespace Number of pods running in each namespace
# TYPE pods_per_namespace gauge
pods_per_namespace{namespace="default"} 3
pods_per_namespace{namespace="kube-system"} 5

# HELP container_image_count Number of containers running each image
# TYPE container_image_count gauge
container_image_count{image="nginx:1.23"} 2
container_image_count{image="coredns:1.9.3"} 2
container_image_count{image="ubi8-fips:8.6"} 1

# HELP container_base_image_type Number of containers running each base image type based on /etc/os-release
# TYPE container_base_image_type gauge
container_base_image_type{base_type="Debian"} 1
container_base_image_type{base_type="Alpine"} 3
container_base_image_type{base_type="RHEL"} 2
container_base_image_type{base_type="Unknown"} 2

# HELP containers_fips_compliant Total number of containers running in FIPS-compliant mode
# TYPE containers_fips_compliant gauge
containers_fips_compliant 1
```

---

### Notes and Improvements
1. **FIPS Detection**:
   - Currently checks `/etc/os-release` for explicit FIPS flags and falls back to image name patterns. This is a heuristic—true FIPS compliance might require:
     - Checking `/proc/sys/crypto/fips_enabled` (if accessible).
     - Querying image metadata or pod annotations.
   - Some containers (e.g., `scratch`) lack `/etc/os-release`, so the fallback to image name is critical.
2. **Base Image Detection**: Same limitations as before—relies on `/etc/os-release` presence.
3. **Performance**: Exec calls add latency; for large clusters, consider sampling or caching results.
4. **Granularity**: Could extend `containers_fips_compliant` to a vector with labels (e.g., per namespace or pod).
5. **Error Handling**: Logs errors and defaults to "Unknown"/non-FIPS; add retries for robustness.

This updated program now tracks FIPS compliance alongside other workload details, emitting all data as Prometheus metrics. Deploy it, scrape with Prometheus, and visualize the results! Let me know if you need further tweaks.