# reverse proxy for docker apps

## config reverse proxy routing
app should have config for routing and some params for reverse proxy
- domain for input request
- port of docker app, which should serve app for given domain
- connection limits (optional)
- limits body size (optional)

## discovery for docker apps, which have specific labels for `redoproxy`
- poll docker sockets for discover new docker containers with labels
- update app config with updated info
- track those labels:
    - `redoproxy.enabled` : `true`
    - `redoproxy.domain` : `example.com`
    - `redoproxy.port` : `3000`
    - `redoproxy.max_connections` : `100` (optional)
    - `redoproxy.max_body_size` : `10000000` (optional)
- container should be in same network as redoproxy
- !!! currently redoproxy support only one network, do not assign several networks to this container
`docker network create proxy-net` make sure external network is created
```
networks:
  redoproxy-net:
    external: true
```
e.g. docker compose files could look like
```
services:
  redoproxy:
    image: redoproxy
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
    ports:
      - "80:8080"
    networks:
      - proxy-net

networks:
  proxy-net:
    external: true
```
```
services:
  app:
    image: my-app
    labels:
      redoproxy.enabled: "true"
      redoproxy.domain: "example.com"
      redoproxy.port: "8765"
    networks:
      - proxy-net
    ports:
      - "127.0.0.1:8765:8765"

networks:
  proxy-net:
    external: true
```

## reverse proxy logic
- read config and compose reverse proxy based on this config
- add middleware to implement headers update (e.g. X-Real-Ip, X-Forwarded-For)
- add middleware to implement limits
- update config on app signal

## support let'sencrypt certificates support to issue TLS certificates for configured domains
- add standard golang flow to issue and cash TLS certificates

## cli commands
- to show redoproxy status
- to reload config
