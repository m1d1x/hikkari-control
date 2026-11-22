package hoptrace

import "testing"

func TestFullBucketKeepsEarlierAndLaterPacketsFromTheSameSample(t *testing.T) {
	b := Bucket{SamplingBits: 1, Packets: make([]Packet, 0, 30)}
	for id := uint64(1); id <= 997; id++ {
		b.record(Packet{ID: id})
	}
	if b.Overflow || b.SamplingBits <= 1 {
		t.Fatalf("capacity did not thin the bucket: %+v", b)
	}
	var expected []uint64
	for id := uint64(1); id <= 997; id++ {
		if Sampled(id, b.SamplingBits) {
			expected = append(expected, id)
		}
	}
	if len(b.Packets) != len(expected) {
		t.Fatalf("retained %d packets, want %d", len(b.Packets), len(expected))
	}
	for i, sample := range b.Packets {
		if sample.ID != expected[i] {
			t.Fatalf("packet %d: got %d, want %d", i, sample.ID, expected[i])
		}
	}
}
