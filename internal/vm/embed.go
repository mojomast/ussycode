package vm

import _ "embed"

// ussycodeInitScript is the VM boot init script.
// It is written into every rootfs at start time so that existing VMs
// automatically pick up fixes without needing a full image rebuild.
//
// IMPORTANT: This file is a copy of images/ussyuntu/init-ussycode.sh.
// When changing the init script, update BOTH files and rebuild the
// Docker image for new VMs.
//
//go:embed init-ussycode.sh
var ussycodeInitScript string
