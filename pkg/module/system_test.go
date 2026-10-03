package module

import "testing"

func TestResolveBlockDevice(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// SATA/SCSI/virtio partitions -> strip the partition number.
		{"/dev/sda1", "/dev/sda"},
		{"/dev/sdb12", "/dev/sdb"},
		{"/dev/vda2", "/dev/vda"},
		// NVMe partitions -> strip the trailing p<num>.
		{"/dev/nvme0n1p1", "/dev/nvme0n1"},
		{"/dev/nvme0n1p12", "/dev/nvme0n1"},
		// NVMe whole disk -> unchanged (regression: was /dev/nvme0n).
		{"/dev/nvme0n1", "/dev/nvme0n1"},
		// mmcblk partition -> strip p<num>.
		{"/dev/mmcblk0p1", "/dev/mmcblk0"},
		// Whole mmcblk device (no partition suffix) -> unchanged. Regression:
		// sdRe matched "mmcblk0" and stripped the digit to /dev/mmcblk.
		{"/dev/mmcblk0", "/dev/mmcblk0"},
		// LVM / mapper -> unchanged.
		{"/dev/mapper/ubuntu--vg-ubuntu--lv", "/dev/mapper/ubuntu--vg-ubuntu--lv"},
		// Virtual / RAID whole devices whose trailing digits are part of the
		// name must NOT be treated as partitions (regression).
		{"/dev/md0", "/dev/md0"},
		{"/dev/dm-0", "/dev/dm-0"},
		{"/dev/loop0", "/dev/loop0"},
		{"/dev/sr0", "/dev/sr0"},
		{"/dev/nbd0", "/dev/nbd0"},
		// Non-/dev input -> unchanged.
		{"tmpfs", "tmpfs"},
	}
	for _, c := range cases {
		if got := resolveBlockDevice(c.in); got != c.want {
			t.Errorf("resolveBlockDevice(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
