package shim

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/testfs"
)

func pathTestOptions(t *testing.T, shell string) PathOptions {
	t.Helper()
	root := t.TempDir()
	return PathOptions{HomeDir: filepath.Join(root, "home"), ConfigDir: filepath.Join(root, "config"), Shell: shell}
}

func TestPathSetupChoosesShellStartupFiles(t *testing.T) {
	t.Parallel()
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			t.Parallel()
			options := pathTestOptions(t, shell)
			options.ZDotDir = filepath.Join(options.HomeDir, "zdot")
			options.XDGConfigHome = filepath.Join(options.HomeDir, "xdg")
			testfs.Write(t, filepath.Join(options.HomeDir, ".profile"), "# login profile\n")
			bin := filepath.Join(options.HomeDir, "bin")
			setup, err := PreparePath(bin, options)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string][]string{
				"bash": {filepath.Join(options.HomeDir, ".bashrc"), filepath.Join(options.HomeDir, ".profile")},
				"zsh":  {filepath.Join(options.ZDotDir, ".zshrc")},
				"fish": {filepath.Join(options.XDGConfigHome, "fish", "config.fish")},
			}[shell]
			if _, err := os.Stat(pathStatePath(options.ConfigDir)); !os.IsNotExist(err) {
				t.Fatalf("preflight wrote state: %v", err)
			}
			for range 2 {
				if err := setup.Apply(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			state, err := loadPathState(pathStatePath(options.ConfigDir))
			if err != nil || len(state.Entries) != len(want) {
				t.Fatalf("state = %+v, error = %v", state, err)
			}
			for _, path := range want {
				data, err := os.ReadFile(path)
				if err != nil || strings.Count(string(data), "# >>> wuko shim PATH") != 1 {
					t.Fatalf("startup file %s = %q, error = %v", path, data, err)
				}
			}
		})
	}
}

func TestPathBlocksAppendWithoutDuplicates(t *testing.T) {
	t.Parallel()
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			t.Parallel()
			executable, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("%s not installed", shell)
			}
			bin := filepath.Join(t.TempDir(), "bin with 'quotes' $dollars `ticks` \\backslash\nand newline")
			entry := pathEntry{Directory: bin, Shell: shell}
			file := filepath.Join(t.TempDir(), "startup")
			testfs.Write(t, file, entry.block())
			quotedFile := shellQuote(file)
			var source string
			if shell == "fish" {
				source = "source " + quotedFile + "; source " + quotedFile + "; printf '%s\\000' $PATH"
			} else {
				source = ". " + quotedFile + "; . " + quotedFile + "; printf '%s' \"$PATH\""
			}
			command := exec.CommandContext(t.Context(), executable, "-c", source)
			command.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"}
			out, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("shell: %v: %s", err, out)
			}
			want := "/usr/bin:/bin:" + bin
			if shell == "fish" {
				want = "/usr/bin\x00/bin\x00" + bin + "\x00"
			}
			if diff := cmp.Diff(want, string(out)); diff != "" {
				t.Fatalf("PATH (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPathCleanupWaitsForAllShimsAndPreservesDotfile(t *testing.T) {
	t.Parallel()
	options := pathTestOptions(t, "zsh")
	startup := filepath.Join(options.HomeDir, ".zshrc")
	original := "# original without trailing newline"
	testfs.Write(t, startup, original)
	if err := os.Chmod(startup, 0o640); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(options.HomeDir, "bin")
	setup, err := PreparePath(bin, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := setup.Apply(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"project", "workflow"} {
		if _, err := Install(bin, name, Spec{Workflow: "check"}, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Remove(bin, "project"); err != nil {
		t.Fatal(err)
	}
	if err := CleanupPath(t.Context(), bin, options.ConfigDir); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(startup)
	if !strings.Contains(string(data), pathMarker(bin)) {
		t.Fatal("PATH removed while another shim still exists")
	}
	if _, err := Remove(bin, "workflow"); err != nil {
		t.Fatal(err)
	}
	if err := CleanupPath(t.Context(), bin, options.ConfigDir); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(startup)
	if err != nil || string(data) != original {
		t.Fatalf("startup = %q, error = %v", data, err)
	}
	info, err := os.Stat(startup)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("startup mode = %v, error = %v", info, err)
	}
}

func TestPathSetupPreservesSymlinkAndModifiedBlocks(t *testing.T) {
	t.Parallel()
	options := pathTestOptions(t, "zsh")
	target := filepath.Join(options.HomeDir, "dotfiles", "zshrc")
	testfs.Write(t, target, "# dotfiles\n")
	link := filepath.Join(options.HomeDir, ".zshrc")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(options.HomeDir, "bin")
	setup, err := PreparePath(bin, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := setup.Apply(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Readlink(link); err != nil {
		t.Fatalf("startup symlink replaced: %v", err)
	}
	data, _ := os.ReadFile(target)
	modified := strings.Replace(string(data), "export PATH=", "export CUSTOM_PATH=", 1)
	testfs.Write(t, target, modified)
	if _, err := PreparePath(bin, options); err == nil || !strings.Contains(err.Error(), "modified") {
		t.Fatalf("preflight modified block: %v", err)
	}
	if err := CleanupPath(t.Context(), bin, options.ConfigDir); err == nil || !strings.Contains(err.Error(), "modified") {
		t.Fatalf("cleanup modified block: %v", err)
	}
	data, _ = os.ReadFile(target)
	if string(data) != modified {
		t.Fatal("modified block changed")
	}
	state, err := loadPathState(pathStatePath(options.ConfigDir))
	if err != nil || len(state.Entries) != 1 {
		t.Fatalf("recovery record lost: %+v, %v", state, err)
	}
}

func TestPathSetupRecoversInterruptedWrites(t *testing.T) {
	t.Parallel()
	options := pathTestOptions(t, "bash")
	bin := filepath.Join(options.HomeDir, "bin")
	setup, err := PreparePath(bin, options)
	if err != nil {
		t.Fatal(err)
	}
	// The first startup file can be written, but the second fails after preflight.
	testfs.MkdirAll(t, filepath.Join(options.HomeDir, ".bash_profile"), 0o755)
	if err := setup.Apply(t.Context()); err == nil {
		t.Fatal("startup directory accepted")
	}
	if _, err := os.Stat(filepath.Join(options.HomeDir, ".bashrc")); !os.IsNotExist(err) {
		t.Fatal("preflight failure wrote another startup file")
	}
	if err := os.Remove(filepath.Join(options.HomeDir, ".bash_profile")); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after ownership and the first startup file were published.
	state, edits, err := setup.plan()
	if err != nil {
		t.Fatal(err)
	}
	statePath := pathStatePath(options.ConfigDir)
	testfs.MkdirAll(t, filepath.Dir(statePath), 0o755)
	if err := savePathState(statePath, state); err != nil {
		t.Fatal(err)
	}
	testfs.Write(t, edits[0].path, string(edits[0].data))
	if err := setup.Apply(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, edit := range edits {
		data, _ := os.ReadFile(edit.path)
		if strings.Count(string(data), "# >>> wuko shim PATH") != 1 {
			t.Fatalf("not recovered: %s = %q", edit.path, data)
		}
	}
	if err := CleanupPath(t.Context(), bin, options.ConfigDir); err != nil {
		t.Fatal(err)
	}
}

func TestPathSetupRejectsUnsupportedShellWithoutWrites(t *testing.T) {
	t.Parallel()
	for _, shell := range []string{"/bin/unknown", ""} {
		options := pathTestOptions(t, shell)
		if _, err := PreparePath(filepath.Join(options.HomeDir, "bin"), options); err == nil {
			t.Fatalf("shell %q accepted", shell)
		}
		if _, err := os.Stat(options.HomeDir); !os.IsNotExist(err) {
			t.Fatalf("preflight mutated home: %v", err)
		}
	}
}

func TestPathSetupWriteFailureRetainsRecoveryRecords(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	options := pathTestOptions(t, "bash")
	target := filepath.Join(t.TempDir(), "bashrc")
	testfs.Write(t, target, "# existing bash configuration\n")
	testfs.MkdirAll(t, options.HomeDir, 0o755)
	if err := os.Symlink(target, filepath.Join(options.HomeDir, ".bashrc")); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(options.HomeDir, "bin")
	setup, err := PreparePath(bin, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(options.HomeDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(options.HomeDir, 0o755) })
	if err := setup.Apply(t.Context()); err == nil {
		t.Fatal("write to read-only startup directory succeeded")
	}
	state, err := loadPathState(pathStatePath(options.ConfigDir))
	if err != nil || len(state.Entries) != 2 {
		t.Fatalf("partial setup lost ownership: %+v, %v", state, err)
	}
	data, _ := os.ReadFile(target)
	if !strings.Contains(string(data), pathMarker(bin)) {
		t.Fatal("expected first startup file to be written before failure")
	}
	if err := os.Chmod(options.HomeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := setup.Apply(t.Context()); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if err := CleanupPath(t.Context(), bin, options.ConfigDir); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(target)
	if string(data) != "# existing bash configuration\n" {
		t.Fatalf("original configuration changed: %q", data)
	}
}

func TestPathCleanupPreservesOtherBinDirectories(t *testing.T) {
	t.Parallel()
	options := pathTestOptions(t, "zsh")
	first := filepath.Join(options.HomeDir, "first")
	second := filepath.Join(options.HomeDir, "second")
	for _, bin := range []string{first, second} {
		setup, err := PreparePath(bin, options)
		if err != nil {
			t.Fatal(err)
		}
		if err := setup.Apply(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if err := CleanupPath(t.Context(), first, options.ConfigDir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(options.HomeDir, ".zshrc"))
	if err != nil || strings.Contains(string(data), pathMarker(first)) || !strings.Contains(string(data), pathMarker(second)) {
		t.Fatalf("wrong PATH entry removed: %q, %v", data, err)
	}
}

func TestPathCleanupPreservesConfigurationAppendedByUser(t *testing.T) {
	t.Parallel()
	options := pathTestOptions(t, "zsh")
	startup := filepath.Join(options.HomeDir, ".zshrc")
	testfs.Write(t, startup, "export BEFORE=1")
	bin := filepath.Join(options.HomeDir, "bin")
	setup, err := PreparePath(bin, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := setup.Apply(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(startup)
	if err != nil {
		t.Fatal(err)
	}
	testfs.Write(t, startup, string(data)+"export AFTER=2\n")
	if err := CleanupPath(t.Context(), bin, options.ConfigDir); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(startup)
	if err != nil || string(data) != "export BEFORE=1\nexport AFTER=2\n" {
		t.Fatalf("user configuration changed: %q, %v", data, err)
	}
}

func TestPathSetupRejectsUnownedBlocksAndInvalidFiles(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"unowned block", "directory", "dangling symlink", "bad ownership"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			options := pathTestOptions(t, "zsh")
			startup := filepath.Join(options.HomeDir, ".zshrc")
			bin := filepath.Join(options.HomeDir, "bin")
			switch kind {
			case "unowned block":
				testfs.Write(t, startup, (pathEntry{Directory: bin, Shell: "zsh"}).block())
			case "directory":
				testfs.MkdirAll(t, startup, 0o755)
			case "dangling symlink":
				testfs.MkdirAll(t, options.HomeDir, 0o755)
				if err := os.Symlink(filepath.Join(options.HomeDir, "missing"), startup); err != nil {
					t.Fatal(err)
				}
			case "bad ownership":
				testfs.Write(t, pathStatePath(options.ConfigDir), `{"version":42}`)
			}
			if _, err := PreparePath(bin, options); err == nil {
				t.Fatal("invalid setup accepted")
			}
		})
	}
}

func TestConcurrentPathUpdatesPreserveEveryDirectory(t *testing.T) {
	t.Parallel()
	options := pathTestOptions(t, "zsh")
	const count = 4
	setups := make([]*PathSetup, count)
	for i := range setups {
		setup, err := PreparePath(filepath.Join(options.HomeDir, strings.Repeat("x", i+1)), options)
		if err != nil {
			t.Fatal(err)
		}
		setups[i] = setup
	}
	var workers sync.WaitGroup
	errors := make(chan error, count)
	for _, setup := range setups {
		workers.Go(func() { errors <- setup.Apply(t.Context()) })
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(options.HomeDir, ".zshrc"))
	if err != nil {
		t.Fatal(err)
	}
	for _, setup := range setups {
		if strings.Count(string(data), pathMarker(setup.directory)) != 2 {
			t.Fatalf("concurrent update lost %s", setup.directory)
		}
	}
	state, err := loadPathState(pathStatePath(options.ConfigDir))
	if err != nil || len(state.Entries) != count {
		t.Fatalf("concurrent ownership = %+v, error = %v", state, err)
	}
}
