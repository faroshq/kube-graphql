# Generated SQL Examples

This document shows the actual SQL queries kuery generates for each type of query.
All examples use the SQLite dialect. PostgreSQL equivalents use `@>`, `jsonb_build_object`, and `jsonb_array_elements` instead of `json_extract` and `json_each`.

## Query 1: No filter (list all objects)

**Input:**
```yaml
spec:
  count: true
  limit: 3
```

**Generated SQL:**
```sql
SELECT obj.id, obj.uid, obj.cluster, obj.api_group, obj.api_version,
       obj.kind, obj.resource, obj.namespace, obj.name,
       obj.labels, obj.annotations, obj.owner_refs, obj.conditions,
       obj.creation_ts, obj.resource_version,
       obj.object AS projected_object,
       '.' || lower(obj.kind) || '.' || obj.namespace || '/' || obj.name AS path,
       0 AS level, '' AS relation_name
FROM objects obj
ORDER BY obj.name ASC, obj.namespace ASC
LIMIT 3
```

**Count SQL:**
```sql
SELECT COUNT(*) FROM objects obj
```

**Args:** `[]`

---

## Query 2: GroupKind filter (find all Deployments)

**Input:**
```yaml
spec:
  filter:
    objects:
      - groupKind:
          apiGroup: apps
          kind: Deployment
```

**Generated SQL:**
```sql
SELECT obj.id, ..., obj.object AS projected_object,
       '.' || lower(obj.kind) || '.' || obj.namespace || '/' || obj.name AS path,
       0 AS level, '' AS relation_name
FROM objects obj
WHERE (
  obj.api_group = ?
  AND EXISTS (
    SELECT 1 FROM resource_types rt
    WHERE rt.cluster = obj.cluster
      AND rt.api_group = obj.api_group
      AND rt.kind = obj.kind
      AND (
        lower(rt.kind) = lower(?)
        OR lower(rt.resource) = lower(?)
        OR lower(rt.singular) = lower(?)
        OR EXISTS (
          SELECT 1 FROM json_each(rt.short_names)
          WHERE json_each.value = lower(?)
        )
      )
  )
)
ORDER BY obj.name ASC, obj.namespace ASC
LIMIT 100
```

**Args:** `[apps, Deployment, Deployment, Deployment, deployment]`

The `EXISTS` subquery resolves kind by checking kind name, resource name (plural), singular name, and short names. This avoids JOIN-based row duplication when multiple API versions exist for the same resource.

---

## Query 3: Cluster + GroupKind filter

**Input:**
```yaml
spec:
  cluster:
    name: kuery-alpha
  filter:
    objects:
      - groupKind:
          apiGroup: apps
          kind: Deployment
```

**Generated SQL:**
```sql
SELECT ...
FROM objects obj
WHERE obj.cluster = ?
  AND (
    obj.api_group = ?
    AND EXISTS (
      SELECT 1 FROM resource_types rt
      WHERE rt.cluster = obj.cluster
        AND rt.api_group = obj.api_group
        AND rt.kind = obj.kind
        AND (lower(rt.kind) = lower(?) OR lower(rt.resource) = lower(?)
             OR lower(rt.singular) = lower(?)
             OR EXISTS (SELECT 1 FROM json_each(rt.short_names) WHERE json_each.value = lower(?)))
    )
  )
ORDER BY obj.name ASC, obj.namespace ASC
LIMIT 100
```

**Args:** `[kuery-alpha, apps, Deployment, Deployment, Deployment, deployment]`

---

## Query 4: Non-transitive descendants (Deploy -> RS -> Pod)

Uses BFS `UNION ALL` — one SELECT per level, each re-joining through all ancestors.

**Input:**
```yaml
spec:
  filter:
    objects:
      - groupKind: { apiGroup: apps, kind: Deployment }
        namespace: demo
  objects:
    relations:
      descendants:         # Level 1: direct children (ReplicaSets)
        objects:
          relations:
            descendants:   # Level 2: grandchildren (Pods)
```

**Generated SQL:**
```sql
-- CTE: root objects with ORDER BY/LIMIT (avoids ORDER BY inside UNION ALL)
WITH root_objects AS (
  SELECT * FROM objects l0
  WHERE (l0.api_group = ?
    AND EXISTS (SELECT 1 FROM resource_types rt WHERE ...)
    AND l0.namespace = ?)
  ORDER BY l0.name ASC, l0.namespace ASC
  LIMIT 100
)

-- Level 0: Root Deployments
SELECT l0.id, ..., l0.object AS projected_object,
       '.' || lower(l0.kind) || '.' || l0.namespace || '/' || l0.name AS path,
       0 AS level, '' AS relation_name
FROM root_objects l0

UNION ALL

-- Level 1: Descendants (ReplicaSets owned by the Deployment)
SELECT l1.id, ..., l1.object AS projected_object,
       '.' || lower(l0.kind) || '.' || l0.namespace || '/' || l0.name
         || '.' || lower(l1.kind) || '.' || l1.namespace || '/' || l1.name AS path,
       1 AS level, 'descendants' AS relation_name
FROM root_objects l0
JOIN objects l1 ON l1.cluster = l0.cluster
  AND EXISTS (
    SELECT 1 FROM json_each(l1.owner_refs) oref
    WHERE json_extract(oref.value, '$.uid') = l0.uid
  )

UNION ALL

-- Level 2: Descendants of descendants (Pods owned by the ReplicaSet)
SELECT l2.id, ..., l2.object AS projected_object,
       '.' || lower(l0.kind) || '.' || l0.namespace || '/' || l0.name
         || '.' || lower(l1.kind) || '.' || l1.namespace || '/' || l1.name
         || '.' || lower(l2.kind) || '.' || l2.namespace || '/' || l2.name AS path,
       2 AS level, 'descendants' AS relation_name
FROM root_objects l0
JOIN objects l1 ON l1.cluster = l0.cluster
  AND EXISTS (SELECT 1 FROM json_each(l1.owner_refs) oref
    WHERE json_extract(oref.value, '$.uid') = l0.uid)
JOIN objects l2 ON l2.cluster = l1.cluster
  AND EXISTS (SELECT 1 FROM json_each(l2.owner_refs) oref
    WHERE json_extract(oref.value, '$.uid') = l1.uid)

ORDER BY path
```

**Args:** `[apps, Deployment, Deployment, Deployment, deployment, demo]`

The `path` column encodes the hierarchical position. Results are sorted by path so the Go tree assembler can reconstruct the nested structure by grouping rows by their path prefix.

---

## Query 5: Recursive CTE (descendants+) with references

The most complex query — uses `WITH RECURSIVE` for transitive descendants, plus a sub-relation JOIN for spec references (Secrets, ConfigMaps, PVCs, ServiceAccounts).

**Input:**
```yaml
spec:
  filter:
    objects:
      - groupKind: { apiGroup: apps, kind: Deployment }
        name: nginx
        namespace: demo
  objects:
    relations:
      descendants+:         # Recursive: RS, Pods, and anything below
        objects:
          relations:
            references: {}  # At each level: find referenced Secrets/ConfigMaps/PVCs
```

**Generated SQL:**
```sql
WITH RECURSIVE
  -- CTE 1: Root objects (the nginx Deployment)
  root_objects AS (
    SELECT * FROM objects l0
    WHERE (l0.api_group = ? AND EXISTS (...) AND l0.name = ? AND l0.namespace = ?)
    ORDER BY l0.name ASC, l0.namespace ASC
    LIMIT 100
  ),

  -- CTE 2: Recursive transitive descendants with cycle detection
  trans_descendants_1 AS (
    -- Base case: direct children of root
    SELECT curr.id, curr.uid, curr.cluster, ..., curr.object,
           curr.object AS projected_object,
           '.' || lower(l0.kind) || '.' || l0.namespace || '/' || l0.name
             || '.' || lower(curr.kind) || '.' || curr.namespace || '/' || curr.name AS path,
           1 AS depth,
           ',' || curr.uid || ',' AS visited,
           1 AS level,
           'descendants+' AS relation_name
    FROM root_objects l0
    JOIN objects curr ON EXISTS (
      SELECT 1 FROM json_each(curr.owner_refs) oref
      WHERE json_extract(oref.value, '$.uid') = l0.uid
    ) AND curr.cluster = l0.cluster

    UNION ALL

    -- Recursive step: children of the previous level
    SELECT next.id, next.uid, ..., next.object,
           next.object AS projected_object,
           trans_descendants_1.path
             || '.' || lower(next.kind) || '.' || next.namespace || '/' || next.name AS path,
           trans_descendants_1.depth + 1,
           trans_descendants_1.visited || next.uid || ',' AS visited,
           1 AS level,
           'descendants+' AS relation_name
    FROM objects next
    JOIN trans_descendants_1 ON EXISTS (
      SELECT 1 FROM json_each(next.owner_refs) oref
      WHERE json_extract(oref.value, '$.uid') = trans_descendants_1.uid
    ) AND next.cluster = trans_descendants_1.cluster
    WHERE trans_descendants_1.visited NOT LIKE '%,' || next.uid || ',%'  -- cycle detection
      AND trans_descendants_1.depth < 10                                  -- depth limit
  )

-- Part 1: Root objects (Deployment)
SELECT l0.*, l0.object AS projected_object,
       '.' || lower(l0.kind) || '.' || l0.namespace || '/' || l0.name AS path,
       0 AS level, '' AS relation_name
FROM root_objects l0

UNION ALL

-- Part 2: All transitive descendants (ReplicaSet, Pods)
SELECT trans_descendants_1.id, ...,
       trans_descendants_1.projected_object,
       trans_descendants_1.path,
       trans_descendants_1.level,
       trans_descendants_1.relation_name
FROM trans_descendants_1

UNION ALL

-- Part 3: References from each descendant (Secrets, ConfigMaps, PVCs, ServiceAccounts)
SELECT l2.id, ..., l2.object AS projected_object,
       trans_descendants_1.path
         || '.' || lower(l2.kind) || '.' || l2.namespace || '/' || l2.name AS path,
       2 AS level, 'references' AS relation_name
FROM trans_descendants_1
JOIN objects l2 ON l2.cluster = trans_descendants_1.cluster
  AND l2.namespace = trans_descendants_1.namespace
  AND l2.name IN (
    -- Extract referenced Secret names from volumes
    SELECT json_extract(je.value, '$.secret.secretName')
    FROM json_each(json_extract(trans_descendants_1.object, '$.spec.volumes')) je
    WHERE json_extract(je.value, '$.secret.secretName') IS NOT NULL

    UNION ALL

    -- Extract referenced ConfigMap names from volumes
    SELECT json_extract(je.value, '$.configMap.name')
    FROM json_each(json_extract(trans_descendants_1.object, '$.spec.volumes')) je
    WHERE json_extract(je.value, '$.configMap.name') IS NOT NULL

    UNION ALL

    -- Extract referenced PVC names from volumes
    SELECT json_extract(je.value, '$.persistentVolumeClaim.claimName')
    FROM json_each(json_extract(trans_descendants_1.object, '$.spec.volumes')) je
    WHERE json_extract(je.value, '$.persistentVolumeClaim.claimName') IS NOT NULL

    UNION ALL

    -- Extract serviceAccountName
    SELECT json_extract(trans_descendants_1.object, '$.spec.serviceAccountName')

    UNION ALL

    -- Extract imagePullSecrets
    SELECT json_extract(je.value, '$.name')
    FROM json_each(json_extract(trans_descendants_1.object, '$.spec.imagePullSecrets')) je
    WHERE json_extract(je.value, '$.name') IS NOT NULL

    UNION ALL

    -- Extract env secretKeyRef names (nested: containers[].env[].valueFrom.secretKeyRef.name)
    SELECT json_extract(env.value, '$.valueFrom.secretKeyRef.name')
    FROM json_each(json_extract(trans_descendants_1.object, '$.spec.containers')) cont,
         json_each(json_extract(cont.value, '$.env')) env
    WHERE json_extract(env.value, '$.valueFrom.secretKeyRef.name') IS NOT NULL

    UNION ALL

    -- Extract env configMapKeyRef names
    SELECT json_extract(env.value, '$.valueFrom.configMapKeyRef.name')
    FROM json_each(json_extract(trans_descendants_1.object, '$.spec.containers')) cont,
         json_each(json_extract(cont.value, '$.env')) env
    WHERE json_extract(env.value, '$.valueFrom.configMapKeyRef.name') IS NOT NULL

    -- ... (envFrom secretRef, configMapRef, Ingress backends, etc.)
  )

ORDER BY path
```

**Args:** `[apps, Deployment, Deployment, Deployment, deployment, nginx, demo]`

Key techniques:
- **Cycle detection:** `visited` column accumulates UIDs as comma-separated string. `NOT LIKE '%,uid,%'` prevents revisiting.
- **Depth limit:** `depth < 10` (configurable via `spec.maxDepth`).
- **Ref-path registry:** The `IN (...)` subquery unions all built-in reference extraction patterns (volumes, env, imagePullSecrets, etc.) from the ref-path registry.
- **CTE carries raw `object`:** The recursive CTE includes the full JSON object so the references sub-relation can extract spec fields via `json_extract`.

---

## Query 6: Label filter + custom ordering

**Input:**
```yaml
spec:
  filter:
    objects:
      - namespace: demo
        labels:
          app: nginx
  order:
    - field: kind
      direction: Asc
```

**Generated SQL:**
```sql
SELECT ...
FROM objects obj
WHERE (obj.namespace = ? AND json_extract(obj.labels, '$.app') = ?)
ORDER BY obj.kind ASC, obj.namespace ASC, obj.name ASC
LIMIT 100
```

**Args:** `[demo, nginx]`

Labels use `json_extract` for SQLite (PostgreSQL uses `@>` with GIN index). Tiebreaker `namespace ASC, name ASC` is always appended.

---

## Query 7: OR filter (Pod OR Service)

**Input:**
```yaml
spec:
  filter:
    objects:
      - groupKind: { kind: Pod }
        namespace: demo
      - groupKind: { kind: Service }
        namespace: demo
```

**Generated SQL:**
```sql
SELECT ...
FROM objects obj
WHERE (
  (
    EXISTS (SELECT 1 FROM resource_types rt
      WHERE rt.cluster = obj.cluster AND rt.api_group = obj.api_group AND rt.kind = obj.kind
        AND (lower(rt.kind) = lower(?) OR lower(rt.resource) = lower(?)
             OR lower(rt.singular) = lower(?)
             OR EXISTS (SELECT 1 FROM json_each(rt.short_names) WHERE json_each.value = lower(?))))
    AND obj.namespace = ?
  )
  OR
  (
    EXISTS (SELECT 1 FROM resource_types rt
      WHERE rt.cluster = obj.cluster AND rt.api_group = obj.api_group AND rt.kind = obj.kind
        AND (lower(rt.kind) = lower(?) OR lower(rt.resource) = lower(?)
             OR lower(rt.singular) = lower(?)
             OR EXISTS (SELECT 1 FROM json_each(rt.short_names) WHERE json_each.value = lower(?))))
    AND obj.namespace = ?
  )
)
ORDER BY obj.name ASC, obj.namespace ASC
LIMIT 100
```

**Args:** `[Pod, Pod, Pod, pod, demo, Service, Service, Service, service, demo]`

Filter entries within `filter.objects[]` are OR-ed. Criteria within a single entry are AND-ed.

---

## Relation JOIN Patterns

### Descendants (parent -> children via ownerRefs)

**SQLite:**
```sql
JOIN objects child ON child.cluster = parent.cluster
  AND EXISTS (
    SELECT 1 FROM json_each(child.owner_refs) oref
    WHERE json_extract(oref.value, '$.uid') = parent.uid
  )
```

**PostgreSQL:**
```sql
JOIN objects child ON child.cluster = parent.cluster
  AND child.owner_refs @> jsonb_build_array(jsonb_build_object('uid', parent.uid))
```

### Owners (child -> parent via ownerRefs)

**SQLite:**
```sql
JOIN objects parent ON parent.cluster = child.cluster
  AND parent.uid IN (
    SELECT json_extract(oref.value, '$.uid')
    FROM json_each(child.owner_refs) oref
  )
```

### Selects (selector holder -> matched by labels)

**SQLite:**
```sql
JOIN objects target ON target.cluster = source.cluster
  AND target.namespace = source.namespace
  AND target.id != source.id
  AND json_extract(source.object, '$.spec.selector.matchLabels') IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM json_each(json_extract(source.object, '$.spec.selector.matchLabels')) sel
    WHERE json_extract(target.labels, '$.' || sel.key) IS NULL
       OR json_extract(target.labels, '$.' || sel.key) != sel.value
  )
```

### Events (object -> events via involvedObject.uid)

```sql
JOIN objects event ON event.cluster = source.cluster
  AND event.kind = 'Event'
  AND json_extract(event.object, '$.involvedObject.uid') = source.uid
```

### Linked (annotation-based cross-cluster)

```sql
JOIN json_each(json_extract(source.annotations, '$."kuery.io/relates-to"')) ref ON 1=1
JOIN objects target
  ON target.cluster = COALESCE(json_extract(ref.value, '$.cluster'), source.cluster)
  AND target.api_group = COALESCE(json_extract(ref.value, '$.group'), '')
  AND target.kind = json_extract(ref.value, '$.kind')
  AND target.namespace = COALESCE(json_extract(ref.value, '$.namespace'), '')
  AND target.name = json_extract(ref.value, '$.name')
```

### Grouped (bidirectional via kuery.io/group label)

```sql
JOIN objects other
  ON json_extract(other.labels, '$."kuery.io/group"')
     = json_extract(source.labels, '$."kuery.io/group"')
  AND other.id != source.id
  AND json_extract(source.labels, '$."kuery.io/group"') IS NOT NULL
```
