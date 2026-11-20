package input

import (
	"fmt"
	"testing"
)

func acceptedCodes() []string {
	codes := []string{}
	for code := range scanCodes {
		codes = append(codes, code)
	}
	for code := range evdevExtended {
		codes = append(codes, code)
	}
	for i := 13; i <= 24; i++ {
		codes = append(codes, fmt.Sprintf("F%d", i))
	}
	return codes
}

func TestWindowsKeyCoverage(t *testing.T) {
	for _, code := range acceptedCodes() {
		if _, ok := EvdevCode(code); !ok {
			t.Fatalf("invalid fixture %s", code)
		}
		for _, down := range []bool{true, false} {
			if _, _, _, err := windowsKeyFields(code, down); err != nil {
				t.Error(err)
			}
		}
	}
	for code, want := range map[string]uint16{"PrintScreen": 0x137, "NumpadEqual": 0x59, "F13": 0x64, "F23": 0x6e, "F24": 0x76} {
		vk, scan, flags, err := windowsKeyFields(code, true)
		if err != nil || vk != 0 || scan != want&255 || (flags&1 != 0) != (want&0x100 != 0) {
			t.Errorf("%s: %x %x %x %v", code, vk, scan, flags, err)
		}
		_, _, up, _ := windowsKeyFields(code, false)
		if up != flags|2 {
			t.Errorf("%s keyup flags: %x", code, up)
		}
	}
	vk, scan, flags, err := windowsKeyFields("Pause", true)
	if err != nil || vk != 0x13 || scan != 0 || flags != 0 {
		t.Fatal("Pause must use VK_PAUSE")
	}
	if _, _, _, err := windowsKeyFields("", true); err == nil {
		t.Fatal("accepted empty key")
	}
}
