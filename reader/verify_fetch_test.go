package main

import (
	"encoding/hex"
	"testing"

	"google.golang.org/protobuf/proto"

	"reader/pb"
)

// The app's captured fetch-all request on .../thing/property/get (seq varies
// per send, everything else is fixed).
const capturedFetchAll = "0a1a1020182070ebf7e99002ba0107416e64726f6964d20100da0100"

func TestFetchAllMatchesAppCapture(t *testing.T) {
	captured, err := hex.DecodeString(capturedFetchAll)
	if err != nil {
		t.Fatal(err)
	}
	var want, got pb.Send_Header_Msg
	if err := proto.Unmarshal(captured, &want); err != nil {
		t.Fatal(err)
	}
	if err := proto.Unmarshal(buildFetchAllRequest(), &got); err != nil {
		t.Fatal(err)
	}
	// seq is a per-send random; zero it before comparing.
	want.GetMsg()[0].Seq = 0
	got.GetMsg()[0].Seq = 0
	if !proto.Equal(&want, &got) {
		t.Errorf("request mismatch:\n got %v\nwant %v", &got, &want)
	}
}
