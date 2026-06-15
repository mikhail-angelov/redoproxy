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
	"strconv"
	"strings"
	"sync"
	"time"
)

type Discovery struct {
	client      *http.Client
	interval    time.Duration
	networkName string
	routeMap    map[string]ContainerRoute
	lock        sync.RWMutex
}

type ContainerRoute struct {
	Domain         string
	Server         string
	MaxBodySize    int64
	ConcurrentRequestsLimit int
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
		routeMap:    make(map[string]ContainerRoute),
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

func (d *Discovery) Lookup(host string) (ContainerRoute, bool) {
	host = strings.ToLower(strings.TrimSpace(host))

	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	d.lock.RLock()
	defer d.lock.RUnlock()

	route, ok := d.routeMap[host]
	return route, ok
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

func sameRouteMap(a, b map[string]ContainerRoute) bool {
	if len(a) != len(b) {
		return false
	}

	for k, av := range a {
		bv, ok := b[k]
		if !ok {
			return false
		}

		if av != bv {
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

	if sameRouteMap(d.routeMap, next) {
		return nil
	}

	d.routeMap = next
	slog.Info("container routes refreshed", "network", d.networkName, "routes", len(d.routeMap))

	return nil
}

func buildRouteMap(containers []dockerContainer) map[string]ContainerRoute {
	routes := make(map[string]ContainerRoute)

	for _, c := range containers {
		if c.State != "running" {
			continue
		}

		if c.Labels["redoproxy.enabled"] != "true" {
			continue
		}

		domain := strings.ToLower(strings.TrimSpace(c.Labels["redoproxy.domain"]))
		if domain == "" {
			slog.Warn("skip container route without domain", "container", c.Name, "domain", domain)
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

		maxBodySize := 0
		if c.Labels["redoproxy.max_body_size"] != "" {
			value, err := strconv.Atoi(c.Labels["redoproxy.max_body_size"])
			if err != nil || value < 0 {
				slog.Warn("invalid max_body_size label", "value", c.Labels["redoproxy.max_body_size"])
			} else {
				maxBodySize = value
			}
		}

		concurrentRequestsLimit := 0
		if c.Labels["redoproxy.max_connections"] != "" {
			value, err := strconv.Atoi(c.Labels["redoproxy.max_connections"])
			if err != nil || value < 0 {
				slog.Warn("invalid max_connections label", "value", c.Labels["redoproxy.max_connections"])
			} else {
				concurrentRequestsLimit = value
			}
		}

		if existing, ok := routes[domain]; ok {
			slog.Warn(
				"duplicate redoproxy domain, overriding route",
				"domain", domain,
				"old_server", existing.Server,
				"new_container", c.Name,
			)
		}

		routes[domain] = ContainerRoute{
			Domain:         domain,
			Server:         "http://" + net.JoinHostPort(c.IP, strconv.Itoa(c.Port)),
			MaxBodySize:    int64(maxBodySize),
			ConcurrentRequestsLimit: concurrentRequestsLimit,
		}
	}

	return routes
}
