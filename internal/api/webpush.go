package api

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var errPushSubscription = errors.New("invalid web push subscription")

// pushBlocked and pushTLS are replaced only by tests, which deliver to a local TLS server.
var (
	pushBlocked = blockedPushAddress
	pushTLS     *tls.Config
)

// pushKeys checks the subscription keys the way WebPushPayloadEncryptor does: an uncompressed P-256 point and a
// 16-byte auth secret. crypto/ecdh also refuses a point off the curve, which the JVM only notices when encrypting.
func pushKeys(p256dh, auth string) (*ecdh.PublicKey, []byte, error) {
	raw, err := decodeB64URL(p256dh)
	if err != nil || len(raw) != 65 || raw[0] != 4 {
		return nil, nil, errPushSubscription
	}
	pub, err := ecdh.P256().NewPublicKey(raw)
	if err != nil {
		return nil, nil, errPushSubscription
	}
	secret, err := decodeB64URL(auth)
	if err != nil || len(secret) != 16 {
		return nil, nil, errPushSubscription
	}
	return pub, secret, nil
}

// encryptPush is RFC 8291 aes128gcm with a single 4096-byte record, as WebPushPayloadEncryptor.encryptWithMaterial.
func encryptPush(payload []byte, p256dh, auth string, server *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(salt) != 16 || len(payload) > 3993 {
		return nil, errors.New("push payload too large")
	}
	ua, secret, err := pushKeys(p256dh, auth)
	if err != nil {
		return nil, err
	}
	shared, err := server.ECDH(ua)
	if err != nil {
		return nil, errPushSubscription
	}
	serverPub := server.PublicKey().Bytes()
	info := append(append([]byte("WebPush: info\x00"), ua.Bytes()...), serverPub...)
	prk, _ := hkdf.Extract(sha256.New, shared, secret)
	ikm, _ := hkdf.Expand(sha256.New, prk, string(info), 32)
	prk2, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk2, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk2, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	out := make([]byte, 0, 86+len(payload)+17)
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, 4096)
	out = append(out, byte(len(serverPub)))
	out = append(out, serverPub...)
	return gcm.Seal(out, nonce, append(bytes.Clone(payload), 2), nil), nil
}

// blockedPushAddress is UnifiedPushEndpointValidator.isBlockedAddress, with the JVM's address classes spelled out.
func blockedPushAddress(a netip.Addr) bool {
	a = a.Unmap()
	if a.Is4() {
		b := a.As4()
		return b[0] == 0 || b[0] == 10 || b[0] == 127 || b[0] == 100 && b[1] >= 64 && b[1] <= 127 || b[0] == 169 && b[1] == 254 ||
			b[0] == 172 && b[1] >= 16 && b[1] <= 31 || b[0] == 192 && b[1] == 0 || b[0] == 192 && b[1] == 168 || b[0] == 198 && (b[1] == 18 || b[1] == 19) ||
			b[0] >= 224
	}
	b := a.As16()
	return a.IsUnspecified() || a.IsLoopback() || a.IsMulticast() || b[0] == 0xfe && b[1]&0xc0 == 0x80 || b[0] == 0xfe && b[1]&0xc0 == 0xc0 ||
		b[0] == 0xfc || b[0] == 0xfd
}

// validPushEndpoint is UnifiedPushEndpointValidator.validate: an https URL without credentials or fragment whose
// host resolves only to public addresses. It answers the reason code on failure.
func validPushEndpoint(ctx context.Context, raw string) (*url.URL, []netip.Addr, string) {
	if len(raw) < 1 || len(raw) > 2048 {
		return nil, nil, "endpoint_length"
	}
	u, err := url.Parse(raw)
	if err != nil || strings.ContainsFunc(raw, func(c rune) bool { return c <= ' ' || c == 0x7f || strings.ContainsRune("\"<>\\^`{|}", c) }) {
		return nil, nil, "endpoint_uri"
	}
	if strings.ToLower(u.Scheme) != "https" || u.User != nil || u.Fragment != "" || strings.Contains(raw, "#") {
		return nil, nil, "endpoint_scheme"
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "" {
		return nil, nil, "endpoint_host"
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n <= 0 || n > 65535 {
			return nil, nil, "endpoint_port"
		}
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(rctx, "ip", host)
	if err != nil {
		return nil, nil, "endpoint_unresolvable"
	}
	if len(addrs) == 0 {
		return nil, nil, "endpoint_private_address"
	}
	for _, a := range addrs {
		if pushBlocked(a) {
			return nil, nil, "endpoint_private_address"
		}
	}
	return u, addrs, ""
}

type pushResult int

const (
	pushDelivered pushResult = iota
	pushInvalidEndpoint
	pushInvalidSubscription
	pushRetry
)

// sendPush is UnifiedPushSender.send: the endpoint is validated again and dialled only on the addresses that
// validation saw, so a DNS answer cannot change between the check and the request; redirects are not followed.
func sendPush(ctx context.Context, endpoint, eventID string, payload []byte, p256dh, auth string) (pushResult, int) {
	u, addrs, reason := validPushEndpoint(ctx, endpoint)
	if reason != "" {
		return pushInvalidEndpoint, 0
	}
	server, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return pushRetry, 0
	}
	salt := make([]byte, 16)
	rand.Read(salt)
	body, err := encryptPush(payload, p256dh, auth, server, salt)
	if errors.Is(err, errPushSubscription) {
		return pushInvalidSubscription, 0
	} else if err != nil {
		return pushRetry, 0
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	tr := &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: pushTLS, TLSHandshakeTimeout: 10 * time.Second, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var last error
		for _, a := range addrs {
			c, err := dialer.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
			if err == nil {
				return c, nil
			}
			last = err
		}
		return nil, last
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return pushInvalidEndpoint, 0
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", "86400")
	req.Header.Set("X-TypeType-Event-Id", eventID)
	res, err := client.Do(req)
	if err != nil {
		return pushRetry, 0
	}
	res.Body.Close()
	switch {
	case res.StatusCode/100 == 2:
		return pushDelivered, res.StatusCode
	case res.StatusCode == 404 || res.StatusCode == 410:
		return pushInvalidEndpoint, res.StatusCode
	}
	return pushRetry, res.StatusCode
}
