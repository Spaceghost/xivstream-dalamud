package cpupolicy

import "testing"

func TestCPUSelections(t *testing.T) {
	for _, selection := range []string{"4", "4-7", "0-7", "0-0", "0,2,4,6"} {
		if err := ValidateSelection(selection, "0-7"); err != nil {
			t.Errorf("%s: %v", selection, err)
		}
	}
	for _, selection := range []string{"0", "9", "8-9", "4-", "-1", "5-2", "1,,2"} {
		if err := ValidateSelection(selection, "0-7"); err == nil {
			t.Errorf("accepted %q", selection)
		}
	}
	if err := ValidateSelection("2-2", "0-1,4-7"); err == nil {
		t.Error("accepted offline CPU")
	}
}
