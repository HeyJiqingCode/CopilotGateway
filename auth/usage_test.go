package auth

import (
	"encoding/json"
	"testing"
)

// TestUsageInfo_ParsesQuotaSnapshots 锁定 /copilot_internal/user 的额度字段。
func TestUsageInfo_ParsesQuotaSnapshots(t *testing.T) {
	raw := []byte(`{
		"login": "octocat",
		"access_type_sku": "copilot_for_business_seat_quota",
		"copilot_plan": "business",
		"token_based_billing": true,
		"quota_reset_date": "2026-08-01",
		"quota_reset_date_utc": "2026-08-01T00:00:00.000Z",
		"organization_login_list": ["example-org"],
		"quota_snapshots": {
			"premium_interactions": {
				"quota_id": "premium_interactions",
				"entitlement": 100000,
				"remaining": 81000,
				"quota_remaining": 81000.3,
				"percent_remaining": 81,
				"unlimited": false,
				"has_quota": true,
				"token_based_billing": true,
				"overage_permitted": true,
				"overage_count": 0,
				"overage_entitlement": 0,
				"timestamp_utc": "2026-07-07T11:40:16.143Z"
			},
			"chat": {
				"quota_id": "chat",
				"percent_remaining": 100,
				"unlimited": true,
				"has_quota": true,
				"token_based_billing": true
			}
		}
	}`)

	var got UsageInfo
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	normalizeUsageInfo(&got)

	if got.SKU != "copilot_for_business_seat_quota" {
		t.Fatalf("SKU = %q, want access_type_sku fallback", got.SKU)
	}
	if got.CopilotPlan != "business" {
		t.Fatalf("CopilotPlan = %q, want business", got.CopilotPlan)
	}
	if !got.TokenBasedBilling {
		t.Fatal("TokenBasedBilling = false, want true")
	}

	premium := got.QuotaSnapshots["premium_interactions"]
	if premium.QuotaRemaining != 81000.3 {
		t.Fatalf("premium quota remaining = %v, want 81000.3", premium.QuotaRemaining)
	}
	if premium.PercentRemaining == nil || *premium.PercentRemaining != 81 {
		t.Fatalf("premium percent remaining = %v, want 81", premium.PercentRemaining)
	}
	if !premium.OveragePermitted {
		t.Fatal("premium overage permitted = false, want true")
	}

	chat := got.QuotaSnapshots["chat"]
	if !chat.Unlimited {
		t.Fatal("chat unlimited = false, want true")
	}
}
