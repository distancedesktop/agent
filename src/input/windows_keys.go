package input

import "fmt"

var windowsExtraScans = map[string]uint16{
	"PrintScreen": 0x137, "NumpadEqual": 0x59,
	"F13": 0x64, "F14": 0x65, "F15": 0x66, "F16": 0x67,
	"F17": 0x68, "F18": 0x69, "F19": 0x6a, "F20": 0x6b,
	"F21": 0x6c, "F22": 0x6d, "F23": 0x6e, "F24": 0x76,
}

func windowsKeyFields(code string, down bool) (vk, scan uint16, flags uint32, err error) {
	if !down {
		flags = 2
	}
	if code == "Pause" {
		return 0x13, 0, flags, nil
	} // VK_PAUSE handles the E1 sequence.
	scan, ok := scanCodes[code]
	if !ok {
		scan, ok = windowsExtraScans[code]
	}
	if !ok {
		return 0, 0, 0, fmt.Errorf("unsupported Windows key %q", code)
	}
	flags |= 8
	if scan&0x100 != 0 {
		flags |= 1
	}
	return 0, scan & 255, flags, nil
}
