package ssh

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mathrand "math/rand"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/creack/pty/v2"
	"github.com/mojomast/ussycode/internal/db"
	gossh "golang.org/x/crypto/ssh"
)

// CommandFunc is a handler for a shell command.
type CommandFunc func(s *Shell, args []string) error

var commands = map[string]CommandFunc{
	"help":    cmdHelp,
	"whoami":  cmdWhoami,
	"ls":      cmdLs,
	"new":     cmdNew,
	"rm":      cmdRm,
	"ssh":     cmdSSH,
	"stop":    cmdStop,
	"restart": cmdRestart,
	"tag":     cmdTag,
	"rename":  cmdRename,
	"cp":      cmdCp,
	"start":   cmdStart,
	"ssh-key": cmdSSHKey,
	"share":   cmdShare,
	"admin":   cmdAdmin,
	"llm-key": cmdLLMKey,
	// tutorial is registered in tutorial.go init() to avoid init cycle
}

// RegisterCommand adds a command to the commands map at runtime.
// Used by packages that would cause init cycles if registered statically.
func RegisterCommand(name string, fn CommandFunc) {
	commands[name] = fn
}

// ── help ──────────────────────────────────────────────────────────────

func cmdHelp(s *Shell, args []string) error {
	s.writeln("")
	s.writeln("  \033[1m=== USSYCODE COMMANDS ===\033[0m")
	s.writeln("")
	s.writeln("  \033[33mBASICS\033[0m")
	s.writeln("    help          show this help")
	s.writeln("    whoami        show your info")
	s.writeln("    exit          disconnect")
	s.writeln("")
	s.writeln("  \033[33mVM LIFECYCLE\033[0m")
	s.writeln("    new           create a new dev environment")
	s.writeln("    ls            list your environments")
	s.writeln("    rm <name>     delete an environment")
	s.writeln("    start <name>  start a stopped environment")
	s.writeln("    stop <name>   stop a running environment")
	s.writeln("    restart <name> restart an environment")
	s.writeln("    rename <old> <new>  rename an environment")
	s.writeln("    cp <name> [new]     clone an environment")
	s.writeln("    tag <name> <tag>    add a tag")
	s.writeln("    tag -d <name> <tag> remove a tag")
	s.writeln("")
	s.writeln("  \033[33mACCESS\033[0m")
	s.writeln("    ssh <name>    connect to an environment")
	s.writeln("    share         share access with others")
	s.writeln("    browser       open web dashboard (magic link)")
	s.writeln("")
	s.writeln("  \033[33mIDENTITY\033[0m")
	s.writeln("    ssh-key       manage your SSH keys")
	s.writeln("    llm-key       manage LLM API keys")
	s.writeln("")
	s.writeln("  \033[33mARENA\033[0m")
	s.writeln("    arena         CTF & agent competition")
	s.writeln("")
	s.writeln("  \033[33mUSSYVERSE\033[0m")
	s.writeln("    community     ussyverse info, links & your stats")
	s.writeln("")
	s.writeln("  \033[33mLEARN\033[0m")
	s.writeln("    tutorial      interactive tutorial (10 lessons)")
	s.writeln("")
	if s.user.TrustLevel == "admin" {
		s.writeln("  \033[33mADMIN\033[0m")
		s.writeln("    admin         admin-only commands")
		s.writeln("")
	}
	s.writeln("  \033[33mOPTIONS\033[0m")
	s.writeln("    new --name=<n> --image=<img>")
	s.writeln("    ls -l         long format")
	s.writeln("    --json        machine-readable output (on most commands)")
	s.writeln("")
	return nil
}

// ── whoami ────────────────────────────────────────────────────────────

func cmdWhoami(s *Shell, args []string) error {
	ctx := context.Background()

	fp, _ := s.gw.DB.FingerprintByUser(ctx, s.user.ID)
	vmCount, _ := s.gw.DB.VMCountByUser(ctx, s.user.ID)

	s.writeln("")
	s.writef("  handle:      %s\n", s.user.Handle)
	s.writef("  trust level: %s\n", s.user.TrustLevel)
	s.writef("  fingerprint: %s\n", fp)
	s.writef("  vms:         %d\n", vmCount)
	s.writef("  member since: %s\n", s.user.CreatedAt.Format("Jan 2, 2006"))
	s.writeln("")
	return nil
}

// ── ls ────────────────────────────────────────────────────────────────

func cmdLs(s *Shell, args []string) error {
	ctx := context.Background()
	vms, err := s.gw.DB.VMsByUser(ctx, s.user.ID)
	if err != nil {
		return fmt.Errorf("list vms: %w", err)
	}

	if len(vms) == 0 {
		s.writeln("  no environments yet. type 'new' to create one.")
		return nil
	}

	long := false
	for _, a := range args {
		if a == "-l" || a == "--long" {
			long = true
		}
	}

	s.writeln("")
	if long {
		s.writef("  %-16s %-10s %-14s %4s %6s %5s  %s\n",
			"NAME", "STATUS", "IMAGE", "CPU", "MEM", "DISK", "CREATED")
		s.writef("  %-16s %-10s %-14s %4s %6s %5s  %s\n",
			"────", "──────", "─────", "───", "───", "────", "───────")
		for _, v := range vms {
			s.writef("  %-16s %-10s %-14s %4d %4dMB %3dGB  %s\n",
				v.Name, colorStatus(v.Status), v.Image,
				v.VCPU, v.MemoryMB, v.DiskGB, relativeTime(v.CreatedAt.Time))
		}
	} else {
		s.writef("  %-16s %-10s %-14s %s\n", "NAME", "STATUS", "IMAGE", "CREATED")
		s.writef("  %-16s %-10s %-14s %s\n", "────", "──────", "─────", "───────")
		for _, v := range vms {
			s.writef("  %-16s %-10s %-14s %s\n",
				v.Name, colorStatus(v.Status), v.Image, relativeTime(v.CreatedAt.Time))
		}
	}
	s.writeln("")
	return nil
}

// ── new ───────────────────────────────────────────────────────────────

var validName = regexp.MustCompile(`^[a-z][a-z0-9-]{1,28}[a-z0-9]$`)

func cmdNew(s *Shell, args []string) error {
	ctx := context.Background()

	name := ""
	image := "ussyuntu"

	for _, a := range args {
		if strings.HasPrefix(a, "--name=") {
			name = strings.TrimPrefix(a, "--name=")
		} else if strings.HasPrefix(a, "--image=") {
			image = strings.TrimPrefix(a, "--image=")
		}
	}

	if name == "" {
		name = randomName()
	}

	name = strings.ToLower(name)
	if !validName.MatchString(name) {
		return fmt.Errorf("invalid name %q: must be 3-30 chars, lowercase letters/numbers/hyphens, start with letter", name)
	}

	// Check for duplicate
	existing, err := s.gw.DB.VMByUserAndName(ctx, s.user.ID, name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check existing: %w", err)
	}
	if existing != nil && err == nil {
		return fmt.Errorf("vm %q already exists", name)
	}

	// Enforce VM quota based on user's trust level
	vmCount, err := s.gw.DB.GetUserVMCount(ctx, s.user.ID)
	if err != nil {
		return fmt.Errorf("check vm count: %w", err)
	}
	limits := db.GetTrustLimits(s.user.TrustLevel)
	if limits.VMLimit >= 0 && vmCount >= limits.VMLimit {
		return fmt.Errorf("VM limit reached (%d/%d). Upgrade trust level or remove a VM.", vmCount, limits.VMLimit)
	}

	vcpu := 2
	memoryMB := 2048
	if limits.CPULimit > 0 && vcpu > limits.CPULimit {
		vcpu = limits.CPULimit
	}
	if limits.RAMLimit > 0 && memoryMB > limits.RAMLimit {
		memoryMB = limits.RAMLimit
	}
	if memoryMB < 512 {
		memoryMB = 512
	}

	vmRecord, err := s.gw.DB.CreateVM(ctx, s.user.ID, name, image, vcpu, memoryMB, 5)
	if err != nil {
		return fmt.Errorf("create vm: %w", err)
	}

	s.writef("  creating %s...", name)

	// Provision and start the VM via the manager
	if s.gw.VM != nil {
		if err := s.gw.VM.CreateAndStart(ctx, vmRecord.ID, name, image, vcpu, memoryMB, s.vmSSHKeys(ctx)); err != nil {
			// VM creation failed -- update DB status but don't remove the record
			// so user can see it in `ls` and retry or `rm` it
			s.writef(" failed!\n")
			return fmt.Errorf("provision vm: %w", err)
		}

		// Register metadata for the newly started VM
		s.registerVMMetadata(ctx, vmRecord.ID, name, image)

		// Register proxy route for HTTPS access
		s.addProxyRoute(ctx, vmRecord.ID, name)
	} else {
		// No VM manager available (dev mode) -- just mark as stopped
		_ = s.gw.DB.UpdateVMStatus(ctx, vmRecord.ID, "stopped", nil, nil, nil, nil)
	}

	s.writeln(" done!")
	s.writeln("")
	s.writef("  name:  %s\n", vmRecord.Name)
	s.writef("  image: %s\n", vmRecord.Image)
	s.writef("  url:   https://%s.%s\n", vmRecord.Name, s.gw.domain)
	s.writef("  ssh:   ssh %s (from this shell)\n", vmRecord.Name)
	s.writeln("")
	return nil
}

// ── ssh ───────────────────────────────────────────────────────────────

func cmdSSH(s *Shell, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: ssh <name>")
	}

	ctx := context.Background()
	name := args[0]

	vmRecord, err := s.gw.DB.VMByUserAndName(ctx, s.user.ID, name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no vm named %q", name)
		}
		return fmt.Errorf("lookup: %w", err)
	}

	if vmRecord.Status != "running" {
		return fmt.Errorf("vm %q is %s (must be running)", name, vmRecord.Status)
	}

	if !vmRecord.IPAddress.Valid || vmRecord.IPAddress.String == "" {
		return fmt.Errorf("vm %q has no IP address assigned", name)
	}

	s.writef("  connecting to %s...\n", name)

	// Proxy the SSH session through to the VM's SSH server.
	// We dial the VM at port 22 using a host-level key, then pipe
	// the user's gateway session I/O to the VM SSH session.
	if err := proxySSHSession(s, vmRecord.IPAddress.String); err != nil {
		return fmt.Errorf("ssh to %s: %w", name, err)
	}

	return nil
}

// proxySSHSession dials the VM's SSH server and pipes the gateway
// user's terminal to the VM's shell session, handling window resize.
// It allocates a local PTY so the inner ssh process has a real tty,
// and forwards window-change events from the gateway session.
func proxySSHSession(s *Shell, vmIP string) error {
	args := []string{
		"-tt",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "IdentitiesOnly=yes",
		"-o", "PreferredAuthentications=publickey",
		"-i", s.gw.hostKeyPath,
		"ussycode@" + vmIP,
	}
	cmd := exec.Command("ssh", args...)
	cmd.Env = os.Environ()
	cmd.Dir = "/"

	ptyReq, winCh, isPty := s.session.Pty()
	if isPty && ptyReq.Term != "" {
		cmd.Env = append(cmd.Env, "TERM="+ptyReq.Term)
	}

	// Start the ssh subprocess with a local PTY so it gets a real
	// controlling terminal. Set the initial size from the client.
	ws := &pty.Winsize{Rows: 24, Cols: 80}
	if isPty {
		ws.Rows = uint16(ptyReq.Window.Height)
		ws.Cols = uint16(ptyReq.Window.Width)
	}

	ptmx, err := pty.StartWithSize(cmd, ws)
	if err != nil {
		return fmt.Errorf("start ssh with pty: %w", err)
	}
	defer ptmx.Close()

	// Forward window-change events from the gateway session to the
	// local PTY. The inner ssh process receives SIGWINCH and forwards
	// the new size to the VM's sshd automatically.
	if isPty {
		go func() {
			for win := range winCh {
				pty.Setsize(ptmx, &pty.Winsize{
					Rows: uint16(win.Height),
					Cols: uint16(win.Width),
				})
			}
		}()
	}

	// Bidirectional copy between the gateway session and the PTY.
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(ptmx, s.session) // user -> ssh subprocess
		done <- struct{}{}
	}()
	go func() {
		io.Copy(s.session, ptmx) // ssh subprocess -> user
		done <- struct{}{}
	}()

	// Wait for the subprocess to exit, then drain any remaining output.
	if err := cmd.Wait(); err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return nil
		}
		return fmt.Errorf("connect to VM at %s: %w", vmIP, err)
	}
	return nil
}

// ── stop ──────────────────────────────────────────────────────────────

func cmdStop(s *Shell, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: stop <name>")
	}

	ctx := context.Background()
	name := args[0]

	vmRecord, err := s.gw.DB.VMByUserAndName(ctx, s.user.ID, name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no vm named %q", name)
		}
		return fmt.Errorf("lookup: %w", err)
	}

	if vmRecord.Status != "running" {
		return fmt.Errorf("vm %q is already %s", name, vmRecord.Status)
	}

	s.writef("  stopping %s...", name)

	// Unregister from metadata before stopping
	s.unregisterVMMetadata(ctx, vmRecord.ID)

	// Remove proxy route
	s.removeProxyRoute(ctx, name)

	if s.gw.VM != nil {
		if err := s.gw.VM.Stop(ctx, vmRecord.ID); err != nil {
			s.writef(" failed!\n")
			return fmt.Errorf("stop vm: %w", err)
		}
	} else {
		_ = s.gw.DB.UpdateVMStatus(ctx, vmRecord.ID, "stopped", nil, nil, nil, nil)
	}

	s.writeln(" done.")
	return nil
}

// ── restart ───────────────────────────────────────────────────────────

func cmdRestart(s *Shell, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: restart <name>")
	}

	ctx := context.Background()
	name := args[0]

	vmRecord, err := s.gw.DB.VMByUserAndName(ctx, s.user.ID, name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no vm named %q", name)
		}
		return fmt.Errorf("lookup: %w", err)
	}

	s.writef("  restarting %s...", name)

	if s.gw.VM != nil {
		// Unregister metadata before stopping
		s.unregisterVMMetadata(ctx, vmRecord.ID)

		// Remove old proxy route
		s.removeProxyRoute(ctx, name)

		// Stop if running
		if vmRecord.Status == "running" {
			if err := s.gw.VM.Stop(ctx, vmRecord.ID); err != nil {
				s.writef(" stop failed!\n")
				return fmt.Errorf("stop vm: %w", err)
			}
		}
		// Start again
		if err := s.gw.VM.Start(ctx, vmRecord.ID, name, vmRecord.Image, vmRecord.VCPU, vmRecord.MemoryMB, s.vmSSHKeys(ctx)); err != nil {
			s.writef(" start failed!\n")
			return fmt.Errorf("start vm: %w", err)
		}

		// Re-register metadata
		s.registerVMMetadata(ctx, vmRecord.ID, name, vmRecord.Image)

		// Re-register proxy route
		s.addProxyRoute(ctx, vmRecord.ID, name)
	}

	s.writeln(" done.")
	return nil
}

// ── start ─────────────────────────────────────────────────────────────

func cmdStart(s *Shell, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: start <name>")
	}

	ctx := context.Background()
	name := args[0]

	vmRecord, err := s.gw.DB.VMByUserAndName(ctx, s.user.ID, name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no vm named %q", name)
		}
		return fmt.Errorf("lookup: %w", err)
	}

	if vmRecord.Status == "running" {
		return fmt.Errorf("vm %q is already running", name)
	}

	s.writef("  starting %s...", name)

	if s.gw.VM != nil {
		if err := s.gw.VM.Start(ctx, vmRecord.ID, name, vmRecord.Image, vmRecord.VCPU, vmRecord.MemoryMB, s.vmSSHKeys(ctx)); err != nil {
			s.writef(" failed!\n")
			return fmt.Errorf("start vm: %w", err)
		}

		// Register metadata and proxy route
		s.registerVMMetadata(ctx, vmRecord.ID, name, vmRecord.Image)
		s.addProxyRoute(ctx, vmRecord.ID, name)
	} else {
		_ = s.gw.DB.UpdateVMStatus(ctx, vmRecord.ID, "running", nil, nil, nil, nil)
	}

	s.writeln(" done.")
	s.writef("  ssh %s to connect.\n", name)
	return nil
}

// ── cp ────────────────────────────────────────────────────────────────

func cmdCp(s *Shell, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: cp <name> [new-name]")
	}

	ctx := context.Background()
	srcName := args[0]
	dstName := ""
	if len(args) >= 2 {
		dstName = strings.ToLower(args[1])
	}

	srcVM, err := s.gw.DB.VMByUserAndName(ctx, s.user.ID, srcName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no vm named %q", srcName)
		}
		return fmt.Errorf("lookup: %w", err)
	}

	if dstName == "" {
		dstName = randomName()
	}
	if !validName.MatchString(dstName) {
		return fmt.Errorf("invalid name %q: must be 3-30 chars, lowercase letters/numbers/hyphens, start with letter", dstName)
	}

	// Check new name isn't taken
	existing, err := s.gw.DB.VMByUserAndName(ctx, s.user.ID, dstName)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check existing: %w", err)
	}
	if existing != nil && err == nil {
		return fmt.Errorf("vm %q already exists", dstName)
	}

	// Enforce VM quota
	vmCount, err := s.gw.DB.GetUserVMCount(ctx, s.user.ID)
	if err != nil {
		return fmt.Errorf("check vm count: %w", err)
	}
	limits := db.GetTrustLimits(s.user.TrustLevel)
	if limits.VMLimit >= 0 && vmCount >= limits.VMLimit {
		return fmt.Errorf("VM limit reached (%d/%d). Upgrade trust level or remove a VM.", vmCount, limits.VMLimit)
	}

	s.writef("  cloning %s -> %s...", srcName, dstName)

	// Create a new DB record with the same specs
	newVM, err := s.gw.DB.CreateVM(ctx, s.user.ID, dstName, srcVM.Image, srcVM.VCPU, srcVM.MemoryMB, srcVM.DiskGB)
	if err != nil {
		s.writef(" failed!\n")
		return fmt.Errorf("create clone record: %w", err)
	}

	// Clone disk images if VM manager is available
	if s.gw.VM != nil {
		if err := s.gw.VM.CloneDisks(ctx, srcVM.ID, newVM.ID); err != nil {
			s.writef(" failed!\n")
			s.gw.DB.DeleteVM(ctx, newVM.ID)
			return fmt.Errorf("clone disks: %w", err)
		}
	}

	_ = s.gw.DB.UpdateVMStatus(ctx, newVM.ID, "stopped", nil, nil, nil, nil)

	// Copy tags from source
	tags, err := s.gw.DB.TagsByVM(ctx, srcVM.ID)
	if err == nil {
		for _, tag := range tags {
			s.gw.DB.AddTag(ctx, newVM.ID, tag)
		}
	}

	s.writeln(" done!")
	s.writeln("")
	s.writef("  name:  %s\n", newVM.Name)
	s.writef("  image: %s\n", newVM.Image)
	s.writef("  url:   https://%s.%s\n", newVM.Name, s.gw.domain)
	s.writef("  start it with: start %s\n", newVM.Name)
	s.writeln("")
	return nil
}

// ── rm ────────────────────────────────────────────────────────────────

func cmdRm(s *Shell, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: rm <name>")
	}

	ctx := context.Background()
	name := args[0]

	vmRecord, err := s.gw.DB.VMByUserAndName(ctx, s.user.ID, name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no vm named %q", name)
		}
		return fmt.Errorf("lookup: %w", err)
	}
	if vmRecord == nil {
		return fmt.Errorf("no vm named %q", name)
	}

	// Confirm deletion
	s.writef("  are you sure? type the vm name to confirm: ")
	confirm, err := s.term.ReadLine()
	if err != nil {
		return fmt.Errorf("read confirmation: %w", err)
	}

	if strings.TrimSpace(confirm) != name {
		s.writeln("  cancelled.")
		return nil
	}

	// Use VM manager for full cleanup (stop + remove disks + DB)
	s.removeProxyRoute(ctx, name)
	s.unregisterVMMetadata(ctx, vmRecord.ID)

	if s.gw.VM != nil {
		if err := s.gw.VM.Destroy(ctx, vmRecord.ID); err != nil {
			return fmt.Errorf("destroy: %w", err)
		}
	} else {
		if err := s.gw.DB.DeleteVM(ctx, vmRecord.ID); err != nil {
			return fmt.Errorf("delete: %w", err)
		}
	}

	s.writef("  deleted %s.\n", name)
	return nil
}

// ── tag ───────────────────────────────────────────────────────────────

func cmdTag(s *Shell, args []string) error {
	ctx := context.Background()
	remove := false
	filteredArgs := args[:0]

	for _, a := range args {
		if a == "-d" || a == "--delete" {
			remove = true
		} else {
			filteredArgs = append(filteredArgs, a)
		}
	}

	if len(filteredArgs) < 2 {
		return fmt.Errorf("usage: tag [-d] <name> <tag>")
	}

	name := filteredArgs[0]
	tag := filteredArgs[1]

	vmRecord, err := s.gw.DB.VMByUserAndName(ctx, s.user.ID, name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no vm named %q", name)
		}
		return fmt.Errorf("lookup: %w", err)
	}

	if remove {
		if err := s.gw.DB.RemoveTag(ctx, vmRecord.ID, tag); err != nil {
			return fmt.Errorf("remove tag: %w", err)
		}
		s.writef("  removed tag %q from %s\n", tag, name)
	} else {
		if err := s.gw.DB.AddTag(ctx, vmRecord.ID, tag); err != nil {
			return fmt.Errorf("add tag: %w", err)
		}
		s.writef("  tagged %s with %q\n", name, tag)
	}

	return nil
}

// ── rename ────────────────────────────────────────────────────────────

func cmdRename(s *Shell, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: rename <old> <new>")
	}

	ctx := context.Background()
	oldName := args[0]
	newName := strings.ToLower(args[1])

	if !validName.MatchString(newName) {
		return fmt.Errorf("invalid name %q: must be 3-30 chars, lowercase letters/numbers/hyphens, start with letter", newName)
	}

	vmRecord, err := s.gw.DB.VMByUserAndName(ctx, s.user.ID, oldName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no vm named %q", oldName)
		}
		return fmt.Errorf("lookup: %w", err)
	}

	// Check new name isn't taken
	existing, err := s.gw.DB.VMByUserAndName(ctx, s.user.ID, newName)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check existing: %w", err)
	}
	if existing != nil && err == nil {
		return fmt.Errorf("vm %q already exists", newName)
	}

	if err := s.gw.DB.RenameVM(ctx, vmRecord.ID, newName); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	// Update proxy route if VM is running
	if vmRecord.Status == "running" {
		s.removeProxyRoute(ctx, oldName)
		s.addProxyRoute(ctx, vmRecord.ID, newName)
	}

	s.writef("  renamed %s -> %s\n", oldName, newName)
	s.writef("  new url: https://%s.%s\n", newName, s.gw.domain)
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────

func colorStatus(status string) string {
	switch status {
	case "running":
		return "\033[32m" + status + "\033[0m"
	case "stopped":
		return "\033[33m" + status + "\033[0m"
	case "creating":
		return "\033[36m" + status + "\033[0m"
	case "error":
		return "\033[31m" + status + "\033[0m"
	default:
		return status
	}
}

func relativeTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		return fmt.Sprintf("%dm ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		return fmt.Sprintf("%dh ago", h)
	case d < 30*24*time.Hour:
		days := int(d.Hours() / 24)
		return fmt.Sprintf("%dd ago", days)
	default:
		return t.Format("Jan 2")
	}
}

// Random name generation
var adjectives = []string{
	"swift", "bold", "calm", "dark", "eager",
	"fair", "glad", "keen", "live", "mild",
	"neat", "pale", "rare", "safe", "vast",
	"warm", "wise", "cool", "free", "pure",
}

var nouns = []string{
	"fox", "owl", "bee", "elk", "jay",
	"ant", "bat", "cat", "dog", "emu",
	"gnu", "hen", "ape", "yak", "ram",
	"cod", "doe", "ewe", "kit", "pup",
}

func randomName() string {
	a := adjectives[mathrand.Intn(len(adjectives))]
	n := nouns[mathrand.Intn(len(nouns))]
	return fmt.Sprintf("%s-%s", a, n)
}

// hasFlag checks if a flag is present in args and returns (found, filtered args).
func hasFlag(args []string, flags ...string) (bool, []string) {
	found := false
	filtered := make([]string, 0, len(args))
	for _, a := range args {
		isFlag := false
		for _, f := range flags {
			if a == f {
				isFlag = true
				found = true
				break
			}
		}
		if !isFlag {
			filtered = append(filtered, a)
		}
	}
	return found, filtered
}

// writeJSON encodes v as indented JSON to the shell.
func (s *Shell) writeJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	s.writeln(string(data))
	return nil
}

// generateLinkToken creates a random URL-safe token for share links.
func generateLinkToken() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func normalizeShareLinkToken(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	if idx := strings.Index(input, "ussy_share="); idx >= 0 {
		return strings.TrimSpace(input[idx+len("ussy_share="):])
	}
	if idx := strings.LastIndex(input, "/"); idx >= 0 && idx < len(input)-1 && strings.Contains(input, "://") {
		return strings.TrimSpace(input[idx+1:])
	}
	return input
}

// ── ssh-key ──────────────────────────────────────────────────────────

func cmdSSHKey(s *Shell, args []string) error {
	if len(args) == 0 {
		return cmdSSHKeyHelp(s)
	}

	switch args[0] {
	case "list", "ls":
		return cmdSSHKeyList(s, args[1:])
	case "add":
		return cmdSSHKeyAdd(s, args[1:])
	case "remove", "rm":
		return cmdSSHKeyRemove(s, args[1:])
	case "help":
		return cmdSSHKeyHelp(s)
	default:
		return fmt.Errorf("unknown ssh-key subcommand %q. try: ssh-key help", args[0])
	}
}

func cmdSSHKeyHelp(s *Shell) error {
	s.writeln("")
	s.writeln("  \033[1mssh-key\033[0m -- manage your SSH keys")
	s.writeln("")
	s.writeln("    ssh-key list          list your keys")
	s.writeln("    ssh-key add           add a new key (paste authorized_keys format)")
	s.writeln("    ssh-key remove <id>   remove a key by ID")
	s.writeln("")
	return nil
}

func cmdSSHKeyList(s *Shell, args []string) error {
	ctx := context.Background()
	jsonOut, _ := hasFlag(args, "--json")

	keys, err := s.gw.DB.SSHKeysByUser(ctx, s.user.ID)
	if err != nil {
		return fmt.Errorf("list keys: %w", err)
	}

	if jsonOut {
		type keyJSON struct {
			ID          int64  `json:"id"`
			Fingerprint string `json:"fingerprint"`
			Comment     string `json:"comment,omitempty"`
			CreatedAt   string `json:"created_at"`
		}
		out := make([]keyJSON, len(keys))
		for i, k := range keys {
			out[i] = keyJSON{
				ID:          k.ID,
				Fingerprint: k.Fingerprint,
				Comment:     k.Comment,
				CreatedAt:   k.CreatedAt.Format(time.RFC3339),
			}
		}
		return s.writeJSON(out)
	}

	if len(keys) == 0 {
		s.writeln("  no ssh keys found.")
		return nil
	}

	s.writeln("")
	s.writef("  %-4s  %-48s  %-12s  %s\n", "ID", "FINGERPRINT", "COMMENT", "ADDED")
	s.writef("  %-4s  %-48s  %-12s  %s\n", "──", "───────────", "───────", "─────")
	for _, k := range keys {
		s.writef("  %-4d  %-48s  %-12s  %s\n",
			k.ID, k.Fingerprint, k.Comment, relativeTime(k.CreatedAt.Time))
	}
	s.writeln("")
	return nil
}

func cmdSSHKeyAdd(s *Shell, args []string) error {
	ctx := context.Background()

	s.writeln("  paste your public key (authorized_keys format):")
	s.writeln("  (e.g. ssh-ed25519 AAAA... comment)")
	s.writeln("")

	line, err := s.term.ReadLine()
	if err != nil {
		return fmt.Errorf("read key: %w", err)
	}

	line = strings.TrimSpace(line)
	if line == "" {
		return fmt.Errorf("no key provided")
	}

	// Parse the key to validate and get fingerprint
	pubKey, comment, _, _, err := gossh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return fmt.Errorf("invalid key format: %w", err)
	}

	fingerprint := gossh.FingerprintSHA256(pubKey)

	// Check it's not a duplicate
	existingKeys, err := s.gw.DB.SSHKeysByUser(ctx, s.user.ID)
	if err != nil {
		return fmt.Errorf("check existing: %w", err)
	}
	for _, k := range existingKeys {
		if k.Fingerprint == fingerprint {
			return fmt.Errorf("key already registered (fingerprint: %s)", fingerprint)
		}
	}

	key, err := s.gw.DB.AddSSHKey(ctx, s.user.ID, line, fingerprint, comment)
	if err != nil {
		return fmt.Errorf("add key: %w", err)
	}

	s.writeln("")
	s.writef("  key added (id=%d, fingerprint=%s)\n", key.ID, key.Fingerprint)
	s.writeln("  you can now authenticate with this key.")
	s.writeln("")
	return nil
}

func cmdSSHKeyRemove(s *Shell, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: ssh-key remove <id>")
	}

	ctx := context.Background()

	var keyID int64
	if _, err := fmt.Sscanf(args[0], "%d", &keyID); err != nil {
		return fmt.Errorf("invalid key ID %q", args[0])
	}

	keys, err := s.gw.DB.SSHKeysByUser(ctx, s.user.ID)
	if err != nil {
		return fmt.Errorf("list keys: %w", err)
	}

	found := false
	for _, k := range keys {
		if k.ID == keyID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("key %d not found", keyID)
	}

	count, err := s.gw.DB.SSHKeyCountByUser(ctx, s.user.ID)
	if err != nil {
		return fmt.Errorf("count keys: %w", err)
	}
	if count <= 1 {
		return fmt.Errorf("can't remove your last SSH key (you'd be locked out)")
	}

	if err := s.gw.DB.DeleteSSHKey(ctx, keyID); err != nil {
		return fmt.Errorf("delete key: %w", err)
	}

	s.writef("  removed key %d.\n", keyID)
	return nil
}

// ── share ────────────────────────────────────────────────────────────

func cmdShare(s *Shell, args []string) error {
	if len(args) == 0 {
		return cmdShareHelp(s)
	}

	switch args[0] {
	case "add":
		return cmdShareAdd(s, args[1:])
	case "remove", "rm":
		return cmdShareRemove(s, args[1:])
	case "add-link":
		return cmdShareAddLink(s, args[1:])
	case "remove-link":
		return cmdShareRemoveLink(s, args[1:])
	case "set-public":
		return cmdShareSetPublic(s, args[1:], true)
	case "set-private":
		return cmdShareSetPublic(s, args[1:], false)
	case "show", "list", "ls":
		return cmdShareList(s, args[1:])
	case "cname":
		return cmdShareCname(s, args[1:])
	case "cname-verify":
		return cmdShareCnameVerify(s, args[1:])
	case "cname-rm":
		return cmdShareCnameRm(s, args[1:])
	case "help":
		return cmdShareHelp(s)
	default:
		return fmt.Errorf("unknown share subcommand %q. try: share help", args[0])
	}
}

func cmdShareHelp(s *Shell) error {
	s.writeln("")
	s.writeln("  \033[1mshare\033[0m -- share access to your environments")
	s.writeln("")
	s.writeln("    share add <vm> <handle>      share with a user by handle")
	s.writeln("    share remove <vm> <handle>   revoke a user's access")
	s.writeln("    share add-link <vm>          create a shareable link")
	s.writeln("    share remove-link <vm> <token-or-url>  revoke a share link")
	s.writeln("    share set-public <vm>        make HTTPS endpoint public")
	s.writeln("    share set-private <vm>       make HTTPS endpoint private")
	s.writeln("    share show <vm>              show share state for a VM")
	s.writeln("")
	s.writeln("  \033[1mcustom domains\033[0m")
	s.writeln("    share cname <vm> <domain>         add a custom domain")
	s.writeln("    share cname-verify <vm> <domain>  verify DNS ownership")
	s.writeln("    share cname-rm <vm> <domain>      remove a custom domain")
	s.writeln("")
	return nil
}

func cmdShareAdd(s *Shell, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: share add <vm> <handle>")
	}

	ctx := context.Background()
	vmName := args[0]
	targetHandle := args[1]

	vmRecord, err := s.gw.DB.VMByUserAndName(ctx, s.user.ID, vmName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no vm named %q", vmName)
		}
		return fmt.Errorf("lookup vm: %w", err)
	}

	targetUser, err := s.gw.DB.UserByHandle(ctx, targetHandle)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no user named %q", targetHandle)
		}
		return fmt.Errorf("lookup user: %w", err)
	}

	if targetUser.ID == s.user.ID {
		return fmt.Errorf("you already own this VM")
	}

	if err := s.gw.DB.ShareVMWithUser(ctx, vmRecord.ID, targetUser.ID); err != nil {
		return fmt.Errorf("share: %w", err)
	}

	s.writef("  shared %s with %s\n", vmName, targetHandle)
	return nil
}

func cmdShareRemove(s *Shell, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: share remove <vm> <handle>")
	}

	ctx := context.Background()
	vmName := args[0]
	targetHandle := args[1]

	vmRecord, err := s.gw.DB.VMByUserAndName(ctx, s.user.ID, vmName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no vm named %q", vmName)
		}
		return fmt.Errorf("lookup vm: %w", err)
	}

	targetUser, err := s.gw.DB.UserByHandle(ctx, targetHandle)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no user named %q", targetHandle)
		}
		return fmt.Errorf("lookup user: %w", err)
	}

	if err := s.gw.DB.RemoveShareByVMAndUser(ctx, vmRecord.ID, targetUser.ID); err != nil {
		return fmt.Errorf("remove share: %w", err)
	}

	s.writef("  removed %s's access to %s\n", targetHandle, vmName)
	return nil
}

func cmdShareAddLink(s *Shell, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: share add-link <vm>")
	}

	ctx := context.Background()
	vmName := args[0]

	vmRecord, err := s.gw.DB.VMByUserAndName(ctx, s.user.ID, vmName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no vm named %q", vmName)
		}
		return fmt.Errorf("lookup vm: %w", err)
	}

	token, err := generateLinkToken()
	if err != nil {
		return fmt.Errorf("generate token: %w", err)
	}

	_, err = s.gw.DB.ShareVMWithLink(ctx, vmRecord.ID, token)
	if err != nil {
		return fmt.Errorf("create link share: %w", err)
	}

	s.writeln("")
	s.writef("  https://%s.%s/?ussy_share=%s\n", vmName, s.gw.domain, token)
	s.writeln("")
	s.writeln("  opening the link redeems a share cookie for this VM.")
	s.writeln("")
	return nil
}

func cmdShareRemoveLink(s *Shell, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: share remove-link <vm> <token-or-url>")
	}

	ctx := context.Background()
	vmName := args[0]
	token := normalizeShareLinkToken(args[1])
	if token == "" {
		return fmt.Errorf("invalid share link token")
	}

	vmRecord, err := s.gw.DB.VMByUserAndName(ctx, s.user.ID, vmName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no vm named %q", vmName)
		}
		return fmt.Errorf("lookup vm: %w", err)
	}

	if err := s.gw.DB.RemoveShareLink(ctx, vmRecord.ID, token); err != nil {
		return fmt.Errorf("remove share link: %w", err)
	}

	s.writef("  removed share link for %s\n", vmName)
	return nil
}

func cmdShareSetPublic(s *Shell, args []string, public bool) error {
	if len(args) == 0 {
		if public {
			return fmt.Errorf("usage: share set-public <vm>")
		}
		return fmt.Errorf("usage: share set-private <vm>")
	}

	ctx := context.Background()
	vmName := args[0]

	vmRecord, err := s.gw.DB.VMByUserAndName(ctx, s.user.ID, vmName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no vm named %q", vmName)
		}
		return fmt.Errorf("lookup vm: %w", err)
	}

	if err := s.gw.DB.SetVMPublic(ctx, vmRecord.ID, public); err != nil {
		return fmt.Errorf("set public: %w", err)
	}

	if public {
		s.writef("  %s is now public at https://%s.%s/\n", vmName, vmName, s.gw.domain)
	} else {
		s.writef("  %s is now private.\n", vmName)
	}
	return nil
}

func cmdShareList(s *Shell, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: share list <vm>")
	}

	ctx := context.Background()
	vmName := args[0]
	jsonOut, _ := hasFlag(args[1:], "--json")

	vmRecord, err := s.gw.DB.VMByUserAndName(ctx, s.user.ID, vmName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no vm named %q", vmName)
		}
		return fmt.Errorf("lookup vm: %w", err)
	}

	shares, err := s.gw.DB.SharesByVM(ctx, vmRecord.ID)
	if err != nil {
		return fmt.Errorf("list shares: %w", err)
	}

	isPublic, _ := s.gw.DB.IsVMPublic(ctx, vmRecord.ID)

	if jsonOut {
		type shareJSON struct {
			ID        int64  `json:"id"`
			Type      string `json:"type"`
			Target    string `json:"target,omitempty"`
			Link      string `json:"link,omitempty"`
			CreatedAt string `json:"created_at"`
		}
		out := struct {
			VM       string      `json:"vm"`
			IsPublic bool        `json:"is_public"`
			Shares   []shareJSON `json:"shares"`
		}{
			VM:       vmName,
			IsPublic: isPublic,
			Shares:   make([]shareJSON, 0),
		}
		for _, sh := range shares {
			sj := shareJSON{
				ID:        sh.ID,
				CreatedAt: sh.CreatedAt.Format(time.RFC3339),
			}
			if sh.SharedWith.Valid {
				sj.Type = "user"
				if u, err := s.gw.DB.UserByID(ctx, sh.SharedWith.Int64); err == nil {
					sj.Target = u.Handle
				}
			} else if sh.LinkToken.Valid {
				sj.Type = "link"
				sj.Link = fmt.Sprintf("https://%s.%s/?ussy_share=%s", vmName, s.gw.domain, sh.LinkToken.String)
			} else if sh.IsPublic {
				sj.Type = "public"
			}
			out.Shares = append(out.Shares, sj)
		}
		return s.writeJSON(out)
	}

	s.writeln("")
	if isPublic {
		s.writef("  %s is \033[32mpublic\033[0m\n", vmName)
	} else {
		s.writef("  %s is \033[33mprivate\033[0m\n", vmName)
	}

	if len(shares) == 0 {
		s.writeln("  no shares.")
	} else {
		s.writeln("")
		for _, sh := range shares {
			if sh.SharedWith.Valid {
				if u, err := s.gw.DB.UserByID(ctx, sh.SharedWith.Int64); err == nil {
					s.writef("  [user]  %s  (since %s)\n", u.Handle, relativeTime(sh.CreatedAt.Time))
				}
			} else if sh.LinkToken.Valid {
				s.writef("  [link]  https://%s.%s/?ussy_share=%s  (since %s)\n",
					vmName, s.gw.domain, sh.LinkToken.String, relativeTime(sh.CreatedAt.Time))
			}
		}
	}
	s.writeln("")
	return nil
}

// ── share cname ──────────────────────────────────────────────────────

// cmdShareCname adds a custom domain mapping for a VM.
// Usage: share cname <vm> <domain>
//
// This creates a DNS verification challenge. The user must add a TXT record
// at _ussycode-verify.<domain> with the provided token, then run
// `share cname-verify <vm> <domain>` to complete verification.
func cmdShareCname(s *Shell, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: share cname <vm> <domain>")
	}

	ctx := context.Background()
	vmName := args[0]
	domain := strings.ToLower(strings.TrimSpace(args[1]))

	// Basic domain validation
	if !isValidDomain(domain) {
		return fmt.Errorf("invalid domain %q", domain)
	}

	// Look up the VM
	vmRecord, err := s.gw.DB.VMByUserAndName(ctx, s.user.ID, vmName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no vm named %q", vmName)
		}
		return fmt.Errorf("lookup vm: %w", err)
	}

	// Check if domain is already registered
	existing, err := s.gw.DB.GetCustomDomain(ctx, domain)
	if err == nil && existing != nil {
		if existing.VMID == vmRecord.ID {
			if existing.Verified {
				return fmt.Errorf("domain %s is already verified for %s", domain, vmName)
			}
			s.writeln("")
			s.writef("  domain %s is already pending verification for %s.\n", domain, vmName)
			s.writeln("  add this DNS TXT record to verify:")
			s.writeln("")
			s.writef("    _ussycode-verify.%s  TXT  %s\n", domain, existing.VerificationToken.String)
			s.writeln("")
			s.writeln("  then run: share cname-verify " + vmName + " " + domain)
			s.writeln("")
			return nil
		}
		return fmt.Errorf("domain %s is already registered to another VM", domain)
	}

	// Generate a verification token
	token, err := generateLinkToken()
	if err != nil {
		return fmt.Errorf("generate verification token: %w", err)
	}

	// Store the custom domain record
	if err := s.gw.DB.CreateCustomDomain(ctx, vmRecord.ID, domain, token); err != nil {
		return fmt.Errorf("create custom domain: %w", err)
	}

	s.writeln("")
	s.writef("  custom domain %s added for %s.\n", domain, vmName)
	s.writeln("")
	s.writeln("  to verify ownership, add this DNS TXT record:")
	s.writeln("")
	s.writef("    _ussycode-verify.%s  TXT  %s\n", domain, token)
	s.writeln("")
	s.writeln("  also add a CNAME record pointing to your VM:")
	s.writeln("")
	s.writef("    %s  CNAME  %s.%s\n", domain, vmName, s.gw.domain)
	s.writeln("")
	s.writef("  then run: share cname-verify %s %s\n", vmName, domain)
	s.writeln("")
	return nil
}

// cmdShareCnameVerify verifies DNS ownership of a custom domain by
// looking up the TXT record at _ussycode-verify.<domain>.
func cmdShareCnameVerify(s *Shell, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: share cname-verify <vm> <domain>")
	}

	ctx := context.Background()
	vmName := args[0]
	domain := strings.ToLower(strings.TrimSpace(args[1]))

	// Look up the VM
	vmRecord, err := s.gw.DB.VMByUserAndName(ctx, s.user.ID, vmName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no vm named %q", vmName)
		}
		return fmt.Errorf("lookup vm: %w", err)
	}

	// Look up the custom domain record
	cd, err := s.gw.DB.GetCustomDomain(ctx, domain)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no custom domain %q found. add it with: share cname %s %s", domain, vmName, domain)
		}
		return fmt.Errorf("lookup custom domain: %w", err)
	}

	if cd.VMID != vmRecord.ID {
		return fmt.Errorf("domain %s is not associated with %s", domain, vmName)
	}

	if cd.Verified {
		s.writef("  domain %s is already verified.\n", domain)
		return nil
	}

	// Look up DNS TXT records for _ussycode-verify.<domain>
	verifyHost := "_ussycode-verify." + domain
	expectedToken := cd.VerificationToken.String

	s.writef("  checking DNS TXT record at %s...\n", verifyHost)

	txtRecords, err := net.LookupTXT(verifyHost)
	if err != nil {
		return fmt.Errorf("DNS lookup failed for %s: %w\n  make sure you added the TXT record and DNS has propagated", verifyHost, err)
	}

	// Check if any TXT record matches the expected token
	found := false
	for _, txt := range txtRecords {
		if strings.TrimSpace(txt) == expectedToken {
			found = true
			break
		}
	}

	if !found {
		s.writeln("")
		s.writef("  verification failed: no matching TXT record found at %s\n", verifyHost)
		s.writeln("")
		s.writeln("  expected TXT value:")
		s.writef("    %s\n", expectedToken)
		s.writeln("")
		s.writeln("  found:")
		for _, txt := range txtRecords {
			s.writef("    %s\n", txt)
		}
		if len(txtRecords) == 0 {
			s.writeln("    (none)")
		}
		s.writeln("")
		s.writeln("  DNS changes can take up to 48 hours to propagate. try again later.")
		s.writeln("")
		return nil
	}

	// Mark as verified in DB
	if err := s.gw.DB.VerifyCustomDomain(ctx, domain); err != nil {
		return fmt.Errorf("verify custom domain: %w", err)
	}

	// Add the custom domain route to the proxy if VM is running
	if s.gw.Proxy != nil && vmRecord.Status == "running" {
		if err := s.gw.Proxy.AddCustomDomain(ctx, domain, vmName); err != nil {
			s.gw.Proxy.Logger().Warn("failed to add custom domain proxy route",
				"domain", domain, "vm", vmName, "error", err)
			s.writef("  warning: proxy route creation failed (domain verified but routing may not work yet)\n")
		}
	}

	s.writeln("")
	s.writef("  domain %s verified and active for %s!\n", domain, vmName)
	s.writef("  https://%s is now live.\n", domain)
	s.writeln("")
	return nil
}

// cmdShareCnameRm removes a custom domain mapping from a VM.
func cmdShareCnameRm(s *Shell, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: share cname-rm <vm> <domain>")
	}

	ctx := context.Background()
	vmName := args[0]
	domain := strings.ToLower(strings.TrimSpace(args[1]))

	// Look up the VM
	vmRecord, err := s.gw.DB.VMByUserAndName(ctx, s.user.ID, vmName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no vm named %q", vmName)
		}
		return fmt.Errorf("lookup vm: %w", err)
	}

	// Look up the custom domain record
	cd, err := s.gw.DB.GetCustomDomain(ctx, domain)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no custom domain %q found for %s", domain, vmName)
		}
		return fmt.Errorf("lookup custom domain: %w", err)
	}

	if cd.VMID != vmRecord.ID {
		return fmt.Errorf("domain %s is not associated with %s", domain, vmName)
	}

	// Remove proxy route if it was verified
	if cd.Verified && s.gw.Proxy != nil {
		if err := s.gw.Proxy.RemoveCustomDomain(ctx, domain); err != nil {
			s.gw.Proxy.Logger().Warn("failed to remove custom domain proxy route",
				"domain", domain, "error", err)
		}
	}

	// Delete from DB
	if err := s.gw.DB.DeleteCustomDomain(ctx, domain); err != nil {
		return fmt.Errorf("delete custom domain: %w", err)
	}

	s.writef("  removed custom domain %s from %s.\n", domain, vmName)
	return nil
}

// isValidDomain performs basic validation on a domain name.
func isValidDomain(domain string) bool {
	if len(domain) < 3 || len(domain) > 253 {
		return false
	}
	// Must contain at least one dot (not a bare hostname)
	if !strings.Contains(domain, ".") {
		return false
	}
	// Must not start or end with a dot or hyphen
	if strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") ||
		strings.HasPrefix(domain, "-") || strings.HasSuffix(domain, "-") {
		return false
	}
	// Check each label
	labels := strings.Split(domain, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		for _, c := range label {
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
				return false
			}
		}
	}
	return true
}

// ── llm-key ──────────────────────────────────────────────────────────

func cmdLLMKey(s *Shell, args []string) error {
	if len(args) == 0 {
		return cmdLLMKeyHelp(s)
	}

	switch args[0] {
	case "set":
		return cmdLLMKeySet(s, args[1:])
	case "list", "ls":
		return cmdLLMKeyList(s, args[1:])
	case "remove", "rm":
		return cmdLLMKeyRemove(s, args[1:])
	case "help":
		return cmdLLMKeyHelp(s)
	default:
		return fmt.Errorf("unknown llm-key subcommand %q. try: llm-key help", args[0])
	}
}

func cmdLLMKeyHelp(s *Shell) error {
	s.writeln("")
	s.writeln("  \033[1mllm-key\033[0m -- manage your LLM API keys")
	s.writeln("")
	s.writeln("    llm-key set <provider> <key>   store an API key")
	s.writeln("    llm-key list                   show configured providers")
	s.writeln("    llm-key rm <provider>          remove an API key")
	s.writeln("")
	s.writeln("  supported providers: anthropic, openai, fireworks")
	s.writeln("  self-hosted (ollama, vllm) don't need keys.")
	s.writeln("")
	return nil
}

func cmdLLMKeySet(s *Shell, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: llm-key set <provider> <key>")
	}

	ctx := context.Background()
	provider := strings.ToLower(args[0])
	key := args[1]

	// Validate provider is a known BYOK provider
	validProviders := map[string]bool{
		"anthropic": true,
		"openai":    true,
		"fireworks": true,
	}
	if !validProviders[provider] {
		return fmt.Errorf("unknown BYOK provider %q. Supported: anthropic, openai, fireworks", provider)
	}

	if key == "" {
		return fmt.Errorf("API key must not be empty")
	}

	// Use the LLM gateway to encrypt and store
	if s.gw.LLMGateway == nil {
		return fmt.Errorf("LLM gateway not configured on this server")
	}

	if err := s.gw.LLMGateway.SetUserKey(ctx, s.user.ID, provider, key); err != nil {
		return fmt.Errorf("store key: %w", err)
	}

	s.writef("  API key for %s stored successfully.\n", provider)
	s.writef("  Your VMs can now use /gateway/llm/%s\n", provider)
	return nil
}

func cmdLLMKeyList(s *Shell, args []string) error {
	ctx := context.Background()

	providers, err := s.gw.DB.LLMKeyProvidersByUser(ctx, s.user.ID)
	if err != nil {
		return fmt.Errorf("list keys: %w", err)
	}

	if len(providers) == 0 {
		s.writeln("  no LLM API keys configured.")
		s.writeln("  use 'llm-key set <provider> <key>' to add one.")
		return nil
	}

	s.writeln("")
	s.writeln("  configured LLM providers:")
	s.writeln("")
	for _, p := range providers {
		s.writef("    - %s\n", p)
	}
	s.writeln("")
	s.writeln("  (keys are stored encrypted; use 'llm-key rm <provider>' to remove)")
	s.writeln("")
	return nil
}

func cmdLLMKeyRemove(s *Shell, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: llm-key rm <provider>")
	}

	ctx := context.Background()
	provider := strings.ToLower(args[0])

	if err := s.gw.DB.DeleteLLMKey(ctx, s.user.ID, provider); err != nil {
		return fmt.Errorf("remove key: %w", err)
	}

	s.writef("  removed API key for %s.\n", provider)
	return nil
}

// ── admin ────────────────────────────────────────────────────────────

func cmdAdmin(s *Shell, args []string) error {
	// Gate: only admin-level users can use admin commands
	if s.user.TrustLevel != "admin" {
		return fmt.Errorf("permission denied: admin commands require admin trust level")
	}

	if len(args) == 0 {
		return cmdAdminHelp(s)
	}

	switch args[0] {
	case "set-trust":
		return cmdAdminSetTrust(s, args[1:])
	case "help":
		return cmdAdminHelp(s)
	default:
		return fmt.Errorf("unknown admin subcommand %q. try: admin help", args[0])
	}
}

func cmdAdminHelp(s *Shell) error {
	s.writeln("")
	s.writeln("  \033[1madmin\033[0m -- admin-only commands")
	s.writeln("")
	s.writeln("    admin set-trust <handle> <level>   set user trust level")
	s.writeln("")
	s.writeln("  trust levels: newbie, citizen, operator, admin")
	s.writeln("")
	return nil
}

func cmdAdminSetTrust(s *Shell, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: admin set-trust <handle> <level>")
	}

	ctx := context.Background()
	handle := args[0]
	level := strings.ToLower(args[1])

	if !db.IsValidTrustLevel(level) {
		return fmt.Errorf("invalid trust level %q. valid levels: newbie, citizen, operator, admin", level)
	}

	targetUser, err := s.gw.DB.UserByHandle(ctx, handle)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no user named %q", handle)
		}
		return fmt.Errorf("lookup user: %w", err)
	}

	oldLevel := targetUser.TrustLevel
	if err := s.gw.DB.SetUserTrustLevel(ctx, targetUser.ID, level); err != nil {
		return fmt.Errorf("set trust level: %w", err)
	}

	s.writef("  %s: %s -> %s\n", handle, oldLevel, level)
	return nil
}
