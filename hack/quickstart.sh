#!/usr/bin/env bash
#
# kuery quickstart — creates 2 kind clusters, syncs them, and runs example queries.
#
# Prerequisites: kind, kubectl, go, curl, jq
#
# Usage:
#   ./hack/quickstart.sh          # full setup + demo
#   ./hack/quickstart.sh cleanup  # tear down clusters
#
set -euo pipefail

CLUSTER_1="kuery-alpha"
CLUSTER_2="kuery-beta"
KUBECONFIG_DIR="/tmp/kuery-quickstart"
KUERY_PORT=6443
KUERY_PID=""

RED='\033[0;31m'
GREEN='\033[0;32m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m' # No Color

log()  { echo -e "${GREEN}==>${NC} $*"; }
info() { echo -e "${CYAN}    $*${NC}"; }
bold() { echo -e "${BOLD}$*${NC}"; }

cleanup() {
    log "Cleaning up..."
    if [[ -n "${KUERY_PID}" ]] && kill -0 "${KUERY_PID}" 2>/dev/null; then
        kill "${KUERY_PID}" 2>/dev/null || true
        wait "${KUERY_PID}" 2>/dev/null || true
    fi
    kind delete cluster --name "${CLUSTER_1}" 2>/dev/null || true
    kind delete cluster --name "${CLUSTER_2}" 2>/dev/null || true
    rm -rf "${KUBECONFIG_DIR}"
    log "Done."
}

if [[ "${1:-}" == "cleanup" ]]; then
    cleanup
    exit 0
fi

trap cleanup EXIT

# --- Check prerequisites ---
for cmd in kind kubectl go curl jq; do
    if ! command -v "$cmd" &>/dev/null; then
        echo "Error: $cmd is required but not installed." >&2
        exit 1
    fi
done

# --- Create clusters ---
mkdir -p "${KUBECONFIG_DIR}"

log "Creating kind cluster: ${CLUSTER_1}"
kind create cluster --name "${CLUSTER_1}" --wait 60s 2>&1 | sed 's/^/    /'
kind get kubeconfig --name "${CLUSTER_1}" > "${KUBECONFIG_DIR}/${CLUSTER_1}.kubeconfig"

log "Creating kind cluster: ${CLUSTER_2}"
kind create cluster --name "${CLUSTER_2}" --wait 60s 2>&1 | sed 's/^/    /'
kind get kubeconfig --name "${CLUSTER_2}" > "${KUBECONFIG_DIR}/${CLUSTER_2}.kubeconfig"

# --- Deploy sample workloads ---
log "Deploying sample workloads to ${CLUSTER_1}"
kubectl --kubeconfig="${KUBECONFIG_DIR}/${CLUSTER_1}.kubeconfig" create namespace demo 2>/dev/null || true
kubectl --kubeconfig="${KUBECONFIG_DIR}/${CLUSTER_1}.kubeconfig" -n demo apply -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx
  labels:
    app: nginx
    env: production
spec:
  replicas: 2
  selector:
    matchLabels:
      app: nginx
  template:
    metadata:
      labels:
        app: nginx
    spec:
      containers:
        - name: nginx
          image: nginx:1.27
          ports:
            - containerPort: 80
---
apiVersion: v1
kind: Service
metadata:
  name: nginx
  labels:
    app: nginx
spec:
  selector:
    app: nginx
  ports:
    - port: 80
      targetPort: 80
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: nginx-config
  labels:
    app: nginx
data:
  nginx.conf: |
    server { listen 80; }
EOF

log "Deploying sample workloads to ${CLUSTER_2}"
kubectl --kubeconfig="${KUBECONFIG_DIR}/${CLUSTER_2}.kubeconfig" create namespace demo 2>/dev/null || true
kubectl --kubeconfig="${KUBECONFIG_DIR}/${CLUSTER_2}.kubeconfig" -n demo apply -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: redis
  labels:
    app: redis
    env: staging
spec:
  replicas: 1
  selector:
    matchLabels:
      app: redis
  template:
    metadata:
      labels:
        app: redis
    spec:
      containers:
        - name: redis
          image: redis:7
          ports:
            - containerPort: 6379
---
apiVersion: v1
kind: Service
metadata:
  name: redis
  labels:
    app: redis
spec:
  selector:
    app: redis
  ports:
    - port: 6379
      targetPort: 6379
EOF

# Wait for deployments to be ready.
log "Waiting for workloads to be ready..."
kubectl --kubeconfig="${KUBECONFIG_DIR}/${CLUSTER_1}.kubeconfig" -n demo rollout status deployment/nginx --timeout=120s 2>&1 | sed 's/^/    /'
kubectl --kubeconfig="${KUBECONFIG_DIR}/${CLUSTER_2}.kubeconfig" -n demo rollout status deployment/redis --timeout=120s 2>&1 | sed 's/^/    /'

# --- Start kuery ---
log "Starting kuery server (syncing both clusters)..."
go run ./cmd/kuery \
    --store-driver=sqlite \
    --store-dsn="${KUBECONFIG_DIR}/kuery.db" \
    --secure-port=${KUERY_PORT} \
    --kubeconfigs="${CLUSTER_1}=${KUBECONFIG_DIR}/${CLUSTER_1}.kubeconfig,${CLUSTER_2}=${KUBECONFIG_DIR}/${CLUSTER_2}.kubeconfig" \
    > "${KUBECONFIG_DIR}/kuery.log" 2>&1 &
KUERY_PID=$!

# Wait for the server to be ready.
log "Waiting for kuery API to be ready..."
for i in $(seq 1 60); do
    if curl -sk "https://localhost:${KUERY_PORT}/apis/kuery.io/v1alpha1" >/dev/null 2>&1; then
        break
    fi
    if ! kill -0 "${KUERY_PID}" 2>/dev/null; then
        echo "Error: kuery server exited. Logs:" >&2
        cat "${KUBECONFIG_DIR}/kuery.log" >&2
        exit 1
    fi
    sleep 1
done

# Give the sync controller time to discover and sync objects.
log "Waiting for object sync (15s)..."
sleep 15

CURL="curl -sk -X POST https://localhost:${KUERY_PORT}/apis/kuery.io/v1alpha1/queries -H Content-Type:application/json"

# ============================================================================
echo ""
bold "=========================================="
bold "  kuery quickstart — example queries"
bold "=========================================="
echo ""

# --- Query 1: List all objects across both clusters ---
log "Query 1: Count all synced objects across both clusters"
echo ""
info "POST /apis/kuery.io/v1alpha1/queries"
cat <<'QUERY'
{
  "apiVersion": "kuery.io/v1alpha1",
  "kind": "Query",
  "spec": {
    "count": true,
    "limit": 5,
    "objects": {
      "id": true,
      "cluster": true,
      "object": {
        "metadata": { "name": true, "namespace": true }
      }
    }
  }
}
QUERY
echo ""
RESULT=$(${CURL} -d '{
  "apiVersion": "kuery.io/v1alpha1",
  "kind": "Query",
  "spec": {
    "count": true,
    "limit": 5,
    "objects": {
      "id": true,
      "cluster": true,
      "object": {
        "metadata": { "name": true, "namespace": true }
      }
    }
  }
}')
echo "${RESULT}" | jq '{
  total_count: .status.count,
  showing: (.status.objects | length),
  incomplete: .status.incomplete,
  first_5: [.status.objects[] | {cluster, name: .object.metadata.name, namespace: .object.metadata.namespace}]
}'

echo ""
echo "---"
echo ""

# --- Query 2: Find all Deployments ---
log "Query 2: Find all Deployments across clusters"
echo ""
RESULT=$(${CURL} -d '{
  "apiVersion": "kuery.io/v1alpha1",
  "kind": "Query",
  "spec": {
    "filter": {
      "objects": [
        { "groupKind": { "apiGroup": "apps", "kind": "Deployment" } }
      ]
    },
    "objects": {
      "cluster": true,
      "mutablePath": true,
      "object": {
        "metadata": { "name": true, "namespace": true, "labels": true },
        "spec": { "replicas": true }
      }
    }
  }
}')
echo "${RESULT}" | jq '[.status.objects[] | {
  cluster,
  name: .object.metadata.name,
  namespace: .object.metadata.namespace,
  replicas: .object.spec.replicas,
  labels: .object.metadata.labels,
  mutablePath
}]'

echo ""
echo "---"
echo ""

# --- Query 3: Find Deployments in a specific cluster ---
log "Query 3: Find Deployments in ${CLUSTER_1} only"
echo ""
RESULT=$(${CURL} -d "{
  \"apiVersion\": \"kuery.io/v1alpha1\",
  \"kind\": \"Query\",
  \"spec\": {
    \"cluster\": { \"name\": \"${CLUSTER_1}\" },
    \"filter\": {
      \"objects\": [
        { \"groupKind\": { \"apiGroup\": \"apps\", \"kind\": \"Deployment\" } }
      ]
    },
    \"objects\": {
      \"cluster\": true,
      \"object\": {
        \"metadata\": { \"name\": true, \"namespace\": true }
      }
    }
  }
}")
echo "${RESULT}" | jq '[.status.objects[] | {cluster, name: .object.metadata.name, namespace: .object.metadata.namespace}]'

echo ""
echo "---"
echo ""

# --- Query 4: Find Deployments with their descendant ReplicaSets and Pods ---
log "Query 4: Deployment -> ReplicaSet -> Pod tree (namespace: demo)"
echo ""
RESULT=$(${CURL} -d '{
  "apiVersion": "kuery.io/v1alpha1",
  "kind": "Query",
  "spec": {
    "filter": {
      "objects": [
        {
          "groupKind": { "apiGroup": "apps", "kind": "Deployment" },
          "namespace": "demo"
        }
      ]
    },
    "objects": {
      "cluster": true,
      "object": {
        "metadata": { "name": true, "namespace": true }
      },
      "relations": {
        "descendants": {
          "objects": {
            "object": {
              "metadata": { "name": true }
            },
            "relations": {
              "descendants": {
                "objects": {
                  "object": {
                    "metadata": { "name": true }
                  }
                }
              }
            }
          }
        }
      }
    }
  }
}')
echo "${RESULT}" | jq '[.status.objects[] | {
  cluster,
  deployment: .object.metadata.name,
  replicasets: [(.relations.descendants // [])[] | {
    name: .object.metadata.name,
    pods: [(.relations.descendants // [])[] | .object.metadata.name]
  }]
}]'

echo ""
echo "---"
echo ""

# --- Query 5: Transitive descendants (full ownership tree) ---
log "Query 5: Transitive descendants+ of nginx deployment"
echo ""
RESULT=$(${CURL} -d '{
  "apiVersion": "kuery.io/v1alpha1",
  "kind": "Query",
  "spec": {
    "filter": {
      "objects": [
        {
          "groupKind": { "apiGroup": "apps", "kind": "Deployment" },
          "name": "nginx",
          "namespace": "demo"
        }
      ]
    },
    "objects": {
      "cluster": true,
      "object": {
        "metadata": { "name": true },
        "spec": { "replicas": true }
      },
      "relations": {
        "descendants+": {
          "objects": {
            "object": {
              "metadata": { "name": true }
            }
          }
        }
      }
    }
  }
}')
echo "${RESULT}" | jq '.status.objects[0] | {
  deployment: .object.metadata.name,
  replicas: .object.spec.replicas,
  all_descendants: [(.relations["descendants+"] // [])[] | {name: .object.metadata.name}]
}'

echo ""
echo "---"
echo ""

# --- Query 6: Filter by namespace + label + ordering ---
log "Query 6: All objects in 'demo' namespace labeled app=nginx, ordered by kind"
echo ""
RESULT=$(${CURL} -d '{
  "apiVersion": "kuery.io/v1alpha1",
  "kind": "Query",
  "spec": {
    "filter": {
      "objects": [
        {
          "namespace": "demo",
          "labels": { "app": "nginx" }
        }
      ]
    },
    "count": true,
    "order": [
      { "field": "kind", "direction": "Asc" },
      { "field": "name", "direction": "Asc" }
    ],
    "objects": {
      "cluster": true,
      "object": {
        "metadata": { "name": true, "namespace": true },
        "kind": true
      }
    }
  }
}')
echo "${RESULT}" | jq '{
  count: .status.count,
  objects: [.status.objects[] | {cluster, kind: .object.kind, name: .object.metadata.name}]
}'

echo ""
echo "---"
echo ""

# --- Query 7: OR filter — Pods or Services ---
log "Query 7: OR filter — find all Pods OR Services in 'demo'"
echo ""
RESULT=$(${CURL} -d '{
  "apiVersion": "kuery.io/v1alpha1",
  "kind": "Query",
  "spec": {
    "filter": {
      "objects": [
        { "groupKind": { "kind": "Pod" }, "namespace": "demo" },
        { "groupKind": { "kind": "Service" }, "namespace": "demo" }
      ]
    },
    "count": true,
    "objects": {
      "cluster": true,
      "object": {
        "metadata": { "name": true }
      }
    }
  }
}')
echo "${RESULT}" | jq '{count: .status.count, objects: [.status.objects[] | {cluster, name: .object.metadata.name}]}'

echo ""
bold "=========================================="
bold "  quickstart complete!"
bold "=========================================="
echo ""
log "kuery is running at https://localhost:${KUERY_PORT}"
log "Logs: ${KUBECONFIG_DIR}/kuery.log"
log "Clusters: ${CLUSTER_1}, ${CLUSTER_2}"
echo ""
info "Try your own queries:"
info "  curl -sk -X POST https://localhost:${KUERY_PORT}/apis/kuery.io/v1alpha1/queries \\"
info "    -H Content-Type:application/json \\"
info "    -d '{\"apiVersion\":\"kuery.io/v1alpha1\",\"kind\":\"Query\",\"spec\":{\"limit\":10}}'"
echo ""
info "Press Ctrl+C to stop and clean up."
echo ""

# Keep running until interrupted.
wait "${KUERY_PID}"
