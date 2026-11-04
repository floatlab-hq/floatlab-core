// Package integrationvm owns disposable appliance resources for integration tests.
package integrationvm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
	"github.com/google/uuid"
	"github.com/vishvananda/netlink"
)

const MemoryMiB = 3584

type Run struct {
	Name, Directory, Gateway string
	Network                  libvirt.Network
	pool                     libvirt.StoragePool
	volumes                  []libvirt.StorageVol
	Guests                   []*Guest
	Socket                   string
}

type Guest struct {
	Run           *Run
	Domain        libvirt.Domain
	Name          string
	SerialLogPath string
}

func connect(ctx context.Context, socket string) (*libvirt.Libvirt, func(), error) {
	if socket == "" {
		socket = "/var/run/libvirt/libvirt-sock"
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	l := libvirt.New(conn)
	if err := l.Connect(); err != nil {
		stop()
		conn.Close()
		return nil, nil, err
	}
	return l, func() { stop(); conn.Close() }, nil
}

// Open records ownership as resources are created, so Close also works after partial setup.
func (r *Run) Open(ctx context.Context) error {
	dir, err := os.MkdirTemp("/tmp", "floatlab-integration-")
	if err != nil {
		return err
	}
	r.Directory = dir
	r.Name = filepath.Base(dir)
	if err := os.Chmod(dir, 0755); err != nil {
		return err
	}
	l, close, err := connect(ctx, r.Socket)
	if err != nil {
		return err
	}
	defer close()
	used, err := usedPrefixes(l)
	if err != nil {
		return err
	}
	prefix, err := availableSubnet(used)
	if err != nil {
		return err
	}
	r.Gateway = prefix.Addr().Next().String()
	// DHCP ends below the static appliance and workload ranges.
	last := prefix.Addr().As4()
	last[3] = 99
	network := fmt.Sprintf(`<network><name>%s</name><forward mode="nat"/><ip address="%s" prefix="24"><dhcp><range start="%s" end="%s"/></dhcp></ip></network>`, r.Name, r.Gateway, prefix.Addr().Next().Next(), netip.AddrFrom4(last))
	r.Network = libvirt.Network{Name: r.Name, UUID: libvirt.UUID(uuid.New())}
	network = strings.Replace(network, "<network>", "<network><uuid>"+uuid.UUID(r.Network.UUID).String()+"</uuid>", 1)
	_, err = l.NetworkCreateXML(network)
	if err != nil {
		return err
	}
	r.pool = libvirt.StoragePool{Name: r.Name, UUID: libvirt.UUID(uuid.New())}
	_, err = l.StoragePoolCreateXML(fmt.Sprintf(`<pool type="dir"><name>%s</name><uuid>%s</uuid><target><path>%s</path></target></pool>`, r.Name, uuid.UUID(r.pool.UUID).String(), escape(dir)), 0)
	return err
}

func usedPrefixes(l *libvirt.Libvirt) ([]netip.Prefix, error) {
	var used []netip.Prefix
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return nil, err
	}
	for _, route := range routes {
		if route.Dst != nil && route.Dst.String() != "0.0.0.0/0" {
			p, err := netip.ParsePrefix(route.Dst.String())
			if err != nil {
				return nil, err
			}
			used = append(used, p)
		}
	}
	nets, _, err := l.ConnectListAllNetworks(1, 0)
	if err != nil {
		return nil, err
	}
	for _, n := range nets {
		raw, err := l.NetworkGetXMLDesc(n, 0)
		if err != nil {
			return nil, err
		}
		var doc struct {
			IPs []struct {
				Address string `xml:"address,attr"`
				Prefix  int    `xml:"prefix,attr"`
				Netmask string `xml:"netmask,attr"`
			} `xml:"ip"`
		}
		if err := xml.Unmarshal([]byte(raw), &doc); err != nil {
			return nil, err
		}
		for _, ip := range doc.IPs {
			address, err := netip.ParseAddr(ip.Address)
			if err != nil || !address.Is4() {
				continue
			}
			bits := ip.Prefix
			if ip.Netmask != "" {
				mask := net.ParseIP(ip.Netmask).To4()
				if mask == nil {
					return nil, fmt.Errorf("invalid network mask %q", ip.Netmask)
				}
				bits, _ = net.IPMask(mask).Size()
			}
			used = append(used, netip.PrefixFrom(address, bits).Masked())
		}
	}
	return used, nil
}

func availableSubnet(used []netip.Prefix) (netip.Prefix, error) {
	for octet := 1; octet < 255; octet++ {
		candidate := netip.MustParsePrefix(fmt.Sprintf("198.19.%d.0/24", octet))
		free := true
		for _, old := range used {
			if candidate.Overlaps(old) {
				free = false
				break
			}
		}
		if free {
			return candidate, nil
		}
	}
	return netip.Prefix{}, errors.New("no unused integration subnet in 198.19.0.0/16")
}

func escape(value string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}

func (r *Run) Boot(ctx context.Context, iso, answersISO string, diskGiB uint64) (*Guest, error) {
	l, close, err := connect(ctx, r.Socket)
	if err != nil {
		return nil, err
	}
	defer close()
	name := fmt.Sprintf("%s-%d", r.Name, len(r.Guests)+1)
	g := &Guest{Run: r, Name: name, Domain: libvirt.Domain{Name: name, UUID: libvirt.UUID(uuid.New()), ID: -1}}
	r.Guests = append(r.Guests, g)
	g.SerialLogPath = filepath.Join(r.Directory, name+"-serial.log")
	log, err := os.OpenFile(g.SerialLogPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	// Retain host read access after libvirt relabels ownership to QEMU.
	err = log.Chmod(0644)
	if err := errors.Join(err, log.Close()); err != nil {
		return nil, err
	}
	create := func(name, format string, size uint64, source string) (string, error) {
		vol, err := l.StorageVolCreateXML(r.pool, fmt.Sprintf(`<volume><name>%s</name><capacity unit="bytes">%d</capacity><allocation>0</allocation><target><format type="%s"/></target></volume>`, name, size, format), 0)
		if err != nil {
			return "", err
		}
		r.volumes = append(r.volumes, vol)
		if source != "" {
			f, err := os.Open(source)
			if err != nil {
				return "", err
			}
			defer f.Close()
			if err := l.StorageVolUpload(vol, f, 0, size, 0); err != nil {
				return "", err
			}
		}
		return l.StorageVolGetPath(vol)
	}
	disk, err := create(name+".qcow2", "qcow2", diskGiB<<30, "")
	if err != nil {
		return nil, err
	}
	stat, err := os.Stat(answersISO)
	if err != nil {
		return nil, err
	}
	answers, err := create(name+"-answers.iso", "raw", uint64(stat.Size()), answersISO)
	if err != nil {
		return nil, err
	}
	xml := strings.Replace(domainXML(name, r.Name, iso, disk, answers, g.SerialLogPath), "<domain type=\"kvm\">", "<domain type=\"kvm\"><uuid>"+uuid.UUID(g.Domain.UUID).String()+"</uuid>", 1)
	domain, err := l.DomainDefineXML(xml)
	if err != nil {
		return nil, err
	}
	g.Domain = domain
	if err := l.DomainCreate(domain); err != nil {
		return nil, err
	}
	return g, nil
}

func domainXML(name, network, iso, disk, answers, serialLog string) string {
	return fmt.Sprintf(`<domain type="kvm"><name>%s</name><memory unit="MiB">%d</memory><currentMemory unit="MiB">%d</currentMemory><vcpu>4</vcpu><cpu mode="host-passthrough"/><os firmware="efi"><type arch="x86_64" machine="q35">hvm</type><firmware><feature enabled="no" name="secure-boot"/></firmware></os><features><acpi/><apic/></features><devices><disk type="file" device="disk"><driver name="qemu" type="qcow2"/><source file="%s"/><target dev="vda" bus="virtio"/><serial>floatlab-zfs</serial><boot order="1"/></disk><disk type="file" device="cdrom"><driver name="qemu" type="raw"/><source file="%s"/><target dev="sda" bus="sata"/><readonly/><boot order="2"/></disk><disk type="file" device="cdrom"><driver name="qemu" type="raw"/><source file="%s"/><target dev="sdb" bus="sata"/><readonly/></disk><interface type="network"><source network="%s"/><model type="virtio"/></interface><channel type="unix"><target type="virtio" name="org.qemu.guest_agent.0"/></channel><serial type="pty"><log file="%s" append="on"/></serial><console type="pty"/></devices></domain>`, escape(name), MemoryMiB, MemoryMiB, escape(disk), escape(iso), escape(answers), escape(network), escape(serialLog))
}

func (g *Guest) Agent(ctx context.Context, operation string, args any, result any) error {
	l, close, err := connect(ctx, g.Run.Socket)
	if err != nil {
		return err
	}
	defer close()
	raw, err := json.Marshal(map[string]any{"execute": operation, "arguments": args})
	if err != nil {
		return err
	}
	response, err := l.QEMUDomainAgentCommand(g.Domain, string(raw), 15, 0)
	if err != nil {
		return err
	}
	if len(response) != 1 {
		return errors.New("empty guest-agent response")
	}
	var envelope struct {
		Return json.RawMessage `json:"return"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(response[0]), &envelope); err != nil {
		return err
	}
	if len(envelope.Error) > 0 {
		return fmt.Errorf("guest-agent %s: %s", operation, envelope.Error)
	}
	if result != nil {
		return json.Unmarshal(envelope.Return, result)
	}
	return nil
}

func (g *Guest) Exec(ctx context.Context, script string) (string, error) {
	var process struct {
		PID int `json:"pid"`
	}
	script = "export PATH=/run/current-system/sw/bin:/run/wrappers/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:$PATH; " + script
	if err := g.Agent(ctx, "guest-exec", map[string]any{"path": "/bin/sh", "arg": []string{"-c", script}, "capture-output": true}, &process); err != nil {
		return "", err
	}
	if process.PID == 0 {
		return "", errors.New("guest-exec returned no PID")
	}
	for {
		var status struct {
			Exited       bool   `json:"exited"`
			ExitCode     int    `json:"exitcode"`
			Signal       int    `json:"signal"`
			Out          string `json:"out-data"`
			Err          string `json:"err-data"`
			Truncated    bool   `json:"out-truncated"`
			ErrTruncated bool   `json:"err-truncated"`
		}
		if err := g.Agent(ctx, "guest-exec-status", map[string]int{"pid": process.PID}, &status); err != nil {
			return "", err
		}
		if status.Exited {
			stdout, err := base64.StdEncoding.DecodeString(status.Out)
			if err != nil {
				return "", err
			}
			stderr, err := base64.StdEncoding.DecodeString(status.Err)
			if err != nil {
				return "", err
			}
			if status.Truncated || status.ErrTruncated {
				return string(stdout), errors.New("guest output truncated")
			}
			if status.ExitCode != 0 || status.Signal != 0 {
				return string(stdout), fmt.Errorf("guest command exited %d signal %d: %s", status.ExitCode, status.Signal, stderr)
			}
			return string(stdout), nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (g *Guest) Upload(ctx context.Context, source, target string) (err error) {
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := g.Exec(ctx, "mkdir -p "+quote(filepath.Dir(target))); err != nil {
		return err
	}
	var handle int
	if err := g.Agent(ctx, "guest-file-open", map[string]any{"path": target + ".upload", "mode": "w"}, &handle); err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, g.Agent(ctx, "guest-file-close", map[string]int{"handle": handle}, nil))
	}()
	buffer := make([]byte, 32<<10)
	for {
		n, readErr := f.Read(buffer)
		if n > 0 {
			var written struct {
				Count int `json:"count"`
			}
			if err := g.Agent(ctx, "guest-file-write", map[string]any{"handle": handle, "buf-b64": base64.StdEncoding.EncodeToString(buffer[:n])}, &written); err != nil {
				return err
			}
			if written.Count != n {
				return io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	_, err = g.Exec(ctx, "chmod 0700 "+quote(target+".upload")+"; mv -f "+quote(target+".upload")+" "+quote(target))
	return err
}

func (g *Guest) Download(ctx context.Context, source, target string) (err error) {
	var handle int
	if err := g.Agent(ctx, "guest-file-open", map[string]any{"path": source, "mode": "r"}, &handle); err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, g.Agent(ctx, "guest-file-close", map[string]int{"handle": handle}, nil))
	}()
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	for {
		var chunk struct {
			Data  string `json:"buf-b64"`
			EOF   bool   `json:"eof"`
			Count int    `json:"count"`
		}
		if err := g.Agent(ctx, "guest-file-read", map[string]int{"handle": handle, "count": 32 << 10}, &chunk); err != nil {
			return err
		}
		data, err := base64.StdEncoding.DecodeString(chunk.Data)
		if err != nil {
			return err
		}
		if len(data) != chunk.Count {
			return io.ErrUnexpectedEOF
		}
		if _, err := f.Write(data); err != nil {
			return err
		}
		if chunk.EOF {
			return nil
		}
		if chunk.Count == 0 {
			return io.ErrNoProgress
		}
	}
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func (g *Guest) Link(ctx context.Context, up bool) error {
	l, close, err := connect(ctx, g.Run.Socket)
	if err != nil {
		return err
	}
	defer close()
	raw, err := l.DomainGetXMLDesc(g.Domain, 0)
	if err != nil {
		return err
	}
	// Decode the interface itself to retain libvirt's assigned MAC and target.
	var domain struct {
		Interfaces []struct {
			XMLName xml.Name `xml:"interface"`
			Type    string   `xml:"type,attr"`
			MAC     struct {
				Address string `xml:"address,attr"`
			} `xml:"mac"`
			Source struct {
				Network string `xml:"network,attr"`
			} `xml:"source"`
			Model struct {
				Type string `xml:"type,attr"`
			} `xml:"model"`
			Link struct {
				State string `xml:"state,attr"`
			} `xml:"link"`
		} `xml:"devices>interface"`
	}
	if err := xml.Unmarshal([]byte(raw), &domain); err != nil {
		return err
	}
	for _, device := range domain.Interfaces {
		if device.Source.Network != g.Run.Name {
			continue
		}
		device.Link.State = "down"
		if up {
			device.Link.State = "up"
		}
		data, err := xml.Marshal(device)
		if err != nil {
			return err
		}
		return l.DomainUpdateDeviceFlags(g.Domain, string(data), libvirt.DomainDeviceModifyLive)
	}
	return errors.New("owned network interface missing")
}

func (g *Guest) PowerCycle(ctx context.Context) error {
	l, close, err := connect(ctx, g.Run.Socket)
	if err != nil {
		return err
	}
	defer close()
	if err := l.DomainDestroy(g.Domain); err != nil {
		return err
	}
	return l.DomainCreate(g.Domain)
}

func (r *Run) Close(ctx context.Context) error {
	if r.Directory == "" {
		return nil
	}
	if r.pool.Name == "" && r.Network.Name == "" && len(r.Guests) == 0 && len(r.volumes) == 0 {
		return os.RemoveAll(r.Directory)
	}
	l, close, err := connect(ctx, r.Socket)
	if err != nil {
		return err
	}
	defer close()
	var errs []error
	safeToDelete := true
	for i := len(r.Guests) - 1; i >= 0; i-- {
		g := r.Guests[i]
		active, e := l.DomainIsActive(g.Domain)
		if e != nil && missing(e, libvirt.ErrNoDomain) == nil {
			continue
		}
		if e != nil {
			errs = append(errs, e)
			safeToDelete = false
			continue
		}
		if active != 0 {
			if e := l.DomainDestroy(g.Domain); e != nil {
				errs = append(errs, e)
				safeToDelete = false
				continue
			}
		}
		errs = append(errs, missing(l.DomainUndefineFlags(g.Domain, libvirt.DomainUndefineNvram), libvirt.ErrNoDomain))
	}
	if !safeToDelete {
		return errors.Join(errs...)
	}
	volumes := r.volumes
	if r.pool.Name != "" {
		owned, _, err := l.StoragePoolListAllVolumes(r.pool, 1, 0)
		if err == nil {
			volumes = owned
		} else {
			errs = append(errs, missing(err, libvirt.ErrNoStoragePool))
		}
	}
	for i := len(volumes) - 1; i >= 0; i-- {
		errs = append(errs, missing(l.StorageVolDelete(volumes[i], 0), libvirt.ErrNoStorageVol))
	}
	if r.pool.Name != "" {
		errs = append(errs, missing(l.StoragePoolDestroy(r.pool), libvirt.ErrNoStoragePool))
	}
	if r.Network.Name != "" {
		errs = append(errs, missing(l.NetworkDestroy(r.Network), libvirt.ErrNoNetwork))
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	return os.RemoveAll(r.Directory)
}

func missing(err error, code libvirt.ErrorNumber) error {
	var rpc libvirt.Error
	if errors.As(err, &rpc) && rpc.Code == uint32(code) {
		return nil
	}
	return err
}
