// Command check-manage-image verifies the management container's UI and API.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/html"
)

func docker(args ...string) (string, error) {
	cmd := exec.Command("docker", args...)
	cmd.Stderr = os.Stderr
	output, err := cmd.Output()
	return strings.TrimSpace(string(output)), err
}

func retry(check func() error) error {
	var err error
	for range 60 {
		if err = check(); err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return err
}

type asset struct{ path, kind string }

func readAssets(source io.Reader) ([]asset, error) {
	var assets []asset
	var app, javascript, stylesheet bool
	tokens := html.NewTokenizer(source)
	for {
		switch tokens.Next() {
		case html.ErrorToken:
			if err := tokens.Err(); err != io.EOF {
				return nil, err
			}
			if !app || !javascript || !stylesheet {
				return nil, errors.New("UI is missing its app root, JavaScript, or stylesheet")
			}
			return assets, nil
		case html.StartTagToken, html.SelfClosingTagToken:
			token := tokens.Token()
			var src, href, rel string
			for _, attribute := range token.Attr {
				switch attribute.Key {
				case "id":
					app = app || attribute.Val == "app"
				case "src":
					src = attribute.Val
				case "href":
					href = attribute.Val
				case "rel":
					rel = attribute.Val
				}
			}
			local := func(path string) bool { return strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "//") }
			if token.Data == "script" && local(src) {
				javascript = true
				assets = append(assets, asset{src, "javascript"})
			}
			if token.Data == "link" && rel == "stylesheet" && local(href) {
				stylesheet = true
				assets = append(assets, asset{href, "text/css"})
			}
		}
	}
}

func get(client *http.Client, url, media string) (io.ReadCloser, error) {
	response, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	content := response.Header.Get("Content-Type")
	if response.StatusCode != http.StatusOK || !strings.Contains(content, media) {
		response.Body.Close()
		return nil, fmt.Errorf("%s: unexpected HTTP %s (%s)", url, response.Status, content)
	}
	return response.Body, nil
}

func check(image, version string) (err error) {
	name := "floatlab-image-check-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	database, management := name+"-db", name+"-manage"
	if _, err := docker("network", "create", name); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			command := exec.Command("docker", "logs", management)
			command.Stdout, command.Stderr = os.Stderr, os.Stderr
			_ = command.Run()
		}
		_ = exec.Command("docker", "rm", "--force", "--volumes", management, database).Run()
		_, cleanup := docker("network", "rm", name)
		err = errors.Join(err, cleanup)
	}()
	if _, err := docker("run", "--detach", "--name", database, "--network", name, "--network-alias", "rqlite",
		"rqlite/rqlite:8.26.0", "-http-addr=0.0.0.0:4001", "-raft-addr=0.0.0.0:4002"); err != nil {
		return err
	}
	if err := retry(func() error {
		return exec.Command("docker", "exec", database, "wget", "-qO-", "http://127.0.0.1:4001/readyz").Run()
	}); err != nil {
		return fmt.Errorf("smoke-test rqlite did not become ready: %w", err)
	}
	if _, err := docker("run", "--detach", "--name", management, "--network", name, "--publish", "127.0.0.1::8080",
		image, "--rqlite-url=http://rqlite:4001", "--raft-data=/tmp/raft", "--workload-db=/tmp/workloads/local.sqlite",
		"--lab-data=/tmp/workloads/lab", "--raft-bootstrap=true", "--jwt-secret=container-smoke-test"); err != nil {
		return err
	}
	address, err := docker("port", management, "8080/tcp")
	if err != nil {
		return err
	}
	base := "http://" + address
	var health struct{ Version string }
	healthClient := &http.Client{Timeout: 2 * time.Second}
	if err := retry(func() error {
		body, err := get(healthClient, base+"/api/v1/health", "")
		if err != nil {
			return err
		}
		defer body.Close()
		return json.NewDecoder(body).Decode(&health)
	}); err != nil {
		return fmt.Errorf("management container did not become healthy: %w", err)
	}
	if health.Version != version {
		return fmt.Errorf("management version %q, expected %q", health.Version, version)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	body, err := get(client, base+"/", "text/html")
	if err != nil {
		return err
	}
	assets, err := readAssets(body)
	body.Close()
	if err != nil {
		return err
	}
	for _, asset := range assets {
		body, err := get(client, base+asset.path, asset.kind)
		if err != nil {
			return err
		}
		size, err := io.Copy(io.Discard, body)
		body.Close()
		if err != nil || size == 0 {
			return fmt.Errorf("could not read asset %s (%d bytes): %v", asset.path, size, err)
		}
	}
	fmt.Printf("PASS: %s serves UI, %d JS/CSS assets and management %s\n", image, len(assets), version)
	return nil
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: check-manage-image IMAGE VERSION")
		os.Exit(2)
	}
	if err := check(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
