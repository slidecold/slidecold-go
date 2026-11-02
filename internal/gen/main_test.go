package main

import (
	"strings"
	"testing"
)

func TestNaming(t *testing.T) {
	for id, want := range map[string]string{
		"getCampaignStats":       "Campaigns Stats stats get_campaign_stats",
		"getLoginStatus":         "Accounts LoginStatus login-status get_login_status",
		"verifyAccount2fa":       "Accounts Verify2FA verify-2fa verify_account_2fa",
		"getAccountsProxyHealth": "Accounts ProxyHealth proxy-health get_accounts_proxy_health",
		"listProxies":            "Proxies List list list_proxies",
		"listCampaignRecipients": "Campaigns ListRecipients list-recipients list_campaign_recipients",
	} {
		tag := strings.Fields(want)[0]
		ws := actionWords(id, tag)
		got := strings.Join([]string{tag, pascalWords(ws), strings.Join(ws, "-"), strings.Join(words(id), "_")}, " ")
		if got != want {
			t.Errorf("%s: got %q, want %q", id, got, want)
		}
	}
	if got := pascal("NEEDS_RELOGIN") + pascal("_count") + lowerCamel("messageId") + pascal("audienceIds"); got != "NeedsReloginCountmessageIDAudienceIDs" {
		t.Errorf("got %q", got)
	}
}
