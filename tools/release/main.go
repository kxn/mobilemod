// Command release cross-compiles and packages an explicit public-file allowlist.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type target struct{ name, goos, goarch, goarm string }

var targets = []target{
	{"linux/amd64", "linux", "amd64", ""},
	{"linux/arm64", "linux", "arm64", ""},
	{"windows/amd64", "windows", "amd64", ""},
	{"darwin/amd64", "darwin", "amd64", ""},
	{"darwin/arm64", "darwin", "arm64", ""},
}

var versionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$`)

// Never archive the workspace recursively. These are public runtime resources,
// not user configuration, data, logs, downloaded firmware or local test results.
var packagePaths = []string{"README.md", "LICENSE", "config.example.json", "sms-bridge-protocol.txt", "docs", "deploy", "sms-bridge", "third_party", "scripts/start.ps1", "scripts/stop.ps1"}

func selectedTargets(value string) ([]target, error) {
	if value == "all" {
		return append([]target(nil), targets...), nil
	}
	var result []target
	seen := map[string]bool{}
	for _, name := range strings.Split(value, ",") {
		found := false
		for _, t := range targets {
			if t.name == name && !seen[name] {
				result = append(result, t)
				seen[name] = true
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("unsupported or duplicate target %q", name)
		}
	}
	return result, nil
}

func environment(t target) []string {
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "GOOS", "GOARCH", "GOARM", "CGO_ENABLED":
			continue
		}
		env = append(env, entry)
	}
	return append(env, "GOOS="+t.goos, "GOARCH="+t.goarch, "GOARM="+t.goarm, "CGO_ENABLED=0")
}

type entry struct {
	name string
	data []byte
	mode fs.FileMode
}

func collect(root, binary, name string) ([]entry, error) {
	data, err := os.ReadFile(binary)
	if err != nil {
		return nil, err
	}
	files := []entry{{name, data, 0755}}
	for _, relative := range packagePaths {
		err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(relative)), func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing package symlink %s", path)
			}
			if d.IsDir() {
				return nil
			}
			if !d.Type().IsRegular() {
				return fmt.Errorf("refusing non-regular package file %s", path)
			}
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files = append(files, entry{filepath.ToSlash(rel), contents, 0644})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	return files, nil
}

func archive(files []entry, prefix string, windows bool) ([]byte, error) {
	var output bytes.Buffer
	fixed := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if windows {
		w := zip.NewWriter(&output)
		for _, f := range files {
			h := &zip.FileHeader{Name: prefix + "/" + f.name, Method: zip.Deflate}
			h.SetMode(f.mode)
			h.SetModTime(fixed)
			writer, err := w.CreateHeader(h)
			if err != nil {
				return nil, err
			}
			if _, err = writer.Write(f.data); err != nil {
				return nil, err
			}
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
	} else {
		gz := gzip.NewWriter(&output)
		w := tar.NewWriter(gz)
		for _, f := range files {
			if err := w.WriteHeader(&tar.Header{Name: prefix + "/" + f.name, Mode: int64(f.mode.Perm()), Size: int64(len(f.data)), ModTime: fixed, Typeflag: tar.TypeReg}); err != nil {
				return nil, err
			}
			if _, err := w.Write(f.data); err != nil {
				return nil, err
			}
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		if err := gz.Close(); err != nil {
			return nil, err
		}
	}
	return output.Bytes(), nil
}

func build(ctx context.Context, root, out, version string, t target) (string, []byte, error) {
	tmp, err := os.MkdirTemp("", "mobilemod-release-")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(tmp)
	binaryName := "mobilemod"
	if t.goos == "windows" {
		binaryName += ".exe"
	}
	binary := filepath.Join(tmp, binaryName)
	command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-buildvcs=false", "-ldflags", "-s -w -X main.version="+version, "-o", binary, "./cmd/mobilemod")
	command.Dir = root
	command.Env = environment(t)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err = command.Run(); err != nil {
		return "", nil, fmt.Errorf("build %s: %w", t.name, err)
	}
	files, err := collect(root, binary, binaryName)
	if err != nil {
		return "", nil, err
	}
	name := "mobilemod-" + version + "-" + strings.ReplaceAll(t.name, "/", "-")
	data, err := archive(files, name, t.goos == "windows")
	if err != nil {
		return "", nil, err
	}
	ext := ".tar.gz"
	if t.goos == "windows" {
		ext = ".zip"
	}
	name += ext
	if err = os.WriteFile(filepath.Join(out, name), data, 0644); err != nil {
		return "", nil, err
	}
	return name, data, nil
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	f := flag.NewFlagSet("release", flag.ContinueOnError)
	version := f.String("version", "v0.0.0-dev", "semantic version/tag")
	list := f.String("targets", "all", "comma-separated os/arch targets or all")
	out := f.String("output", "dist", "output directory (only new packages go into SHA256SUMS)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	if !versionPattern.MatchString(*version) {
		return fmt.Errorf("invalid version")
	}
	selected, err := selectedTargets(*list)
	if err != nil {
		return err
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	output, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(output, 0755); err != nil {
		return err
	}
	var sums []string
	for _, t := range selected {
		name, data, err := build(ctx, root, output, *version, t)
		if err != nil {
			return err
		}
		sums = append(sums, fmt.Sprintf("%x  %s\n", sha256.Sum256(data), name))
		fmt.Fprintln(stdout, name)
	}
	sort.Strings(sums)
	return os.WriteFile(filepath.Join(output, "SHA256SUMS"), []byte(strings.Join(sums, "")), 0644)
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
