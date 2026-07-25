package proxy

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
	assert.NoError(t, err)
	assert.Equal(t, 3, len(containers))

	nginx := containers[0]

	assert.Equal(t, "6f3c99ca4d83811ae1bf1c3e288ac229288028a8b097565cb93eed85ccc4c9b4", nginx.ID)
	assert.Equal(t, "nginx", nginx.Name)
	assert.Equal(t, "running", nginx.State)
	assert.Equal(t, "bridge", nginx.NetworkName)
	assert.Equal(t, "172.17.0.3", nginx.IP)
	assert.Equal(t, 8765, nginx.Port)
}

func TestBuildRouteMapFromFixture(t *testing.T) {
	body := readFixture(t, "testData/containers.json")
	d := NewDiscovery(10*time.Millisecond, "bridge")

	containers, err := d.parseContainerResponse(strings.NewReader(body))
	assert.NoError(t, err)

	routes := buildRouteMap(containers)

	assert.Equal(t, 1, len(routes))
	route, ok := routes["example.com"]

	assert.True(t, ok)
	assert.Equal(t, "example.com", route.Domain)
	assert.Equal(t, "http://172.17.0.3:8765", route.Upstreams[0].Server)
}

func TestBuildRouteMapSkipsContainersOutsideConfiguredNetwork(t *testing.T) {
	body := readFixture(t, "testData/containers.json")
	d := NewDiscovery(10*time.Millisecond, "someOtherNetwork")

	containers, err := d.parseContainerResponse(strings.NewReader(body))
	assert.Nil(t, err)

	routes := buildRouteMap(containers)

	assert.Equal(t, 0, len(routes))
}

func TestGetContainersFromFixture(t *testing.T) {
	body := readFixture(t, "testData/containers.json")
	d := newTestDiscovery(body, http.StatusOK, nil, "bridge")

	containers, err := d.getContainers(context.Background())
	assert.Nil(t, err)
	assert.Equal(t, 3, len(containers))
}

func TestGetContainersDockerAPIError(t *testing.T) {
	d := newTestDiscovery("error", http.StatusInternalServerError, nil, "bridge")

	_, err := d.getContainers(context.Background())
	assert.True(t, err != nil)
	assert.Contains(t, err.Error(), "docker containers API returned status 500")
}

func TestGetContainersTransportError(t *testing.T) {
	d := newTestDiscovery("", http.StatusOK, errors.New("socket failed"), "bridge")

	_, err := d.getContainers(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list containers")
}

func TestParseContainerResponseInvalidJSON(t *testing.T) {
	d := NewDiscovery(10*time.Millisecond, "bridge")

	_, err := d.parseContainerResponse(strings.NewReader(`{invalid json`))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode container list")
}

func TestRefreshUpdatesRouteMapFromFixture(t *testing.T) {
	body := readFixture(t, "testData/containers.json")
	d := newTestDiscovery(body, http.StatusOK, nil, "bridge")

	assert.NoError(t, d.refresh(context.Background()))

	route, ok := d.LookupGroup("example.com")
	assert.True(t, ok)
	assert.Equal(t, "example.com", route.Domain)
	assert.Equal(t, "http://172.17.0.3:8765", route.Upstreams[0].Server)
}

func TestLookupNormalizesHost(t *testing.T) {
	d := NewDiscovery(10*time.Millisecond, "bridge")

	d.routeMap = map[string]*RouteGroup{
		"example.com": {
			Domain:    "example.com",
			Upstreams: []Upstream{{Server: "http://172.17.0.3:8765"}},
		},
	}

	route, ok := d.LookupGroup("Example.COM:8080")
	assert.True(t, ok)
	assert.Equal(t, "example.com", route.Domain)
	assert.Equal(t, "http://172.17.0.3:8765", route.Upstreams[0].Server)
}

func TestSameRouteMap(t *testing.T) {
	a := map[string]*RouteGroup{
		"example.com": {
			Domain:    "example.com",
			Upstreams: []Upstream{{Server: "http://172.17.0.3:8765"}},
		},
	}

	b := map[string]*RouteGroup{
		"example.com": {
			Domain:    "example.com",
			Upstreams: []Upstream{{Server: "http://172.17.0.3:8765"}},
		},
	}

	c := map[string]*RouteGroup{
		"example.com": {
			Domain:    "example.com",
			Upstreams: []Upstream{{Server: "http://172.17.0.4:8765"}},
		},
	}

	assert.True(t, sameRouteMap(a, b))
	assert.False(t, sameRouteMap(a, c))
}

func TestRunRequiresNetworkName(t *testing.T) {
	d := NewDiscovery(10*time.Millisecond, "")

	err := d.Run(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "discovery network is not initialized")
}

func TestCheckAndBootstrapDockerClientWithExplicitNetwork(t *testing.T) {
	body := readFixture(t, "testData/containers.json")
	d := newTestDiscovery(body, http.StatusOK, nil, "bridge")

	assert.NoError(t, d.CheckAndBootstrapDockerClient())
	assert.Equal(t, "bridge", d.networkName)
}

func TestDetectContainerNetworkByIDPrefixFromFixture(t *testing.T) {
	body := readFixture(t, "testData/containers.json")
	d := NewDiscovery(10*time.Millisecond, "")

	containers, err := d.parseContainerResponse(strings.NewReader(body))
	assert.NoError(t, err)

	networkName, ok := detectContainerNetworkByIDPrefix(
		containers,
		"d5503b81f356718bb01357daab63acb88eaae682bcb638383fadbac66e0bb831",
	)
	assert.True(t, ok)
	assert.Equal(t, "bridge", networkName)
}

func TestDetectContainerNetworkByIDPrefixFailure(t *testing.T) {
	body := readFixture(t, "testData/containers.json")
	d := NewDiscovery(10*time.Millisecond, "")

	containers, err := d.parseContainerResponse(strings.NewReader(body))
	assert.NoError(t, err)

	_, ok := detectContainerNetworkByIDPrefix(containers, "not-existing-id")
	assert.False(t, ok)
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

	assert.Equal(t, 0, len(routes))
}

func TestBuildRouteMapGroupsSameDomain(t *testing.T) {
	body := readFixture(t, "testData/groupedContainers.json")
	d := NewDiscovery(10*time.Millisecond, "bridge")

	containers, err := d.parseContainerResponse(strings.NewReader(body))
	assert.Nil(t, err)
	assert.Equal(t, 3, len(containers))

	routes := buildRouteMap(containers)
	assert.Equal(t, 1, len(routes))
	assert.Equal(t, 2, len(routes["example.com"].Upstreams))

}
func TestRouteGroupNextRoundRobin(t *testing.T) {
	body := readFixture(t, "testData/groupedContainers.json")
	d := NewDiscovery(10*time.Millisecond, "bridge")

	containers, err := d.parseContainerResponse(strings.NewReader(body))
	assert.Nil(t, err)
	routes := buildRouteMap(containers)
	group := routes["example.com"]

	assert.Equal(t, 0, group.nextIndex)
	u1, ok := group.Next(nil)
	assert.True(t, ok)
	assert.Equal(t, "http://172.17.0.2:8765", u1.Server)
	u2, ok := group.Next(nil)
	assert.True(t, ok)
	assert.Equal(t, "http://172.17.0.3:8765", u2.Server)
	u3, ok := group.Next(nil)
	assert.True(t, ok)
	assert.Equal(t, "http://172.17.0.2:8765", u3.Server)
}
func TestLookupGroupDoesNotAdvanceRoundRobin(t *testing.T) {
	d := NewDiscovery(10*time.Millisecond, "bridge")

	d.routeMap = map[string]*RouteGroup{
		"example.com": {
			Domain: "example.com",
			Upstreams: []Upstream{
				{Server: "http://172.17.0.2:8765"},
				{Server: "http://172.17.0.3:8765"},
			},
		},
	}

	group1, ok := d.LookupGroup("example.com")
	assert.True(t, ok)

	group2, ok := d.LookupGroup("example.com")
	assert.True(t, ok)

	assert.Same(t, group1, group2)
	assert.Equal(t, 0, group1.nextIndex)

	u1, ok := group1.Next(nil)
	assert.True(t, ok)
	assert.Equal(t, "http://172.17.0.2:8765", u1.Server)
}
func TestRefreshPreservesRoundRobinStateWhenRoutesUnchanged(t *testing.T) {
	body := readFixture(t, "testData/groupedContainers.json")
	d := newTestDiscovery(body, http.StatusOK, nil, "bridge")

	assert.NoError(t, d.refresh(context.Background()))

	group, ok := d.LookupGroup("example.com")
	assert.True(t, ok)

	u1, ok := group.Next(nil)
	assert.True(t, ok)
	assert.Equal(t, "http://172.17.0.2:8765", u1.Server)

	assert.NoError(t, d.refresh(context.Background()))

	groupAfter, ok := d.LookupGroup("example.com")
	assert.True(t, ok)
	assert.Same(t, group, groupAfter)

	u2, ok := groupAfter.Next(nil)
	assert.True(t, ok)
	assert.Equal(t, "http://172.17.0.3:8765", u2.Server)
}

func TestSameRouteMapDetectsPolicyChange(t *testing.T) {
	body := readFixture(t, "testData/groupedContainers.json")
	d := NewDiscovery(10*time.Millisecond, "bridge")

	containers, err := d.parseContainerResponse(strings.NewReader(body))
	assert.NoError(t, err)

	base := buildRouteMap(containers)

	// buildRouteMap processes containers in deterministic (IP, port) order,
	// so the lowest-IP container ("weather", 172.17.0.2) is first-seen and
	// wins on a label conflict; mutate that one to trigger a detectable change.
	changed := append([]dockerContainer(nil), containers...)
	for i := range changed {
		if changed[i].IP == "172.17.0.2" {
			changed[i].Labels = copyLabels(changed[i].Labels)
			changed[i].Labels["redoproxy.max_body_size"] = "100"
			break
		}
	}

	next := buildRouteMap(changed)

	assert.False(t, sameRouteMap(base, next))
}

func copyLabels(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	maps.Copy(dst, src)
	return dst
}

func TestBuildRouteMapParsesTimeoutLabel(t *testing.T) {
	containers := []dockerContainer{
		{
			ID:    "with-timeout",
			Name:  "with-timeout",
			State: "running",
			Labels: map[string]string{
				"redoproxy.enabled": "true",
				"redoproxy.domain":  "slow.example.com",
				"redoproxy.port":    "8765",
				"redoproxy.timeout": "120",
			},
			IP:   "172.17.0.20",
			Port: 8765,
		},
		{
			ID:    "no-timeout",
			Name:  "no-timeout",
			State: "running",
			Labels: map[string]string{
				"redoproxy.enabled": "true",
				"redoproxy.domain":  "fast.example.com",
				"redoproxy.port":    "8765",
			},
			IP:   "172.17.0.21",
			Port: 8765,
		},
	}

	routes := buildRouteMap(containers)

	assert.Equal(t, 120*time.Second, routes["slow.example.com"].Timeout)
	// Unset label → zero, so the proxy applies its default at request time.
	assert.Equal(t, time.Duration(0), routes["fast.example.com"].Timeout)
}
