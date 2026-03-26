# kuery

[![CI](https://github.com/faroshq/kuery/actions/workflows/ci.yml/badge.svg)](https://github.com/faroshq/kuery/actions/workflows/ci.yml)

A Kubernetes query API server that enables rich, nested queries across multiple clusters. kuery syncs objects from multiple Kubernetes clusters into a SQL database and exposes a powerful query engine via a standard Kubernetes API (`POST /apis/kuery.io/v1alpha1/queries`).

Think of it as a read-only, cross-cluster search engine for Kubernetes — submit a `Query` object and get back a nested tree of related resources with exactly the fields you need.

## Why kuery?

- **Multi-cluster**: Query objects across dozens of clusters in a single request
- **Relationship traversal**: Follow ownerRefs, label selectors, spec references, and annotations to build a complete picture of your workloads
- **Sparse projection**: Request only the fields you need — projection happens in SQL, not in Go
- **Standard Kubernetes API**: Works with any Kubernetes client. Deploy as an aggregated API server or standalone

## Architecture

```
Clusters (vanilla k8s, kcp, ...)
    │
    │  multicluster-runtime (kubeconfig provider)
    │
    ├── Cluster A ──┐
    ├── Cluster B ──┤  informer events (Add/Update/Delete)
    └── Cluster N ──┤
                    │
                    ▼
          ┌─────────────────┐
          │  Sync Controller │  Upserts/deletes rows per event
          └────────┬────────┘
                   │
                   ▼
          ┌─────────────────┐
          │   SQL Database   │  SQLite (dev) / PostgreSQL (prod)
          └────────┬────────┘
                   │
                   ▼
          ┌─────────────────┐
          │  Query Engine    │  Query spec → SQL → tree assembly
          └────────┬────────┘
                   │
                   ▼
          ┌─────────────────┐
          │  Generic API     │  k8s.io/apiserver, POST-only
          │  Server          │  kind: Query → response inline
          └─────────────────┘
```

## Quick Start

### Try it now (requires kind, kubectl, go, curl, jq)

```bash
./hack/quickstart.sh
```

This creates 2 kind clusters with sample workloads, starts kuery, syncs objects, and runs 7 example queries demonstrating filters, relations, projections, and cross-cluster queries. Run `./hack/quickstart.sh cleanup` to tear down.

### Build

```bash
go build -o kuery ./cmd/kuery
```

### Run locally (SQLite)

```bash
./kuery --store-driver=sqlite --store-dsn=kuery.db --secure-port=6443
```

### Run with multiple clusters (kubeconfig files)

```bash
./kuery --store-driver=sqlite --store-dsn=kuery.db --secure-port=6443 \
  --kubeconfigs="prod=/path/to/prod.kubeconfig,staging=/path/to/staging.kubeconfig"
```

### Run locally (PostgreSQL)

```bash
./kuery --store-driver=postgres --store-dsn="host=localhost user=kuery dbname=kuery sslmode=disable" --secure-port=6443
```

### Server flags

| Flag | Default | Description |
|---|---|---|
| `--store-driver` | `sqlite` | Database driver (`sqlite` or `postgres`) |
| `--store-dsn` | `kuery.db` | Connection string |
| `--secure-port` | `6443` | HTTPS port |
| `--sync-enabled` | `false` | Enable sync controller for multi-cluster |
| `--kubeconfigs` | | Comma-separated `name=path` pairs to sync clusters from kubeconfig files |

## Usage

kuery exposes a single POST-only endpoint. Submit a `Query` object and get back results inline in the status — the same pattern as `SubjectAccessReview` or `TokenReview`.

### Basic query: find all Deployments

```yaml
apiVersion: kuery.io/v1alpha1
kind: Query
spec:
  filter:
    objects:
      - groupKind:
          apiGroup: apps
          kind: Deployment
  objects:
    id: true
    cluster: true
    object:
      metadata:
        name: true
        namespace: true
```

```bash
curl -k -X POST https://localhost:6443/apis/kuery.io/v1alpha1/queries \
  -H "Content-Type: application/json" \
  -d @query.json
```

### Filter by labels and namespace

```yaml
spec:
  filter:
    objects:
      - groupKind:
          kind: Pod
        namespace: production
        labels:
          app: nginx
```

### Label expressions (In, NotIn, Exists, DoesNotExist)

```yaml
spec:
  filter:
    objects:
      - labelExpressions:
          - key: env
            operator: In
            values: [prod, staging]
          - key: deprecated
            operator: DoesNotExist
```

### Filter by conditions

```yaml
spec:
  filter:
    objects:
      - groupKind:
          kind: Deployment
        conditions:
          - type: Available
            status: "True"
```

### Filter by creation timestamp

```yaml
spec:
  filter:
    objects:
      - creationTimestamp:
          after: "2025-01-01T00:00:00Z"
          before: "2025-12-31T23:59:59Z"
```

### Filter by cluster

```yaml
spec:
  cluster:
    name: production-east
  filter:
    objects:
      - groupKind:
          kind: Pod
```

### Filter by cluster labels

```yaml
spec:
  cluster:
    labels:
      env: production
      region: us-east
```

### Filter by categories

```yaml
spec:
  filter:
    objects:
      - categories:
          - all
```

### JSONPath filter (last resort)

```yaml
spec:
  filter:
    objects:
      - jsonpath: "$.status.phase"
```

### Sparse projection

Request only the fields you need. Projection is compiled to SQL (`json_object` / `jsonb_build_object`) so unused fields never leave the database.

```yaml
spec:
  objects:
    object:
      metadata:
        name: true
        namespace: true
        labels: true
      spec:
        replicas: true
      status:
        conditions: true
```

### Pagination

Offset-based:

```yaml
spec:
  limit: 50
  page:
    first: 100
```

Cursor-based (stable, efficient):

```yaml
spec:
  limit: 50
  cursor: true
  page:
    cursor: "eyJraW5kIjoiRGVwbG95bWVudCIsIm5hbWUiOiJuZ2lueCJ9"
```

### Ordering

```yaml
spec:
  order:
    - field: creationTimestamp
      direction: Desc
    - field: name
      direction: Asc
```

Sortable fields: `name`, `namespace`, `kind`, `apiGroup`, `cluster`, `creationTimestamp`.

A deterministic tiebreaker (`namespace ASC, name ASC`) is always appended.

### Count

```yaml
spec:
  count: true
  limit: 10
```

Returns `status.count` with the total number of matching objects (regardless of limit).

## Relationships

The real power of kuery is relationship traversal. Each relation has its own filters, limit, and nested projection.

### Relation types

| Relation | Direction | Mechanism | Cross-cluster |
|---|---|---|---|
| `owners` | child -> parent | `ownerReferences` UID | No |
| `owners+` | transitive owners | Recursive CTE | No |
| `descendants` | parent -> children | Reverse ownerRef | No |
| `descendants+` | transitive descendants | Recursive CTE | No |
| `references` | source -> target | Spec field extraction (ref-path registry) | No |
| `selects` | selector -> matched | `selector.matchLabels` containment | No |
| `selected-by` | matched -> selector | Reverse selector | No |
| `events` | object -> events | `involvedObject.uid` | No |
| `linked` | annotation ref | `kuery.io/relates-to` | Yes |
| `linked+` | transitive annotation | Recursive CTE | Yes |
| `grouped` | bidirectional | `kuery.io/group` label | Yes |

### Example: Deployment with full descendant tree and secrets

```yaml
apiVersion: kuery.io/v1alpha1
kind: Query
spec:
  filter:
    objects:
      - groupKind:
          apiGroup: apps
          kind: Deployment
        namespace: default
  objects:
    id: true
    cluster: true
    mutablePath: true
    object:
      metadata:
        name: true
        namespace: true
      spec:
        replicas: true
    relations:
      descendants:
        limit: 10
        filters:
          - groupKind:
              kind: ReplicaSet
        objects:
          object:
            metadata:
              name: true
          relations:
            descendants:
              limit: 20
              objects:
                object:
                  metadata:
                    name: true
                relations:
                  references:
                    filters:
                      - groupKind:
                          kind: Secret
                      - groupKind:
                          kind: ConfigMap
                    objects:
                      object:
                        metadata:
                          name: true
                        data: true
```

### Response

```yaml
status:
  objects:
    - id: "abc-123"
      cluster: cluster-a
      mutablePath: /apis/apps/v1/namespaces/default/deployments/nginx
      object:
        metadata:
          name: nginx
          namespace: default
        spec:
          replicas: 3
      relations:
        descendants:
          - object:
              metadata:
                name: nginx-7c8d9b
            relations:
              descendants:
                - object:
                    metadata:
                      name: nginx-7c8d9b-x4k2p
                  relations:
                    references:
                      - object:
                          metadata:
                            name: nginx-tls
                          data:
                            tls.crt: "..."
  count: 47
  incomplete: false
  cursor:
    next: "eyJraW5kIjoiRGVwbG95bWVudCIsIm5hbWUiOiJuZ2lueCJ9"
    pageSize: 50
```

### Transitive descendants (entire ownership tree)

```yaml
spec:
  filter:
    objects:
      - groupKind:
          apiGroup: apps
          kind: Deployment
        name: nginx
  objects:
    relations:
      descendants+:
        objects:
          object:
            metadata:
              name: true
              namespace: true
```

Finds all descendants at any depth (ReplicaSets, Pods, Jobs, etc.) with cycle detection and depth limiting.

### Cross-cluster: linked via annotation

Source object with annotation:

```yaml
metadata:
  annotations:
    kuery.io/relates-to: |
      [{"cluster": "cluster-b", "group": "", "kind": "Secret",
        "namespace": "default", "name": "shared-cert"}]
```

Query:

```yaml
spec:
  filter:
    objects:
      - name: my-app
  objects:
    relations:
      linked: {}
```

### Cross-cluster: grouped by label

Any objects with the same `kuery.io/group` label value are related, across clusters:

```yaml
metadata:
  labels:
    kuery.io/group: "my-app-stack"
```

```yaml
spec:
  filter:
    objects:
      - name: frontend
  objects:
    relations:
      grouped: {}
```

## Deploy to Kubernetes

### As an aggregated API server

```bash
kubectl apply -f deploy/kuery.yaml
```

This creates:
- `kuery-system` namespace
- ServiceAccount + ClusterRoleBinding
- Deployment running the kuery server (SQLite, ephemeral storage)
- Service exposing port 443
- APIService registration for `v1alpha1.kuery.io`

After deployment, the API is available at:

```bash
kubectl get --raw /apis/kuery.io/v1alpha1
```

### With PostgreSQL (production)

Edit `deploy/kuery.yaml` and change the container args:

```yaml
args:
  - --store-driver=postgres
  - --store-dsn=host=postgres.kuery-system user=kuery dbname=kuery sslmode=disable
  - --secure-port=6443
```

### Docker

```bash
docker build -f deploy/Dockerfile -t kuery .
docker run -p 6443:6443 kuery --store-driver=sqlite --store-dsn=/data/kuery.db
```

## Multi-Cluster Sync

kuery uses [multicluster-runtime](https://github.com/multicluster-runtime/multicluster-runtime) to discover and watch clusters. The sync controller:

1. Discovers clusters via the kubeconfig provider
2. Runs API discovery on each cluster to populate `resource_types`
3. Starts dynamic informers for all watchable resources (configurable blacklist)
4. Upserts objects into the SQL database on Add/Update, deletes on Delete
5. Watches CRD changes to refresh discovery automatically

### Cluster lifecycle

| Event | Action |
|---|---|
| Cluster engaged | Upsert cluster row (status=active), run discovery, start informers |
| Object sync | Upsert object row, update cluster `last_seen` |
| Cluster disengaged | Cancel informers, set cluster status=stale |
| GC (opt-in) | Delete objects for stale clusters past TTL |

### Blacklist

High-volume or sensitive resources can be excluded from sync:

```go
// Default blacklist
"events",  "events.events.k8s.io",
"secrets",
"componentstatuses",
"endpoints", "endpointslices.discovery.k8s.io",
```

## Custom Reference Paths

kuery ships with built-in reference paths for core Kubernetes types (Pod -> Secret, Ingress -> Service, etc.). You can extend this for CRDs using annotations:

```yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  annotations:
    kuery.io/refs: |
      [
        {"path": "$.spec.secretRef.name", "targetKind": "Secret"},
        {"path": "$.spec.clusterRef.name", "targetKind": "Cluster", "targetGroup": "cluster.x-k8s.io"}
      ]
```

## kcp Support

kuery supports [kcp](https://github.com/kcp-dev/kcp) environments with APIExport identity disambiguation. The `resource_types` table includes an `identity` column that distinguishes resources with the same name provided by different APIExports.

## Safety Limits

| Limit | Default | Hard cap |
|---|---|---|
| Query timeout | 30s | 30s |
| Max total rows returned | 10,000 | 10,000 |
| Max relation depth | 10 | 20 |
| Max relation blocks per query | 10 | 10 |
| Root object limit | 100 | configurable |

## Observability

kuery exposes Prometheus metrics on the standard `/metrics` endpoint:

| Metric | Type | Description |
|---|---|---|
| `kuery_query_duration_seconds` | Histogram | Query latency by `has_relations`, `incomplete` |
| `kuery_query_errors_total` | Counter | Errors by `error_type` (validation, generation, execution) |
| `kuery_objects_total` | Gauge | Synced objects per cluster |
| `kuery_clusters_total` | Gauge | Clusters by status (active, stale) |
| `kuery_sync_lag_seconds` | Gauge | Time since last sync event per cluster |

## Project Structure

```
github.com/faroshq/kuery/
├── apis/query/v1alpha1/     # API types (Query, QuerySpec, QueryStatus)
├── cmd/kuery/               # Server entrypoint
├── deploy/                  # Dockerfile + Kubernetes manifests
├── docs/                    # Design document
└── internal/
    ├── engine/              # Query engine (SQL generator, projection, relations, tree assembly)
    ├── gc/                  # Cluster garbage collector
    ├── metrics/             # Prometheus metrics
    ├── server/              # Generic API server (handler, apiserver)
    ├── store/               # GORM store (SQLite + PostgreSQL)
    └── sync/                # Multi-cluster sync controller
```

## Development

### Run unit tests

```bash
go test ./... -v -count=1
```

Tests use SQLite in-memory databases. No external dependencies required.

### Run e2e tests

Requires `kind` and `kubectl` installed. Creates 2 kind clusters, deploys workloads, starts kuery, and runs 36 tests covering all query patterns including cross-cluster relations.

```bash
go test -tags=e2e -v -count=1 -timeout=20m ./test/e2e/...
```

### Run a single e2e test

```bash
go test -tags=e2e -v -count=1 -timeout=20m -run TestCrossCluster_Linked ./test/e2e/...
```

### CI

GitHub Actions runs both unit and e2e tests on every push and PR. See [CI workflow](.github/workflows/ci.yml).

## License

See [LICENSE](LICENSE) file.
