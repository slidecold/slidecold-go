package slidecold

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestVerifyWebhookSignature(t *testing.T) {
	body := []byte(`{"event":"lead.imported","data":{"recipientUsername":"bob"}}`)
	mac := hmac.New(sha256.New, []byte("whsec_1"))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	if !VerifyWebhookSignature(body, sig, "whsec_1") {
		t.Error("valid signature rejected")
	}
	if !VerifyWebhookSignature(body, strings.ToUpper(sig), "whsec_1") {
		t.Error("upper-case hex rejected")
	}
	for name, ok := range map[string]bool{
		"wrong secret":  VerifyWebhookSignature(body, sig, "whsec_2"),
		"tampered body": VerifyWebhookSignature(append([]byte(nil), append(body, ' ')...), sig, "whsec_1"),
		"truncated sig": VerifyWebhookSignature(body, sig[:62], "whsec_1"),
		"not hex":       VerifyWebhookSignature(body, "zz"+sig[2:], "whsec_1"),
		"empty sig":     VerifyWebhookSignature(body, "", "whsec_1"),
		"empty secret":  VerifyWebhookSignature(body, sig, ""),
	} {
		if ok {
			t.Errorf("%s: accepted", name)
		}
	}
}
