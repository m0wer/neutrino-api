package neutrino

import (
	"net"
	"testing"

	"github.com/lightninglabs/neutrino/banman"
)

func TestParseIPNetOnionV3(t *testing.T) {
	addr := "yov4edh4vgbgywplxuxv4esroksz2brb64fdtjbryc5wbo43wtlbsiad.onion:38333"

	ipNet, err := banman.ParseIPNet(addr, nil)
	if err != nil {
		t.Fatalf("ParseIPNet() failed for onion v3: %v", err)
	}

	if ipNet == nil || ipNet.IP == nil || ipNet.IP.To16() == nil {
		t.Fatalf("expected IPv6 ipnet for onion v3, got: %#v", ipNet)
	}

	ones, bits := ipNet.Mask.Size()
	if ones != 128 || bits != 128 {
		t.Fatalf("expected /128 mask for onion v3, got /%d (%d bits)", ones, bits)
	}
}

func TestParseIPNetOnionV2(t *testing.T) {
	addr := "777myonionurl777.onion:8333"

	ipNet, err := banman.ParseIPNet(addr, nil)
	if err != nil {
		t.Fatalf("ParseIPNet() failed for onion v2: %v", err)
	}

	if ipNet == nil || ipNet.IP == nil || ipNet.IP.To16() == nil {
		t.Fatalf("expected IPv6 ipnet for onion v2, got: %#v", ipNet)
	}
}

func TestParseIPNetOnionStableAcrossPortVariants(t *testing.T) {
	host := "yov4edh4vgbgywplxuxv4esroksz2brb64fdtjbryc5wbo43wtlbsiad.onion"

	withPort, err := banman.ParseIPNet(net.JoinHostPort(host, "38333"), nil)
	if err != nil {
		t.Fatalf("ParseIPNet() with port failed: %v", err)
	}

	withoutPort, err := banman.ParseIPNet(host, nil)
	if err != nil {
		t.Fatalf("ParseIPNet() without port failed: %v", err)
	}

	if withPort.String() != withoutPort.String() {
		t.Fatalf("expected stable onion mapping, withPort=%s withoutPort=%s", withPort.String(), withoutPort.String())
	}
}

func TestParseIPNetRejectsInvalidOnion(t *testing.T) {
	_, err := banman.ParseIPNet("invalid.onion:8333", nil)
	if err == nil {
		t.Fatal("expected error for invalid onion hostname")
	}
}
