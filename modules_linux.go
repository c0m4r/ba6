//go:build linux

package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const (
	sysFinitModule          = 313 // amd64 __NR_finit_module
	moduleInitCompressed    = 4
	deleteModuleNonblocking = syscall.O_NONBLOCK
	deleteModuleForce       = syscall.O_TRUNC
)

func cmdLsmod(args []string) int {
	if len(args) != 0 {
		fatalf("lsmod", "unexpected operand %q", args[0])
		return 1
	}
	file, err := os.Open("/proc/modules")
	if err != nil {
		fatalf("lsmod", "%v", err)
		return 1
	}
	defer file.Close()
	fmt.Println("Module                  Size  Used by")
	scanner := newLineScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		usedBy := strings.TrimSuffix(fields[3], ",")
		if usedBy == "-" {
			usedBy = ""
		}
		fmt.Printf("%-20s %8s  %s", fields[0], fields[1], fields[2])
		if usedBy != "" {
			fmt.Printf(" %s", usedBy)
		}
		fmt.Println()
	}
	if err := scanner.Err(); err != nil {
		fatalf("lsmod", "%v", err)
		return 1
	}
	return 0
}

func cmdInsmod(args []string) int {
	if len(args) == 0 {
		fatalf("insmod", "missing module file")
		return 1
	}
	if err := insertModule(args[0], strings.Join(args[1:], " ")); err != nil {
		fatalf("insmod", "%s: %v", args[0], err)
		return 1
	}
	return 0
}

func insertModule(path, parameters string) error {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	parameterPointer, err := syscall.BytePtrFromString(parameters)
	if err != nil {
		return err
	}
	flags := uintptr(0)
	if strings.HasSuffix(path, ".gz") || strings.HasSuffix(path, ".xz") || strings.HasSuffix(path, ".zst") {
		flags = moduleInitCompressed
	}
	_, _, errno := syscall.Syscall(sysFinitModule, uintptr(fd), uintptr(unsafe.Pointer(parameterPointer)), flags) //nolint:gosec // G103: kernel receives a NUL-terminated parameter string.
	if errno == 0 {
		return nil
	}
	// Older kernels may not implement finit_module. Fall back to init_module
	// only for uncompressed modules, with a bounded read into memory.
	if errno != syscall.ENOSYS || flags != 0 {
		return errno
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		return statErr
	}
	if info.Size() <= 0 || info.Size() > 512<<20 {
		return fmt.Errorf("module size is out of range")
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		return readErr
	}
	_, _, errno = syscall.Syscall(syscall.SYS_INIT_MODULE, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(unsafe.Pointer(parameterPointer))) //nolint:gosec // G103: slice and C string are held live across the syscall.
	if errno != 0 {
		return errno
	}
	return nil
}

func cmdRmmod(args []string) int {
	force := false
	if len(args) > 0 && (args[0] == "-f" || args[0] == "--force") {
		force, args = true, args[1:]
	}
	if len(args) == 0 {
		fatalf("rmmod", "missing module name")
		return 1
	}
	status := 0
	for _, name := range args {
		if err := removeModule(name, force); err != nil {
			fatalf("rmmod", "%s: %v", name, err)
			status = 1
		}
	}
	return status
}

func removeModule(name string, force bool) error {
	name = moduleName(name)
	pointer, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	flags := uintptr(deleteModuleNonblocking)
	if force {
		flags |= deleteModuleForce
	}
	_, _, errno := syscall.Syscall(syscall.SYS_DELETE_MODULE, uintptr(unsafe.Pointer(pointer)), flags, 0) //nolint:gosec // G103: kernel receives a NUL-terminated module name.
	if errno != 0 {
		return errno
	}
	return nil
}

type moduleDatabase struct {
	root     string
	paths    map[string]string
	deps     map[string][]string
	aliases  []moduleAlias
	builtins map[string]bool
}

type moduleAlias struct{ pattern, module string }

func cmdModprobe(args []string) int {
	remove, quiet, verbose := false, false, false
	for len(args) > 0 {
		switch args[0] {
		case "-r", "--remove":
			remove, args = true, args[1:]
		case "-q", "--quiet":
			quiet, args = true, args[1:]
		case "-v", "--verbose":
			verbose, args = true, args[1:]
		case "--":
			args = args[1:]
			goto optionsDone
		default:
			if strings.HasPrefix(args[0], "-") {
				fatalf("modprobe", "unsupported option %q", args[0])
				return 1
			}
			goto optionsDone
		}
	}
optionsDone:
	if len(args) == 0 {
		fatalf("modprobe", "missing module name")
		return 1
	}
	var uts syscall.Utsname
	if err := syscall.Uname(&uts); err != nil {
		fatalf("modprobe", "%v", err)
		return 1
	}
	root := filepath.Join("/lib/modules", utsField(uts.Release[:]))
	database, err := readModuleDatabase(root)
	if err != nil {
		if !quiet {
			fatalf("modprobe", "%v", err)
		}
		return 1
	}
	target := database.resolve(args[0])
	if target == "" {
		if !quiet {
			fatalf("modprobe", "module %s not found in %s", args[0], root)
		}
		return 1
	}
	if remove {
		order := database.loadOrder(target)
		for i := len(order) - 1; i >= 0; i-- {
			if verbose {
				fmt.Printf("rmmod %s\n", order[i])
			}
			if err := removeModule(order[i], false); err != nil && !errors.Is(err, syscall.ENOENT) {
				if !quiet {
					fatalf("modprobe", "%s: %v", order[i], err)
				}
				return 1
			}
		}
		return 0
	}
	parameters := strings.Join(args[1:], " ")
	for _, name := range database.loadOrder(target) {
		if database.builtins[name] {
			continue
		}
		path := database.paths[name]
		if path == "" {
			continue
		}
		if verbose {
			fmt.Printf("insmod %s\n", path)
		}
		moduleParameters := ""
		if name == target {
			moduleParameters = parameters
		}
		if err := insertModule(path, moduleParameters); err != nil && !errors.Is(err, syscall.EEXIST) {
			if !quiet {
				fatalf("modprobe", "%s: %v", name, err)
			}
			return 1
		}
	}
	return 0
}

func readModuleDatabase(root string) (*moduleDatabase, error) {
	database := &moduleDatabase{root: root, paths: map[string]string{}, deps: map[string][]string{}, builtins: map[string]bool{}}
	depFile, err := os.Open(filepath.Join(root, "modules.dep"))
	if err != nil {
		return nil, err
	}
	defer depFile.Close()
	scanner := bufio.NewScanner(depFile)
	for scanner.Scan() {
		path, dependencyText, ok := strings.Cut(scanner.Text(), ":")
		if !ok {
			continue
		}
		name := moduleName(path)
		database.paths[name] = filepath.Join(root, path)
		for _, dependency := range strings.Fields(dependencyText) {
			dependencyName := moduleName(dependency)
			database.deps[name] = append(database.deps[name], dependencyName)
			if _, present := database.paths[dependencyName]; !present {
				database.paths[dependencyName] = filepath.Join(root, dependency)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if data, readErr := os.ReadFile(filepath.Join(root, "modules.alias")); readErr == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 3 && fields[0] == "alias" {
				database.aliases = append(database.aliases, moduleAlias{fields[1], moduleName(fields[2])})
			}
		}
	}
	if data, readErr := os.ReadFile(filepath.Join(root, "modules.builtin")); readErr == nil {
		for _, line := range strings.Fields(string(data)) {
			database.builtins[moduleName(line)] = true
		}
	}
	return database, nil
}

func (database *moduleDatabase) resolve(request string) string {
	name := moduleName(request)
	if _, exists := database.paths[name]; exists || database.builtins[name] {
		return name
	}
	for _, alias := range database.aliases {
		if matched, _ := filepath.Match(alias.pattern, request); matched {
			return alias.module
		}
	}
	return ""
}

func (database *moduleDatabase) loadOrder(target string) []string {
	seen := map[string]bool{}
	var order []string
	var visit func(string)
	visit = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		for _, dependency := range database.deps[name] {
			visit(dependency)
		}
		order = append(order, name)
	}
	visit(target)
	return order
}

func moduleName(path string) string {
	name := filepath.Base(path)
	for _, suffix := range []string{".zst", ".xz", ".gz"} {
		name = strings.TrimSuffix(name, suffix)
	}
	name = strings.TrimSuffix(name, ".ko")
	return strings.ReplaceAll(name, "-", "_")
}
