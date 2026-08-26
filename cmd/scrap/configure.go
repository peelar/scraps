package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const configureUsage = `usage: scrap configure [--cpus N] [--memory GIB] [--disk GIB]

Configure the local worker VM. With an interactive terminal, omitted values are
prompted. Existing VMs must be deleted and recreated before size changes apply.
`

type workerConfig struct {
	CPUs      int
	MemoryGiB int
	DiskGiB   int
}

var defaultWorkerConfig = workerConfig{CPUs: 4, MemoryGiB: 8, DiskGiB: 60}

func runConfigure(args []string) int {
	if err := configure(args, os.Stdin, os.Stdout, workerConfigPath()); err != nil {
		fmt.Fprintf(os.Stderr, "scrap configure: %v\n", err)
		return 1
	}
	return 0
}

func configure(args []string, input io.Reader, output io.Writer, configPath string) error {
	current, err := readWorkerConfig(configPath)
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("configure", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.Usage = func() { fmt.Fprint(output, configureUsage) }
	cpus := flags.Int("cpus", 0, "worker VM CPU count")
	memory := flags.Int("memory", 0, "worker VM memory in GiB")
	disk := flags.Int("disk", 0, "worker VM disk in GiB")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}

	interactive := false
	if f, ok := input.(*os.File); ok {
		if info, statErr := f.Stat(); statErr == nil {
			interactive = info.Mode()&os.ModeCharDevice != 0
		}
	}
	reader := bufio.NewReader(input)
	if interactive {
		if *cpus, err = promptSize(reader, output, "CPUs", current.CPUs, *cpus); err != nil {
			return err
		}
		if *memory, err = promptSize(reader, output, "Memory (GiB)", current.MemoryGiB, *memory); err != nil {
			return err
		}
		if *disk, err = promptSize(reader, output, "Disk (GiB)", current.DiskGiB, *disk); err != nil {
			return err
		}
	}
	configured := current
	if *cpus != 0 {
		configured.CPUs = *cpus
	}
	if *memory != 0 {
		configured.MemoryGiB = *memory
	}
	if *disk != 0 {
		configured.DiskGiB = *disk
	}
	if err := validateWorkerConfig(configured); err != nil {
		return err
	}
	if err := writeWorkerConfig(configPath, configured); err != nil {
		return err
	}
	fmt.Fprintf(output, "Worker VM configured: %d CPUs, %d GiB memory, %d GiB disk\n", configured.CPUs, configured.MemoryGiB, configured.DiskGiB)
	fmt.Fprintf(output, "Saved to %s\n", configPath)
	return nil
}

func promptSize(reader *bufio.Reader, output io.Writer, label string, current, supplied int) (int, error) {
	if supplied != 0 {
		return supplied, nil
	}
	fmt.Fprintf(output, "%s [%d]: ", label, current)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return current, nil
	}
	value, err := strconv.Atoi(line)
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number", strings.ToLower(label))
	}
	return value, nil
}

func workerConfigPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".config", "scraps", "worker.conf")
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "scraps", "worker.conf")
}

func readWorkerConfig(path string) (workerConfig, error) {
	configured := defaultWorkerConfig
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return configured, nil
	}
	if err != nil {
		return configured, fmt.Errorf("read %s: %w", path, err)
	}
	for lineNumber, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, text, ok := strings.Cut(line, "=")
		if !ok {
			return configured, fmt.Errorf("%s:%d: expected key=value", path, lineNumber+1)
		}
		value, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil {
			return configured, fmt.Errorf("%s:%d: value must be a whole number", path, lineNumber+1)
		}
		switch strings.TrimSpace(key) {
		case "cpus":
			configured.CPUs = value
		case "memory_gib":
			configured.MemoryGiB = value
		case "disk_gib":
			configured.DiskGiB = value
		default:
			return configured, fmt.Errorf("%s:%d: unknown setting %q", path, lineNumber+1, strings.TrimSpace(key))
		}
	}
	return configured, validateWorkerConfig(configured)
}

func validateWorkerConfig(configured workerConfig) error {
	if configured.CPUs < 1 {
		return errors.New("CPUs must be at least 1")
	}
	if configured.MemoryGiB < 2 {
		return errors.New("memory must be at least 2 GiB")
	}
	if configured.DiskGiB < 10 {
		return errors.New("disk must be at least 10 GiB")
	}
	return nil
}

func writeWorkerConfig(path string, configured workerConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	content := fmt.Sprintf("# Scraps worker VM sizing\ncpus=%d\nmemory_gib=%d\ndisk_gib=%d\n", configured.CPUs, configured.MemoryGiB, configured.DiskGiB)
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("install config: %w", err)
	}
	return nil
}
