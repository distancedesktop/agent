//go:build windows

package main

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// File mode bits mean nothing on Windows: a token file gets its access from the
// ACL it inherits, which outside the profile directory usually lets every local
// user read it. These two keep it to the current user, SYSTEM and Administrators.

func tokenReaders() ([]*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	sids := []*windows.SID{user.User.Sid}
	for _, t := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid} {
		sid, err := windows.CreateWellKnownSid(t)
		if err != nil {
			return nil, err
		}
		sids = append(sids, sid)
	}
	return sids, nil
}

func restrictTokenFile(path string) error {
	sids, err := tokenReaders()
	if err != nil {
		return err
	}
	sddl := "D:P"
	for _, sid := range sids {
		sddl += "(A;;FA;;;" + sid.String() + ")"
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func checkTokenFile(path string) error {
	sids, err := tokenReaders()
	if err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	fix := fmt.Sprintf(`icacls "%s" /inheritance:r /grant:r "%%USERNAME%%:F"`, path)
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("token file %s: no access list, so every user can read it; run: %s", path, fix)
	}
	const read = windows.FILE_READ_DATA | windows.GENERIC_READ | windows.GENERIC_ALL
ace:
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var a *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &a); err != nil {
			return err
		}
		if a.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || a.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || a.Mask&read == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&a.SidStart))
		for _, ok := range sids {
			if sid.Equals(ok) {
				continue ace
			}
		}
		name := sid.String()
		if account, domain, _, err := sid.LookupAccount(""); err == nil {
			name = strings.TrimPrefix(domain+``+account, ``)
		}
		return fmt.Errorf("token file %s is readable by %s; run: %s /remove:g *%s", path, name, fix, sid)
	}
	return nil
}
