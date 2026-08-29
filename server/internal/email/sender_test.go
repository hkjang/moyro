package email

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSMTPSenderSendTransportModes(t *testing.T) {
	serverTLS, roots := newTestTLSConfiguration(t, true)
	tests := []struct {
		name       string
		mode       TLSMode
		options    smtpServerOptions
		roots      *x509.CertPool
		wantTLS    bool
		wantMinTLS uint16
	}{
		{
			name: "plain even when STARTTLS is advertised", mode: TLSModeNone,
			options: smtpServerOptions{advertiseSTARTTLS: true, tlsConfig: serverTLS},
		},
		{
			name: "STARTTLS", mode: TLSModeSTARTTLS,
			options: smtpServerOptions{advertiseSTARTTLS: true, tlsConfig: serverTLS},
			roots:   roots, wantTLS: true, wantMinTLS: tls.VersionTLS12,
		},
		{
			name: "implicit TLS with custom CA", mode: TLSModeImplicit,
			options: smtpServerOptions{implicitTLS: true, tlsConfig: serverTLS},
			roots:   roots, wantTLS: true, wantMinTLS: tls.VersionTLS12,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := startSMTPServer(t, test.options)
			sender := &SMTPSender{
				Host: server.host, Port: server.port,
				From: "Moyro <sender@example.test>", TLSMode: test.mode,
				RootCAs: test.roots,
			}
			if err := sender.Send(context.Background(), "Recipient <recipient@example.test>", "Nightly digest", "<b>HTML body</b>", "text body"); err != nil {
				t.Fatalf("Send() error = %v", err)
			}
			if err := server.wait(); err != nil {
				t.Fatalf("SMTP server error = %v", err)
			}

			snapshot := server.state()
			if snapshot.usedTLS != test.wantTLS {
				t.Fatalf("used TLS = %v, want %v", snapshot.usedTLS, test.wantTLS)
			}
			if test.wantMinTLS != 0 && snapshot.tlsVersion < test.wantMinTLS {
				t.Fatalf("TLS version = %#x, want >= %#x", snapshot.tlsVersion, test.wantMinTLS)
			}
			if snapshot.mailFrom != "sender@example.test" || snapshot.recipient != "recipient@example.test" {
				t.Fatalf("envelope = from %q to %q", snapshot.mailFrom, snapshot.recipient)
			}
			for _, fragment := range []string{
				`From: "Moyro" <sender@example.test>`,
				`To: "Recipient" <recipient@example.test>`,
				"Subject: Nightly digest",
				"text body",
				"<b>HTML body</b>",
			} {
				if !strings.Contains(snapshot.message, fragment) {
					t.Fatalf("message does not contain %q:\n%s", fragment, snapshot.message)
				}
			}
		})
	}
}

func TestSMTPSenderRequiresAdvertisedSTARTTLS(t *testing.T) {
	server := startSMTPServer(t, smtpServerOptions{})
	sender := &SMTPSender{
		Host: server.host, Port: server.port,
		From: "sender@example.test", TLSMode: TLSModeSTARTTLS,
	}
	err := sender.Send(context.Background(), "recipient@example.test", "subject", "html", "text")
	if !errors.Is(err, ErrSTARTTLSUnavailable) {
		t.Fatalf("Send() error = %v, want ErrSTARTTLSUnavailable", err)
	}
	if err := server.wait(); err != nil {
		t.Fatalf("SMTP server error = %v", err)
	}
}

func TestSMTPSenderLegacyUseTLSMapping(t *testing.T) {
	tests := []struct {
		name   string
		sender SMTPSender
		port   string
		want   TLSMode
	}{
		{name: "legacy plaintext", sender: SMTPSender{}, port: "25", want: TLSModeNone},
		{name: "legacy port 465", sender: SMTPSender{UseTLS: true}, port: "465", want: TLSModeImplicit},
		{name: "legacy other TLS port", sender: SMTPSender{UseTLS: true}, port: "587", want: TLSModeSTARTTLS},
		{name: "explicit plaintext overrides legacy", sender: SMTPSender{UseTLS: true, TLSMode: TLSModePlain}, port: "465", want: TLSModeNone},
		{name: "explicit STARTTLS", sender: SMTPSender{TLSMode: TLSModeSTARTTLS}, port: "25", want: TLSModeSTARTTLS},
		{name: "explicit implicit TLS", sender: SMTPSender{TLSMode: TLSModeImplicit}, port: "25", want: TLSModeImplicit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.sender.effectiveTLSMode(test.port)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("effectiveTLSMode() = %q, want %q", got, test.want)
			}
		})
	}

	if _, err := (&SMTPSender{TLSMode: "downgrade"}).effectiveTLSMode("25"); err == nil {
		t.Fatal("unsupported TLS mode was accepted")
	}
}

func TestSMTPSenderValidatesEnvelopeAndHeadersBeforeDial(t *testing.T) {
	tests := []struct {
		name    string
		from    string
		to      string
		subject string
		want    string
	}{
		{name: "sender injection", from: "sender@example.test\r\nBcc: attacker@example.test", to: "recipient@example.test", subject: "subject", want: "from header contains a line break"},
		{name: "recipient injection", from: "sender@example.test", to: "recipient@example.test\nCc: attacker@example.test", subject: "subject", want: "to header contains a line break"},
		{name: "subject injection", from: "sender@example.test", to: "recipient@example.test", subject: "subject\r\nBcc: attacker@example.test", want: "subject header contains a line break"},
		{name: "invalid sender", from: "not an address", to: "recipient@example.test", subject: "subject", want: "invalid sender address"},
		{name: "multiple recipients", from: "sender@example.test", to: "one@example.test, two@example.test", subject: "subject", want: "invalid recipient address"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sender := &SMTPSender{
				Host: "127.0.0.1", Port: "1", From: test.from, TLSMode: TLSModeNone,
			}
			err := sender.Send(context.Background(), test.to, test.subject, "html", "text")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Send() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestSMTPSenderCancellationClosesConnection(t *testing.T) {
	server := startSMTPServer(t, smtpServerOptions{stallGreeting: true})
	sender := &SMTPSender{
		Host: server.host, Port: server.port,
		From: "sender@example.test", TLSMode: TLSModeNone, Timeout: time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- sender.Send(ctx, "recipient@example.test", "subject", "html", "text")
	}()

	select {
	case <-server.accepted:
	case <-time.After(time.Second):
		t.Fatal("sender did not connect")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Send() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Send did not return after cancellation")
	}
	if err := server.wait(); err != nil {
		t.Fatalf("SMTP server error = %v", err)
	}
}

func TestSMTPSenderInternalTimeoutClosesConnection(t *testing.T) {
	server := startSMTPServer(t, smtpServerOptions{stallGreeting: true})
	sender := &SMTPSender{
		Host: server.host, Port: server.port,
		From: "sender@example.test", TLSMode: TLSModeNone, Timeout: 50 * time.Millisecond,
	}
	started := time.Now()
	err := sender.Send(context.Background(), "recipient@example.test", "subject", "html", "text")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Send() error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Send() returned after %s, want prompt timeout", elapsed)
	}
	if err := server.wait(); err != nil {
		t.Fatalf("SMTP server error = %v", err)
	}
}

func TestSMTPSenderVerifiesTLSHostname(t *testing.T) {
	serverTLS, roots := newTestTLSConfiguration(t, false)
	server := startSMTPServer(t, smtpServerOptions{implicitTLS: true, tlsConfig: serverTLS})
	sender := &SMTPSender{
		Host: server.host, Port: server.port,
		From: "sender@example.test", TLSMode: TLSModeImplicit, RootCAs: roots,
	}
	err := sender.Send(context.Background(), "recipient@example.test", "subject", "html", "text")
	if err == nil {
		t.Fatal("Send() accepted a certificate for the wrong hostname")
	}
	var hostnameError x509.HostnameError
	if !errors.As(err, &hostnameError) {
		t.Fatalf("Send() error = %v, want x509.HostnameError", err)
	}
	// The server observes the client's verification alert as a handshake error.
	_ = server.wait()
}

func TestSMTPSenderRejectsTLSBelowVersion12(t *testing.T) {
	serverTLS, roots := newTestTLSConfiguration(t, true)
	serverTLS.MinVersion = tls.VersionTLS10
	serverTLS.MaxVersion = tls.VersionTLS11
	server := startSMTPServer(t, smtpServerOptions{implicitTLS: true, tlsConfig: serverTLS})
	sender := &SMTPSender{
		Host: server.host, Port: server.port,
		From: "sender@example.test", TLSMode: TLSModeImplicit, RootCAs: roots,
	}
	if err := sender.Send(context.Background(), "recipient@example.test", "subject", "html", "text"); err == nil {
		t.Fatal("Send() accepted TLS below 1.2")
	}
	_ = server.wait()
}

type smtpServerOptions struct {
	implicitTLS       bool
	advertiseSTARTTLS bool
	tlsConfig         *tls.Config
	stallGreeting     bool
}

type smtpServerSnapshot struct {
	usedTLS    bool
	tlsVersion uint16
	mailFrom   string
	recipient  string
	message    string
}

type smtpTestServer struct {
	host     string
	port     string
	listener net.Listener
	options  smtpServerOptions
	accepted chan struct{}
	done     chan struct{}

	mu       sync.Mutex
	conn     net.Conn
	err      error
	snapshot smtpServerSnapshot
}

func startSMTPServer(t *testing.T, options smtpServerOptions) *smtpTestServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	server := &smtpTestServer{
		host: host, port: port, listener: listener, options: options,
		accepted: make(chan struct{}), done: make(chan struct{}),
	}
	go server.run()
	t.Cleanup(server.shutdown)
	return server
}

func (s *smtpTestServer) run() {
	connection, err := s.listener.Accept()
	if err != nil {
		if errors.Is(err, net.ErrClosed) {
			err = nil
		}
		s.finish(err)
		return
	}
	_ = s.listener.Close()
	s.mu.Lock()
	s.conn = connection
	s.mu.Unlock()
	close(s.accepted)
	err = s.serve(connection)
	s.finish(err)
}

func (s *smtpTestServer) finish(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
	close(s.done)
}

func (s *smtpTestServer) serve(connection net.Conn) error {
	defer connection.Close()
	if s.options.implicitTLS {
		var err error
		connection, err = s.upgradeTLS(connection)
		if err != nil {
			return err
		}
	}
	if s.options.stallGreeting {
		buffer := make([]byte, 1)
		_, err := connection.Read(buffer)
		if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	}
	if _, err := io.WriteString(connection, "220 smtp.test ESMTP ready\r\n"); err != nil {
		return err
	}

	reader := bufio.NewReader(connection)
	tlsActive := s.options.implicitTLS
	for {
		line, err := reader.ReadString('\n')
		if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		if err != nil {
			return err
		}
		command := strings.TrimSpace(line)
		upper := strings.ToUpper(command)
		switch {
		case strings.HasPrefix(upper, "EHLO "):
			if s.options.advertiseSTARTTLS && !tlsActive {
				_, err = io.WriteString(connection, "250-smtp.test\r\n250-STARTTLS\r\n250 SIZE 1048576\r\n")
			} else {
				_, err = io.WriteString(connection, "250-smtp.test\r\n250 SIZE 1048576\r\n")
			}
		case strings.HasPrefix(upper, "HELO "):
			_, err = io.WriteString(connection, "250 smtp.test\r\n")
		case upper == "STARTTLS":
			if !s.options.advertiseSTARTTLS || s.options.tlsConfig == nil {
				_, err = io.WriteString(connection, "454 TLS unavailable\r\n")
				break
			}
			if _, err = io.WriteString(connection, "220 begin TLS\r\n"); err != nil {
				break
			}
			connection, err = s.upgradeTLS(connection)
			if err == nil {
				reader = bufio.NewReader(connection)
				tlsActive = true
			}
		case strings.HasPrefix(upper, "MAIL FROM:"):
			s.mu.Lock()
			s.snapshot.mailFrom = smtpPath(command[len("MAIL FROM:"):])
			s.mu.Unlock()
			_, err = io.WriteString(connection, "250 sender accepted\r\n")
		case strings.HasPrefix(upper, "RCPT TO:"):
			s.mu.Lock()
			s.snapshot.recipient = smtpPath(command[len("RCPT TO:"):])
			s.mu.Unlock()
			_, err = io.WriteString(connection, "250 recipient accepted\r\n")
		case upper == "DATA":
			if _, err = io.WriteString(connection, "354 send message\r\n"); err != nil {
				break
			}
			var message []byte
			message, err = textproto.NewReader(reader).ReadDotBytes()
			if err == nil {
				s.mu.Lock()
				s.snapshot.message = string(message)
				s.mu.Unlock()
				_, err = io.WriteString(connection, "250 queued\r\n")
			}
		case upper == "QUIT":
			_, err = io.WriteString(connection, "221 bye\r\n")
			if err == nil {
				return nil
			}
		default:
			_, err = io.WriteString(connection, "500 unsupported\r\n")
		}
		if err != nil {
			return err
		}
	}
}

func (s *smtpTestServer) upgradeTLS(connection net.Conn) (net.Conn, error) {
	if s.options.tlsConfig == nil {
		return nil, errors.New("test SMTP server has no TLS configuration")
	}
	tlsConnection := tls.Server(connection, s.options.tlsConfig)
	if err := tlsConnection.Handshake(); err != nil {
		return nil, err
	}
	state := tlsConnection.ConnectionState()
	s.mu.Lock()
	s.snapshot.usedTLS = true
	s.snapshot.tlsVersion = state.Version
	s.mu.Unlock()
	return tlsConnection, nil
}

func (s *smtpTestServer) wait() error {
	select {
	case <-s.done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.err
	case <-time.After(2 * time.Second):
		return errors.New("test SMTP server did not stop")
	}
}

func (s *smtpTestServer) state() smtpServerSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot
}

func (s *smtpTestServer) shutdown() {
	_ = s.listener.Close()
	s.mu.Lock()
	if s.conn != nil {
		_ = s.conn.Close()
	}
	s.mu.Unlock()
	select {
	case <-s.done:
	case <-time.After(time.Second):
	}
}

func smtpPath(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "<")
	value = strings.TrimSuffix(value, ">")
	return value
}

func newTestTLSConfiguration(t *testing.T, validForLoopback bool) (*tls.Config, *x509.CertPool) {
	t.Helper()
	now := time.Now()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Moyro test CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCertificate, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "smtp.test"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		DNSNames:     []string{"localhost"},
	}
	if validForLoopback {
		leafTemplate.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCertificate, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate := tls.Certificate{
		Certificate: [][]byte{leafDER, caDER},
		PrivateKey:  leafKey,
	}
	roots := x509.NewCertPool()
	roots.AddCert(caCertificate)
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{certificate},
	}, roots
}

func ExampleSMTPSender_legacyTLSMapping() {
	sender := &SMTPSender{Port: "465", UseTLS: true}
	mode, _ := sender.effectiveTLSMode(sender.Port)
	fmt.Println(mode)
	// Output: implicit
}
