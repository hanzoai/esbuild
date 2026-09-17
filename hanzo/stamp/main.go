// Stamp describes the built artifacts, and it gets the version by asking them
// rather than by being told.
//
// The hazard it closes: a host that drives esbuild in service mode must send
// --service=<version>, and cmd/esbuild/main.go exits 1 on any mismatch. A
// version written down anywhere other than inside the artifact can drift from
// it, and the drift only shows up as "Cannot start service" at run time. So
// every artifact here is started in service mode with the version the lane
// claims, and the version recorded in the manifest is the one the artifact
// itself announces on its first stdout message.
//
// Cross-compiled binaries cannot run on the build host. Those are recorded
// with their size and digest and "probed": false, so a reader can see exactly
// which claims were tested.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

type artifact struct {
	File     string `json:"file"`
	Platform string `json:"platform"`
	Bytes    int64  `json:"bytes"`
	SHA256   string `json:"sha256"`
	Probed   bool   `json:"probed"`
	Announce string `json:"announced,omitempty"`
}

type manifest struct {
	Version   string     `json:"version"`
	Service   string     `json:"service"`
	Commit    string     `json:"commit"`
	Toolchain string     `json:"toolchain"`
	Artifacts []artifact `json:"artifacts"`
}

func main() {
	dist := flag.String("dist", "", "directory holding the built artifacts")
	version := flag.String("version", "", "version the lane built")
	commit := flag.String("commit", "", "source commit")
	out := flag.String("out", "", "manifest to write")
	flag.Parse()
	if *dist == "" || *version == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "stamp: -dist, -version and -out are required")
		os.Exit(2)
	}

	names, err := filepath.Glob(filepath.Join(*dist, "esbuild-"+*version+"-*"))
	if err != nil || len(names) == 0 {
		fmt.Fprintf(os.Stderr, "stamp: no artifacts for %s in %s\n", *version, *dist)
		os.Exit(1)
	}
	sort.Strings(names)

	m := manifest{
		Version:   *version,
		Service:   "--service=" + *version,
		Commit:    *commit,
		Toolchain: runtime.Version(),
	}
	failed := false
	for _, name := range names {
		a, err := describe(name, *version)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stamp: %s: %v\n", filepath.Base(name), err)
			failed = true
			continue
		}
		m.Artifacts = append(m.Artifacts, a)
		state := "size and digest only"
		if a.Probed {
			state = "announced " + a.Announce
		}
		fmt.Printf("  %-40s %9d bytes  %s\n", a.File, a.Bytes, state)
	}
	if failed {
		os.Exit(1)
	}

	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "stamp:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, append(body, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "stamp:", err)
		os.Exit(1)
	}
	fmt.Printf("  %s\n", *out)
}

func describe(name, version string) (artifact, error) {
	info, err := os.Stat(name)
	if err != nil {
		return artifact{}, err
	}
	body, err := os.ReadFile(name)
	if err != nil {
		return artifact{}, err
	}
	sum := sha256.Sum256(body)
	base := filepath.Base(name)
	a := artifact{
		File:     base,
		Platform: platform(base, version),
		Bytes:    info.Size(),
		SHA256:   hex.EncodeToString(sum[:]),
	}

	var announced string
	switch {
	case strings.HasSuffix(name, ".wasm"):
		announced, err = askWasm(body, version)
	case a.Platform == runtime.GOOS+"-"+runtime.GOARCH:
		announced, err = askNative(name, version)
	default:
		return a, nil // cross-compiled: not executable here
	}
	if err != nil {
		return artifact{}, err
	}
	if announced != version {
		return artifact{}, fmt.Errorf("announced %q, lane built %q", announced, version)
	}
	a.Probed, a.Announce = true, announced
	return a, nil
}

func platform(base, version string) string {
	p := strings.TrimPrefix(base, "esbuild-"+version+"-")
	return strings.TrimSuffix(p, ".wasm")
}

// askNative starts the binary in service mode and closes its stdin, which is
// how a host shuts the service down cleanly.
func askNative(name, version string) (string, error) {
	cmd := exec.Command(name, "--service="+version)
	cmd.Stdin = bytes.NewReader(nil)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("service mode: %w", err)
	}
	return announced(stdout)
}

// askWasm runs the module under wazero with no preopened directories at all.
// The service protocol carries every file the host wants to hand over, so the
// module needs no filesystem, and giving it none is also the cheapest proof
// that the artifact is the service and not a CLI that happens to start.
func askWasm(body []byte, version string) (string, error) {
	ctx := context.Background()
	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, r)

	var stdout bytes.Buffer
	cfg := wazero.NewModuleConfig().WithName("").
		WithArgs("esbuild", "--service="+version).
		WithStdin(bytes.NewReader(nil)).WithStdout(&stdout).WithStderr(os.Stderr).
		WithSysWalltime().WithSysNanotime().WithSysNanosleep()
	if _, err := r.InstantiateWithConfig(ctx, body, cfg); err != nil {
		var exit *sys.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 0 {
			return "", fmt.Errorf("service mode: %w", err)
		}
	}
	return announced(stdout.Bytes())
}

// The first thing esbuild writes in service mode is its own version: a bare
// uint32 length followed by that many bytes (cmd/esbuild/service.go).
func announced(stdout []byte) (string, error) {
	if len(stdout) < 4 {
		return "", io.ErrUnexpectedEOF
	}
	n := binary.LittleEndian.Uint32(stdout)
	if int(n) > len(stdout)-4 {
		return "", fmt.Errorf("version message claims %d bytes, got %d", n, len(stdout)-4)
	}
	return string(stdout[4 : 4+n]), nil
}
