// Package adb is a minimal client for the ADB wire protocol over TCP, so
// `tv setup` can install the companion app and grant its permissions
// without the user installing Android's platform tools. It talks to the
// device's adbd directly; there is no adb server in the picture.
//
// Protocol in one paragraph: both sides exchange 24-byte little-endian
// headers {command, arg0, arg1, length, checksum, magic} followed by a
// payload. We open with CNXN; the device answers AUTH with a 20-byte token
// we must sign with our RSA key. If it doesn't know the key it asks again,
// we send the public key, and the TV shows "Allow USB debugging?". After
// the device's own CNXN, each OPEN starts a stream ("shell:cmd", "exec:cmd")
// that carries WRTE payloads acknowledged by OKAY until CLSE.
package adb

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	cmdCNXN = 0x4e584e43
	cmdAUTH = 0x48545541
	cmdOPEN = 0x4e45504f
	cmdOKAY = 0x59414b4f
	cmdCLSE = 0x45534c43
	cmdWRTE = 0x45545257

	authToken        = 1
	authSignature    = 2
	authRSAPublicKey = 3

	version = 0x01000000
	maxData = 256 * 1024 // we accept up to this per WRTE; devices send less
)

// Conn is one authenticated session with a device.
type Conn struct {
	c           net.Conn
	devMax      uint32 // device's advertised max payload per WRTE
	nextID      uint32
	Banner      string // device's CNXN banner, e.g. "device::ro.product.name=ginzaeu;..."
	Authorizing bool   // set when the device showed the allow dialog
}

type msg struct {
	cmd, arg0, arg1 uint32
	data            []byte
}

// ---- key management -------------------------------------------------------

// LoadOrCreateKey returns the RSA key at path, generating a 2048-bit one on
// first use. adbd remembers the public key after the user accepts, so the
// prompt appears once per computer.
func LoadOrCreateKey(path string) (*rsa.PrivateKey, error) {
	if b, err := os.ReadFile(path); err == nil {
		blk, _ := pem.Decode(b)
		if blk != nil {
			if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
				return k, nil
			}
		}
	}
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

// androidPublicKey encodes the public key the way adbd expects: a packed
// struct (word count, n0inv, modulus LE, rr LE, exponent) in base64, then
// a space and a name. n0inv and rr are Montgomery precomputation values the
// device uses to verify signatures quickly; we compute them exactly as
// Android's libcrypto_utils does.
func androidPublicKey(k *rsa.PrivateKey, name string) []byte {
	const words = 64 // 2048 bits / 32
	n := k.N
	r32 := new(big.Int).Lsh(big.NewInt(1), 32)
	// n0inv = -1 / n mod 2^32
	n0 := new(big.Int).Mod(n, r32)
	n0inv := new(big.Int).ModInverse(n0, r32)
	n0inv.Sub(r32, n0inv)
	n0inv.Mod(n0inv, r32)
	// rr = (2^(32*words))^2 mod n
	rr := new(big.Int).Lsh(big.NewInt(1), 32*words*2)
	rr.Mod(rr, n)

	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint32(words))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(n0inv.Uint64()))
	buf.Write(leWords(n, words))
	buf.Write(leWords(rr, words))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(k.E))
	out := base64.StdEncoding.EncodeToString(buf.Bytes()) + " " + name + "\x00"
	return []byte(out)
}

// leWords writes v as `words` little-endian 32-bit words.
func leWords(v *big.Int, words int) []byte {
	b := v.FillBytes(make([]byte, words*4)) // big-endian, fixed width
	out := make([]byte, len(b))
	for i := range b {
		out[i] = b[len(b)-1-i]
	}
	return out
}

// ---- connection -----------------------------------------------------------

// Dial connects to host:5555 and completes authentication. onPrompt is
// called once if the TV needs the user to accept the key; Dial then waits
// up to `wait` for the acceptance.
func Dial(ctx context.Context, addr string, key *rsa.PrivateKey, onPrompt func(), wait time.Duration) (*Conn, error) {
	d := net.Dialer{Timeout: 5 * time.Second}
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	c := &Conn{c: nc, devMax: 4096, nextID: 1}
	banner := []byte("host::tvctl\x00")
	if err := c.send(msg{cmdCNXN, version, maxData, banner}); err != nil {
		nc.Close()
		return nil, err
	}
	sentKey := false
	deadline := time.Now().Add(15 * time.Second)
	for {
		_ = nc.SetReadDeadline(deadline)
		m, err := c.recv()
		if err != nil {
			nc.Close()
			if sentKey {
				return nil, errors.New("timed out waiting for the TV to accept the debugging prompt")
			}
			return nil, fmt.Errorf("adb handshake: %w", err)
		}
		switch m.cmd {
		case cmdCNXN:
			c.Banner = strings.TrimRight(string(m.data), "\x00")
			if m.arg1 > 0 && m.arg1 < c.devMax*64 {
				c.devMax = m.arg1
			}
			_ = nc.SetReadDeadline(time.Time{})
			return c, nil
		case cmdAUTH:
			if m.arg0 != authToken {
				nc.Close()
				return nil, fmt.Errorf("unexpected AUTH type %d", m.arg0)
			}
			if !sentKey {
				// First try: sign the token. The token is 20 bytes and adbd
				// verifies it as a SHA-1 PKCS#1 v1.5 signature.
				sig, err := rsa.SignPKCS1v15(nil, key, crypto.SHA1, m.data)
				if err != nil {
					nc.Close()
					return nil, err
				}
				if err := c.send(msg{cmdAUTH, authSignature, 0, sig}); err != nil {
					nc.Close()
					return nil, err
				}
				sentKey = true
				continue
			}
			// Second AUTH means the signature wasn't accepted: the device
			// doesn't know our key. Offer it; the TV prompts the user.
			host, _ := os.Hostname()
			if err := c.send(msg{cmdAUTH, authRSAPublicKey, 0, androidPublicKey(key, "tvctl@"+host)}); err != nil {
				nc.Close()
				return nil, err
			}
			c.Authorizing = true
			if onPrompt != nil {
				onPrompt()
			}
			deadline = time.Now().Add(wait)
		default:
			// Ignore anything else during the handshake.
		}
	}
}

func (c *Conn) Close() error { return c.c.Close() }

func (c *Conn) send(m msg) error {
	var sum uint32
	for _, b := range m.data {
		sum += uint32(b)
	}
	hdr := make([]byte, 24)
	binary.LittleEndian.PutUint32(hdr[0:], m.cmd)
	binary.LittleEndian.PutUint32(hdr[4:], m.arg0)
	binary.LittleEndian.PutUint32(hdr[8:], m.arg1)
	binary.LittleEndian.PutUint32(hdr[12:], uint32(len(m.data)))
	binary.LittleEndian.PutUint32(hdr[16:], sum)
	binary.LittleEndian.PutUint32(hdr[20:], m.cmd^0xffffffff)
	if _, err := c.c.Write(hdr); err != nil {
		return err
	}
	if len(m.data) > 0 {
		if _, err := c.c.Write(m.data); err != nil {
			return err
		}
	}
	return nil
}

func (c *Conn) recv() (msg, error) {
	hdr := make([]byte, 24)
	if _, err := io.ReadFull(c.c, hdr); err != nil {
		return msg{}, err
	}
	m := msg{
		cmd:  binary.LittleEndian.Uint32(hdr[0:]),
		arg0: binary.LittleEndian.Uint32(hdr[4:]),
		arg1: binary.LittleEndian.Uint32(hdr[8:]),
	}
	n := binary.LittleEndian.Uint32(hdr[12:])
	if n > 4*maxData {
		return msg{}, fmt.Errorf("adb: oversized packet %d", n)
	}
	if m.cmd^0xffffffff != binary.LittleEndian.Uint32(hdr[20:]) {
		return msg{}, errors.New("adb: bad magic")
	}
	if n > 0 {
		m.data = make([]byte, n)
		if _, err := io.ReadFull(c.c, m.data); err != nil {
			return msg{}, err
		}
	}
	return m, nil
}

// ---- streams --------------------------------------------------------------

// stream is one OPENed service. ADB multiplexes streams over the single
// socket; we only ever run one at a time, which keeps this simple.
type stream struct {
	c      *Conn
	local  uint32
	remote uint32
	closed bool
}

func (c *Conn) open(ctx context.Context, service string) (*stream, error) {
	s := &stream{c: c, local: c.nextID}
	c.nextID++
	if err := c.send(msg{cmdOPEN, s.local, 0, append([]byte(service), 0)}); err != nil {
		return nil, err
	}
	for {
		m, err := c.recv()
		if err != nil {
			return nil, err
		}
		switch m.cmd {
		case cmdOKAY:
			if m.arg1 == s.local {
				s.remote = m.arg0
				return s, nil
			}
		case cmdCLSE:
			// Only a CLSE addressed to this stream is a refusal; the device
			// may still be tearing down the previous stream.
			if m.arg1 == s.local {
				return nil, fmt.Errorf("adb: device refused %q", service)
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
}

// write sends one chunk and waits for its OKAY.
func (s *stream) write(p []byte) error {
	if err := s.c.send(msg{cmdWRTE, s.local, s.remote, p}); err != nil {
		return err
	}
	for {
		m, err := s.c.recv()
		if err != nil {
			return err
		}
		switch m.cmd {
		case cmdOKAY:
			return nil
		case cmdWRTE:
			// Device output arriving while we write (e.g. install progress);
			// acknowledge and ignore here, Shell reads it later.
			_ = s.c.send(msg{cmdOKAY, s.local, s.remote, nil})
		case cmdCLSE:
			s.closed = true
			return errors.New("adb: stream closed by device")
		}
	}
}

// readAll drains device output until the stream closes.
func (s *stream) readAll(ctx context.Context) ([]byte, error) {
	var out bytes.Buffer
	for !s.closed {
		if ctx.Err() != nil {
			return out.Bytes(), ctx.Err()
		}
		m, err := s.c.recv()
		if err != nil {
			return out.Bytes(), err
		}
		switch m.cmd {
		case cmdWRTE:
			if m.arg1 == s.local {
				out.Write(m.data)
				if err := s.c.send(msg{cmdOKAY, s.local, s.remote, nil}); err != nil {
					return out.Bytes(), err
				}
			}
		case cmdCLSE:
			if m.arg1 == s.local {
				s.closed = true
				_ = s.c.send(msg{cmdCLSE, s.local, s.remote, nil})
			}
		}
	}
	return out.Bytes(), nil
}

// Shell runs a command and returns its combined output. No exit code is
// available on the legacy "shell:" service, so callers inspect the text.
func (c *Conn) Shell(ctx context.Context, cmd string) (string, error) {
	s, err := c.open(ctx, "shell:"+cmd)
	if err != nil {
		return "", err
	}
	out, err := s.readAll(ctx)
	return strings.TrimSpace(strings.ReplaceAll(string(out), "\r\n", "\n")), err
}

// Install streams an APK into `pm install -S <size>`, which reads the
// package from stdin. This avoids implementing the sync (file push)
// protocol. progress, if set, is called with bytes sent so far.
func (c *Conn) Install(ctx context.Context, apk []byte, progress func(sent, total int)) (string, error) {
	svc := fmt.Sprintf("exec:cmd package install -r -S %d", len(apk))
	s, err := c.open(ctx, svc)
	if err != nil {
		return "", err
	}
	chunk := int(c.devMax)
	if chunk > maxData {
		chunk = maxData
	}
	for off := 0; off < len(apk); off += chunk {
		end := off + chunk
		if end > len(apk) {
			end = len(apk)
		}
		if err := s.write(apk[off:end]); err != nil {
			return "", fmt.Errorf("install upload: %w", err)
		}
		if progress != nil {
			progress(end, len(apk))
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	out, err := s.readAll(ctx)
	res := strings.TrimSpace(string(out))
	if err != nil && res == "" {
		return "", err
	}
	if !strings.Contains(res, "Success") {
		return res, fmt.Errorf("install failed: %s", res)
	}
	return res, nil
}
