# Installer developer guide

Run the Bubble Tea installer locally with its mock service to develop the UI and
exercise installation flows. Mock mode uses simulated disks and progress, requires
no root privileges or ZFS tools, and exits when you choose Reboot.

## Requirements

Use a Linux development environment, Go 1.25.5 or newer, and an interactive
terminal. Run the commands below from the repository root, where `go.mod` lives. Go downloads dependencies on the first
build when they are not already cached.

## Run mock mode with Go

Build the mock service, then run the TUI directly from source:

```bash
go build -o /tmp/floatlab-install-service-mock ./cmd/install-service-mock
go run ./cmd/install-tui --mock --service /tmp/floatlab-install-service-mock
```

The TUI launches the service as a separate process. Pass `--service` explicitly
with `go run`, because Go places the temporary TUI executable in its own build
directory. The `--mock` flag forwards mock scenario and timing options to the
service.

After editing the TUI, run the second command again. After editing the installer
service, rebuild the mock service with the first command before rerunning the TUI.

## Choose a mock scenario

For example, reject the first configuration submission and run the simulation
faster:

```bash
go run ./cmd/install-tui --mock \
  --service /tmp/floatlab-install-service-mock \
  --scenario rejected-config --step-delay 100ms
```

| Scenario | Behavior |
| --- | --- |
| `success` | Completes after valid configuration is submitted. This is the default. |
| `delayed-config` | Adds a delay before the configuration phase. |
| `rejected-config` | Rejects the first configuration submission; allows correction and resubmission. |
| `failure` | Fails halfway through the simulated image copy. |
| `cancellation` | Reports simulated frontend loss and cleanup as a terminal failure. |

`--step-delay` controls the time between simulated progress updates. The success
screen still uses a 30-second reboot countdown. Mock pool dry-runs always succeed;
command parsing and host, network, and account validation still run.

## Walk through the UI

1. Press Enter to begin. Select disks with Space and move with the arrow keys.
   The installer creates one pool, `floatlab`; selecting multiple disks generates
   a mirror within that pool.
2. Edit the bordered ZFS command. Its three logical lines contain the pool name,
   options, and disk topology. Enter inserts a newline; Ctrl+S validates and opens
   the preview. Type `ERASE ALL DATA` to confirm the simulated disk layout.
3. Enter host settings. Open the timezone list with Enter and type to filter it.
   Select DHCP or Static with Left/Right or Space. Static address fields appear
   only for Static. Submit the network settings to validate them before account
   setup opens.
4. Set up the administrator account, review the configuration, and submit it.
   Optional TOTP enrollment includes an authenticator code and confirmation that
   the recovery codes have been saved.
5. Watch the six-line progress footer. On success, P or Space pauses the countdown;
   R or Enter reboots. Mock reboot exits the installer. Failures remain visible
   until you choose Reboot.

Tab and Shift+Tab move between form fields. Alt+Up/Down scroll the main area.
The installer has no quit action; Ctrl+C and Ctrl+D are ignored by the TUI.

## Build standalone executables

Build both executables into the same directory so the TUI can find its mock service
without `--service`:

```bash
go build -o /tmp/floatlab-installer/ ./cmd/install-tui ./cmd/install-service-mock
/tmp/floatlab-installer/install-tui --mock
```

## Run development checks

```bash
go test -race ./internal/installer ./internal/installtui
go vet ./internal/installer ./internal/installtui ./cmd/install-tui ./cmd/install-service ./cmd/install-service-mock
go build ./cmd/install-tui ./cmd/install-service ./cmd/install-service-mock
```

The tests cover command parsing and disk mapping, configuration validation,
service lifecycle, UI choices, progress, and reboot behavior.

## Find the implementation

| Location | Responsibility |
| --- | --- |
| `internal/installtui` | Screens, keyboard handling, timezone choices, and progress display. |
| `internal/installer/service.go` | Stdio JSON protocol, validation, session state, and mock scenarios. |
| `internal/installer/storage.go` | Disk discovery, partition layout, and ZFS command parsing. |
| `internal/installer/worker.go` | Real installation, verified image copying, and configuration. |

For ISO builds and tests against disposable VM disks, see the
[appliance README](README.md#build-the-iso). Changes to dependency versions or imported dependency packages require
updating `vendorHash` in `floatlab-appliance-image/flake.nix` before building the ISO.
