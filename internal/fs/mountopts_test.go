//go:build linux

package fs

import (
	"reflect"
	"testing"
)

// TestMountOptions checks that -allow-other and -fuse-opts merge into a
// single fusermount3 list without duplicating the read-only overlay's
// built-ins (docs/CLI.zh.md documents `-fuse-opts default_permissions`
// alongside -allow-other, which must not trip fusermount3 up).
func TestMountOptions(t *testing.T) {
	builtin := []string{"ro", "default_permissions", "fsname=cuefs"}
	for _, tc := range []struct {
		name       string
		allowOther bool
		extra      []string
		want       []string
	}{
		{"plain", false, nil, builtin},
		{"allow-other", true, nil,
			[]string{"ro", "default_permissions", "fsname=cuefs", "allow_other"}},
		{"extras", false, []string{"auto_unmount", "max_read=131072"},
			[]string{"ro", "default_permissions", "fsname=cuefs", "auto_unmount", "max_read=131072"}},
		{"allow-other-twice", true, []string{"allow_other"},
			[]string{"ro", "default_permissions", "fsname=cuefs", "allow_other"}},
		{"dup-flags", true, []string{"default_permissions", "allow_other"},
			[]string{"ro", "default_permissions", "fsname=cuefs", "allow_other"}},
		{"own-fsname", false, []string{"fsname=navidrome-cuefs"},
			[]string{"ro", "default_permissions", "fsname=navidrome-cuefs"}},
		{"own-fsname-and-allow-other", true, []string{"fsname=navidrome-cuefs"},
			[]string{"ro", "default_permissions", "fsname=navidrome-cuefs", "allow_other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mountOptions(tc.allowOther, tc.extra)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("mountOptions(%v, %q) = %q, want %q",
					tc.allowOther, tc.extra, got, tc.want)
			}
		})
	}
}
