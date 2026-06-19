package deploy

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"marshal/internal/pb"
)

// confine resolves a caller-supplied relative path against a trusted root and
// guarantees the result stays inside root. It rejects absolute paths and any
// path that escapes via "..", and resolves symlinks so a symlink inside the
// tree cannot point outside it. Returns the absolute, symlink-resolved path.
func confine(root, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute paths not allowed")
	}
	// Lexical containment check: join root+rel, clean, then verify we stay inside root.
	// filepath.Clean(filepath.Join(root, rel)) correctly propagates ".." escapes before
	// any symlink resolution, so ".."-based attacks are caught here.
	full := filepath.Clean(filepath.Join(root, rel))
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes deploy root")
	}

	// Defeat symlink escape: resolve symlinks on both sides and re-check
	// containment against the *real* root.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	realFull, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", err // includes "does not exist"
	}
	if realFull != realRoot && !strings.HasPrefix(realFull, realRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes deploy root via symlink")
	}
	return realFull, nil
}

const (
	maxFileBytes = 1 << 20 // 1 MiB read cap
	sniffBytes   = 8 << 10 // bytes inspected for binary detection
)

// ListDir returns the entries of rel under root, dirs first then files, each
// group alphabetical. rel="" lists the root.
func ListDir(root, rel string) (*pb.DirListing, error) {
	full, err := confine(root, rel)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(full)
	if err != nil {
		return nil, err
	}
	out := make([]*pb.DirEntry, 0, len(ents))
	for _, e := range ents {
		info, err := e.Info()
		if err != nil {
			continue // raced away between ReadDir and Info; skip
		}
		out = append(out, &pb.DirEntry{
			Name:    e.Name(),
			IsDir:   e.IsDir(),
			Size:    info.Size(),
			ModUnix: info.ModTime().Unix(),
			Mode:    uint32(info.Mode().Perm()),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir // dirs first
		}
		return out[i].Name < out[j].Name
	})
	return &pb.DirListing{Path: rel, Entries: out}, nil
}

// ReadFile returns the head (up to maxFileBytes) of the file at rel under root.
// Directories are rejected. Binary files (NUL byte in the first sniffBytes) are
// flagged and their content is omitted.
func ReadFile(root, rel string) (*pb.FileContent, error) {
	full, err := confine(root, rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("path is a directory")
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	buf := make([]byte, maxFileBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	buf = buf[:n]

	sniff := n
	if sniff > sniffBytes {
		sniff = sniffBytes
	}
	binary := bytes.IndexByte(buf[:sniff], 0) >= 0

	content := buf
	if binary {
		content = nil
	}
	return &pb.FileContent{
		Path:      rel,
		Content:   content,
		Size:      info.Size(),
		Truncated: info.Size() > int64(n),
		Binary:    binary,
	}, nil
}
