package clash

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTailscaleExportsReportAMissingNode(t *testing.T) {
	var status tailscaleStatusResult
	if err := json.Unmarshal([]byte(TailscaleStatus(`{"name":"nope","start":true}`)), &status); err != nil {
		t.Fatal(err)
	}
	if status.Error == "" || status.Started || status.Peers == nil {
		t.Errorf("TailscaleStatus for a missing node = %+v; want an error and an empty peer list", status)
	}
	var pings []tailscalePingResult
	if err := json.Unmarshal([]byte(TailscalePing(`{"name":"nope","targets":["100.64.0.1"]}`)), &pings); err != nil {
		t.Fatal(err)
	}
	if len(pings) != 1 || pings[0].LatencyMs != -1 || pings[0].Error == "" || pings[0].Target != "100.64.0.1" {
		t.Errorf("TailscalePing for a missing node = %+v", pings)
	}
	var routes tailscaleDERPRoutesResult
	if err := json.Unmarshal([]byte(TailscaleDERPRoutes("nope")), &routes); err != nil {
		t.Fatal(err)
	}
	if routes.Error == "" || routes.DERPRoutes == nil {
		t.Errorf("TailscaleDERPRoutes for a missing node = %+v", routes)
	}
	var reconnect tailscaleActionResult
	if err := json.Unmarshal([]byte(TailscaleReconnect("nope")), &reconnect); err != nil {
		t.Fatal(err)
	}
	if reconnect.Error == "" {
		t.Error("TailscaleReconnect for a missing node reported no error")
	}
	if got := TailscaleNodes(); got != `{"nodes":[]}` {
		t.Errorf("TailscaleNodes with no node = %s; want an empty list", got)
	}
	if got := TailscaleNetworkChanged(); got != "{}" {
		t.Errorf("TailscaleNetworkChanged with no node = %s; want {}", got)
	}
}

func TestTailscaleExportsRefuseBadJSON(t *testing.T) {
	for name, got := range map[string]string{
		"TailscaleStatus": TailscaleStatus("{"),
		"TailscalePing":   TailscalePing("{"),
	} {
		if !strings.Contains(got, `"error"`) {
			t.Errorf("%s(bad JSON) = %s; want an error field", name, got)
		}
	}
}

func TestTailscaleExportsRecoverAPanic(t *testing.T) {
	for _, name := range []string{"TailscaleStatus", "TailscalePing", "TailscaleDERPRoutes", "TailscaleNetworkChanged", "TailscaleReconnect", "TailscaleNodes"} {
		target := name
		appCallTestPanic.Store(&target)
		var got string
		switch name {
		case "TailscaleStatus":
			got = TailscaleStatus(`{"name":"ts"}`)
		case "TailscalePing":
			got = TailscalePing(`{"name":"ts","targets":[]}`)
		case "TailscaleDERPRoutes":
			got = TailscaleDERPRoutes("ts")
		case "TailscaleNetworkChanged":
			got = TailscaleNetworkChanged()
		case "TailscaleReconnect":
			got = TailscaleReconnect("ts")
		case "TailscaleNodes":
			got = TailscaleNodes()
		}
		appCallTestPanic.Store(nil)
		if !strings.Contains(got, "panicked") {
			t.Errorf("%s under a deliberate panic returned %s; want the panic reported", name, got)
		}
	}
}
