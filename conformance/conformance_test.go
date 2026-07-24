//go:build integration

package conformance

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/docker/oci"
	"github.com/docker/oci/ocilayout"
	"github.com/docker/oci/ocimem"
	"github.com/docker/oci/ociserver"
)

const conformanceImage = "docker-oci-conformance:integration"

type backendCase struct {
	name string
	new  func(*testing.T) oci.Interface
}

func TestOCIConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping OCI conformance tests in short mode")
	}

	root := repositoryRoot(t)
	requireDocker(t, root)
	buildConformanceImage(t, root)

	backends := []backendCase{
		{
			name: "ocimem",
			new: func(*testing.T) oci.Interface {
				return ocimem.New()
			},
		},
		{
			name: "ocilayout",
			new: func(t *testing.T) oci.Interface {
				r, err := ocilayout.New(t.TempDir(), nil)
				if err != nil {
					t.Fatalf("creating ocilayout backend: %v", err)
				}
				return r
			},
		},
	}

	for _, backend := range backends {
		t.Run(backend.name, func(t *testing.T) {
			runConformance(t, root, backend.name, backend.new(t))
		})
	}
}

func runConformance(t *testing.T, root, backendName string, backend oci.Interface) {
	t.Helper()

	handler, err := ociserver.New(backend, nil)
	if err != nil {
		t.Fatalf("creating OCI server: %v", err)
	}
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listening for OCI server: %v", err)
	}
	httpServer := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- httpServer.Serve(listener)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			t.Errorf("shutting down OCI server: %v", err)
			_ = httpServer.Close()
		}
		select {
		case err := <-serveErr:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("serving OCI registry: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("timed out waiting for OCI server to stop")
		}
	})

	port := listener.Addr().(*net.TCPAddr).Port
	waitForRegistry(t, fmt.Sprintf("http://127.0.0.1:%d/v2/", port))

	resultsDir := filepath.Join(root, "conformance", "results", backendName)
	if err := os.RemoveAll(resultsDir); err != nil {
		t.Fatalf("removing old conformance results: %v", err)
	}
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		t.Fatalf("creating conformance results directory: %v", err)
	}

	runID := strconv.FormatInt(time.Now().UnixNano(), 36)
	args := []string{
		"run", "--rm",
		"--add-host=host.docker.internal:host-gateway",
		"-v", filepath.Join(root, "conformance", "oci-conformance.yaml") + ":/work/oci-conformance.yaml:ro",
		"-v", resultsDir + ":/results",
		"-e", fmt.Sprintf("OCI_REGISTRY=host.docker.internal:%d", port),
		"-e", "OCI_REPO1=conformance/" + backendName + "/" + runID + "/repo1",
		"-e", "OCI_REPO2=conformance/" + backendName + "/" + runID + "/repo2",
	}
	if currentUser, err := user.Current(); err == nil && currentUser.Uid != "" && currentUser.Gid != "" {
		args = append(args, "--user", currentUser.Uid+":"+currentUser.Gid)
	}
	args = append(args, conformanceImage)

	runCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	output, err := runCommand(runCtx, root, "docker", args...)
	t.Logf("OCI conformance output for %s:\n%s", backendName, output)
	if err != nil {
		t.Fatalf("OCI conformance failed for %s: %v; reports: %s", backendName, err, resultsDir)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getting working directory: %v", err)
	}
	root, err := filepath.Abs(filepath.Join(wd, ".."))
	if err != nil {
		t.Fatalf("resolving repository root: %v", err)
	}
	return root
}

func requireDocker(t *testing.T, root string) {
	t.Helper()
	if output, err := runCommand(context.Background(), root, "docker", "version"); err != nil {
		t.Fatalf("Docker is required for conformance tests: %v\n%s", err, output)
	}
}

func buildConformanceImage(t *testing.T, root string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	output, err := runCommand(ctx, root, "docker", "build",
		"-f", "conformance/Dockerfile",
		"-t", conformanceImage,
		"conformance/",
	)
	if err != nil {
		t.Fatalf("building OCI conformance image: %v\n%s", err, output)
	}
}

func waitForRegistry(t *testing.T, registryURL string) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(registryURL)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			lastErr = fmt.Errorf("status %s", resp.Status)
		} else {
			lastErr = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for registry %s: %v", registryURL, lastErr)
}

func runCommand(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return string(output), nil
}
