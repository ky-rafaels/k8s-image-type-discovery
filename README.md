# k8s-image-type-discovery
Dashboarding image types being used in a kubernetes cluster along with displaying other container metrics

## Setup

First deploy kube-prom-stack

```bash
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm install prometheus-stack prometheus-community/kube-prometheus-stack -n monitoring --create-namespace
```

## 
Below is a Go program designed to run in Kubernetes, discover other workloads (pods), and emit Prometheus metrics about the container images and their FIPS (Federal Information Processing Standards) status. This program uses the Kubernetes client-go library to interact with the cluster and the Prometheus client library to expose metrics. It assumes it’s running inside a Kubernetes cluster with appropriate RBAC permissions.

---

### Program Overview
- **Functionality**:
  - Queries all pods in the cluster.
  - Extracts container images and checks for FIPS-enabled status (based on a heuristic, e.g., image names containing "fips").
  - Exposes two Prometheus metrics:
    - `container_image_count`: Number of containers per image.
    - `workload_fips_enabled`: Whether a workload (pod) uses FIPS-enabled images (1 or 0).
- **Deployment**: Runs as a pod in Kubernetes, uses in-cluster config, and exposes metrics on port 8080.
- **Assumptions**:
  - FIPS detection is simplistic (checks for "fips" in image name). In a real scenario, you’d need more robust logic (e.g., image metadata or labels).
  - Requires RBAC permissions to list pods cluster-wide.

---

### Go Code
```go
package main

import (
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
	"log"
)

var (
	// Metric for counting containers per image
	imageCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "container_image_count",
			Help: "Number of containers running a specific image",
		},
		[]string{"image"},
	)

	// Metric for FIPS-enabled workloads
	fipsEnabled = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "workload_fips_enabled",
			Help: "Indicates if a workload is running FIPS-enabled images (1 = yes, 0 = no)",
		},
		[]string{"namespace", "pod"},
	)
)

func main() {
	// Register Prometheus metrics
	prometheus.MustRegister(imageCount)
	prometheus.MustRegister(fipsEnabled)

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
	for {
		updateMetrics(clientset)
		time.Sleep(30 * time.Second) // Refresh every 30 seconds
	}
}

func updateMetrics(clientset *kubernetes.Clientset) {
	// List all pods in the cluster
	pods, err := clientset.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		log.Printf("Failed to list pods: %v", err)
		return
	}

	// Reset metrics to avoid stale data
	imageCount.Reset()
	fipsEnabled.Reset()

	// Track image counts and FIPS status
	imageMap := make(map[string]float64)
	for _, pod := range pods.Items {
		isFips := false
		for _, container := range pod.Spec.Containers {
			// Increment image count
			image := container.Image
			imageMap[image] = imageMap[image] + 1

			// Check for FIPS (simple heuristic: "fips" in image name)
			if strings.Contains(strings.ToLower(image), "fips") {
				isFips = true
			}
		}

		// Set FIPS metric for the pod
		fipsValue := 0.0
		if isFips {
			fipsValue = 1.0
		}
		fipsEnabled.WithLabelValues(pod.Namespace, pod.Name).Set(fipsValue)
	}

	// Set image count metrics
	for image, count := range imageMap {
		imageCount.WithLabelValues(image).Set(count)
	}

	log.Printf("Updated metrics for %d pods", len(pods.Items))
}

// Simple heuristic to detect FIPS-enabled images
func isFipsImage(image string) bool {
	return strings.Contains(strings.ToLower(image), "fips")
}
```

---

### Explanation
1. **Dependencies**:
   - `k8s.io/client-go`: Interacts with the Kubernetes API.
   - `github.com/prometheus/client_golang`: Exposes metrics for Prometheus.

2. **Metrics**:
   - `container_image_count`: A gauge tracking how many containers use each image (e.g., `nginx:1.23` might have 5 instances).
   - `workload_fips_enabled`: A gauge per pod (namespace + name), set to 1 if any container uses a FIPS-enabled image, 0 otherwise.

3. **Logic**:
   - Uses in-cluster config (`rest.InClusterConfig()`) to authenticate with the Kubernetes API.
   - Lists all pods cluster-wide every 30 seconds.
   - Counts images and checks for "fips" in image names (a placeholder—you’d replace this with real FIPS detection logic).
   - Resets and updates Prometheus gauges to reflect current state.

4. **Metrics Server**: Runs on `:8080`, exposing `/metrics` for Prometheus scraping.

---

### Dockerfile
To run this in Kubernetes, build it into a container image:
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
Deploy the program as a pod with RBAC permissions to list pods:

#### Deployment YAML
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: workload-discovery
  namespace: monitoring
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

#### Service YAML (for Prometheus scraping)
```yaml
apiVersion: v1
kind: Service
metadata:
  name: workload-discovery
  namespace: monitoring
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
  namespace: monitoring

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: workload-discovery-role
rules:
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["list"]

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: workload-discovery-binding
subjects:
- kind: ServiceAccount
  name: workload-discovery-sa
  namespace: monitoring
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
Configure Prometheus to scrape the metrics:
```yaml
scrape_configs:
  - job_name: 'workload-discovery'
    static_configs:
      - targets: ['workload-discovery.monitoring.svc.cluster.local:8080']
```

---

### Sample Metrics Output
After running, you might see:
```
# HELP container_image_count Number of containers running a specific image
# TYPE container_image_count gauge
container_image_count{image="nginx:1.23"} 3
container_image_count{image="registry.fips.com/ubuntu-fips:20.04"} 2

# HELP workload_fips_enabled Indicates if a workload is running FIPS-enabled images (1 = yes, 0 = no)
# TYPE workload_fips_enabled gauge
workload_fips_enabled{namespace="default",pod="nginx-abc123"} 0
workload_fips_enabled{namespace="monitoring",pod="fips-app-xyz789"} 1
```

---

### Notes and Improvements
1. **FIPS Detection**: The current `isFipsImage` function is a placeholder. Enhance it by:
   - Checking image labels (e.g., `kubectl get pod -o json | jq '.spec.containers[].image'`) for FIPS metadata.
   - Querying a registry API or image manifest for FIPS compliance.
2. **Error Handling**: Add retries or exponential backoff for API failures.
3. **Security**: Use a non-root user and minimal base image (done with `alpine` and `USER 1001`).
4. **Metrics**: Add more granularity (e.g., per-container FIPS status, image digest).

This program gives you a starting point to monitor Kubernetes workloads and their images, with Prometheus-ready metrics for observability. Deploy it, tweak as needed, and pair it with a Prometheus/Grafana stack for visualization!