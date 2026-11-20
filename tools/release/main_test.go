package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTargetSelectionAndVersionSafety(t *testing.T) {
	all, err := selectedTargets("all")
	if err != nil || len(all) != 5 {
		t.Fatal(all, err)
	}
	linux, err := selectedTargets("linux/amd64,linux/arm64")
	if err != nil || len(linux) != 2 || linux[1].goarch != "arm64" {
		t.Fatal(linux, err)
	}
	for _, bad := range []string{"", "linux/386", "linux/armv7", "linux/riscv64", "linux/amd64,linux/amd64", "../private"} {
		if _, err := selectedTargets(bad); err == nil {
			t.Fatal(bad)
		}
	}
	for _, bad := range []string{"../v1.0.0", "v1.0.0 -X private", "main", "v1.2.3/secret"} {
		if versionPattern.MatchString(bad) {
			t.Fatal(bad)
		}
	}
	if !versionPattern.MatchString("v1.2.3-rc.1") {
		t.Fatal("prerelease rejected")
	}
}

func TestPublicAllowlistAndArchivePermissions(t *testing.T) {
	root := t.TempDir()
	for _, path := range packagePaths {
		isDir := path == "docs" || path == "deploy" || path == "sms-bridge" || path == "third_party"
		if !isDir {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0700); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(root, path), []byte("public"), 0600)
		} else {
			os.MkdirAll(filepath.Join(root, path), 0700)
			os.WriteFile(filepath.Join(root, path, "readme.txt"), []byte("public"), 0600)
		}
	}
	os.MkdirAll(filepath.Join(root, ".local"), 0700)
	os.WriteFile(filepath.Join(root, ".local", "config.json"), []byte("private-fixture"), 0600)
	os.WriteFile(filepath.Join(root, "config.json"), []byte("private-fixture"), 0600)
	binary := filepath.Join(root, "compiled")
	os.WriteFile(binary, []byte("binary-fixture"), 0600)
	files, err := collect(root, binary, "mobilemod")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.Contains(string(f.data), "private-fixture") || strings.Contains(f.name, ".local") {
			t.Fatal("private data archived")
		}
	}
	for _, windows := range []bool{false, true} {
		data, err := archive(files, "package", windows)
		if err != nil {
			t.Fatal(err)
		}
		again, err := archive(files, "package", windows)
		if err != nil || !bytes.Equal(data, again) {
			t.Fatal("nonreproducible archive", err)
		}
		seen := false
		if windows {
			r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range r.File {
				if f.Name == "package/mobilemod" {
					seen = true
					if f.Mode().Perm() != 0755 {
						t.Fatal(f.Mode())
					}
				}
			}
		} else {
			gz, err := gzip.NewReader(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			defer gz.Close()
			r := tar.NewReader(gz)
			for {
				h, err := r.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if h.Name == "package/mobilemod" {
					seen = true
					if h.Mode != 0755 {
						t.Fatal(h.Mode)
					}
				}
			}
		}
		if !seen {
			t.Fatal("missing executable")
		}
	}
}
