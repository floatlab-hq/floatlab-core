package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/floatlab/floatlab-core/internal/credentials"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/crypto/bcrypt"
)

// Answers contains password hashes. Keep it private and out of diagnostics.
type Answers struct {
	Version      int           `toml:"version"`
	Command      string        `toml:"command"`
	Confirmation string        `toml:"confirmation"`
	Config       InstallConfig `toml:"config"`
}

func NewAnswers(command string, config InstallConfig) (Answers, error) {
	if err := validateAccount(config); err != nil {
		return Answers{}, err
	}
	if config.PasswordHash == "" {
		password, err := bcrypt.GenerateFromPassword([]byte(config.Password), bcrypt.DefaultCost)
		if err != nil {
			return Answers{}, err
		}
		recovery, err := bcrypt.GenerateFromPassword([]byte(config.RecoveryPassword), bcrypt.DefaultCost)
		if err != nil {
			return Answers{}, err
		}
		config.PasswordHash, config.RecoveryPasswordHash = string(password), string(recovery)
		config.Password, config.PasswordConfirmation, config.RecoveryPassword, config.RecoveryConfirmation = "", "", "", ""
	}
	answers := Answers{Version: 1, Command: command, Confirmation: "ERASE ALL DATA", Config: config}
	return answers, answers.Validate()
}

func (a Answers) Validate() error {
	if a.Version != 1 {
		return fmt.Errorf("unsupported answers version; expected 1")
	}
	if strings.TrimSpace(a.Command) == "" || a.Confirmation != "ERASE ALL DATA" {
		return fmt.Errorf("answers require a zpool command naming exact disks and confirmation ERASE ALL DATA")
	}
	if a.Config.TOTP || a.Config.TOTPCode != "" || a.Config.RecoverySaved {
		return fmt.Errorf("answers cannot reuse authenticator enrollment; use the interactive installer for TOTP")
	}
	if a.Config.PasswordHash == "" || a.Config.RecoveryPasswordHash == "" {
		return fmt.Errorf("answers require administrator and recovery bcrypt password hashes")
	}
	if err := (Appliance{NodeName: a.Config.NodeName, Timezone: a.Config.Timezone, Network: a.Config.Network, EnableLab: a.Config.EnableLab}).validateSettings(); err != nil {
		return err
	}
	return validateAccount(a.Config)
}

func ReadAnswers(input io.Reader) (Answers, error) {
	var answers Answers
	data, err := io.ReadAll(io.LimitReader(input, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return answers, fmt.Errorf("answers must be a TOML file no larger than 1 MiB")
	}
	decoder := toml.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&answers); err != nil {
		return answers, fmt.Errorf("invalid answers TOML or unknown field")
	}
	return answers, answers.Validate()
}

func WriteAnswers(path string, answers Answers) error {
	if err := answers.Validate(); err != nil {
		return err
	}
	data, err := toml.Marshal(answers)
	if err != nil {
		return err
	}
	return credentials.WriteBytes(path, data)
}

// InstallAnswers drives the same preview, confirmation and worker as the TUI.
func (s *Service) InstallAnswers(parent context.Context, answers Answers, reboot bool, output io.Writer) error {
	if err := answers.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	input, requests := io.Pipe()
	events, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := s.Serve(ctx, input, writer)
		writer.CloseWithError(err)
		input.CloseWithError(err)
		done <- err
	}()
	defer func() {
		cancel()
		requests.Close()
		events.Close()
		<-done
	}()
	decoder, encoder := json.NewDecoder(events), json.NewEncoder(requests)
	progress := json.NewEncoder(output)
	for {
		var event Event
		if err := decoder.Decode(&event); err != nil {
			return fmt.Errorf("installer stopped before completion: %w", err)
		}
		if err := progress.Encode(event); err != nil {
			return err
		}
		var request Request
		switch event.Type {
		case "inventory":
			request = Request{Type: "preview", Command: answers.Command}
		case "preview":
			request = Request{Type: "confirm", Command: event.Command, Confirmation: answers.Confirmation}
		case "confirm":
			request = Request{Type: "configure", Config: &answers.Config}
		case "complete":
			if !reboot {
				return nil
			}
			request = Request{Type: "reboot"}
		case "reboot":
			return nil
		case "error", "fatal":
			return fmt.Errorf("unattended installation: %s", event.Error)
		case "failed":
			return fmt.Errorf("unattended installation: %s", event.Status.Error)
		default:
			continue
		}
		if err := encoder.Encode(request); err != nil {
			return err
		}
	}
}
