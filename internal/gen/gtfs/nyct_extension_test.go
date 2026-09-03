package gtfs

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

func TestNyctFeedHeaderExtensionRoundTrip(t *testing.T) {
	want := &NyctFeedHeader{NyctSubwayVersion: proto.String("1.0")}
	in := &FeedMessage{Header: &FeedHeader{GtfsRealtimeVersion: proto.String("2.0")}}
	proto.SetExtension(in.Header, E_NyctFeedHeader, want)

	wire, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out FeedMessage
	if err := proto.Unmarshal(wire, &out); err != nil {
		t.Fatal(err)
	}
	if !proto.HasExtension(out.Header, E_NyctFeedHeader) {
		t.Fatal("NYCT feed-header extension missing after round trip")
	}
	got := proto.GetExtension(out.Header, E_NyctFeedHeader)
	if got.(*NyctFeedHeader).GetNyctSubwayVersion() != want.GetNyctSubwayVersion() {
		t.Fatalf("NYCT extension = %q, want %q", got.(*NyctFeedHeader).GetNyctSubwayVersion(), want.GetNyctSubwayVersion())
	}
}
