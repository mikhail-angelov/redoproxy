package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Discovery struct {
	client      *http.Client
	interval    time.Duration
	networkName string
	routeMap    map[string]*RouteGroup
	lock        sync.RWMutex
}

type RouteGroup struct {
	Domain                  string
	Upstreams               []Upstream
	MaxBodySize             int64
	ConcurrentRequestsLimit int

	mu        sync.Mutex
	nextIndex int
}

type Upstream struct {
	Server string
}

type dockerContainer struct {
	ID          string
	State       string
	Labels      map[string]string
	Name        string
	NetworkName string
	IP          string
	Port        int
}

// dockerAPIContainer matches the Docker Engine API JSON response for container list.
type dockerAPIContainer struct {
	ID              string            `json:"Id"`
	Names           []string          `json:"Names"`
	State           string            `json:"State"`
	Labels          map[string]string `json:"Labels"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

func NewDiscovery(interval time.Duration, networkName string) *Discovery {
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", "/var/run/docker.sock")
			},
		},
	}
	if interval <= 0 {
		interval = 10 * time.Second
	}

	return &Discovery{
		client:      client,
		interval:    interval,
		networkName: networkName,
		routeMap:    make(map[string]*RouteGroup),
	}
}

func detectContainerNetworkByIDPrefix(containers []dockerContainer, idPrefix string) (string, bool) {
	for _, c := range containers {
		if !strings.HasPrefix(c.ID, idPrefix) {
			continue
		}

		if c.NetworkName == "" {
			return "", false
		}

		return c.NetworkName, true
	}

	return "", false
}

func (d *Discovery) CheckAndBootstrapDockerClient() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if d.networkName != "" {
		if err := d.refresh(ctx); err != nil {
			return fmt.Errorf("failed to init containers: %w", err)
		}

		slog.Info(
			"Docker client initialized successfully",
			"network", d.networkName,
		)
		return nil
	}

	hostname, err := os.Hostname() //suppose to return container ID, if run in docker
	if err != nil {
		return fmt.Errorf("failed to get host name: %w", err)
	}
	containers, err := d.getContainers(ctx)
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}
	networkName, ok := detectContainerNetworkByIDPrefix(containers, hostname)
	if !ok {
		return fmt.Errorf("cannot detect redoproxy network: run redoproxy in Docker with a single network or pass networkName explicitly")
	}

	d.networkName = networkName

	//bootstrap containers
	if err := d.refresh(ctx); err != nil {
		return fmt.Errorf("failed to refresh initial routes: %w", err)
	}

	slog.Info(
		"Docker client initialized successfully",
		"network", d.networkName,
	)
	return nil
}

func (d *Discovery) LookupGroup(host string) (*RouteGroup, bool) {
	host = normalizeLookupHost(host)

	d.lock.RLock()
	defer d.lock.RUnlock()

	group, ok := d.routeMap[host]
	return group, ok
}

func (d *Discovery) Run(ctx context.Context) error {
	if d.networkName == "" {
		return fmt.Errorf("discovery network is not initialized; call CheckAndBootstrapDockerClient first or pass networkName explicitly")
	}
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	if err := d.refresh(ctx); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("discovery service interrupted: %w", ctx.Err())

		case <-ticker.C:
			if err := d.refresh(ctx); err != nil {
				slog.Error("failed to refresh discovery", "err", err)
				continue
			}
		}
	}
}

func (d *Discovery) getContainers(ctx context.Context) ([]dockerContainer, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://localhost/containers/json", http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("failed to close docker response body", "err", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker containers API returned status %d", resp.StatusCode)
	}

	return d.parseContainerResponse(resp.Body)
}

// parseContainerResponse decodes and converts a Docker API container list JSON response.
func (d *Discovery) parseContainerResponse(r io.Reader) ([]dockerContainer, error) {
	var raw []dockerAPIContainer
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, fmt.Errorf("failed to decode container list: %w", err)
	}

	containers := make([]dockerContainer, len(raw))
	for i, c := range raw {
		dc := dockerContainer{
			ID:     c.ID,
			State:  c.State,
			Labels: c.Labels,
		}

		if len(c.Names) > 0 {
			dc.Name = strings.TrimPrefix(c.Names[0], "/")
		}

		for name, netCfg := range c.NetworkSettings.Networks {
			// During bootstrap d.networkName can be empty, so we take the first
			// available network. This is safe only if redoproxy is attached to one network.
			if name == d.networkName || d.networkName == "" {
				dc.NetworkName = name
				dc.IP = netCfg.IPAddress
				break
			}
		}

		if portStr, ok := c.Labels["redoproxy.port"]; ok && portStr != "" {
			portStr := strings.TrimSpace(portStr)
			port, err := strconv.Atoi(portStr)
			if err != nil || port <= 0 || port > 65535 {
				slog.Warn("invalid redoproxy.port label", "container", dc.Name, "port", portStr)
			} else {
				dc.Port = port
			}
		}

		containers[i] = dc
	}

	return containers, nil
}

func sameRouteGroup(a, b *RouteGroup) bool {
	if a == nil || b == nil {
		return false
	}
	if a.MaxBodySize != b.MaxBodySize {
		return false
	}
	if a.ConcurrentRequestsLimit != b.ConcurrentRequestsLimit {
		return false
	}
	if len(a.Upstreams) != len(b.Upstreams) {
		return false
	}

	for i := range a.Upstreams {
		if a.Upstreams[i] != b.Upstreams[i] {
			return false
		}
	}

	return true
}

func sameRouteMap(a, b map[string]*RouteGroup) bool {
	if len(a) != len(b) {
		return false
	}

	for k, av := range a {
		bv, ok := b[k]
		if !ok || !sameRouteGroup(av, bv) {
			return false
		}
	}

	return true
}

func (d *Discovery) refresh(ctx context.Context) error {
	containers, err := d.getContainers(ctx)
	if err != nil {
		return err
	}

	next := buildRouteMap(containers)

	d.lock.Lock()
	defer d.lock.Unlock()

	changed := len(next) != len(d.routeMap)
	for domain, group := range next {
		if existing, ok := d.routeMap[domain]; ok && sameRouteGroup(existing, group) {
			// Keep the existing group so its round-robin position
			// (nextIndex) survives a refresh that didn't change it.
			next[domain] = existing
			continue
		}
		changed = true
	}

	if !changed {
		return nil
	}

	d.routeMap = next
	slog.Info("container routes refreshed", "network", d.networkName, "routes", len(d.routeMap))

	return nil
}

func buildRouteMap(containers []dockerContainer) map[string]*RouteGroup {
	// Sort by IP/port first so that, when several containers share a domain,
	// which container's labels "win" (and the resulting Upstreams order) is
	// deterministic instead of depending on Docker API listing order.
	sorted := make([]dockerContainer, len(containers))
	copy(sorted, containers)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].IP != sorted[j].IP {
			return sorted[i].IP < sorted[j].IP
		}
		return sorted[i].Port < sorted[j].Port
	})

	routes := make(map[string]*RouteGroup)

	for _, c := range sorted {
		if c.State != "running" {
			continue
		}

		if c.Labels["redoproxy.enabled"] != "true" {
			continue
		}

		domain := normalizeLookupHost(c.Labels["redoproxy.domain"])
		if domain == "" {
			slog.Warn("skip container route without domain", "container", c.Name)
			continue
		}

		if c.IP == "" {
			slog.Warn("skip container route without ip", "container", c.Name, "id", c.ID)
			continue
		}

		if c.Port == 0 {
			slog.Warn("skip container route without port", "container", c.Name, "port", c.Port)
			continue
		}

		maxBodySize := parseInt64Label(c.Labels, "redoproxy.max_body_size", c.Name)
		concurrentRequestsLimit := parseIntLabel(c.Labels, "redoproxy.concurrent_requests_limit", c.Name)

		group, ok := routes[domain]
		if !ok {
			group = &RouteGroup{
				Domain:                  domain,
				MaxBodySize:             maxBodySize,
				ConcurrentRequestsLimit: concurrentRequestsLimit,
			}
			routes[domain] = group
		} else {
			if group.MaxBodySize != maxBodySize {
				slog.Warn(
					"conflicting max_body_size labels for same domain",
					"domain", domain,
					"existing", group.MaxBodySize,
					"container", c.Name,
					"value", maxBodySize,
				)
			}

			if group.ConcurrentRequestsLimit != concurrentRequestsLimit {
				slog.Warn(
					"conflicting concurrent_requests_limit labels for same domain",
					"domain", domain,
					"existing", group.ConcurrentRequestsLimit,
					"container", c.Name,
					"value", concurrentRequestsLimit,
				)
			}
		}

		group.Upstreams = append(group.Upstreams, Upstream{
			Server: "http://" + net.JoinHostPort(c.IP, strconv.Itoa(c.Port)),
		})

		slog.Info(
			"server added to route group",
			"domain", domain,
			"container", c.Name,
			"upstreams", len(group.Upstreams),
		)
	}

	return routes
}

func (g *RouteGroup) Next(r *http.Request) (Upstream, bool) {
	if g == nil {
		return Upstream{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.Upstreams) == 0 {
		return Upstream{}, false
	}

	idx := g.nextIndex % len(g.Upstreams)
	g.nextIndex++
	return g.Upstreams[idx], true
}

func normalizeLookupHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))

	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}

	return host
}

func parseInt64Label(labels map[string]string, labelTag string, from string) int64 {
	valueStr, ok := labels[labelTag]
	if !ok || valueStr == "" {
		return 0
	}
	value, err := strconv.ParseInt(strings.TrimSpace(valueStr), 10, 64)
	if err != nil || value < 0 {
		slog.Warn("invalid label", "label", labelTag, "container", from, "value", valueStr)
		return 0
	}
	return value
}

func parseIntLabel(labels map[string]string, labelTag string, from string) int {
	return int(parseInt64Label(labels, labelTag, from))
}
