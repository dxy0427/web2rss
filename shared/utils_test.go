package shared

import "testing"

func TestParseSizeToBytes(t *testing.T) {
	tb12 := 1.2
	mb := 485.98
	cases := []struct {
		in   string
		want int64
	}{
		{"1.2TB", int64(tb12 * 1024 * 1024 * 1024 * 1024)},
		{"50GB", 50 * 1024 * 1024 * 1024},
		{"485.98MB", int64(mb * 1024 * 1024)},
		{"700KB", 700 * 1024},
		{"garbage", 0},
		{"", 0},
	}
	for _, c := range cases {
		got := ParseSizeToBytes(c.in)
		if got != c.want {
			t.Errorf("ParseSizeToBytes(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
