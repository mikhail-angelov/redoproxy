package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func readFixture(t *testing.T, name string) string {
	t.Helper()

	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("failed to read fixture %s: %v", name, err)
	}

	return string(data)
}

func newTestDiscovery(body string, status int, rtErr error, networkName string) *Discovery {
	d := NewDiscovery(10*time.Millisecond, networkName)

	d.client = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if rtErr != nil {
				return nil, rtErr
			}

			return &http.Response{
				StatusCode: status,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		}),
	}

	return d
}

func TestParseContainerResponseFromFixture(t *testing.T) {
	body := readFixture(t, "testData/containers.json")
	d := NewDiscovery(10*time.Millisecond, "bridge")

	containers, err := d.parseContainerResponse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parseContainerResponse returned error: %v", err)
	}

	if len(containers) != 3 {
		t.Fatalf("expected 3 containers, got %d", len(containers))
	}

	nginx := containers[0]

	if nginx.ID != "6f3c99ca4d83811ae1bf1c3e288ac229288028a8b097565cb93eed85ccc4c9b4" {
		t.Fatalf("unexpected nginx id: %q", nginx.ID)
	}

	if nginx.Name != "nginx" {
		t.Fatalf("expected nginx name, got %q", nginx.Name)
	}

	if nginx.State != "running" {
		t.Fatalf("expected running state, got %q", nginx.State)
	}

	if nginx.NetworkName != "bridge" {
		t.Fatalf("expected bridge network, got %q", nginx.NetworkName)
	}

	if nginx.IP != "172.17.0.3" {
		t.Fatalf("expected nginx IP 172.17.0.3, got %q", nginx.IP)
	}

	if nginx.Port != 8765 {
		t.Fatalf("expected nginx port 8765 from label, got %d", nginx.Port)
	}
}

func TestBuildRouteMapFromFixture(t *testing.T) {
	body := readFixture(t, "testData/containers.json")
	d := NewDiscovery(10*time.Millisecond, "bridge")

	containers, err := d.parseContainerResponse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parseContainerResponse returned error: %v", err)
	}

	routes := buildRouteMap(containers)

	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}

	route, ok := routes["example.com"]
	if !ok {
		t.Fatal("expected route for example.com")
	}

	if route.Domain != "example.com" {
		t.Fatalf("expected domain example.com, got %q", route.Domain)
	}

	if route.Server != "http://172.17.0.3:8765" {
		t.Fatalf("expected server http://172.17.0.3:8765, got %q", route.Server)
	}
}

func TestBuildRouteMapSkipsContainersOutsideConfiguredNetwork(t *testing.T) {
	body := readFixture(t, "testData/containers.json")
	d := NewDiscovery(10*time.Millisecond, "someOtherNetwork")

	containers, err := d.parseContainerResponse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parseContainerResponse returned error: %v", err)
	}

	routes := buildRouteMap(containers)

	if len(routes) != 0 {
		t.Fatalf("expected 0 routes because nginx is not in someOtherNetwork, got %d", len(routes))
	}
}

func TestGetContainersFromFixture(t *testing.T) {
	body := readFixture(t, "testData/containers.json")
	d := newTestDiscovery(body, http.StatusOK, nil, "bridge")

	containers, err := d.getContainers(context.Background())
	if err != nil {
		t.Fatalf("getContainers returned error: %v", err)
	}

	if len(containers) != 3 {
		t.Fatalf("expected 3 containers, got %d", len(containers))
	}
}

func TestGetContainersDockerAPIError(t *testing.T) {
	d := newTestDiscovery("error", http.StatusInternalServerError, nil, "bridge")

	_, err := d.getContainers(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !strings.Contains(err.Error(), "docker containers API returned status 500") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetContainersTransportError(t *testing.T) {
	d := newTestDiscovery("", http.StatusOK, errors.New("socket failed"), "bridge")

	_, err := d.getContainers(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !strings.Contains(err.Error(), "failed to list containers") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseContainerResponseInvalidJSON(t *testing.T) {
	d := NewDiscovery(10*time.Millisecond, "bridge")

	_, err := d.parseContainerResponse(strings.NewReader(`{invalid json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}

	if !strings.Contains(err.Error(), "failed to decode container list") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRefreshUpdatesRouteMapFromFixture(t *testing.T) {
	body := readFixture(t, "testData/containers.json")
	d := newTestDiscovery(body, http.StatusOK, nil, "bridge")

	if err := d.refresh(context.Background()); err != nil {
		t.Fatalf("refresh returned error: %v", err)
	}

	route, ok := d.Lookup("example.com")
	if !ok {
		t.Fatal("expected route for example.com")
	}

	if route.Server != "http://172.17.0.3:8765" {
		t.Fatalf("expected server http://172.17.0.3:8765, got %q", route.Server)
	}
}

func TestLookupNormalizesHost(t *testing.T) {
	d := NewDiscovery(10*time.Millisecond, "bridge")

	d.routeMap = map[string]ContainerRoute{
		"example.com": {
			Domain: "example.com",
			Server: "http://172.17.0.3:8765",
		},
	}

	route, ok := d.Lookup("Example.COM:8080")
	if !ok {
		t.Fatal("expected route for Example.COM:8080")
	}

	if route.Server != "http://172.17.0.3:8765" {
		t.Fatalf("unexpected route server: %q", route.Server)
	}
}

func TestSameRouteMap(t *testing.T) {
	a := map[string]ContainerRoute{
		"example.com": {
			Domain: "example.com",
			Server: "http://172.17.0.3:8765",
		},
	}

	b := map[string]ContainerRoute{
		"example.com": {
			Domain: "example.com",
			Server: "http://172.17.0.3:8765",
		},
	}

	c := map[string]ContainerRoute{
		"example.com": {
			Domain: "example.com",
			Server: "http://172.17.0.4:8765",
		},
	}

	if !sameRouteMap(a, b) {
		t.Fatal("expected route maps to be equal")
	}

	if sameRouteMap(a, c) {
		t.Fatal("expected route maps to be different")
	}
}

func TestRunRequiresNetworkName(t *testing.T) {
	d := NewDiscovery(10*time.Millisecond, "")

	err := d.Run(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !strings.Contains(err.Error(), "discovery network is not initialized") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckAndBootstrapDockerClientWithExplicitNetwork(t *testing.T) {
	body := readFixture(t, "testData/containers.json")
	d := newTestDiscovery(body, http.StatusOK, nil, "bridge")

	if err := d.CheckAndBootstrapDockerClient(); err != nil {
		t.Fatalf("CheckAndBootstrapDockerClient returned error: %v", err)
	}

	if d.networkName != "bridge" {
		t.Fatalf("expected networkName to stay bridge, got %q", d.networkName)
	}
}

func TestDetectContainerNetworkByIDPrefixFromFixture(t *testing.T) {
	body := readFixture(t, "testData/containers.json")
	d := NewDiscovery(10*time.Millisecond, "")

	containers, err := d.parseContainerResponse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parseContainerResponse returned error: %v", err)
	}

	networkName, ok := detectContainerNetworkByIDPrefix(
		containers,
		"d5503b81f356718bb01357daab63acb88eaae682bcb638383fadbac66e0bb831",
	)
	if !ok {
		t.Fatal("expected to detect redoproxy network")
	}

	if networkName != "bridge" {
		t.Fatalf("expected bridge network, got %q", networkName)
	}
}

func TestDetectContainerNetworkByIDPrefixFailure(t *testing.T) {
	body := readFixture(t, "testData/containers.json")
	d := NewDiscovery(10*time.Millisecond, "")

	containers, err := d.parseContainerResponse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parseContainerResponse returned error: %v", err)
	}

	_, ok := detectContainerNetworkByIDPrefix(containers, "not-existing-id")
	if ok {
		t.Fatal("expected detection failure")
	}
}

func TestBuildRouteMapSkipsInvalidContainers(t *testing.T) {
	containers := []dockerContainer{
		{
			ID:    "not-running",
			Name:  "not-running",
			State: "exited",
			Labels: map[string]string{
				"redoproxy.enabled": "true",
				"redoproxy.domain":  "exited.example.com",
				"redoproxy.port":    "8765",
			},
			IP:   "172.17.0.10",
			Port: 8765,
		},
		{
			ID:    "disabled",
			Name:  "disabled",
			State: "running",
			Labels: map[string]string{
				"redoproxy.enabled": "false",
				"redoproxy.domain":  "disabled.example.com",
				"redoproxy.port":    "8765",
			},
			IP:   "172.17.0.11",
			Port: 8765,
		},
		{
			ID:    "missing-domain",
			Name:  "missing-domain",
			State: "running",
			Labels: map[string]string{
				"redoproxy.enabled": "true",
				"redoproxy.port":    "8765",
			},
			IP:   "172.17.0.12",
			Port: 8765,
		},
		{
			ID:    "missing-ip",
			Name:  "missing-ip",
			State: "running",
			Labels: map[string]string{
				"redoproxy.enabled": "true",
				"redoproxy.domain":  "missing-ip.example.com",
				"redoproxy.port":    "8765",
			},
			Port: 8765,
		},
		{
			ID:    "missing-port",
			Name:  "missing-port",
			State: "running",
			Labels: map[string]string{
				"redoproxy.enabled": "true",
				"redoproxy.domain":  "missing-port.example.com",
				"redoproxy.port":    "8765",
			},
			IP: "172.17.0.13",
		},
	}

	routes := buildRouteMap(containers)

	if len(routes) != 0 {
		t.Fatalf("expected 0 routes, got %d", len(routes))
	}
}
