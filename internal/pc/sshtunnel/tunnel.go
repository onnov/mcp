// Package sshtunnel forwards an SSH server's loopback port to a local HTTP
// server. No shell, SSH agent, subprocess or remote command is used.
package sshtunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/onnov/mcp/internal/pc/netproxy"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type Config struct {
	Addr, User, KeyFile, KeyPassphrase, KnownHosts, RemoteAddr string
}
type Tunnel struct {
	cfg                 Config
	ssh                 *ssh.ClientConfig
	dial                func(context.Context, string, string) (net.Conn, error)
	retry, keepInterval time.Duration
}

func New(c Config, proxy *url.URL) (*Tunnel, error) {
	info, err := os.Stat(c.KeyFile)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, errors.New("SSH key must be an existing regular file <=64 KiB")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("SSH private key permissions must be 0600 or stricter")
	}
	data, err := os.ReadFile(c.KeyFile)
	if err != nil {
		return nil, errors.New("cannot read SSH private key")
	}
	var signer ssh.Signer
	if c.KeyPassphrase == "" {
		signer, err = ssh.ParsePrivateKey(data)
	} else {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(data, []byte(c.KeyPassphrase))
	}
	for i := range data {
		data[i] = 0
	}
	if err != nil {
		return nil, errors.New("cannot parse SSH private key; check key format and PC_MCP_SSH_KEY_PASSPHRASE")
	}
	hostInfo, err := os.Stat(c.KnownHosts)
	if err != nil || !hostInfo.Mode().IsRegular() || hostInfo.Size() > 1<<20 {
		return nil, errors.New("SSH known_hosts must be an existing regular file <=1 MiB")
	}
	if runtime.GOOS != "windows" && hostInfo.Mode().Perm()&0022 != 0 {
		return nil, errors.New("SSH known_hosts must not be group/world writable")
	}
	verify, err := knownhosts.New(c.KnownHosts)
	if err != nil {
		return nil, fmt.Errorf("cannot load SSH known_hosts: %w", err)
	}
	dial, err := netproxy.DialContext(proxy)
	if err != nil {
		return nil, err
	}
	return &Tunnel{cfg: c, ssh: &ssh.ClientConfig{User: c.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: verify, Timeout: 20 * time.Second}, dial: dial, retry: time.Second, keepInterval: 15 * time.Second}, nil
}

func (t *Tunnel) Run(ctx context.Context, localAddr string) error {
	delay := t.retry
	for ctx.Err() == nil {
		started := time.Now()
		err := t.session(ctx, localAddr)
		if ctx.Err() != nil {
			return nil
		}
		log.Printf("pc-mcp: SSH forwarding disconnected (%v); retry in %s", err, delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if time.Since(started) > time.Minute {
			delay = t.retry
		} else if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
	return nil
}

func (t *Tunnel) session(ctx context.Context, localAddr string) error {
	dialCtx, cancelDial := context.WithTimeout(ctx, 20*time.Second)
	conn, err := t.dial(dialCtx, "tcp", t.cfg.Addr)
	cancelDial()
	if err != nil {
		return err
	}
	defer conn.Close()
	stopHandshake := context.AfterFunc(ctx, func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(20 * time.Second))
	c, channels, requests, err := ssh.NewClientConn(conn, t.cfg.Addr, t.ssh)
	stopHandshake()
	if err != nil {
		return err
	}
	conn.SetDeadline(time.Time{})
	client := ssh.NewClient(c, channels, requests)
	defer client.Close()
	sessionCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(sessionCtx, func() { client.Close() })
	defer func() { cancel(); stop(); client.Close() }()
	conn.SetDeadline(time.Now().Add(20 * time.Second))
	listener, err := client.Listen("tcp", t.cfg.RemoteAddr)
	if err != nil {
		return fmt.Errorf("remote loopback port unavailable or SSH forwarding denied: %w", err)
	}
	conn.SetDeadline(time.Time{})
	defer listener.Close()
	log.Printf("pc-mcp: SSH forwarding connected; remote %s -> local %s", t.cfg.RemoteAddr, localAddr)
	keepDone := make(chan struct{})
	go func() { defer close(keepDone); t.keepalive(sessionCtx, client) }()
	var workers sync.WaitGroup
	defer func() { cancel(); client.Close(); workers.Wait(); <-keepDone }()
	slots := make(chan struct{}, 32)
	for {
		remote, err := listener.Accept()
		if err != nil {
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			remote.Close()
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-slots }()
			defer remote.Close()
			local, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(sessionCtx, "tcp", localAddr)
			if err != nil {
				return
			}
			defer local.Close()
			stop := context.AfterFunc(sessionCtx, func() { remote.Close(); local.Close() })
			defer stop()
			copyDone := make(chan struct{})
			go func() {
				io.Copy(local, remote)
				if half, ok := local.(interface{ CloseWrite() error }); ok {
					half.CloseWrite()
				} else {
					local.Close()
				}
				close(copyDone)
			}()
			io.Copy(remote, local)
			remote.Close()
			local.Close()
			<-copyDone
		}()
	}
}

func (t *Tunnel) keepalive(ctx context.Context, client *ssh.Client) {
	ticker := time.NewTicker(t.keepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		done := make(chan error, 1)
		go func() { _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); done <- err }()
		timeout := time.NewTimer(10 * time.Second)
		select {
		case err := <-done:
			timeout.Stop()
			if err != nil {
				client.Close()
				return
			}
		case <-timeout.C:
			client.Close()
			return
		case <-ctx.Done():
			timeout.Stop()
			client.Close()
			return
		}
	}
}
