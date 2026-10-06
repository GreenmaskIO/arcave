// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build fidelity

package test

import (
	"testing"

	"github.com/docker/docker/api/types/mount"

	"github.com/greenmaskio/arcave/internal/model"
)

// busyboxBase is what every busybox service of a want declares unless it says otherwise: what
// the image gave (PATH) and what the daemon reports of its own (ipc, cgroup).
const busyboxBase = `
x-base: &base
  image: "{{busybox}}"
  command: [sleep, infinity]
  environment: {PATH: /usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin}
  pull_policy: missing
  ipc: private
  cgroup: private
`

// defaultNetwork is the network compose creates for a project that declares none.
const defaultNetwork = `
networks:
  default: {name: "{{project}}_default", driver: bridge, ipam: {driver: default}}
`

func replicaCases() []fidelityCase {
	return []fidelityCase{
		{
			name: "deploy.replicas",
			compose: `
services:
  web: {image: "{{busybox}}", command: [sleep, infinity], deploy: {replicas: 2}}
`,
			want: busyboxBase + `
services:
  web: {<<: *base, scale: 2, networks: {default: null}}
` + defaultNetwork,
		},
		{
			name: "scale",
			compose: `
services:
  web: {image: "{{busybox}}", command: [sleep, infinity], scale: 2}
`,
			want: busyboxBase + `
services:
  web: {<<: *base, scale: 2, networks: {default: null}}
` + defaultNetwork,
		},
		{
			name: "anonymous volume on replicas",
			compose: `
services:
  web: {image: "{{busybox}}", command: [sleep, infinity], scale: 2, volumes: [/cache]}
`,
			want: busyboxBase + `
services:
  web:
    <<: *base
    scale: 2
    networks: {default: null}
    volumes: [{type: volume, target: /cache, volume: {}}]
` + defaultNetwork,
		},
		{
			name: "shared named volume",
			compose: `
services:
  web: {image: "{{busybox}}", command: [sleep, infinity], scale: 2, volumes: [data:/data]}
volumes:
  data: {}
`,
			want: busyboxBase + `
services:
  web:
    <<: *base
    scale: 2
    networks: {default: null}
    volumes: [{type: volume, source: data, target: /data}]
volumes:
  data: {name: "{{project}}_data", driver: local}
` + defaultNetwork,
		},
		{
			// H1: the daemon keeps the range on every replica and hands each a port of it.
			name: "port range",
			compose: `
services:
  web: {image: "{{busybox}}", command: [sleep, infinity], scale: 3, ports: ["127.0.0.1:18080-18082:80"]}
`,
			want: busyboxBase + `
services:
  web:
    <<: *base
    scale: 3
    networks: {default: null}
    ports: [{mode: ingress, host_ip: 127.0.0.1, target: 80, published: "18080-18082", protocol: tcp}]
` + defaultNetwork,
		},
		{
			name: "network alias on replicas",
			compose: `
services:
  web:
    image: "{{busybox}}"
    command: [sleep, infinity]
    scale: 2
    networks: {default: {aliases: [api]}}
`,
			want: busyboxBase + `
services:
  web: {<<: *base, scale: 2, networks: {default: {aliases: [api]}}}
` + defaultNetwork,
		},
		{
			// H2: compose resolves the reference to the first replica; the spec names the service again.
			name: "network_mode service: on a replicated service",
			compose: `
services:
  web:  {image: "{{busybox}}", command: [sleep, infinity], deploy: {replicas: 3}}
  side: {image: "{{busybox}}", command: [sleep, infinity], network_mode: "service:web"}
`,
			want: busyboxBase + `
services:
  web:  {<<: *base, scale: 3, networks: {default: null}}
  side:
    <<: *base
    scale: 1
    network_mode: service:web
    depends_on: {web: {condition: service_started, restart: true, required: true}}
` + defaultNetwork,
		},
		{
			// H2: side shares the first replica's volume, not one of its own.
			name: "volumes_from a replicated service",
			compose: `
services:
  web:  {image: "{{busybox}}", command: [sleep, infinity], scale: 3, volumes: [/shared]}
  side: {image: "{{busybox}}", command: [sleep, infinity], volumes_from: [web]}
`,
			want: busyboxBase + `
services:
  web:
    <<: *base
    scale: 3
    networks: {default: null}
    volumes: [{type: volume, target: /shared, volume: {}}]
  side:
    <<: *base
    scale: 1
    networks: {default: null}
    volumes_from: [web]
    depends_on: {web: {condition: service_started, required: true}}
` + defaultNetwork,
		},
		{
			// H3: the spec takes the first replica and says so; pass 2 aligns the second with it.
			name: "divergent replica",
			compose: `
services:
  web: {image: "{{busybox}}", command: [sleep, infinity], scale: 2, mem_limit: 64m}
`,
			afterUp: func(t *testing.T, e *fidelityEnv) {
				e.updateMemory(t, "web", 2, 96<<20)
			},
			want: busyboxBase + `
services:
  web: {<<: *base, scale: 2, mem_limit: 64m, networks: {default: null}}
` + defaultNetwork,
			warns: []string{`{"level":"warn","service":"web","fields":["mem_limit"],
				"message":"replicas differ; the spec declares the first replica's"}`},
			drift: func(r *model.IntrospectionResult) {
				replica := r.Project.Services[0].Containers[1].Inspect.HostConfig
				replica.Memory, replica.MemorySwap = 64<<20, 128<<20
			},
		},
	}
}

func namespaceCases() []fidelityCase {
	return []fidelityCase{
		{
			name: "network_mode service:",
			compose: `
services:
  db:  {image: "{{busybox}}", command: [sleep, infinity]}
  app: {image: "{{busybox}}", command: [sleep, infinity], network_mode: "service:db"}
`,
			want: busyboxBase + `
services:
  db:  {<<: *base, scale: 1, networks: {default: null}}
  app:
    <<: *base
    scale: 1
    network_mode: service:db
    depends_on: {db: {condition: service_started, restart: true, required: true}}
` + defaultNetwork,
		},
		{
			name: "ipc service:",
			compose: `
services:
  db:  {image: "{{busybox}}", command: [sleep, infinity], ipc: shareable}
  app: {image: "{{busybox}}", command: [sleep, infinity], ipc: "service:db"}
`,
			want: busyboxBase + `
services:
  db:  {<<: *base, scale: 1, ipc: shareable, networks: {default: null}}
  app:
    <<: *base
    scale: 1
    ipc: service:db
    networks: {default: null}
    depends_on: {db: {condition: service_started, restart: true, required: true}}
` + defaultNetwork,
		},
		{
			name: "pid service:",
			compose: `
services:
  db:  {image: "{{busybox}}", command: [sleep, infinity]}
  app: {image: "{{busybox}}", command: [sleep, infinity], pid: "service:db"}
`,
			want: busyboxBase + `
services:
  db:  {<<: *base, scale: 1, networks: {default: null}}
  app:
    <<: *base
    scale: 1
    pid: service:db
    networks: {default: null}
    depends_on: {db: {condition: service_started, restart: true, required: true}}
` + defaultNetwork,
		},
		{
			// The daemon resolves the name to the id; the spec declares the name back.
			name: "network_mode container: outside the project",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], network_mode: "container:{{project}}-outside"}
`,
			want: busyboxBase + `
services:
  app: {<<: *base, scale: 1, network_mode: "container:{{project}}-outside"}
`,
			setup:       func(t *testing.T, e *fidelityEnv) { e.runOutsideContainer(t, "outside", "") },
		},
		{
			name: "ipc shareable",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], ipc: shareable}
`,
			want: busyboxBase + `
services:
  app: {<<: *base, scale: 1, ipc: shareable, networks: {default: null}}
` + defaultNetwork,
		},
		{
			name: "pid host",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], pid: host}
`,
			want: busyboxBase + `
services:
  app: {<<: *base, scale: 1, pid: host, networks: {default: null}}
` + defaultNetwork,
		},
		{
			name: "network_mode host",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], network_mode: host}
`,
			want: busyboxBase + `
services:
  app: {<<: *base, scale: 1, network_mode: host}
`,
		},
		{
			name: "network_mode none",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], network_mode: none}
`,
			want: busyboxBase + `
services:
  app: {<<: *base, scale: 1, network_mode: none}
`,
		},
		{
			name: "network_mode bridge",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], network_mode: bridge}
`,
			want: busyboxBase + `
services:
  app: {<<: *base, scale: 1, network_mode: bridge}
`,
		},
		{
			name: "legacy links on bridge",
			compose: `
services:
  db:  {image: "{{busybox}}", command: [sleep, infinity], network_mode: bridge}
  app: {image: "{{busybox}}", command: [sleep, infinity], network_mode: bridge, links: ["db:database"]}
`,
			want: busyboxBase + `
services:
  db:  {<<: *base, scale: 1, network_mode: bridge}
  app:
    <<: *base
    scale: 1
    network_mode: bridge
    links: ["db:database"]
    depends_on: {db: {condition: service_started, restart: true, required: true}}
`,
		},
		{
			// compose drops the mode on the way to the daemon, so no spec can carry it back.
			name: "volumes_from read-only",
			compose: `
services:
  db:  {image: "{{busybox}}", command: [sleep, infinity], volumes: [/var/lib/db]}
  app: {image: "{{busybox}}", command: [sleep, infinity], volumes_from: ["db:ro"]}
`,
			want: busyboxBase + `
services:
  db:
    <<: *base
    scale: 1
    networks: {default: null}
    volumes: [{type: volume, target: /var/lib/db, volume: {}}]
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    volumes_from: [db]
    depends_on: {db: {condition: service_started, required: true}}
` + defaultNetwork,
		},
	}
}

func networkCases() []fidelityCase {
	return []fidelityCase{
		{
			name: "two networks",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], networks: [front, back]}
networks:
  front: {}
  back: {}
`,
			want: busyboxBase + `
services:
  app: {<<: *base, scale: 1, networks: {front: null, back: null}}
networks:
  front: {name: "{{project}}_front", driver: bridge, ipam: {driver: default}}
  back:  {name: "{{project}}_back", driver: bridge, ipam: {driver: default}}
`,
		},
		{
			name: "internal network",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], networks: [back]}
networks:
  back: {internal: true}
`,
			want: busyboxBase + `
services:
  app: {<<: *base, scale: 1, networks: {back: null}}
networks:
  back: {name: "{{project}}_back", driver: bridge, ipam: {driver: default}, internal: true}
`,
		},
		{
			// compose labels no network it does not own, so its key is lost; it keys under its name.
			name: "external network",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], networks: [shared]}
networks:
  shared: {name: "{{project}}-shared", external: true}
`,
			want: busyboxBase + `
services:
  app: {<<: *base, scale: 1, networks: {"{{project}}-shared": null}}
networks:
  "{{project}}-shared": {name: "{{project}}-shared", external: true}
`,
			setup: func(t *testing.T, e *fidelityEnv) { e.createOutsideNetwork(t, "shared") },
		},
		{
			name: "static ipv4 address",
			compose: `
services:
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    networks: {back: {ipv4_address: 10.213.7.10}}
networks:
  back: {ipam: {config: [{subnet: 10.213.7.0/24}]}}
`,
			want: busyboxBase + `
services:
  app: {<<: *base, scale: 1, networks: {back: {ipv4_address: 10.213.7.10}}}
networks:
  back:
    name: "{{project}}_back"
    driver: bridge
    ipam: {driver: default, config: [{subnet: 10.213.7.0/24, gateway: 10.213.7.1}]}
`,
		},
		{
			name: "aliases",
			compose: `
services:
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    networks: {default: {aliases: [api, api.internal]}}
`,
			want: busyboxBase + `
services:
  app: {<<: *base, scale: 1, networks: {default: {aliases: [api, api.internal]}}}
` + defaultNetwork,
		},
		{
			name: "extra_hosts",
			compose: `
services:
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    extra_hosts: ["db.local=10.0.0.5", "cache.local:10.0.0.6"]
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    extra_hosts: ["db.local=10.0.0.5", "cache.local=10.0.0.6"]
` + defaultNetwork,
		},
		{
			name: "dns with its search and options",
			compose: `
services:
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    dns: [1.1.1.1, 8.8.8.8]
    dns_search: [example.internal]
    dns_opt: [ndots:2, timeout:3]
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    dns: [1.1.1.1, 8.8.8.8]
    dns_search: [example.internal]
    dns_opt: [ndots:2, timeout:3]
` + defaultNetwork,
		},
		{
			// The daemon reports a declared MAC address exactly as a generated one, so no spec
			// can carry it back; pass 2 runs on a generated one.
			name: "mac_address",
			compose: `
services:
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    networks: {default: {mac_address: "02:42:ac:11:00:42"}}
`,
			want: busyboxBase + `
services:
  app: {<<: *base, scale: 1, networks: {default: {}}}
` + defaultNetwork,
			drift: func(r *model.IntrospectionResult) {
				r.Project.Services[0].Containers[0].Inspect.NetworkSettings.Networks[r.Project.Name+"_default"].MacAddress = ""
			},
		},
		{
			name: "gw_priority",
			compose: `
services:
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    networks: {front: {gw_priority: 10}, back: {}}
networks:
  front: {}
  back: {}
`,
			want: busyboxBase + `
services:
  app: {<<: *base, scale: 1, networks: {front: {gw_priority: 10}, back: {}}}
networks:
  front: {name: "{{project}}_front", driver: bridge, ipam: {driver: default}}
  back:  {name: "{{project}}_back", driver: bridge, ipam: {driver: default}}
`,
		},
		{
			name: "links and external_links",
			compose: `
services:
  db:  {image: "{{busybox}}", command: [sleep, infinity], networks: [shared]}
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    networks: [shared]
    links: ["db:database"]
    external_links: ["{{project}}-outside:legacy"]
networks:
  shared: {name: "{{project}}-shared", external: true}
`,
			want: busyboxBase + `
services:
  db: {<<: *base, scale: 1, networks: {"{{project}}-shared": null}}
  app:
    <<: *base
    scale: 1
    networks: {"{{project}}-shared": null}
    links: ["db:database"]
    external_links: ["{{project}}-outside:legacy"]
    depends_on: {db: {condition: service_started, restart: true, required: true}}
networks:
  "{{project}}-shared": {name: "{{project}}-shared", external: true}
`,
			setup: func(t *testing.T, e *fidelityEnv) {
				e.createOutsideNetwork(t, "shared")
				e.runOutsideContainer(t, "outside", e.project+"-shared")
			},
		},
	}
}

func portCases() []fidelityCase {
	return []fidelityCase{
		{
			name: "short and long syntax",
			compose: `
services:
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    ports:
      - "127.0.0.1:18180:80"
      - {target: 81, published: "18181", host_ip: 127.0.0.1, protocol: tcp}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    ports:
      - {mode: ingress, host_ip: 127.0.0.1, target: 80, published: "18180", protocol: tcp}
      - {mode: ingress, host_ip: 127.0.0.1, target: 81, published: "18181", protocol: tcp}
` + defaultNetwork,
		},
		{
			name: "host_ip",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], ports: ["0.0.0.0:18182:80"]}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    ports: [{mode: ingress, host_ip: 0.0.0.0, target: 80, published: "18182", protocol: tcp}]
` + defaultNetwork,
		},
		{
			name: "udp",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], ports: ["127.0.0.1:18183:53/udp"]}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    ports: [{mode: ingress, host_ip: 127.0.0.1, target: 53, published: "18183", protocol: udp}]
` + defaultNetwork,
		},
		{
			name: "random published port",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], ports: ["127.0.0.1::80"]}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    ports: [{mode: ingress, host_ip: 127.0.0.1, target: 80, protocol: tcp}]
` + defaultNetwork,
		},
		{
			name: "expose",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], expose: ["8080", "9090/udp"]}
`,
			want: busyboxBase + `
services:
  app: {<<: *base, scale: 1, networks: {default: null}, expose: [8080/tcp, 9090/udp]}
` + defaultNetwork,
		},
	}
}

func runtimeCases() []fidelityCase {
	return []fidelityCase{
		{
			name: "user and group_add",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], user: "1000:1000", group_add: [audio, "2000"]}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    user: "1000:1000"
    group_add: [audio, "2000"]
` + defaultNetwork,
		},
		{
			name: "entrypoint and command",
			compose: `
services:
  app: {image: "{{busybox}}", entrypoint: [sh, -c], command: ["exec sleep infinity"]}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    entrypoint: [sh, -c]
    command: ["exec sleep infinity"]
` + defaultNetwork,
		},
		{
			name: "environment and env_file with a dollar",
			compose: `
services:
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    env_file: [app.env]
    environment: {GREETING: "costs $$5"}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    environment:
      PATH: /usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
      GREETING: costs $5
      SECRET: pa$word
` + defaultNetwork,
			files: map[string]string{"app.env": "SECRET='pa$word'\n"},
		},
		{
			name: "read_only and tmpfs",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], read_only: true, tmpfs: [/tmp, "/run:size=1m"]}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    read_only: true
    tmpfs: [/tmp, "/run:size=1m"]
` + defaultNetwork,
		},
		{
			name: "ulimits",
			compose: `
services:
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    ulimits: {nproc: 512, nofile: {soft: 1024, hard: 2048}}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    ulimits: {nproc: {soft: 512, hard: 512}, nofile: {soft: 1024, hard: 2048}}
` + defaultNetwork,
		},
		{
			name: "sysctls",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], sysctls: {net.core.somaxconn: 1024}}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    sysctls: {net.core.somaxconn: "1024"}
` + defaultNetwork,
		},
		{
			name: "cap_add and cap_drop",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], cap_add: [NET_ADMIN], cap_drop: [MKNOD]}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    cap_add: [CAP_NET_ADMIN]
    cap_drop: [CAP_MKNOD]
` + defaultNetwork,
		},
		{
			name: "security_opt",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], security_opt: ["no-new-privileges:true"]}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    security_opt: ["no-new-privileges:true"]
` + defaultNetwork,
		},
		{
			name: "init",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], init: true}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    init: true
` + defaultNetwork,
		},
		{
			name: "shm_size",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], shm_size: 128m}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    shm_size: 128m
` + defaultNetwork,
		},
		{
			name: "stop_signal and stop_grace_period",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], stop_signal: SIGINT, stop_grace_period: 25s}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    stop_signal: SIGINT
    stop_grace_period: 25s
` + defaultNetwork,
		},
		{
			name: "restart on-failure:3",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], restart: "on-failure:3"}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    restart: "on-failure:3"
` + defaultNetwork,
		},
		{
			name: "healthcheck",
			compose: `
services:
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    healthcheck: {test: [CMD, "true"], interval: 2s, timeout: 1s, retries: 2, start_period: 1s}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    healthcheck: {test: [CMD, "true"], interval: 2s, timeout: 1s, retries: 2, start_period: 1s}
` + defaultNetwork,
		},
		{
			// compose's start --wait refuses a container whose healthcheck is disabled.
			name: "healthcheck disabled",
			compose: `
services:
  app: {image: "{{redis}}", healthcheck: {disable: true}}
`,
			want: `
services:
  app:
    image: "{{redis}}"
    entrypoint: [docker-entrypoint.sh]
    command: [redis-server]
    environment: {PATH: /usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin, REDIS_VERSION: 7.4.11}
    expose: [6379/tcp]
    working_dir: /data
    pull_policy: missing
    ipc: private
    cgroup: private
    scale: 1
    networks: {default: null}
    volumes: [{type: volume, target: /data}]
    healthcheck: {disable: true}
` + defaultNetwork,
			noWait: true,
			drift: func(r *model.IntrospectionResult) {
				// The spec declares the volume the image gave, so compose now asks for it.
				replica := &r.Project.Services[0].Containers[0].Inspect
				replica.HostConfig.Mounts = []mount.Mount{{Type: mount.TypeVolume, Target: "/data"}}
				replica.Mounts[0].Mode = "z"
			},
		},
		{
			name: "limits in short syntax",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], mem_limit: 64m, cpus: 0.5, pids_limit: 100}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    mem_limit: 64m
    cpus: 0.5
    pids_limit: 100
` + defaultNetwork,
		},
		{
			name: "limits through deploy.resources",
			compose: `
services:
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    deploy:
      resources:
        limits: {cpus: "0.5", memory: 64M, pids: 100}
        reservations: {memory: 32M}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    mem_limit: 64m
    mem_reservation: 32m
    cpus: 0.5
    pids_limit: 100
` + defaultNetwork,
		},
		{
			name: "oom_score_adj",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], oom_score_adj: 500}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    oom_score_adj: 500
` + defaultNetwork,
		},
		{
			name: "hostname and domainname",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], hostname: app-host, domainname: example.internal}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    hostname: app-host
    domainname: example.internal
` + defaultNetwork,
		},
		{
			name: "tty and stdin_open",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], tty: true, stdin_open: true}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    tty: true
    stdin_open: true
` + defaultNetwork,
		},
		{
			name: "logging with options",
			compose: `
services:
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    logging: {driver: json-file, options: {max-size: 1m, max-file: "2"}}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    logging: {driver: json-file, options: {max-size: 1m, max-file: "2"}}
` + defaultNetwork,
		},
		{
			// H5: compose mounts both as binds, which is all the daemon has to report.
			name: "secrets and configs from files",
			compose: `
services:
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    secrets: [token]
    configs: [{source: app_conf, target: /etc/app.conf}]
secrets:
  token: {file: ./token.txt}
configs:
  app_conf: {file: ./app.conf}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    volumes:
      - {type: bind, source: "{{dir}}/token.txt", target: /run/secrets/token, read_only: true, bind: {}}
      - {type: bind, source: "{{dir}}/app.conf", target: /etc/app.conf, read_only: true, bind: {}}
` + defaultNetwork,
			files: map[string]string{"token.txt": "s3cr3t\n", "app.conf": "key=value\n"},
			drift: func(r *model.IntrospectionResult) {
				// Declared as binds, they are asked for with empty bind options.
				host := r.Project.Services[0].Containers[0].Inspect.HostConfig
				for i := range host.Mounts {
					host.Mounts[i].BindOptions = &mount.BindOptions{}
				}
			},
		},
	}
}

func volumeCases() []fidelityCase {
	return []fidelityCase{
		{
			name: "named volume with driver_opts",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], volumes: [data:/data]}
volumes:
  data: {driver: local, driver_opts: {type: tmpfs, device: tmpfs, o: size=10m}, labels: {tier: cache}}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    volumes: [{type: volume, source: data, target: /data}]
volumes:
  data:
    name: "{{project}}_data"
    driver: local
    driver_opts: {type: tmpfs, device: tmpfs, o: size=10m}
    labels: {tier: cache}
` + defaultNetwork,
		},
		{
			// compose labels no volume it does not own, so its key is lost; it keys under its name.
			name: "external volume",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], volumes: [shared:/data]}
volumes:
  shared: {name: "{{project}}-shared", external: true}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    volumes: [{type: volume, source: "{{project}}-shared", target: /data}]
volumes:
  "{{project}}-shared": {name: "{{project}}-shared", external: true}
` + defaultNetwork,
			setup: func(t *testing.T, e *fidelityEnv) { e.createOutsideVolume(t, "shared", "") },
		},
		{
			name: "subpath",
			compose: `
services:
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    volumes: [{type: volume, source: shared, target: /data, volume: {subpath: sub}}]
volumes:
  shared: {name: "{{project}}-shared", external: true}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    volumes: [{type: volume, source: "{{project}}-shared", target: /data, volume: {subpath: sub}}]
volumes:
  "{{project}}-shared": {name: "{{project}}-shared", external: true}
` + defaultNetwork,
			setup: func(t *testing.T, e *fidelityEnv) { e.createOutsideVolume(t, "shared", "mkdir /data/sub") },
		},
		{
			name: "nocopy",
			compose: `
services:
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    volumes: [{type: volume, source: data, target: /etc, volume: {nocopy: true}}]
volumes:
  data: {}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    volumes: [{type: volume, source: data, target: /etc, volume: {nocopy: true}}]
volumes:
  data: {name: "{{project}}_data", driver: local}
` + defaultNetwork,
		},
		{
			// Docker Desktop shares no host mount, so propagation is limited to the private modes.
			name: "bind with propagation",
			compose: `
services:
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    volumes: [{type: bind, source: ./data, target: /data, bind: {propagation: private}}]
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    volumes: [{type: bind, source: "{{dir}}/data", target: /data, bind: {propagation: private}}]
` + defaultNetwork,
			files: map[string]string{"data/.keep": ""},
		},
		{
			name: "bind without create_host_path",
			compose: `
services:
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    volumes: [{type: bind, source: ./data, target: /data, bind: {create_host_path: false}}]
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    volumes: [{type: bind, source: "{{dir}}/data", target: /data, bind: {}}]
` + defaultNetwork,
			files: map[string]string{"data/.keep": ""},
		},
		{
			name: "read_only mounts",
			compose: `
services:
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    volumes: ["data:/data:ro", "./conf:/conf:ro"]
volumes:
  data: {}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    volumes:
      - {type: volume, source: data, target: /data, read_only: true}
      - {type: bind, source: "{{dir}}/conf", target: /conf, read_only: true, bind: {create_host_path: true}}
volumes:
  data: {name: "{{project}}_data", driver: local}
` + defaultNetwork,
			files: map[string]string{"conf/.keep": ""},
		},
		{
			name: "tmpfs mount",
			compose: `
services:
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    volumes: [{type: tmpfs, target: /scratch, tmpfs: {size: 1048576, mode: 01777}}]
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    volumes: [{type: tmpfs, target: /scratch, tmpfs: {size: 1048576, mode: 1023}}]
` + defaultNetwork,
		},
		{
			// H4: an image VOLUME nobody declared arrives as an anonymous volume.
			name: "image VOLUME",
			compose: `
services:
  app: {image: "{{redis}}"}
`,
			want: `
services:
  app:
    image: "{{redis}}"
    entrypoint: [docker-entrypoint.sh]
    command: [redis-server]
    environment: {PATH: /usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin, REDIS_VERSION: 7.4.11}
    expose: [6379/tcp]
    working_dir: /data
    pull_policy: missing
    ipc: private
    cgroup: private
    scale: 1
    networks: {default: null}
    volumes: [{type: volume, target: /data}]
` + defaultNetwork,
			drift: func(r *model.IntrospectionResult) {
				// The spec declares the volume the image gave, so compose now asks for it.
				replica := &r.Project.Services[0].Containers[0].Inspect
				replica.HostConfig.Mounts = []mount.Mount{{Type: mount.TypeVolume, Target: "/data"}}
				replica.Mounts[0].Mode = "z"
			},
		},
	}
}

func dependencyCases() []fidelityCase {
	return []fidelityCase{
		{
			name: "depends_on service_healthy",
			compose: `
services:
  db:
    image: "{{busybox}}"
    command: [sleep, infinity]
    healthcheck: {test: [CMD, "true"], interval: 1s, retries: 3}
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    depends_on: {db: {condition: service_healthy}}
`,
			want: busyboxBase + `
services:
  db:
    <<: *base
    scale: 1
    networks: {default: null}
    healthcheck: {test: [CMD, "true"], interval: 1s, retries: 3}
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    depends_on: {db: {condition: service_healthy, required: true}}
` + defaultNetwork,
		},
		{
			// H6: the exited init container is introspected and created again.
			name: "depends_on service_completed_successfully",
			compose: `
services:
  init: {image: "{{busybox}}", command: ["true"]}
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    depends_on: {init: {condition: service_completed_successfully}}
`,
			want: busyboxBase + `
services:
  init: {<<: *base, scale: 1, networks: {default: null}, command: ["true"]}
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    depends_on: {init: {condition: service_completed_successfully, required: true}}
` + defaultNetwork,
		},
		{
			name: "depends_on several services under different conditions",
			compose: `
services:
  db:
    image: "{{busybox}}"
    command: [sleep, infinity]
    healthcheck: {test: [CMD, "true"], interval: 1s, retries: 3}
  init: {image: "{{busybox}}", command: ["true"]}
  cache: {image: "{{busybox}}", command: [sleep, infinity]}
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    depends_on:
      db: {condition: service_healthy}
      init: {condition: service_completed_successfully}
      cache: {condition: service_started, restart: true}
`,
			want: busyboxBase + `
services:
  db:
    <<: *base
    scale: 1
    networks: {default: null}
    healthcheck: {test: [CMD, "true"], interval: 1s, retries: 3}
  init: {<<: *base, scale: 1, networks: {default: null}, command: ["true"]}
  cache: {<<: *base, scale: 1, networks: {default: null}}
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    depends_on:
      db: {condition: service_healthy, required: true}
      init: {condition: service_completed_successfully, required: true}
      cache: {condition: service_started, restart: true, required: true}
` + defaultNetwork,
		},
		{
			name: "depends_on restart false",
			compose: `
services:
  db: {image: "{{busybox}}", command: [sleep, infinity]}
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    depends_on: {db: {condition: service_started, restart: false}}
`,
			want: busyboxBase + `
services:
  db: {<<: *base, scale: 1, networks: {default: null}}
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    depends_on: {db: {condition: service_started, required: true}}
` + defaultNetwork,
		},
		{
			// compose's dependency label carries no required flag, so every dependency reads as required.
			name: "depends_on required false",
			compose: `
services:
  db: {image: "{{busybox}}", command: [sleep, infinity]}
  app:
    image: "{{busybox}}"
    command: [sleep, infinity]
    depends_on: {db: {condition: service_started, required: false}}
`,
			want: busyboxBase + `
services:
  db: {<<: *base, scale: 1, networks: {default: null}}
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    depends_on: {db: {condition: service_started, required: true}}
` + defaultNetwork,
		},
		{
			name: "inactive profile",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity]}
  debug: {image: "{{busybox}}", command: [sleep, infinity], profiles: [debug]}
`,
			want: busyboxBase + `
services:
  app: {<<: *base, scale: 1, networks: {default: null}}
` + defaultNetwork,
		},
	}
}

func buildCases() []fidelityCase {
	return []fidelityCase{
		{
			// H8: an image built here exists in no registry, so the spec never pulls it.
			name: "service built from a Dockerfile",
			compose: `
services:
  app: {build: .}
`,
			want: busyboxBase + `
services:
  app: {<<: *base, image: "{{project}}-app", pull_policy: never, scale: 1, networks: {default: null}}
` + defaultNetwork,
			files: map[string]string{"Dockerfile": `
FROM {{busybox}}
RUN echo built > /built
CMD ["sleep", "infinity"]
`},
		},
	}
}

func provenanceCases() []fidelityCase {
	return []fidelityCase{
		{
			name: "override file",
			compose: `
services:
  app: {image: "{{busybox}}", command: [sleep, infinity]}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    environment: {PATH: /usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin, MODE: override}
` + defaultNetwork + `
x-arcave-origin:
  working_dir: "{{dir}}"
  compose_files: ["{{dir}}/compose.yaml", "{{dir}}/compose.override.yaml"]
`,
			files: map[string]string{"compose.override.yaml": `
services:
  app: {environment: {MODE: override}}
`},
			composeFiles: []string{"compose.yaml", "compose.override.yaml"},
		},
		{
			name: "include",
			compose: `
include: [db.yaml]
services:
  app: {image: "{{busybox}}", command: [sleep, infinity], depends_on: [db]}
`,
			want: busyboxBase + `
services:
  db: {<<: *base, scale: 1, networks: {default: null}}
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    depends_on: {db: {condition: service_started, required: true}}
` + defaultNetwork + `
x-arcave-origin:
  working_dir: "{{dir}}"
  compose_files: ["{{dir}}/compose.yaml"]
`,
			files: map[string]string{"db.yaml": `
services:
  db: {image: "{{busybox}}", command: [sleep, infinity]}
`},
		},
		{
			name: "extends",
			compose: `
services:
  app: {extends: {file: base.yaml, service: base}, environment: {ROLE: app}}
`,
			want: busyboxBase + `
services:
  app:
    <<: *base
    scale: 1
    networks: {default: null}
    environment: {PATH: /usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin, TIER: base, ROLE: app}
` + defaultNetwork + `
x-arcave-origin:
  working_dir: "{{dir}}"
  compose_files: ["{{dir}}/compose.yaml"]
`,
			files: map[string]string{"base.yaml": `
services:
  base: {image: "{{busybox}}", command: [sleep, infinity], environment: {TIER: base}}
`},
		},
	}
}
