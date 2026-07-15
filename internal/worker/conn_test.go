package worker

import "testing"

// TestConnIDKeyMatchesConnKey guarantees connID.key() orders identically to
// the pre-existing connKey(netFlow, transport): AF_PACKET and eBPF-fed
// streams must land in the same p.conns/p.flows bucket whenever they
// describe the same connection.
func TestConnIDKeyMatchesConnKey(t *testing.T) {
	reqNet, reqTr, respNet, respTr := flows(40000, 5432)

	got := connIDFromFlows(reqNet, reqTr).key()
	want := connKey(reqNet, reqTr)
	if got != want {
		t.Errorf("connIDFromFlows(req).key() = %q, want %q", got, want)
	}

	// The response direction's flows are reversed; both directions of one
	// connection must still resolve to the same canonical key.
	gotResp := connIDFromFlows(respNet, respTr).key()
	if gotResp != want {
		t.Errorf("connIDFromFlows(resp).key() = %q, want %q", gotResp, want)
	}
	if gotResp != got {
		t.Errorf("request and response directions disagree: %q vs %q", got, gotResp)
	}
}

func TestConnIDEndpoints(t *testing.T) {
	reqNet, reqTr, _, _ := flows(40000, 5432)
	c := connIDFromFlows(reqNet, reqTr)
	src, dst := c.endpoints()
	wantSrc, wantDst := flowEndpoints(reqNet, reqTr)
	if src != wantSrc || dst != wantDst {
		t.Errorf("endpoints() = (%+v,%+v), want (%+v,%+v)", src, dst, wantSrc, wantDst)
	}
}
