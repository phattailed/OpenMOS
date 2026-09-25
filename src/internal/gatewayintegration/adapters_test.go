package gatewayintegration

import (
	"strings"
	"testing"
)

// TestIsLocalBind_WildcardAddressesAreRejected is the regression for a real
// gap: the original implementation's hand-rolled colon search treated an
// EMPTY host ("" from ":8091", the http.Server/net.Listen convention for
// "every interface") as loopback, so GATEWAY_BIND_ADDR=":8091" -- a
// wildcard bind reachable from every network interface -- passed the guard
// this milestone requires to keep the listener local-only. It must not.
func TestIsLocalBind_WildcardAddressesAreRejected(t *testing.T) {
	wildcard := []string{
		":8091",       // every interface, IPv4 and IPv6
		"0.0.0.0:8091", // every IPv4 interface, spelled out numerically
		"[::]:8091",    // every IPv6 interface, spelled out numerically
	}
	for _, addr := range wildcard {
		if IsLocalBind(addr) {
			t.Errorf("IsLocalBind(%q) = true, want false -- this is a wildcard bind, not loopback-only", addr)
		}
	}
}

// TestIsLocalBind_LoopbackAddressesAreAccepted proves the fix did not
// overcorrect into rejecting genuine loopback addresses, including a
// bracketed IPv6 literal, which the original hand-rolled colon search
// would have mis-parsed (host would have come out as "[::1]", literal
// brackets included, matching neither of its own accepted cases).
func TestIsLocalBind_LoopbackAddressesAreAccepted(t *testing.T) {
	loopback := []string{
		"127.0.0.1:8091",
		"localhost:8091",
		"[::1]:8091",
		"127.0.0.5:8091", // all of 127.0.0.0/8 is loopback, not only .1
	}
	for _, addr := range loopback {
		if !IsLocalBind(addr) {
			t.Errorf("IsLocalBind(%q) = false, want true -- this is a genuine loopback address", addr)
		}
	}
}

// TestIsLocalBind_NonLocalAddressesAreRejected covers an address that is
// neither wildcard nor loopback -- a real, routable interface address --
// which must never be treated as local.
func TestIsLocalBind_NonLocalAddressesAreRejected(t *testing.T) {
	nonLocal := []string{
		"192.168.1.10:8091",
		"10.0.0.5:8091",
		"example.test:8091", // a non-"localhost" hostname is not resolved/trusted here
	}
	for _, addr := range nonLocal {
		if IsLocalBind(addr) {
			t.Errorf("IsLocalBind(%q) = true, want false -- this is not a local address", addr)
		}
	}
}

// TestParseAuthTokens_MalformedEntryErrorNeverContainsTheToken is the
// regression for a real secret-disclosure bug: the original error message
// was fmt.Errorf("malformed caller:token pair %q", pair), which echoes the
// ENTIRE "caller:token" string -- including a syntactically-fine-looking
// token value -- straight back out. That error reaches
// log.Fatalf("Gateway.AuthTokens: %v", err) in main.go, i.e. process logs.
// This uses a synthetic token value chosen to be unmistakable if it leaks,
// and asserts it appears NOWHERE in any returned error across every
// malformed-input shape this parser rejects.
func TestParseAuthTokens_MalformedEntryErrorNeverContainsTheToken(t *testing.T) {
	const secretToken = "sk-live-CANARY-do-not-leak-9f3a7b21c4"

	cases := []struct {
		name string
		raw  string
	}{
		{"empty token after colon", "breakglass:"},
		{"empty caller before colon", ":" + secretToken},
		{"missing colon entirely, token-shaped value alone", secretToken},
		{"one well-formed entry followed by a malformed one", "ok:token-fine," + secretToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAuthTokens(tc.raw)
			if err == nil {
				t.Fatalf("ParseAuthTokens(%q): want an error for malformed input, got none", tc.raw)
			}
			if strings.Contains(err.Error(), secretToken) {
				t.Fatalf("ParseAuthTokens(%q) error leaked the secret token value: %q", tc.raw, err.Error())
			}
		})
	}
}

// TestParseAuthTokens_WellFormedInputStillWorks proves the error-message
// fix did not change accepted-input behavior: valid caller:token pairs
// still parse to the expected token->caller map.
func TestParseAuthTokens_WellFormedInputStillWorks(t *testing.T) {
	tokens, err := ParseAuthTokens("breakglass:tok-1, other-caller:tok-2")
	if err != nil {
		t.Fatalf("ParseAuthTokens: unexpected error: %v", err)
	}
	want := map[string]string{"tok-1": "breakglass", "tok-2": "other-caller"}
	if len(tokens) != len(want) {
		t.Fatalf("tokens = %v, want %v", tokens, want)
	}
	for token, caller := range want {
		if tokens[token] != caller {
			t.Errorf("tokens[%q] = %q, want %q", token, tokens[token], caller)
		}
	}
}
