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
KUERY_PORT=9443
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
apiVersion: v1
kind: Secret
metadata:
  name: nginx-tls
  labels:
    app: nginx
type: kubernetes.io/tls
stringData:
  tls.crt: "-----BEGIN CERTIFICATE-----\nMIIBkTCB+wIJALRiMLAh..."
  tls.key: "-----BEGIN RSA PRIVATE KEY-----\nMIIBogIBAAJBAL..."
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: nginx-config
  labels:
    app: nginx
data:
  nginx.conf: |
    server {
      listen 80;
      location / { proxy_pass http://localhost:8080; }
    }
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: nginx-sa
  labels:
    app: nginx
---
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
      serviceAccountName: nginx-sa
      containers:
        - name: nginx
          image: nginx:1.27
          ports:
            - containerPort: 80
          env:
            - name: TLS_CERT_PATH
              value: /etc/tls
          volumeMounts:
            - name: tls
              mountPath: /etc/tls
              readOnly: true
            - name: config
              mountPath: /etc/nginx/conf.d
            - name: data
              mountPath: /var/www/html
      volumes:
        - name: tls
          secret:
            secretName: nginx-tls
        - name: config
          configMap:
            name: nginx-config
        - name: data
          persistentVolumeClaim:
            claimName: nginx-data
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: nginx-data
  labels:
    app: nginx
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 1Gi
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
EOF

log "Deploying sample workloads to ${CLUSTER_2}"
kubectl --kubeconfig="${KUBECONFIG_DIR}/${CLUSTER_2}.kubeconfig" create namespace demo 2>/dev/null || true
kubectl --kubeconfig="${KUBECONFIG_DIR}/${CLUSTER_2}.kubeconfig" -n demo apply -f - <<'EOF'
apiVersion: v1
kind: Secret
metadata:
  name: redis-auth
  labels:
    app: redis
stringData:
  password: "s3cret-redis-pass"
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: redis-config
  labels:
    app: redis
data:
  redis.conf: |
    maxmemory 256mb
    maxmemory-policy allkeys-lru
---
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
          env:
            - name: REDIS_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: redis-auth
                  key: password
          volumeMounts:
            - name: config
              mountPath: /usr/local/etc/redis
      volumes:
        - name: config
          configMap:
            name: redis-config
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
    --sync-blacklist="events,events.events.k8s.io" \
    > "${KUBECONFIG_DIR}/kuery.log" 2>&1 &
KUERY_PID=$!

# Wait for the server to be ready.
log "Waiting for kuery API to be ready..."
READY=false
for i in $(seq 1 60); do
    if ! kill -0 "${KUERY_PID}" 2>/dev/null; then
        echo "Error: kuery server exited. Logs:" >&2
        tail -20 "${KUBECONFIG_DIR}/kuery.log" >&2
        exit 1
    fi
    # Verify we get the kuery.io API group back (not some other server on this port).
    if curl -sk "https://localhost:${KUERY_PORT}/apis/kuery.io/v1alpha1" 2>/dev/null | grep -q "kuery.io"; then
        READY=true
        break
    fi
    sleep 1
done
if [[ "${READY}" != "true" ]]; then
    echo "Error: kuery server did not become ready. Logs:" >&2
    tail -20 "${KUBECONFIG_DIR}/kuery.log" >&2
    exit 1
fi

# Give the sync controller time to discover and sync objects.
log "Waiting for object sync (15s)..."
sleep 15

CURL="curl -sk -X POST https://localhost:${KUERY_PORT}/apis/kuery.io/v1alpha1/queries -H Content-Type:application/json"

# Helper: run a query, print the spec and full status response.
run_query() {
    local description="$1"
    local body="$2"
    echo ""
    echo "---"
    echo ""
    log "${description}"
    echo ""
    info "Request body:"
    echo "${body}" | jq .
    echo ""
    local RESP
    RESP=$(${CURL} -d "${body}")
    # Check if it's an error response (kind: Status) vs success (kind: Query).
    local resp_kind
    resp_kind=$(echo "${RESP}" | jq -r '.kind // empty')
    if [[ "${resp_kind}" == "Status" ]]; then
        info "Error:"
        echo "${RESP}" | jq '{status: .status, message: .message, reason: .reason}'
    else
        info "Response (.status):"
        echo "${RESP}" | jq '.status'
    fi
}

# ============================================================================
echo ""
bold "=========================================="
bold "  kuery quickstart — example queries"
bold "=========================================="

# --- Query 1 ---
run_query "Query 1: Count all synced objects across both clusters (limit 5)" '{
  "apiVersion": "kuery.io/v1alpha1",
  "kind": "Query",
  "spec": {
    "count": true,
    "limit": 5,
    "objects": {
      "cluster": true,
      "object": {
        "kind": true,
        "apiVersion": true,
        "metadata": { "name": true, "namespace": true }
      }
    }
  }
}'

# --- Query 2 ---
run_query "Query 2: Find all Deployments across clusters" '{
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
        "kind": true,
        "metadata": { "name": true, "namespace": true, "labels": true },
        "spec": { "replicas": true }
      }
    }
  }
}'

# --- Query 3 ---
run_query "Query 3: Find Deployments in ${CLUSTER_1} only" "{
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
        \"kind\": true,
        \"metadata\": { \"name\": true, \"namespace\": true }
      }
    }
  }
}"

# --- Query 4: Deployment -> RS -> Pod tree with kind visible at every level ---
run_query "Query 4: Deployment -> ReplicaSet -> Pod -> referenced Secrets/ConfigMaps" '{
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
        "kind": true,
        "metadata": { "name": true, "namespace": true }
      },
      "relations": {
        "descendants": {
          "objects": {
            "object": {
              "kind": true,
              "metadata": { "name": true }
            },
            "relations": {
              "descendants": {
                "objects": {
                  "object": {
                    "kind": true,
                    "metadata": { "name": true }
                  },
                  "relations": {
                    "references": {
                      "objects": {
                        "object": {
                          "kind": true,
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
      }
    }
  }
}'

# --- Query 5 ---
run_query "Query 5: Full ownership tree with referenced Secrets, ConfigMaps, PVCs" '{
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
        "kind": true,
        "metadata": { "name": true },
        "spec": { "replicas": true }
      },
      "relations": {
        "descendants+": {
          "objects": {
            "object": {
              "kind": true,
              "metadata": { "name": true }
            },
            "relations": {
              "references": {
                "objects": {
                  "object": {
                    "kind": true,
                    "apiVersion": true,
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
}'

# --- Query 6 ---
run_query "Query 6: All objects in demo namespace labeled app=nginx, ordered by kind" '{
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
        "kind": true,
        "metadata": { "name": true, "namespace": true }
      }
    }
  }
}'

# --- Query 7 ---
run_query "Query 7: OR filter — find all Pods OR Services in demo" '{
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
        "kind": true,
        "metadata": { "name": true }
      }
    }
  }
}'

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
