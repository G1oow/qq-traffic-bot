package nft

import "testing"

func TestParseSet(t *testing.T) {
	input := []byte(`{
  "nftables": [
    {"metainfo":{"version":"1.1.3"}},
    {"set":{"family":"ip","name":"hitv4","elem":[
      {"elem":{"val":"192.0.2.1","counter":{"packets":12,"bytes":4096}}},
      {"elem":{"val":"2001:0db8:0:0:0:0:0:1","counter":{"packets":2,"bytes":512}}}
    ]}}
  ]
}`)

	got, err := ParseSet(input)
	if err != nil {
		t.Fatal(err)
	}
	if got["192.0.2.1"] != 4096 {
		t.Fatalf("IPv4 bytes = %d", got["192.0.2.1"])
	}
	if got["2001:db8::1"] != 512 {
		t.Fatalf("IPv6 bytes = %d", got["2001:db8::1"])
	}
}
