package wechat

import "testing"

// TestPinWeChatProtocolConstants pins the iLink timing/retry constants. They
// mirror server-side windows (long-poll hold, ticket TTL, dedup window); a
// drifted value keeps every code path green but desynchronises the client from
// the contract, so the numbers themselves are the behaviour under test.
func TestPinWeChatProtocolConstants(t *testing.T) {
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"LongPollTimeoutMs", LongPollTimeoutMs, 35000},
		{"APITimeoutMs", APITimeoutMs, 15000},
		{"ConfigTimeoutMs", ConfigTimeoutMs, 10000},
		{"QRTimeoutMs", QRTimeoutMs, 35000},
		{"MaxConsecutiveFailures", MaxConsecutiveFailures, 3},
		{"RetryDelaySeconds", RetryDelaySeconds, 2},
		{"BackoffDelaySeconds", BackoffDelaySeconds, 30},
		{"MessageDedupTTLSeconds", MessageDedupTTLSeconds, 300},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}
