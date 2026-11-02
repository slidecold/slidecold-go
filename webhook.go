package slidecold

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// WebhookSignatureHeader is the header carrying a webhook's signature.
const WebhookSignatureHeader = "X-SlideCold-Signature"

// VerifyWebhookSignature reports whether signature (the X-SlideCold-Signature
// header) is the hex HMAC-SHA256 of rawBody keyed with the endpoint secret.
// Pass the body exactly as received, before any JSON decoding.
func VerifyWebhookSignature(rawBody []byte, signature, secret string) bool {
	got, err := hex.DecodeString(signature)
	if err != nil || secret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(rawBody)
	return hmac.Equal(got, mac.Sum(nil))
}
