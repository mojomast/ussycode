// Package vm implements microVM lifecycle management, OCI image pulling,
// rootfs creation, and Firecracker integration for ussycode.
package vm

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// ImageConfig holds extracted OCI image config metadata needed to
// configure the guest VM (entrypoint, cmd, env, exposed ports, etc.).
type ImageConfig struct {
	Entrypoint   []string
	Cmd          []string
	Env          []string
	ExposedPorts []string
	WorkingDir   string
	User         string
}

// ImageManager handles pulling OCI images, extracting rootfs layers,
// and building ext4 filesystem images for Firecracker.
type ImageManager struct {
	cacheDir string
	logger   *slog.Logger
	mu       sync.Mutex // serialize pulls for the same image
}

// NewImageManager creates a new ImageManager.
// cacheDir is where built rootfs images are stored (e.g., /var/lib/ussycode/images).
func NewImageManager(cacheDir string, logger *slog.Logger) (*ImageManager, error) {
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	return &ImageManager{cacheDir: cacheDir, logger: logger}, nil
}

// EnsureRootfs returns the path to a cached ext4 rootfs image for the given
// OCI image reference. If the image hasn't been pulled before, it pulls it,
// extracts the rootfs, and creates an ext4 image. Returns the path to the
// ext4 file and the extracted image config.
func (im *ImageManager) EnsureRootfs(ctx context.Context, imageRef string) (string, *ImageConfig, error) {
	im.mu.Lock()
	defer im.mu.Unlock()

	im.logger.Info("ensuring rootfs", "image", imageRef)

	// Parse the image reference
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return "", nil, fmt.Errorf("parse image ref %q: %w", imageRef, err)
	}

	// Pull the image descriptor (resolves manifest lists to platform-specific image)
	desc, err := remote.Get(ref,
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
		remote.WithPlatform(v1.Platform{
			OS:           "linux",
			Architecture: runtime.GOARCH,
		}),
	)
	if err != nil {
		return "", nil, fmt.Errorf("pull image %q: %w", imageRef, err)
	}

	// Get the image from the descriptor
	img, err := desc.Image()
	if err != nil {
		return "", nil, fmt.Errorf("get image from descriptor: %w", err)
	}

	// Use the image digest as the cache key
	digest, err := img.Digest()
	if err != nil {
		return "", nil, fmt.Errorf("get image digest: %w", err)
	}

	// Check cache
	cacheKey := strings.ReplaceAll(digest.String(), ":", "-")
	ext4Path := filepath.Join(im.cacheDir, cacheKey+".ext4")

	if _, err := os.Stat(ext4Path); err == nil {
		im.logger.Info("using cached rootfs", "image", imageRef, "path", ext4Path)
		// Still need to extract config
		cfg, err := im.extractConfig(img)
		if err != nil {
			return "", nil, err
		}
		return ext4Path, cfg, nil
	}

	im.logger.Info("pulling and building rootfs", "image", imageRef, "digest", digest.String())

	// Extract image configuration
	cfg, err := im.extractConfig(img)
	if err != nil {
		return "", nil, err
	}

	// Extract flattened rootfs to a temp directory
	tmpDir, err := os.MkdirTemp("", "ussycode-rootfs-*")
	if err != nil {
		return "", nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := im.extractRootfs(ctx, img, tmpDir); err != nil {
		return "", nil, fmt.Errorf("extract rootfs: %w", err)
	}

	// Ensure essential directories exist in the rootfs
	if err := im.ensureEssentialDirs(tmpDir); err != nil {
		return "", nil, fmt.Errorf("ensure essential dirs: %w", err)
	}

	// Create ext4 filesystem image from the extracted directory
	tmpExt4 := ext4Path + ".tmp"
	if err := im.createExt4(ctx, tmpDir, tmpExt4); err != nil {
		os.Remove(tmpExt4)
		return "", nil, fmt.Errorf("create ext4: %w", err)
	}

	// Atomic rename into cache
	if err := os.Rename(tmpExt4, ext4Path); err != nil {
		os.Remove(tmpExt4)
		return "", nil, fmt.Errorf("rename ext4 to cache: %w", err)
	}

	im.logger.Info("rootfs ready", "image", imageRef, "path", ext4Path)
	return ext4Path, cfg, nil
}

// extractConfig reads the OCI image config and returns our simplified ImageConfig.
func (im *ImageManager) extractConfig(img v1.Image) (*ImageConfig, error) {
	cfgFile, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("read image config: %w", err)
	}

	cfg := &ImageConfig{
		Entrypoint: cfgFile.Config.Entrypoint,
		Cmd:        cfgFile.Config.Cmd,
		Env:        cfgFile.Config.Env,
		WorkingDir: cfgFile.Config.WorkingDir,
		User:       cfgFile.Config.User,
	}

	for port := range cfgFile.Config.ExposedPorts {
		cfg.ExposedPorts = append(cfg.ExposedPorts, port)
	}

	return cfg, nil
}

// extractRootfs flattens all image layers into a single directory, properly
// handling OCI whiteout files for deleted entries between layers.
func (im *ImageManager) extractRootfs(ctx context.Context, img v1.Image, destDir string) error {
	// mutate.Extract returns a flattened tar stream that already handles whiteouts
	rc := mutate.Extract(img)
	defer rc.Close()

	tr := tar.NewReader(rc)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}

		// Sanitize path - prevent directory traversal
		target := filepath.Join(destDir, filepath.Clean("/"+hdr.Name))
		if !strings.HasPrefix(target, destDir) {
			im.logger.Warn("skipping path outside rootfs", "path", hdr.Name)
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return fmt.Errorf("mkdir %s: %w", hdr.Name, err)
			}

		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("mkdir parent %s: %w", hdr.Name, err)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return fmt.Errorf("create %s: %w", hdr.Name, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("write %s: %w", hdr.Name, err)
			}
			f.Close()

		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("mkdir parent for symlink %s: %w", hdr.Name, err)
			}
			// Remove existing file/symlink before creating
			os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return fmt.Errorf("symlink %s -> %s: %w", hdr.Name, hdr.Linkname, err)
			}

		case tar.TypeLink:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("mkdir parent for hardlink %s: %w", hdr.Name, err)
			}
			linkTarget := filepath.Join(destDir, filepath.Clean("/"+hdr.Linkname))
			os.Remove(target)
			if err := os.Link(linkTarget, target); err != nil {
				return fmt.Errorf("hardlink %s -> %s: %w", hdr.Name, hdr.Linkname, err)
			}

		case tar.TypeChar, tar.TypeBlock:
			// Skip device nodes -- we can't create them without root,
			// and they'll be provided by the guest kernel anyway
			im.logger.Debug("skipping device node", "path", hdr.Name)

		case tar.TypeFifo:
			// Skip FIFOs
			im.logger.Debug("skipping fifo", "path", hdr.Name)

		default:
			im.logger.Debug("skipping unknown tar entry type", "path", hdr.Name, "type", hdr.Typeflag)
		}
	}

	return nil
}

// ensureEssentialDirs creates directories that must exist in the rootfs
// for the VM to boot properly, even if the container image doesn't include them.
func (im *ImageManager) ensureEssentialDirs(rootDir string) error {
	essentials := []string{
		"dev",
		"proc",
		"sys",
		"run",
		"tmp",
		"var/run",
		"var/log",
		"root",
		"home",
		"etc",
	}

	for _, dir := range essentials {
		path := filepath.Join(rootDir, dir)
		if err := os.MkdirAll(path, 0755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}

	// Ensure /etc/resolv.conf exists with a default DNS
	resolvConf := filepath.Join(rootDir, "etc", "resolv.conf")
	if _, err := os.Stat(resolvConf); os.IsNotExist(err) {
		if err := os.WriteFile(resolvConf, []byte("nameserver 8.8.8.8\nnameserver 8.8.4.4\n"), 0644); err != nil {
			return fmt.Errorf("write resolv.conf: %w", err)
		}
	}

	// Ensure /etc/hostname exists
	hostname := filepath.Join(rootDir, "etc", "hostname")
	if _, err := os.Stat(hostname); os.IsNotExist(err) {
		if err := os.WriteFile(hostname, []byte("ussycode\n"), 0644); err != nil {
			return fmt.Errorf("write hostname: %w", err)
		}
	}

	return nil
}

// createExt4 builds an ext4 filesystem image from a directory using mkfs.ext4 -d.
// This requires e2fsprogs >= 1.43 which supports populating from a directory.
// After creation, the image is shrunk to minimum size with resize2fs -M.
func (im *ImageManager) createExt4(ctx context.Context, srcDir, outPath string) error {
	// Calculate the size of the source directory
	var totalSize int64
	err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			totalSize += info.Size()
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("calculate dir size: %w", err)
	}

	// Allocate 1.5x the content size + 64MB for filesystem overhead
	// Minimum 128MB to ensure ext4 has enough space for metadata
	imageSize := int64(float64(totalSize)*1.5) + 64*1024*1024
	if imageSize < 128*1024*1024 {
		imageSize = 128 * 1024 * 1024
	}

	im.logger.Info("creating ext4 image",
		"content_size_mb", totalSize/(1024*1024),
		"image_size_mb", imageSize/(1024*1024),
	)

	// Create the sparse file
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create image file: %w", err)
	}
	if err := f.Truncate(imageSize); err != nil {
		f.Close()
		return fmt.Errorf("truncate image file: %w", err)
	}
	f.Close()

	// mkfs.ext4 -F -d <srcdir> <outpath>
	// -F: force (don't ask questions)
	// -d: populate filesystem from directory
	mkfsCmd := exec.CommandContext(ctx, "mkfs.ext4", "-F", "-d", srcDir, outPath)
	mkfsCmd.Stderr = os.Stderr
	if err := mkfsCmd.Run(); err != nil {
		return fmt.Errorf("mkfs.ext4: %w", err)
	}

	// Shrink to minimum size
	resizeCmd := exec.CommandContext(ctx, "resize2fs", "-M", outPath)
	resizeCmd.Stderr = os.Stderr
	if err := resizeCmd.Run(); err != nil {
		// resize2fs failure is non-fatal -- image just won't be as small
		im.logger.Warn("resize2fs -M failed (non-fatal)", "error", err)
	}

	return nil
}

// CacheDir returns the path to the image cache directory.
func (im *ImageManager) CacheDir() string {
	return im.cacheDir
}

// CleanCache removes all cached rootfs images.
func (im *ImageManager) CleanCache() error {
	entries, err := os.ReadDir(im.cacheDir)
	if err != nil {
		return fmt.Errorf("read cache dir: %w", err)
	}

	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".ext4") {
			path := filepath.Join(im.cacheDir, entry.Name())
			if err := os.Remove(path); err != nil {
				im.logger.Warn("failed to remove cached image", "path", path, "error", err)
			}
		}
	}

	return nil
}
