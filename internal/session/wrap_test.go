package session

import "testing"

func TestWrapTracker_DetectsWrap(t *testing.T) {
	tracker := NewWrapTracker()

	// Initial reading: 3.5 GiB
	in1, out1, wrapped1 := tracker.Update(3758096384, 1073741824)
	if wrapped1 {
		t.Errorf("expected wrapped=false on first reading")
	}
	if in1 != 3758096384 || out1 != 1073741824 {
		t.Errorf("unexpected initial totals: in=%d out=%d", in1, out1)
	}

	// Second reading: Counter advanced past 4 GiB and wrapped to 200 MB (209715200 bytes)
	in2, out2, wrapped2 := tracker.Update(209715200, 1610612736)
	if !wrapped2 {
		t.Errorf("expected wrapped=true after counter decrease")
	}

	expectedInTotal := int64(MaxOctet32) + 209715200
	if in2 != expectedInTotal {
		t.Errorf("expected inputTotal=%d, got %d", expectedInTotal, in2)
	}
	if out2 != 1610612736 {
		t.Errorf("expected outputTotal=1610612736, got %d", out2)
	}

	// Third reading: Counter continues advancing normally from 200 MB to 500 MB
	in3, _, wrapped3 := tracker.Update(524288000, 2147483648)
	if !wrapped3 {
		t.Errorf("expected wrapped flag to remain sticky")
	}
	expectedInTotal3 := int64(MaxOctet32) + 524288000
	if in3 != expectedInTotal3 {
		t.Errorf("expected inputTotal=%d, got %d", expectedInTotal3, in3)
	}
}

func TestComputeTotals_Gigawords(t *testing.T) {
	// 2 GiB in octets + 1 Gigaword (4 GiB) = 6 GiB total
	inTotal, outTotal := ComputeTotals(2147483648, 1073741824, 1, 2)
	expectedIn := (int64(1) << 32) | 2147483648
	expectedOut := (int64(2) << 32) | 1073741824

	if inTotal != expectedIn {
		t.Errorf("expected inTotal=%d, got %d", expectedIn, inTotal)
	}
	if outTotal != expectedOut {
		t.Errorf("expected outTotal=%d, got %d", expectedOut, outTotal)
	}
}
