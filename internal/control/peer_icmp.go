package control

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

const peerICMPPackets = 3

type peerICMP struct {
	replies int
	rtt     time.Duration
}

func (result peerICMP) lossPercent() float64 {
	return 100 * float64(peerICMPPackets-result.replies) / peerICMPPackets
}

func probePeerICMP(ctx context.Context, hostname string) (*peerICMP, error) {
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, hostname)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, &net.DNSError{Err: "no addresses", Name: hostname}
	}
	// shortcut: sample the preferred address, probe each address if lab peers use multi-address DNS.
	address := addresses[0]
	udp, raw, local, protocol := "udp6", "ip6:ipv6-icmp", "::", 58
	var requestType, replyType icmp.Type = ipv6.ICMPTypeEchoRequest, ipv6.ICMPTypeEchoReply
	if address.IP.To4() != nil {
		udp, raw, local, protocol = "udp4", "ip4:icmp", "0.0.0.0", 1
		requestType, replyType = ipv4.ICMPTypeEcho, ipv4.ICMPTypeEchoReply
	}
	var destination net.Addr = &net.UDPAddr{IP: address.IP, Zone: address.Zone}
	conn, err := icmp.ListenPacket(udp, local)
	if err != nil {
		conn, err = icmp.ListenPacket(raw, local)
		if err != nil {
			return nil, err
		}
		destination = &address
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	var result peerICMP
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	buffer := make([]byte, 1500)
	for seq := 1; seq <= peerICMPPackets; seq++ {
		packet, err := (&icmp.Message{Type: requestType, Body: &icmp.Echo{Seq: seq, Data: nonce}}).Marshal(nil)
		if err != nil {
			return nil, err
		}
		started := time.Now()
		if err := conn.SetDeadline(started.Add(750 * time.Millisecond)); err != nil {
			return nil, err
		}
		if _, err := conn.WriteTo(packet, destination); err != nil {
			if errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
				continue
			}
			return nil, err
		}
		for {
			n, source, err := conn.ReadFrom(buffer)
			if err != nil {
				var timeout net.Error
				if errors.As(err, &timeout) && timeout.Timeout() {
					break
				}
				return nil, err
			}
			message, err := icmp.ParseMessage(protocol, buffer[:n])
			if err != nil || message.Type != replyType || source.String() != destination.String() {
				continue
			}
			if echo, ok := message.Body.(*icmp.Echo); ok && echo.Seq == seq && bytes.Equal(echo.Data, nonce) {
				result.replies++
				result.rtt += time.Since(started)
				break
			}
		}
	}
	if result.replies != 0 {
		result.rtt /= time.Duration(result.replies)
	}
	return &result, nil
}

func peerNetworkFailure(err error) bool {
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return false
	}
	var network net.Error
	return errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		(errors.As(err, &network) && network.Timeout())
}
