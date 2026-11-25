//go:build linux || darwin || freebsd

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/containerd/console"
	"github.com/floatlab/floatlab-core/internal/credentials"
)

var apiClient = http.Client{Timeout: 10 * time.Second}

type apiStatus int

func (s apiStatus) Error() string { return fmt.Sprintf("HTTP %d", s) }

func (s scenario) api(path, token string, body, result any) error {
	var request *http.Request
	var err error
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		request, err = http.NewRequestWithContext(s.ctx, "POST", s.base+"/api/v1"+path, bytes.NewReader(data))
		if err != nil {
			return err
		}
	} else {
		request, err = http.NewRequest("GET", s.base+"/api/v1"+path, nil)
		if err != nil {
			return err
		}
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := apiClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return apiStatus(response.StatusCode)
	}
	if result == nil {
		_, err := io.Copy(io.Discard, response.Body)
		return err
	}
	return json.NewDecoder(response.Body).Decode(result)
}

func (s scenario) login(factor map[string]string) (string, error) {
	body := map[string]string{"username": username, "password": password}
	for k, v := range factor {
		body[k] = v
	}
	var session struct {
		Token string `json:"access_token"`
	}
	err := s.api("/auth/token", "", body, &session)
	if err == nil && session.Token == "" {
		err = errors.New("login response has no access token")
	}
	return session.Token, err
}

func (s scenario) rejectLogin(factor map[string]string) error {
	_, err := s.login(factor)
	if err == apiStatus(http.StatusUnauthorized) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("expected login rejection: %w", err)
	}
	return errors.New("replayed second factor accepted")
}

type applianceNode struct{ ID, Name, Hostname string }

func (s scenario) waitStack(vm *terminal, id, secret string) (string, int64, error) {
	deadline := time.Now().Add(15 * time.Minute)
	var lastStep int64
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-vm.done:
			return "", 0, fmt.Errorf("VM exited before the installed stack became ready: %v", vm.exit)
		default:
		}
		lastErr = s.api("/health", "", nil, nil)
		step := time.Now().Unix() / 30
		if lastErr == nil && (secret == "" || step != lastStep) {
			lastStep = step
			var factor map[string]string
			if secret != "" {
				code, err := credentials.Code(secret, step)
				if err != nil {
					return "", 0, err
				}
				factor = map[string]string{"totp_code": code}
			}
			token, err := s.login(factor)
			lastErr = err
			if err == nil {
				var nodes []applianceNode
				lastErr = s.api("/nodes", token, nil, &nodes)
				if lastErr == nil {
					lastErr = fmt.Errorf("node %s not yet registered", s.name)
					for _, node := range nodes {
						if node.Name != s.name {
							continue
						}
						if node.Hostname != s.name || (id != "" && node.ID != id) {
							return "", 0, fmt.Errorf("unexpected appliance identity: %+v", node)
						}
						var health struct {
							Status        string
							Compatibility struct {
								HostVersion string `json:"host_version"`
								Status      string
							}
						}
						lastErr = s.api("/nodes/"+node.ID+"/health", token, nil, &health)
						if lastErr != nil {
							break
						}
						if health.Status != "reachable" || health.Compatibility.HostVersion == "" ||
							(health.Compatibility.Status != "compatible" && health.Compatibility.Status != "incompatible") {
							return "", 0, fmt.Errorf("invalid host health: %+v", health)
						}
						return node.ID, step, nil
					}
				}
			}
		}
		time.Sleep(time.Second)
	}
	return "", 0, fmt.Errorf("installed stack did not become healthy at %s: %v", s.base, lastErr)
}

func (s scenario) cliLogin(factor string) error {
	directory := filepath.Join(s.directory, "cli-config")
	executable, err := filepath.Abs(s.cli)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(s.ctx, executable)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "FLOATLAB_URL", "FLOATLAB_TOKEN", "XDG_CONFIG_HOME":
		default:
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "XDG_CONFIG_HOME="+directory)
	client, err := startTerminal(cmd, console.WinSize{Height: 24, Width: 80}, nil)
	if err != nil {
		return err
	}
	defer client.close()
	const timeout = time.Minute
	for _, entry := range []struct{ prompt, value string }{{"FloatLab URL", s.base}, {"Username", username}, {"Password", password}} {
		if err := client.reply(entry.prompt, entry.value+"\n", timeout); err != nil {
			return err
		}
	}
	if factor != "" {
		if err := client.reply("Second factor", down+"\n", timeout); err != nil {
			return err
		}
		if err := client.reply("Single-use recovery code", factor+"\n", timeout); err != nil {
			return err
		}
	}
	if err := client.reply(`\? floatlab`, "exit\n", timeout); err != nil {
		return err
	}
	if err := client.wait(timeout); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(directory, "floatlab/config.json"))
	if err != nil {
		return err
	}
	var config struct{ Token string }
	if err := json.Unmarshal(data, &config); err != nil {
		return err
	}
	var nodes []applianceNode
	if err := s.api("/nodes", config.Token, nil, &nodes); err != nil {
		return err
	}
	if len(nodes) == 0 {
		return errors.New("CLI token did not list the installed node")
	}
	return nil
}

func (s scenario) repeatBootstrap(vm *terminal, node installedNode) error {
	values := url.Values{"q": {`SELECT password_hash,totp_secret FROM users JOIN user_credentials USING(username) WHERE username='installer-test'`}}
	query := "cd /floatlab/system; docker compose exec -T rqlite wget -qO- " + quote("http://127.0.0.1:4001/db/query?"+values.Encode())
	credentialsRow := func() ([]string, error) {
		data, err := s.guest(query)
		if err != nil {
			return nil, err
		}
		var response struct{ Results []struct{ Values [][]string } }
		if err := json.Unmarshal([]byte(data), &response); err != nil {
			return nil, err
		}
		if len(response.Results) != 1 || len(response.Results[0].Values) != 1 || len(response.Results[0].Values[0]) != 2 {
			return nil, errors.New("missing stored administrator credentials")
		}
		return response.Results[0].Values[0], nil
	}
	original, err := credentialsRow()
	if err != nil {
		return err
	}
	hash := original[0]
	if len(hash) <= 29 {
		return errors.New("stored password hash is too short")
	}
	changed := "A"
	if hash[29] == 'A' {
		changed = "B"
	}
	bootstrap, err := json.Marshal(map[string]any{"username": username, "node_id": node.id, "node_name": s.name,
		"password_hash": hash[:29] + changed + hash[30:], "totp_secret": "", "recovery_hashes": []string{credentials.RecoveryHash(node.recovery[0])}})
	if err != nil {
		return err
	}
	if _, err := s.guest("umask 077; printf '%s\\n' " + quote(string(bootstrap)) +
		" > /floatlab/system/etc/bootstrap.json; cd /floatlab/system; docker compose restart floatlab-control"); err != nil {
		return err
	}
	if err := s.waitGuest(vm, "test ! -e /floatlab/system/etc/bootstrap.json"); err != nil {
		return err
	}
	actual, err := credentialsRow()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(original, actual) {
		return errors.New("repeated bootstrap changed stored administrator credentials")
	}
	if _, _, err := s.waitStack(vm, node.id, node.secret); err != nil {
		return err
	}
	return s.rejectLogin(map[string]string{"recovery_code": node.recovery[0]})
}
