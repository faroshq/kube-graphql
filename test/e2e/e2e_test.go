//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/k3s"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

var (
	// serverURL is the SQLite-backed kuery server (used by most tests).
	serverURL string
	// pgServerURL is the PostgreSQL-backed kuery server (used by TestPostgres_* tests).
	pgServerURL string
	httpClient  *http.Client
	tmpDir      string
)

func TestMain(m *testing.M) {
	var code int
	defer func() { os.Exit(code) }()

	ctx := context.Background()
	httpClient = newInsecureClient()

	var err error
	tmpDir, err = os.MkdirTemp("", "kuery-e2e-")
	if err != nil {
		log.Fatalf("create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// 1. Start K3s clusters via testcontainers.
	log.Println("Starting K3s cluster A...")
	containerA, err := k3s.Run(ctx, "rancher/k3s:v1.31.6-k3s1")
	if err != nil {
		log.Fatalf("start K3s cluster-a: %v", err)
	}
	defer containerA.Terminate(ctx)

	log.Println("Starting K3s cluster B...")
	containerB, err := k3s.Run(ctx, "rancher/k3s:v1.31.6-k3s1")
	if err != nil {
		log.Fatalf("start K3s cluster-b: %v", err)
	}
	defer containerB.Terminate(ctx)

	// 2. Get kubeconfigs and write to files.
	kcA := filepath.Join(tmpDir, "alpha.kubeconfig")
	kcB := filepath.Join(tmpDir, "beta.kubeconfig")

	kubeConfigA, err := containerA.GetKubeConfig(ctx)
	if err != nil {
		log.Fatalf("get kubeconfig cluster-a: %v", err)
	}
	if err := os.WriteFile(kcA, kubeConfigA, 0600); err != nil {
		log.Fatalf("write kubeconfig cluster-a: %v", err)
	}

	kubeConfigB, err := containerB.GetKubeConfig(ctx)
	if err != nil {
		log.Fatalf("get kubeconfig cluster-b: %v", err)
	}
	if err := os.WriteFile(kcB, kubeConfigB, 0600); err != nil {
		log.Fatalf("write kubeconfig cluster-b: %v", err)
	}

	// 3. Apply fixtures via client-go.
	log.Println("Applying fixtures...")
	if err := applyYAMLFromKubeconfig(ctx, kubeConfigA, "demo", clusterAFixtures); err != nil {
		log.Fatalf("apply fixtures cluster-a: %v", err)
	}
	if err := applyYAMLFromKubeconfig(ctx, kubeConfigB, "demo", clusterBFixtures); err != nil {
		log.Fatalf("apply fixtures cluster-b: %v", err)
	}

	// 4. Wait for rollouts via client-go.
	log.Println("Waiting for rollouts...")
	if err := waitForDeploymentReady(ctx, kubeConfigA, "demo", "nginx", 120*time.Second); err != nil {
		log.Fatalf("rollout nginx: %v", err)
	}
	if err := waitForDeploymentReady(ctx, kubeConfigB, "demo", "redis", 120*time.Second); err != nil {
		log.Fatalf("rollout redis: %v", err)
	}

	// 5. Build kuery binary.
	log.Println("Building kuery...")
	kueryBin := filepath.Join(tmpDir, "kuery")
	buildCmd := exec.Command("go", "build", "-o", kueryBin, "./cmd/kuery")
	buildCmd.Dir = findRepoRoot()
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		log.Fatalf("build kuery: %v", err)
	}

	kubeconfigs := fmt.Sprintf("cluster-a=%s,cluster-b=%s", kcA, kcB)

	// 6. Start SQLite-backed kuery server.
	serverURL, err = startKueryServer(ctx, kueryBin, tmpDir, "sqlite",
		filepath.Join(tmpDir, "kuery.db"), kubeconfigs, "kuery-sqlite.log")
	if err != nil {
		log.Fatalf("start SQLite kuery server: %v", err)
	}

	// 7. Start PostgreSQL container + Postgres-backed kuery server.
	log.Println("Starting PostgreSQL container...")
	pgContainer, err := postgres.Run(ctx, "postgres:16",
		postgres.WithDatabase("kuery_e2e"),
		postgres.WithUsername("kuery"),
		postgres.WithPassword("kuery"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second)),
	)
	if err != nil {
		log.Fatalf("start postgres container: %v", err)
	}
	defer pgContainer.Terminate(ctx)

	pgConnStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		log.Fatalf("get postgres connection string: %v", err)
	}

	pgServerURL, err = startKueryServer(ctx, kueryBin, tmpDir, "postgres",
		pgConnStr, kubeconfigs, "kuery-postgres.log")
	if err != nil {
		log.Fatalf("start Postgres kuery server: %v", err)
	}

	// 8. Wait for both servers to sync data.
	log.Println("Waiting for SQLite server data sync...")
	if err := waitForDataSync(serverURL, httpClient); err != nil {
		dumpLogFile(filepath.Join(tmpDir, "kuery-sqlite.log"))
		log.Fatalf("SQLite data sync: %v", err)
	}
	log.Println("Waiting for Postgres server data sync...")
	if err := waitForDataSync(pgServerURL, httpClient); err != nil {
		dumpLogFile(filepath.Join(tmpDir, "kuery-postgres.log"))
		log.Fatalf("Postgres data sync: %v", err)
	}

	log.Println("Setup complete, running tests...")
	code = m.Run()
}

// startKueryServer starts a kuery server as a subprocess and waits for API readiness.
// Returns the server URL.
func startKueryServer(ctx context.Context, kueryBin, tmpDir, driver, dsn, kubeconfigs, logFileName string) (string, error) {
	port, err := getFreePort()
	if err != nil {
		return "", fmt.Errorf("find free port: %w", err)
	}
	url := fmt.Sprintf("https://localhost:%d", port)

	log.Printf("Starting kuery (%s) on port %d...\n", driver, port)
	logFile, err := os.Create(filepath.Join(tmpDir, logFileName))
	if err != nil {
		return "", fmt.Errorf("create log file: %w", err)
	}

	cmd := exec.Command(kueryBin,
		"--store-driver="+driver,
		"--store-dsn="+dsn,
		fmt.Sprintf("--secure-port=%d", port),
		"--kubeconfigs="+kubeconfigs,
		"--sync-blacklist=events,events.events.k8s.io",
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return "", fmt.Errorf("start kuery: %w", err)
	}

	// Register cleanup.
	go func() {
		<-ctx.Done()
		if cmd.Process != nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
		logFile.Close()
	}()

	// Wait for API readiness.
	err = waitForCondition(60*time.Second, time.Second, func() bool {
		resp, err := httpClient.Get(url + "/apis/kuery.io/v1alpha1")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return strings.Contains(string(body), "kuery.io")
	})
	if err != nil {
		cmd.Process.Kill()
		logFile.Close()
		return "", fmt.Errorf("API not ready: %w", err)
	}

	return url, nil
}

// waitForDataSync polls the query API until Deployments from both clusters are synced.
func waitForDataSync(url string, client *http.Client) error {
	return waitForCondition(90*time.Second, 2*time.Second, func() bool {
		body := `{"apiVersion":"kuery.io/v1alpha1","kind":"Query","spec":{"filter":{"objects":[{"groupKind":{"apiGroup":"apps","kind":"Deployment"},"namespace":"demo"}]},"count":true}}`
		resp, err := client.Post(url+"/apis/kuery.io/v1alpha1/queries", "application/json", strings.NewReader(body))
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		return strings.Contains(string(respBody), `"count":`) &&
			!strings.Contains(string(respBody), `"count":0`) &&
			!strings.Contains(string(respBody), `"count":1`)
	})
}

func getFreePort() (int, error) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}

func findRepoRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

func dumpLogFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	start := 0
	if len(lines) > 50 {
		start = len(lines) - 50
	}
	log.Printf("=== %s (last 50 lines) ===\n", filepath.Base(path))
	for _, line := range lines[start:] {
		log.Println(line)
	}
}
