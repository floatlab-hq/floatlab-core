package compose

import (
	"context"
	"testing"
)

func TestParse_BasicDockerCompose(t *testing.T) {
	const source = `services:
  caddy:
    image: caddy:latest
    container_name: caddy
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
      - "443:443/udp" # Optional: Used for HTTP/3 speed optimizations
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data
      - caddy_config:/config
    networks:
      - web_network

  # Example backend service to reverse proxy
  app_backend:
    image: nginx:alpine
    container_name: app_backend
    restart: unless-stopped
    # Do not expose ports to the host; Caddy will handle internal routing
    networks:
      - web_network

volumes:
  caddy_data:      # Crucial: Persists Let's Encrypt/ZeroSSL TLS certificates
  caddy_config:    # Persists Caddy internal configuration states

networks:
  web_network:     # Internal network allowing Caddy to talk to the backend
    driver: bridge
`

	parsed, err := Parse(context.Background(), source, "caddy-stack")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if parsed.ProjectName != "caddy-stack" {
		t.Fatalf("ProjectName = %q, want %q", parsed.ProjectName, "caddy-stack")
	}
	if parsed.ServiceVolumes == nil {
		t.Fatal("ServiceVolumes is nil")
	}
}
