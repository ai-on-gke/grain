package qcow2

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// HasReflinkSupport probes whether the filesystem at dir supports copy-on-write
// reflinks (FICLONE ioctl).
func HasReflinkSupport(dir string) bool {
	srcPath := filepath.Join(dir, ".reflink_probe_src")
	dstPath := filepath.Join(dir, ".reflink_probe_dst")
	defer os.Remove(srcPath)
	defer os.Remove(dstPath)

	if err := os.WriteFile(srcPath, []byte("probe"), 0o600); err != nil {
		return false
	}
	dstFile, err := os.OpenFile(dstPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false
	}
	defer dstFile.Close()

	srcFile, err := os.Open(srcPath)
	if err != nil {
		return false
	}
	defer srcFile.Close()

	err = unix.IoctlFileClone(int(dstFile.Fd()), int(srcFile.Fd()))
	return err == nil
}

// CloneFileReflink performs a direct reflink clone of srcPath to dstPath using
// in-kernel FICLONE ioctl (or cp --reflink=always). It returns true if a reflink was
// used, or false if it fell back to sparse copying.
func CloneFileReflink(srcPath, dstPath string) (bool, error) {
	srcFile, err := os.Open(srcPath)
	if err != nil {
		return false, err
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return false, err
	}

	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return false, err
	}

	dstFile, err := os.OpenFile(dstPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err == nil {
		cloneErr := unix.IoctlFileClone(int(dstFile.Fd()), int(srcFile.Fd()))
		dstFile.Close()
		if cloneErr == nil {
			return true, nil
		}
	} else {
		_ = os.Remove(dstPath)
	}

	// Try cp --reflink=always
	cmd := exec.Command("cp", "--reflink=always", srcPath, dstPath)
	if err := cmd.Run(); err == nil {
		return true, nil
	}

	// Fallback to fast sparse copy
	return CloneOrCopyExtent(srcPath, dstPath, 0, 0, srcInfo.Size())
}

// CloneOrCopyExtent clones or copies a slice of src [srcOffset, srcOffset+sizeBytes]
// into dst at dstOffset. It attempts an in-kernel reflink (FICLONE or FICLONERANGE)
// if supported by the host filesystem, falling back to fast sparse block copying using SEEK_DATA/SEEK_HOLE.
func CloneOrCopyExtent(srcPath, dstPath string, srcOffset, dstOffset, sizeBytes int64) (reflinkUsed bool, err error) {
	srcFile, err := os.Open(srcPath)
	if err != nil {
		return false, err
	}
	defer srcFile.Close()

	srcInfo, statErr := srcFile.Stat()
	if statErr != nil {
		return false, statErr
	}

	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return false, err
	}

	// 1. If offsets are 0 and sizes match full source, try full-file FICLONE on an empty destination
	if srcOffset == 0 && dstOffset == 0 && srcInfo.Size() == sizeBytes {
		dstFile, err := os.OpenFile(dstPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
		if err == nil {
			if cloneErr := unix.IoctlFileClone(int(dstFile.Fd()), int(srcFile.Fd())); cloneErr == nil {
				dstFile.Close()
				return true, nil
			}
			dstFile.Close()
		}
		// Try cp --reflink=always
		_ = os.Remove(dstPath)
		cmd := exec.Command("cp", "--reflink=always", srcPath, dstPath)
		if err := cmd.Run(); err == nil {
			return true, nil
		}
	}

	dstFile, err := os.OpenFile(dstPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return false, err
	}
	defer dstFile.Close()

	// Ensure destination file is at least dstOffset + sizeBytes
	if err := dstFile.Truncate(dstOffset + sizeBytes); err != nil {
		return false, err
	}

	// 2. Try range reflink (FICLONERANGE)
	cloneLen := sizeBytes
	if srcOffset+cloneLen > srcInfo.Size() {
		cloneLen = srcInfo.Size() - srcOffset
	}
	if cloneLen > 0 {
		req := unix.FileCloneRange{
			Src_fd:      int64(srcFile.Fd()),
			Src_offset:  uint64(srcOffset),
			Src_length:  uint64(cloneLen),
			Dest_offset: uint64(dstOffset),
		}
		if err := unix.IoctlFileCloneRange(int(dstFile.Fd()), &req); err == nil {
			return true, nil
		}
	}

	// 3. Fallback to fast sparse copy using SEEK_DATA and SEEK_HOLE
	if err := copySparse(srcFile, dstFile, srcOffset, dstOffset, sizeBytes); err != nil {
		return false, err
	}

	return false, nil
}

func copySparse(srcFile *os.File, dstFile *os.File, srcOffset, dstOffset, sizeBytes int64) error {
	srcEnd := srcOffset + sizeBytes
	currSrc := srcOffset

	buf := make([]byte, 1024*1024) // 1MB buffer

	for currSrc < srcEnd {
		dataOff, err := unix.Seek(int(srcFile.Fd()), currSrc, unix.SEEK_DATA)
		if err != nil {
			if errors.Is(err, unix.ENXIO) {
				// No more data: rest of the range is a sparse hole
				break
			}
			// Fallback to linear chunk copy if SEEK_DATA is unsupported
			return copyLinear(srcFile, dstFile, currSrc, dstOffset+(currSrc-srcOffset), srcEnd-currSrc)
		}

		if dataOff >= srcEnd {
			break
		}

		holeOff, err := unix.Seek(int(srcFile.Fd()), dataOff, unix.SEEK_HOLE)
		if err != nil {
			return copyLinear(srcFile, dstFile, dataOff, dstOffset+(dataOff-srcOffset), srcEnd-dataOff)
		}

		dataLen := holeOff - dataOff
		if dataOff+dataLen > srcEnd {
			dataLen = srcEnd - dataOff
		}

		targetDst := dstOffset + (dataOff - srcOffset)
		if _, err := dstFile.Seek(targetDst, io.SeekStart); err != nil {
			return err
		}
		if _, err := srcFile.Seek(dataOff, io.SeekStart); err != nil {
			return err
		}

		var copied int64
		for copied < dataLen {
			toRead := int64(len(buf))
			if dataLen-copied < toRead {
				toRead = dataLen - copied
			}
			n, rErr := srcFile.Read(buf[:toRead])
			if n > 0 {
				if _, wErr := dstFile.Write(buf[:n]); wErr != nil {
					return wErr
				}
				copied += int64(n)
			}
			if rErr != nil {
				if errors.Is(rErr, io.EOF) {
					break
				}
				return rErr
			}
		}

		currSrc = holeOff
	}

	return dstFile.Sync()
}

func copyLinear(srcFile, dstFile *os.File, srcOffset, dstOffset, sizeBytes int64) error {
	if _, err := srcFile.Seek(srcOffset, io.SeekStart); err != nil {
		return err
	}
	if _, err := dstFile.Seek(dstOffset, io.SeekStart); err != nil {
		return err
	}

	buf := make([]byte, 1024*1024)
	var copied int64
	for copied < sizeBytes {
		toRead := int64(len(buf))
		if sizeBytes-copied < toRead {
			toRead = sizeBytes - copied
		}
		n, err := srcFile.Read(buf[:toRead])
		if n > 0 {
			if _, wErr := dstFile.Write(buf[:n]); wErr != nil {
				return wErr
			}
			copied += int64(n)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
	}
	return dstFile.Sync()
}
