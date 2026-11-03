package integrationvm

import (
	"context"
	"encoding/xml"
	"errors"
	"net"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
)

func TestDomainSerialLogPreservesConsoleAndEscapesPath(t *testing.T) {
	path := "/tmp/owned-run/serial&console.log"
	var domain struct {
		Serial struct {
			Type string `xml:"type,attr"`
			Log  struct {
				File   string `xml:"file,attr"`
				Append string `xml:"append,attr"`
			} `xml:"log"`
		} `xml:"devices>serial"`
		Console struct {
			Type string `xml:"type,attr"`
		} `xml:"devices>console"`
	}
	if err := xml.Unmarshal([]byte(domainXML("guest", "network", "image.iso", "disk.qcow2", "answers.iso", path)), &domain); err != nil {
		t.Fatal(err)
	}
	if domain.Serial.Type != "pty" || domain.Console.Type != "pty" || domain.Serial.Log.File != path || domain.Serial.Log.Append != "on" {
		t.Fatalf("serial console logging lost: %+v", domain)
	}
}

func TestCleanupIgnoresOnlyAlreadyMissingResources(t *testing.T) {
	if err := missing(libvirt.Error{Code: uint32(libvirt.ErrNoDomain)}, libvirt.ErrNoDomain); err != nil {
		t.Fatal(err)
	}
	denied := libvirt.Error{Code: uint32(libvirt.ErrAccessDenied)}
	if err := missing(denied, libvirt.ErrNoDomain); !errors.Is(err, denied) {
		t.Fatalf("discarded authorization failure: %v", err)
	}
}

func TestSubnetAvoidsOverlappingRoutesAndInactiveNetworks(t *testing.T) {
	prefix, err := availableSubnet([]netip.Prefix{netip.MustParsePrefix("198.19.0.0/23"), netip.MustParsePrefix("198.19.2.128/25")})
	if err != nil || prefix.String() != "198.19.3.0/24" {
		t.Fatalf("subnet=%s error=%v", prefix, err)
	}
	if _, err := availableSubnet([]netip.Prefix{netip.MustParsePrefix("198.19.0.0/16")}); err == nil {
		t.Fatal("accepted exhausted subnet range")
	}
}

func TestRPCConnectionCancellationClosesUnresponsiveSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "libvirt.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	closed := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buffer := make([]byte, 4096)
		for {
			if _, err := conn.Read(buffer); err != nil {
				close(closed)
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, err := connect(ctx, path); err == nil {
		t.Fatal("unresponsive RPC connection unexpectedly succeeded")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("cancellation leaked the RPC socket")
	}
}

func TestCloseWithoutOwnedResourcesDoesNotConnect(t *testing.T) {
	run := &Run{Socket: "/does/not/exist"}
	if err := run.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
