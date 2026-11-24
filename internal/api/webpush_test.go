package api

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// The vector of RFC 8291 appendix A, also used by the Kotlin WebPushPayloadEncryptorTest.
func TestEncryptPushRFC8291(t *testing.T) {
	priv, _ := decodeB64URL("yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw")
	server, err := ecdh.P256().NewPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if got := base64.RawURLEncoding.EncodeToString(server.PublicKey().Bytes()); got != "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8" {
		t.Fatalf("server public key %s", got)
	}
	salt, _ := decodeB64URL("DGv6ra1nlYgDCS1FRnbzlw")
	out, err := encryptPush([]byte("When I grow up, I want to be a watermelon"), "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4",
		"BTBZMqHH6r4Tts7J_aSIgg", server, salt)
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if got := base64.RawURLEncoding.EncodeToString(out); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestPushKeysAndAddresses(t *testing.T) {
	if _, _, err := pushKeys("BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4", "BTBZMqHH6r4Tts7J_aSIgg=="); err != nil {
		t.Fatal("padded auth refused")
	}
	if _, _, err := pushKeys("BCVxsr7N", "BTBZMqHH6r4Tts7J_aSIgg"); err == nil {
		t.Fatal("short key accepted")
	}
	for addr, blocked := range map[string]bool{"10.1.2.3": true, "100.64.0.1": true, "100.128.0.1": false, "172.15.0.1": false, "172.16.0.1": true,
		"192.0.9.1": true, "198.18.0.1": true, "224.0.0.1": true, "8.8.8.8": false, "::1": true, "fe80::1": true, "fec0::1": true, "fd00::1": true,
		"::ffff:127.0.0.1": true, "2001:4860:4860::8888": false, "::": true} {
		if got := blockedPushAddress(netip.MustParseAddr(addr)); got != blocked {
			t.Errorf("%s blocked=%v", addr, got)
		}
	}
}

func TestSendPushDeliversDecryptablePayload(t *testing.T) {
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	rand.Read(auth)
	var got []byte
	var headers http.Header
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		got = decryptPushForTest(t, body, ua, auth)
		w.WriteHeader(http.StatusCreated)
	}))
	defer ts.Close()
	pushBlocked = func(netip.Addr) bool { return false }
	pushTLS = ts.Client().Transport.(*http.Transport).TLSClientConfig
	defer func() { pushBlocked, pushTLS = blockedPushAddress, nil }()
	endpoint := ts.URL + "/up/abc"
	payload := encodePushPayload(pushPayload{Version: 1, EventType: "subscription_new_video", Title: "A <b> & c", EventID: "e"})
	res, code := sendPush(context.Background(), endpoint, "ev1", payload, base64.RawURLEncoding.EncodeToString(ua.PublicKey().Bytes()),
		base64.RawURLEncoding.EncodeToString(auth))
	if res != pushDelivered || code != 201 {
		t.Fatalf("result %v %d", res, code)
	}
	if string(got) != string(payload) || !strings.Contains(string(got), `"title":"A <b> & c"`) || strings.Contains(string(got), "thumbnailUrl") {
		t.Fatalf("payload %s", got)
	}
	if headers.Get("Content-Encoding") != "aes128gcm" || headers.Get("TTL") != "86400" || headers.Get("X-TypeType-Event-Id") != "ev1" {
		t.Fatalf("headers %v", headers)
	}
}

// decryptPushForTest is the user agent side of RFC 8291.
func decryptPushForTest(t *testing.T, body []byte, ua *ecdh.PrivateKey, auth []byte) []byte {
	salt, keyLen := body[:16], int(body[20])
	serverPub, err := ecdh.P256().NewPublicKey(body[21 : 21+keyLen])
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := ua.ECDH(serverPub)
	info := append(append([]byte("WebPush: info\x00"), ua.PublicKey().Bytes()...), serverPub.Bytes()...)
	prk, _ := hkdf.Extract(sha256.New, shared, auth)
	ikm, _ := hkdf.Expand(sha256.New, prk, string(info), 32)
	prk2, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk2, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk2, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, body[21+keyLen:], nil)
	if err != nil || plain[len(plain)-1] != 2 {
		t.Fatalf("decrypt %v", err)
	}
	return plain[:len(plain)-1]
}
