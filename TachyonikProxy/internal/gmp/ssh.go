// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The SSH transport: how a GMP session is opened on a current appliance.
//
// The shape is copied from python-gvm's SSHConnection, which is what gvm-tools
// uses and therefore what appliances are configured to expect: connect as the
// GMP SSH account, start a session with an EMPTY command and no PTY, then write
// XML to stdin and read it from stdout. The account's shell on the appliance is
// the gvmd relay, so there is no command to name — asking for one would run it
// instead of reaching gvmd.

package gmp

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"
)

// DefaultSSHPort is where an appliance listens.
const DefaultSSHPort = 22

// DefaultSSHUser is the account that relays to gvmd. python-gvm defaults to the
// same name, and appliances are set up to match.
const DefaultSSHUser = "gmp"

// SSHConfig is what it takes to open the channel. The GMP user that
// Client.Authenticate names is a different identity and is not in here.
type SSHConfig struct {
	Host string
	Port int
	User string
	// Password for the SSH account. Held for the life of the call and never
	// written anywhere by this package.
	Password string
	// HostKeyFingerprint is the appliance's SSH host key in the form
	// ssh.FingerprintSHA256 returns ("SHA256:…"). When set, a key that does not
	// match aborts the connection.
	HostKeyFingerprint string
	// AllowUnknownHostKey permits connecting to an appliance whose key is not
	// yet known. The fingerprint is then reported in UnknownHostKeyError so an
	// operator can confirm and pin it.
	//
	// Off by default deliberately: accepting any key would make the encryption
	// decorative, and this is a security product.
	AllowUnknownHostKey bool
	Timeout             time.Duration
}

// UnknownHostKeyError is returned when the appliance's key is not the expected
// one, and carries what was offered so it can be shown and pinned.
type UnknownHostKeyError struct {
	Host     string
	Got      string
	Expected string
}

func (e *UnknownHostKeyError) Error() string {
	if e.Expected == "" {
		return fmt.Sprintf("the host key of %s is not known yet (%s)", e.Host, e.Got)
	}
	return fmt.Sprintf("the host key of %s is %s, not the expected %s", e.Host, e.Got, e.Expected)
}

// sshTransport is a session's stdin/stdout as one ReadWriter.
type sshTransport struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
}

func (t *sshTransport) Read(p []byte) (int, error)  { return t.stdout.Read(p) }
func (t *sshTransport) Write(p []byte) (int, error) { return t.stdin.Write(p) }

func (t *sshTransport) Close() error {
	// Order matters: close the writer so the far side sees end-of-input, then
	// the session, then the connection.
	if t.stdin != nil {
		_ = t.stdin.Close()
	}
	if t.session != nil {
		_ = t.session.Close()
	}
	return t.client.Close()
}

// DialSSH opens a GMP session over SSH.
func DialSSH(cfg SSHConfig) (*Client, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("no appliance host given")
	}
	port := cfg.Port
	if port == 0 {
		port = DefaultSSHPort
	}
	user := cfg.User
	if user == "" {
		user = DefaultSSHUser
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port))
	clientCfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(cfg.Password)},
		Timeout:         timeout,
		HostKeyCallback: hostKeyCallback(cfg),
	}

	client, err := ssh.Dial("tcp", addr, clientCfg)
	if err != nil {
		// The host-key error is passed through unwrapped so a caller can offer
		// to pin the fingerprint it carries.
		if hk, ok := err.(*UnknownHostKeyError); ok {
			return nil, hk
		}
		return nil, fmt.Errorf("could not open an SSH session to %s: %w", addr, err)
	}

	session, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("could not start a session on %s: %w", addr, err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("could not open the session input: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("could not open the session output: %w", err)
	}

	// The empty command is the point: the account's shell IS the gvmd relay.
	if err := session.Start(""); err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("could not reach gvmd on %s — is this the GMP SSH account? %w", addr, err)
	}

	return New(&sshTransport{client: client, session: session, stdin: stdin, stdout: stdout}), nil
}

// hostKeyCallback enforces the policy in SSHConfig.
func hostKeyCallback(cfg SSHConfig) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		got := ssh.FingerprintSHA256(key)
		if cfg.HostKeyFingerprint != "" {
			if got == cfg.HostKeyFingerprint {
				return nil
			}
			return &UnknownHostKeyError{Host: hostname, Got: got, Expected: cfg.HostKeyFingerprint}
		}
		if cfg.AllowUnknownHostKey {
			return nil
		}
		return &UnknownHostKeyError{Host: hostname, Got: got}
	}
}
