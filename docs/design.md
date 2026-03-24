# kuery Design Document

## Overview

kuery is a Kubernetes query API that enables rich, nested queries across multiple clusters. It syncs Kubernetes objects from multiple clusters into a SQL database and provides a powerful query engine that supports filtering, relationship traversal, sparse projection, and pagination.

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
          │                  │
          │  objects         │  All k8s objects, JSONB
          │  resource_types  │  Flattened REST mapper
          │  clusters        │  Cluster health + TTL
          └────────┬────────┘
                   │
                   ▼
          ┌─────────────────┐
          │  Query Engine    │  Query spec → SQL generator → tree assembly
          └────────┬────────┘
                   │
                   ▼
          ┌─────────────────┐
          │  Generic API     │  k8s.io/apiserver, POST-only
          │  Server          │  kind: Query → response inline
          └─────────────────┘
```

## API

### Group, Version, Resource

- **API Group:** `query.kuery.io`
- **Version:** `v1alpha1`
- **Resource:** `queries`
- **Kind:** `Query`

### Serving Model

POST-only virtual resource using `k8s.io/apiserver` (generic API server library). No persistence to etcd. The Query object is submitted via CREATE, and the response is returned inline in the same object — identical pattern to `SubjectAccessReview` or `TokenReview`.

Can be deployed as:
- Aggregated API server behind a kube-apiserver
- Standalone generic API server

### Query Spec

```yaml
apiVersion: query.kuery.io/v1alpha1
kind: Query
spec:
  # --- Cluster filter ---
  cluster:
    name: my-cluster              # specific cluster, empty = all
    labels:                       # filter clusters by labels
      env: production

  # --- Object filters (OR-ed, AND within each entry) ---
  filter:
    objects:
      - groupKind:
          apiGroup: apps
          kind: Deployment        # resolves kind, resource, short names, plural
        name: nginx
        namespace: default
        labels:                   # matchLabels style
          app: nginx
        conditions:
          - type: Available
            status: "True"
            reason: MinimumReplicasAvailable
        creationTimestamp:
          after: "2025-01-01T00:00:00Z"
          before: "2026-12-31T23:59:59Z"
        id: ""                    # opaque ID from previous query
        jsonpath: ""              # JSONPath boolean filter (last resort)
        categories:               # resolved via resource_types table
          - all

  # --- Pagination ---
  limit: 50                       # root objects, default 100
  page:
    first: 0                      # offset
    cursor: ""                    # opaque cursor from previous response

  # --- Ordering ---
  # Default: name ASC (matches kubectl behavior)
  # Tiebreaker always appended: namespace ASC, name ASC
  order:
    - field: kind
      direction: Asc
    - field: name
      direction: Asc
    - field: creationTimestamp
      direction: Desc

  # Sortable fields: name, namespace, kind, apiGroup, cluster, creationTimestamp

  # --- Response shape ---
  count: true                     # include total count (expensive)
  cursor: true                    # include cursor in response

  # --- Max depth for transitive relations ---
  maxDepth: 10                    # per-query, server hard cap: 20

  objects:
    id: true                      # include opaque object ID
    cluster: true                 # include cluster name
    mutablePath: true             # include REST path for direct mutation

    # Sparse projection — only return these fields from the object.
    # `true` means include this field and all descendants.
    # Nested map means include only specified sub-fields.
    # Implemented as jsonb_build_object in SQL — projection happens in the DB.
    object:
      metadata:
        name: true
        namespace: true
        labels: true
        creationTimestamp: true
      spec:
        replicas: true
      status:
        conditions: true

    # --- Relations ---
    # Nested queries following object relationships.
    # Each relation has its own filters, limit, and nested objects spec.
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

### Query Response

The response is a properly nested tree structure:

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
          labels:
            app: nginx
          creationTimestamp: "2025-06-01T00:00:00Z"
        spec:
          replicas: 3
        status:
          conditions:
            - type: Available
              status: "True"
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
                      - object:
                          metadata:
                            name: nginx-config
  cursor:
    next: "eyJraW5kIjoiRGVwbG95bWVudCIsIm5hbWUiOiJuZ2lueCJ9"
    page: 0
    pageSize: 50
  count: 47
  incomplete: false
  warnings: []
```

### Relation Types

| Relation | Direction | Mechanism | Cross-cluster |
|---|---|---|---|
| `owners` | child → parent | `metadata.ownerReferences` UID lookup | No |
| `owners+` | transitive owners | Recursive CTE on ownerRefs | No |
| `descendants` | parent → children | Reverse ownerRef lookup | No |
| `descendants+` | transitive descendants | Recursive CTE on reverse ownerRefs | No |
| `references` | source → target | JSONPath extraction from spec (ref-path registry) | No |
| `selects` | selector holder → matched | `source.selector @> target.labels` | No |
| `selected-by` | matched → selector holder | `target.labels @> source.selector` | No |
| `events` | object → events | `involvedObject.uid` match | No |
| `linked` | explicit annotation ref | `kuery.io/relates-to` annotation | Yes |
| `linked+` | transitive annotation refs | Recursive CTE on annotation refs | Yes |
| `grouped` | bidirectional grouping | `kuery.io/group` label match | Yes |
| `related+` | all directions, all types | Union of all strategies, recursive | Configurable |

Each relation supports the full filter spec (same as root-level filters) and its own `limit`.

### Cross-Cluster Relationships

Kubernetes ownerRefs are intra-cluster only. Cross-cluster relationships use conventions:

**Explicit annotation (directed):**
```yaml
metadata:
  annotations:
    kuery.io/relates-to: |
      [{"cluster": "cluster-b", "group": "", "kind": "Secret",
        "namespace": "default", "name": "shared-cert"}]
```

**Group label (bidirectional):**
```yaml
metadata:
  labels:
    kuery.io/group: "my-app-stack"
```

## Database Schema

### ORM

GORM with dialect abstraction:
- **SQLite** — default, embedded, for development and small scale
- **PostgreSQL** — for production and large scale

Schema managed via GORM auto-migrate.

### Tables

#### `objects`

Stores all synced Kubernetes objects.

```sql
CREATE TABLE objects (
    id                UUID PRIMARY KEY,
    uid               VARCHAR(256) NOT NULL,
    cluster           VARCHAR(256) NOT NULL,
    api_group         VARCHAR(256) NOT NULL DEFAULT '',
    api_version       VARCHAR(64) NOT NULL,
    kind              VARCHAR(256) NOT NULL,
    resource          VARCHAR(256) NOT NULL,
    namespace         VARCHAR(256) NOT NULL DEFAULT '',
    name              VARCHAR(256) NOT NULL,
    labels            JSONB,
    annotations       JSONB,
    owner_refs        JSONB,                     -- [{apiVersion, kind, name, uid}, ...]
    conditions        JSONB,                     -- [{type, status, reason, message}, ...]
    creation_ts       TIMESTAMP,
    resource_version  VARCHAR(64),
    object            JSONB NOT NULL,            -- full object for projection + JSONPath

    UNIQUE(cluster, api_group, kind, namespace, name)
);

-- Indexes
CREATE INDEX idx_obj_uid ON objects(uid);
CREATE INDEX idx_obj_cluster_gvk ON objects(cluster, api_group, kind);
CREATE INDEX idx_obj_cluster_ns_name ON objects(cluster, namespace, name, kind);
CREATE INDEX idx_obj_creation_ts ON objects(creation_ts);
CREATE INDEX idx_obj_name ON objects(name);

-- PostgreSQL only:
CREATE INDEX idx_obj_labels_gin ON objects USING GIN(labels);
CREATE INDEX idx_obj_owner_refs_gin ON objects USING GIN(owner_refs);
CREATE INDEX idx_obj_conditions_gin ON objects USING GIN(conditions);
```

For SQLite (no GIN support), a supplementary labels table:

```sql
CREATE TABLE object_labels (
    object_id   UUID REFERENCES objects(id) ON DELETE CASCADE,
    key         VARCHAR(256) NOT NULL,
    value       VARCHAR(256) NOT NULL,
    PRIMARY KEY(object_id, key)
);

CREATE INDEX idx_labels_kv ON object_labels(key, value);
```

#### `resource_types`

Flattened REST mapper. Populated from cluster discovery API.

```sql
CREATE TABLE resource_types (
    cluster      VARCHAR(256) NOT NULL,
    api_group    VARCHAR(256) NOT NULL DEFAULT '',
    api_version  VARCHAR(64) NOT NULL,
    kind         VARCHAR(256) NOT NULL,
    singular     VARCHAR(256),
    resource     VARCHAR(256) NOT NULL,           -- plural, lowercase
    short_names  TEXT[],
    categories   TEXT[],
    namespaced   BOOLEAN NOT NULL,
    subresources TEXT[],
    identity     VARCHAR(256) NOT NULL DEFAULT '', -- kcp APIExport identity

    PRIMARY KEY(cluster, api_group, resource, identity)
);

CREATE INDEX idx_rt_kind ON resource_types(cluster, lower(kind));
CREATE INDEX idx_rt_resource ON resource_types(cluster, lower(resource));
CREATE INDEX idx_rt_categories ON resource_types USING GIN(categories);
CREATE INDEX idx_rt_short_names ON resource_types USING GIN(short_names);
```

#### `clusters`

Tracks cluster health and lifecycle.

```sql
CREATE TABLE clusters (
    name        VARCHAR(256) PRIMARY KEY,
    status      VARCHAR(64) NOT NULL,            -- 'active', 'stale', 'deleted'
    last_seen   TIMESTAMP NOT NULL,
    engaged_at  TIMESTAMP,
    labels      JSONB,
    ttl         INTERVAL DEFAULT '1 hour'
);
```

- **On engage:** upsert row, status=active, last_seen=now
- **On sync event:** update last_seen
- **On disengage:** set status=stale
- **GC job:** opt-in, disabled by default. When enabled, deletes objects for clusters where `status='stale' AND last_seen + ttl < now()`

## SQL Generator

The SQL generator is the core of the system. It is a compiler: Query spec AST → SQL AST → SQL string.

### Algorithm

The Query spec is a tree. The generator performs a Breadth-First Search over the tree, emitting one SELECT per level, assembled into a UNION ALL.

Each row carries a **path column** — an internal implementation detail that encodes the row's position in the relationship tree. The path is used server-side for tree reconstruction and is never exposed in the API response.

**Path format:** `.<kind_lower>.<namespace/name>.<kind_lower>.<namespace/name>...`

### Inductive SQL Generation

For a query requesting Deployments → ReplicaSets → Pods → Secrets:

```sql
-- Level 0: root objects (Deployments)
SELECT
    '.' || lower(d.kind) || '.' || d.namespace || '/' || d.name AS path,
    d.*,
    <sparse projection> AS projected_object
FROM objects d
JOIN resource_types rt ON rt.cluster = d.cluster
    AND rt.api_group = d.api_group AND rt.kind = d.kind
WHERE d.kind = 'Deployment' AND d.namespace = 'default'

UNION ALL

-- Level 1: descendants (ReplicaSets owned by Deployments)
SELECT
    '.' || lower(d.kind) || '.' || d.namespace || '/' || d.name
        || '.' || lower(rs.kind) || '.' || rs.namespace || '/' || rs.name AS path,
    rs.*,
    <sparse projection>
FROM objects d
JOIN objects rs ON rs.cluster = d.cluster
    AND rs.owner_refs @> jsonb_build_array(jsonb_build_object('uid', d.uid))
    AND rs.kind = 'ReplicaSet'
WHERE d.kind = 'Deployment' AND d.namespace = 'default'

UNION ALL

-- Level 2: descendants of RS (Pods)
SELECT
    '.' || lower(d.kind) || '.' || d.namespace || '/' || d.name
        || '.' || lower(rs.kind) || '.' || rs.namespace || '/' || rs.name
        || '.' || lower(pod.kind) || '.' || pod.namespace || '/' || pod.name AS path,
    pod.*,
    <sparse projection>
FROM objects d
JOIN objects rs ON rs.cluster = d.cluster
    AND rs.owner_refs @> jsonb_build_array(jsonb_build_object('uid', d.uid))
    AND rs.kind = 'ReplicaSet'
JOIN objects pod ON pod.cluster = rs.cluster
    AND pod.owner_refs @> jsonb_build_array(jsonb_build_object('uid', rs.uid))
WHERE d.kind = 'Deployment' AND d.namespace = 'default'

UNION ALL

-- Level 3: references from Pods (Secrets)
SELECT
    '.' || lower(d.kind) || '.' || d.namespace || '/' || d.name
        || '.' || lower(rs.kind) || '.' || rs.namespace || '/' || rs.name
        || '.' || lower(pod.kind) || '.' || pod.namespace || '/' || pod.name
        || '.' || lower(sec.kind) || '.' || sec.namespace || '/' || sec.name AS path,
    sec.*,
    <sparse projection>
FROM objects d
JOIN objects rs ON ...
JOIN objects pod ON ...
JOIN objects sec ON sec.cluster = pod.cluster
    AND sec.namespace = pod.namespace
    AND sec.kind = 'Secret'
    AND sec.name IN (
        SELECT v#>>'{}' FROM jsonb_path_query(
            pod.object, '$.spec.volumes[*].secret.secretName'
        ) v
    )
WHERE d.kind = 'Deployment' AND d.namespace = 'default'

ORDER BY path;
```

Each level N re-joins through all N-1 ancestor levels to reach its objects.

### Transitive Relations (+ suffix)

Transitive relations use recursive CTEs with cycle detection:

```sql
WITH RECURSIVE descendants AS (
    -- Base case: direct children
    SELECT
        child.*,
        ARRAY[child.uid] AS visited,
        1 AS depth,
        parent_path || '.' || lower(child.kind) || '.' || child.namespace || '/' || child.name AS path
    FROM objects child
    WHERE child.owner_refs @> jsonb_build_array(jsonb_build_object('uid', $parent_uid))

    UNION ALL

    -- Inductive step
    SELECT
        next.*,
        d.visited || next.uid,
        d.depth + 1,
        d.path || '.' || lower(next.kind) || '.' || next.namespace || '/' || next.name
    FROM objects next
    JOIN descendants d ON next.owner_refs @> jsonb_build_array(jsonb_build_object('uid', d.uid))
    WHERE next.uid != ALL(d.visited)              -- cycle detection
        AND d.depth < $max_depth                  -- depth limit
)
SELECT * FROM descendants;
```

**Cycle detection:** The `visited` array accumulates UIDs along the path. Before adding a new object, `uid != ALL(visited)` prevents cycles.

### Sparse Projection in SQL

The projection skeleton from the Query spec maps to nested `jsonb_build_object` calls:

```yaml
# Spec:
object:
  metadata:
    name: true
    labels: true
  spec:
    replicas: true
```

```sql
-- Generated SQL:
jsonb_build_object(
    'metadata', jsonb_build_object(
        'name', obj.object->'metadata'->'name',
        'labels', obj.object->'metadata'->'labels'
    ),
    'spec', jsonb_build_object(
        'replicas', obj.object->'spec'->'replicas'
    )
) AS projected_object
```

Projection happens in SQL to minimize data transfer from the database.

### Filter → SQL Mapping

| Filter field | SQL |
|---|---|
| `groupKind.kind` | JOIN `resource_types`, resolve kind/resource/short_names |
| `groupKind.apiGroup` | `obj.api_group = $1` |
| `name` | `obj.name = $1` |
| `namespace` | `obj.namespace = $1` |
| `labels` (matchLabels) | `obj.labels @> $1::jsonb` (GIN indexed) |
| `labels` (matchExpressions In) | `obj.labels->>'key' IN (...)` |
| `labels` (Exists) | `obj.labels ? 'key'` |
| `labels` (DoesNotExist) | `NOT (obj.labels ? 'key')` |
| `conditions` | `obj.conditions @> '[{"type":"Ready","status":"True"}]'` |
| `creationTimestamp.after` | `obj.creation_ts > $1` |
| `creationTimestamp.before` | `obj.creation_ts < $1` |
| `id` | `obj.id = $1` |
| `jsonpath` | `jsonb_path_match(obj.object, $1)` |
| `categories` | JOIN `resource_types`, `$1 = ANY(rt.categories)` |
| `cluster` | `obj.cluster = $1` |

Filter entries within `filter.objects[]` are OR-ed. Criteria within a single entry are AND-ed.

### Resource Resolution via JOIN

The `resource_types` table is JOINed in every SELECT to resolve kind, resource, short names, and categories:

```sql
SELECT obj.* FROM objects obj
JOIN resource_types rt
    ON rt.cluster = obj.cluster
    AND rt.api_group = obj.api_group
    AND rt.kind = obj.kind
WHERE (
    lower(rt.kind) = lower($1)
    OR lower(rt.resource) = lower($1)
    OR lower(rt.singular) = lower($1)
    OR $1 = ANY(rt.short_names)
)
```

The `rt` alias also provides data for `mutablePath` construction in the response.

### Relationship → SQL Patterns

**ownerRef (child → parent):**
```sql
SELECT parent.* FROM objects parent
WHERE parent.uid IN (
    SELECT ref->>'uid' FROM jsonb_array_elements(child.owner_refs) ref
)
```

**Descendants (parent → children):**
```sql
SELECT child.* FROM objects child
WHERE child.owner_refs @> jsonb_build_array(jsonb_build_object('uid', parent.uid))
```

**Spec name references (source → target):**
```sql
SELECT target.* FROM objects target
WHERE target.cluster = source.cluster
    AND target.namespace = source.namespace
    AND target.kind = 'Secret'
    AND target.name IN (
        SELECT v#>>'{}' FROM jsonb_path_query(
            source.object, '$.spec.volumes[*].secret.secretName'
        ) v
    )
```

**Selector-based (selector holder → matched):**
```sql
SELECT target.* FROM objects target
WHERE target.cluster = source.cluster
    AND target.namespace = source.namespace
    AND target.labels @> (source.object->'spec'->'selector'->'matchLabels')
```

**Annotation-based cross-cluster (linked):**
```sql
SELECT target.* FROM objects target
JOIN jsonb_array_elements(
    (source.object->'metadata'->'annotations'->>'kuery.io/relates-to')::jsonb
) AS ref ON true
WHERE target.cluster = ref->>'cluster'
    AND target.api_group = COALESCE(ref->>'group', '')
    AND target.kind = ref->>'kind'
    AND target.namespace = COALESCE(ref->>'namespace', '')
    AND target.name = ref->>'name'
```

**Group label (bidirectional):**
```sql
SELECT other.* FROM objects other
WHERE other.labels->>'kuery.io/group' = source.labels->>'kuery.io/group'
    AND other.id != source.id
```

### Reference Path Registry

Built-in mappings for core Kubernetes types. Defines which JSONPaths in an object's spec reference other objects:

```
Pod:
  $.spec.volumes[*].secret.secretName           → Secret
  $.spec.volumes[*].configMap.name              → ConfigMap
  $.spec.volumes[*].persistentVolumeClaim.claimName → PersistentVolumeClaim
  $.spec.serviceAccountName                     → ServiceAccount
  $.spec.containers[*].env[*].valueFrom.secretKeyRef.name → Secret
  $.spec.containers[*].env[*].valueFrom.configMapKeyRef.name → ConfigMap
  $.spec.containers[*].envFrom[*].secretRef.name → Secret
  $.spec.containers[*].envFrom[*].configMapRef.name → ConfigMap
  $.spec.imagePullSecrets[*].name               → Secret

Ingress:
  $.spec.rules[*].http.paths[*].backend.service.name → Service
  $.spec.tls[*].secretName                      → Secret

PersistentVolumeClaim:
  $.spec.storageClassName                       → StorageClass (storage.k8s.io)
  $.spec.volumeName                             → PersistentVolume

RoleBinding / ClusterRoleBinding:
  $.roleRef.name                                → ClusterRole/Role (rbac.authorization.k8s.io)
```

Custom CRD ref-paths are defined via annotations on the CRD itself:

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

### Ordering

Default order (when none specified): `ORDER BY obj.name ASC` — matches kubectl behavior.

Deterministic tiebreaker always appended: `namespace ASC, name ASC` to ensure stable ordering for pagination.

Ordering applies to root objects. Within relation subtrees, ordering is done server-side in Go after tree assembly.

### Pagination

Keyset cursor pagination:
- Encode last row's sort key values as opaque base64 token
- Decode on next request → `WHERE (kind, name) > ($last_kind, $last_name)`
- `limit` applies to root objects; each relation has its own `limit`

## Processing Pipeline

```
Query spec (POST)
    │
    ▼
1. Validate (limits, depth, allowed fields)
    │
    ▼
2. SQL Generator
   ├── Walk query tree (BFS)
   ├── At each level: emit SELECT with JOIN chain, WHERE, projection
   ├── Transitive relations → recursive CTE
   ├── Assemble into UNION ALL with path column
   └── Add ORDER BY path, LIMIT
    │
    ▼
3. Execute SQL against database
    │
    ▼
4. Flat result set with path column (internal)
    │
    ▼
5. Tree assembly (server-side Go)
   ├── Group rows by path prefix
   ├── Build nested objects[].relations structure
   ├── Apply per-subtree ordering
   └── Deduplicate if needed
    │
    ▼
6. Response in Query status
```

### SQL vs Go Responsibilities

| Step | SQL | Go |
|---|---|---|
| Filtering | ✅ | |
| JOIN with resource_types | ✅ | |
| Sparse projection | ✅ (jsonb_build_object) | |
| Path construction | ✅ | |
| Root-level ordering | ✅ | |
| Pagination (limit/cursor) | ✅ | |
| Count | ✅ | |
| Relation ordering within subtrees | | ✅ |
| Tree assembly from flat rows | | ✅ |
| Deduplication | | ✅ |

## Write Path: Sync Controller

Uses [multicluster-runtime](https://github.com/multicluster-runtime/multicluster-runtime) with the `kubeconfig` provider.

### Lifecycle

1. **Provider discovers cluster** → `Engage(ctx, clusterName, cluster)`
2. **Run discovery** → populate `resource_types` table for that cluster
3. **Watch CRDs** → refresh `resource_types` on CRD changes
4. **Start informers** for all discoverable GVKs (configurable blacklist for exclusions)
5. **On Add/Update** → upsert row in `objects` table + update `clusters.last_seen`
6. **On Delete** → delete row from `objects` table
7. **On Disengage** → set `clusters.status = 'stale'`

### What to sync

All discoverable resources with list/watch verbs, minus a configurable blacklist. The blacklist can exclude sensitive (Secrets) or high-volume (Events) resources.

### Object storage

Objects are stored in their storage version as returned by the informer. No conversion webhooks, no version negotiation. Schemas may differ between objects of the same resource across clusters or across creation times.

## kcp Support

The `resource_types` table includes an `identity` column for kcp APIExport identity disambiguation. For vanilla Kubernetes this is empty. For kcp it distinguishes resources with the same name under different APIExports.

The kcp provider from `kcp-dev/multicluster-provider` is used for kcp environments.

## Cluster Lifecycle

### `clusters` table

Tracks cluster health:
- **On engage:** upsert, status=active, last_seen=now
- **On sync event:** update last_seen
- **On disengage:** status=stale

### Garbage collection

**Opt-in, disabled by default.** When enabled via server config:
- Configurable TTL per cluster
- Background GC job deletes objects for clusters where `status='stale' AND last_seen + ttl < now()`

Disabled by default to support edge clusters that go on/off frequently.

## Safety Limits

| Limit | Default | Hard cap |
|---|---|---|
| Query timeout | 30s | 30s |
| Max total rows returned | 10,000 | 10,000 |
| Max relation depth | 10 | 20 |
| Max relation blocks per query | 10 | 10 |
| Root object limit (default) | 100 | configurable |

## Cross-Cutting Concerns

### Auth

No per-object RBAC in the query engine. Authentication and authorization handled at the generic API server layer (who can POST a Query resource). Single-tenant for now; multi-tenancy deferred.

### Observability

- Structured logging (controller-runtime)
- Prometheus metrics: query latency, sync lag per cluster, object counts per cluster, error rates
- Tracing deferred

### Testing

- **Unit tests:** SQLite backend
- **Integration tests:** PostgreSQL via testcontainers
- **E2E:** deferred

## Dependencies

- `k8s.io/apiserver` — generic API server
- `k8s.io/apimachinery` — API types
- `sigs.k8s.io/controller-runtime` — controller framework
- `sigs.k8s.io/multicluster-runtime` — multi-cluster support
- `gorm.io/gorm` — ORM
- `gorm.io/driver/sqlite` — SQLite driver
- `gorm.io/driver/postgres` — PostgreSQL driver

## Project Structure

```
github.com/faroshq/kuery/
├── apis/query/v1alpha1/
│   ├── types.go              # Query CRD types
│   ├── groupversion_info.go  # Scheme registration
│   └── doc.go
├── internal/
│   ├── sync/
│   │   ├── controller.go     # multicluster-runtime reconciler
│   │   ├── handler.go        # informer event handlers
│   │   └── discovery.go      # GVK discovery, resource_types sync
│   ├── store/
│   │   ├── store.go          # Store interface (GORM-based)
│   │   ├── models.go         # GORM model structs
│   │   ├── postgres.go       # PostgreSQL-specific (GIN indexes)
│   │   └── sqlite.go         # SQLite-specific (labels table)
│   ├── engine/
│   │   ├── generator.go      # Recursive SQL generator (core)
│   │   ├── projection.go     # Sparse projection builder
│   │   ├── refs.go           # Reference path registry
│   │   ├── tree.go           # Flat rows → nested tree assembly
│   │   └── engine.go         # Orchestrates: validate → generate → execute → assemble
│   └── server/
│       ├── apiserver.go      # k8s.io/apiserver setup
│       └── handler.go        # Query POST handler
├── cmd/kuery/
│   └── main.go
└── deploy/
```

## Implementation Phases

### Phase 1 — Scaffold
- Go module, GORM models, auto-migrate
- API types + codegen
- Generic API server skeleton (POST-only Query)

### Phase 2 — Sync Controller
- multicluster-runtime manager + kubeconfig provider
- Discovery sync → resource_types table
- Informer event handlers → objects table upserts
- Cluster engage/disengage lifecycle
- CRD watch → discovery refresh

### Phase 3 — Query Engine (basic)
- SQL generator: single-level queries (filter, order, pagination)
- Resource resolution via resource_types JOIN
- Sparse projection via jsonb_build_object
- Basic filters: groupKind, name, namespace, labels, conditions

### Phase 4 — Relations
- Owner/descendant relation SQL generation
- Spec reference resolution (ref-path registry)
- Selector-based relations
- Event relations
- BFS UNION ALL assembly with path column
- Server-side tree reconstruction

### Phase 5 — Transitive Relations
- Recursive CTEs for `+` suffix relations
- Cycle detection via visited UID array
- Depth limits

### Phase 6 — Cross-Cluster & Annotations
- Annotation-based explicit refs (`kuery.io/relates-to`)
- Group label relations (`kuery.io/group`)
- Cross-cluster traversal

### Phase 7 — Hardening
- Query timeouts and limits enforcement
- Metrics and structured logging
- Cluster health tracking and optional GC
- CRD annotation-based custom ref-paths

### Phase 8 — kcp Support
- kcp provider integration
- APIExport identity in resource_types
- kcp-specific discovery

## Decisions Log

| # | Decision | Resolution |
|---|---|---|
| Q1 | Serving model | Generic API server (`k8s.io/apiserver`), POST-only like SAR |
| Q2 | Module path | `github.com/faroshq/kuery` |
| Q3 | What GVKs to sync | All discoverable, configurable blacklist |
| Q4 | Database | GORM ORM, SQLite default/dev, PostgreSQL prod |
| Q5 | Provider | kubeconfig provider |
| Q6 | Transitive depth | Per-query maxDepth, default 10, hard cap 20 |
| Q7 | Pagination scope | Separate limits: root + per-relation |
| Q8 | Categories | Discovery-only, custom deferred |
| Q9 | RBAC | No per-object RBAC, auth at API server layer |
| Q10 | Complexity limits | Timeout 30s, max rows 10k, max 10 relation blocks |
| Q11 | Ref-path extensibility | Hardcoded strategies + CRD annotations for custom |
| Q12 | Selector resolution | Query-time, GIN (Postgres) / labels table (SQLite) |
| Q14 | Relation filters | Full filter spec on relations |
| Q17 | Custom CRD ref-paths | Annotation on the CRD itself |
| Q19 | Object versions | Storage version only, no conversion |
| Q20 | Discovery refresh | Watch CRD changes, refresh on change |
| Q21 | API naming | `query.kuery.io`, resource `queries` |
| Q22 | Cluster GC | clusters table, TTL-based GC opt-in, disabled by default |
| Q23 | Schema migrations | GORM auto-migrate |
| Q24 | Observability | Structured logging + Prometheus metrics |
| Q25 | Testing | SQLite unit tests + Postgres testcontainers integration |
| Q26 | Multi-tenancy | Single-tenant, don't block multi-tenancy |
| Q27 | kcp identity | Explicit `identity` column on resource_types |
