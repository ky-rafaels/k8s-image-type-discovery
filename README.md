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
FROM cgr.dev/chainguard/go:latest-dev AS builder
WORKDIR /app
COPY discover.go .
RUN go mod init workload-discovery && \
    go mod tidy && \
    CGO_ENABLED=0 GOOS=linux go build -o workload-discovery .

# Runtime stage
FROM cgr.dev/ky-rafaels.example.com/chainguard-base:20230214
COPY --from=builder /app/workload-discovery /usr/local/bin/
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