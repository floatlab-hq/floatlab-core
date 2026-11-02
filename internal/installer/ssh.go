package installer

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"golang.org/x/crypto/ssh"
)

const maxSSHKeyBytes = 1 << 20

func sshKeyURL(source, reference string) (string, error) {
	if source == "" || source == "none" {
		return "", nil
	}
	if source == "web" {
		u, err := url.Parse(reference)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
			return "", fmt.Errorf("enter an HTTP or HTTPS SSH key URL without credentials or a fragment")
		}
		return u.String(), nil
	}
	if reference == "" || strings.ContainsAny(reference, "/\\?#%@:") || strings.ContainsFunc(reference, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) || reference == "." || reference == ".." {
		return "", fmt.Errorf("enter a single SSH key provider username, without a URL or path")
	}
	switch source {
	case "github":
		return "https://github.com/" + url.PathEscape(reference) + ".keys", nil
	case "gitlab":
		return "https://gitlab.com/" + url.PathEscape(reference) + ".keys", nil
	case "launchpad":
		return "https://launchpad.net/~" + url.PathEscape(reference) + "/+sshkeys", nil
	default:
		return "", fmt.Errorf("select a supported SSH key source")
	}
}

func sshKeyRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > 5 {
		return fmt.Errorf("SSH key source exceeded five redirects")
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return fmt.Errorf("SSH key redirects must use HTTP or HTTPS")
	}
	if req.URL.User != nil {
		return fmt.Errorf("SSH key redirects must not contain credentials")
	}
	if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme == "http" {
		return fmt.Errorf("SSH key source redirected from HTTPS to HTTP")
	}
	return nil
}

func fetchSSHKeys(ctx context.Context, source, reference string) (string, error) {
	address, err := sshKeyURL(source, reference)
	if err != nil || address == "" {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return "", fmt.Errorf("invalid SSH key URL")
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: sshKeyRedirect}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not fetch SSH keys; check the source and connection, then retry")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("SSH key source returned HTTP %d; check the username or URL", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSSHKeyBytes+1))
	if err != nil {
		return "", fmt.Errorf("could not read SSH keys; retry the import")
	}
	if len(data) > maxSSHKeyBytes {
		return "", fmt.Errorf("SSH key response exceeds 1 MiB")
	}
	return parseSSHKeys(data)
}

func parseSSHKeys(data []byte) (string, error) {
	var result strings.Builder
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil || len(options) != 0 || len(rest) != 0 {
			return "", fmt.Errorf("SSH key source must contain public keys without authorized-key options")
		}
		if _, cert := key.(*ssh.Certificate); cert {
			return "", fmt.Errorf("SSH certificates cannot be imported; use public keys")
		}
		normalized := string(ssh.MarshalAuthorizedKey(key))
		if !seen[normalized] {
			result.WriteString(normalized)
			seen[normalized] = true
		}
	}
	if result.Len() == 0 {
		return "", fmt.Errorf("SSH key source contains no public keys")
	}
	return result.String(), nil
}
