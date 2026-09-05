// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

const (
	sysFinitModule       = 313 // amd64 __NR_finit_module
	moduleInitCompressed = 4
	// The two flags -f sets, which let a module built against another kernel
	// version load anyway.
	moduleInitIgnoreModversions = 1
	moduleInitIgnoreVermagic    = 2
	deleteModuleNonblocking     = syscall.O_NONBLOCK
	deleteModuleForce           = syscall.O_TRUNC
)

func cmdLsmod(args []string) int {
	for _, arg := range args {
		switch arg {
		case "-s", "--syslog", "-v", "--verbose":
			// Nothing here logs, and there is nothing more to say.
		default:
			fatalf("lsmod", "unexpected operand %q", arg)
			return 1
		}
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
		if usedBy != "" {
			// kmod reads the holders from sysfs rather than from
			// /proc/modules, and the two list them in different orders.
			if holders := moduleHolders(fields[0]); len(holders) > 0 {
				usedBy = strings.Join(holders, ",")
			}
		}
		// The columns are kmod's: nineteen for the name, eight for the size.
		fmt.Printf("%-19s %8s  %s", fields[0], fields[1], fields[2])
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

// moduleHolders lists the modules holding a reference to this one, in the
// order sysfs gives them, which is the order kmod prints.
func moduleHolders(name string) []string {
	entries, err := readDirRaw("/sys/module/" + name + "/holders")
	if err != nil {
		return nil
	}
	return entries
}

func cmdInsmod(args []string) int {
	force, verbose := false, false
	for len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "-" {
		switch args[0] {
		case "-f", "--force":
			force = true
		case "-v", "--verbose":
			verbose = true
		case "-s", "--syslog":
			// Nothing here logs.
		case "--":
			args = args[1:]
			goto operands
		default:
			fatalf("insmod", "unrecognized option '%s'", args[0])
			return 1
		}
		args = args[1:]
	}
operands:
	if len(args) == 0 {
		fatalf("insmod", "ERROR: missing filename.")
		return 1
	}
	if verbose {
		fmt.Printf("insmod %s\n", args[0])
	}
	if err := insertModule(args[0], strings.Join(args[1:], " "), force); err != nil {
		// kmod words this one itself rather than passing on the errno alone.
		fatalf("insmod", "ERROR: could not load module %s: %s", args[0], errText(err))
		return 1
	}
	return 0
}

// insertModule loads one module file. force sets the flags that let a module
// built for another kernel version in, which is what -f asks for.
func insertModule(path, parameters string, force bool) error {
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
	if force {
		flags |= moduleInitIgnoreModversions | moduleInitIgnoreVermagic
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
	force, verbose := false, false
	for len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "-" {
		switch args[0] {
		case "-f", "--force":
			force = true
		case "-v", "--verbose":
			verbose = true
		case "-s", "--syslog":
			// Nothing here logs.
		case "--":
			args = args[1:]
			goto operands
		default:
			fatalf("rmmod", "unrecognized option '%s'", args[0])
			return 1
		}
		args = args[1:]
	}
operands:
	if len(args) == 0 {
		fatalf("rmmod", "ERROR: missing module name.")
		return 1
	}
	status := 0
	for _, name := range args {
		if verbose {
			fmt.Printf("rmmod %s\n", moduleName(name))
		}
		// kmod looks the module up in the loaded list before it asks the
		// kernel, so a name that is not there is reported as such whatever
		// privilege the caller has. -f skips the check, as it does there.
		if !force && !moduleLoaded(moduleName(name)) {
			fatalf("rmmod", "ERROR: Module %s is not currently loaded", moduleName(name))
			status = 1
			continue
		}
		if err := removeModule(name, force); err != nil {
			if errors.Is(err, syscall.EWOULDBLOCK) {
				fatalf("rmmod", "ERROR: Module %s is in use", moduleName(name))
			} else {
				// kmod reports the failure twice, once against the name it was
				// given and once against the module it stands for.
				fatalf("rmmod", "ERROR: could not remove '%s': %s", moduleName(name), errText(err))
				fatalf("rmmod", "ERROR: could not remove module %s: %s", moduleName(name), errText(err))
			}
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

//nolint:gocyclo // one option table; splitting it would only scatter the command line.
func cmdModprobe(args []string) int {
	remove, quiet, verbose := false, false, false
	all, dryRun, force, showDepends, firstTime := false, false, false, false, false
	release, directory := "", "/lib/modules"
	for len(args) > 0 {
		arg := args[0]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			break
		}
		if arg == "--" {
			args = args[1:]
			break
		}
		name, value, hasValue := arg, "", false
		if strings.HasPrefix(arg, "--") {
			if eq := strings.IndexByte(arg, '='); eq >= 0 {
				name, value, hasValue = arg[:eq], arg[eq+1:], true
			}
		}
		needValue := func() (string, bool) {
			if hasValue {
				return value, true
			}
			if len(args) < 2 {
				fatalf("modprobe", "option '%s' requires an argument", name)
				return "", false
			}
			args = args[1:]
			return args[0], true
		}
		switch name {
		case "-r", "--remove":
			remove = true
		case "-q", "--quiet":
			quiet = true
		case "-v", "--verbose":
			verbose = true
		case "-a", "--all":
			all = true
		case "-n", "--dry-run", "--show":
			dryRun = true
		case "-f", "--force", "--force-vermagic", "--force-modversion":
			force = true
		case "-D", "--show-depends":
			showDepends, dryRun = true, true
		case "--first-time":
			firstTime = true
		case "-i", "--ignore-install", "--ignore-remove", "-b", "--use-blacklist",
			"-s", "--syslog", "--remove-dependencies", "--remove-holders":
			// There are no install or remove commands to ignore here, and no
			// blacklist is consulted, so these change nothing.
		case "-d", "--dirname":
			text, ok := needValue()
			if !ok {
				return 1
			}
			directory = text
		case "-S", "--set-version":
			text, ok := needValue()
			if !ok {
				return 1
			}
			release = text
		case "-C", "--config", "-w", "--wait":
			if _, ok := needValue(); !ok {
				return 1
			}
		default:
			fatalf("modprobe", "unrecognized option '%s'", arg)
			return 1
		}
		args = args[1:]
	}
	if len(args) == 0 {
		fatalf("modprobe", "ERROR: missing parameters. See -h.")
		return 1
	}
	if release == "" {
		var uts syscall.Utsname
		if err := syscall.Uname(&uts); err != nil {
			fatalf("modprobe", "%v", err)
			return 1
		}
		release = utsField(uts.Release[:])
	}
	root := filepath.Join(directory, release)
	database, err := readModuleDatabase(root)
	if err != nil {
		if !quiet {
			fatalf("modprobe", "%v", err)
		}
		return 1
	}
	// -a treats every operand as a module name; without it the operands after
	// the first are that module's parameters.
	names := args[:1]
	if all {
		names = args
	}
	status := 0
	for _, operand := range names {
		parameters := ""
		if !all {
			parameters = strings.Join(args[1:], " ")
		}
		if code := modprobeOne(database, root, operand, parameters, modprobeRun{
			remove: remove, quiet: quiet, verbose: verbose, dryRun: dryRun,
			force: force, showDepends: showDepends, firstTime: firstTime,
		}); code != 0 {
			status = code
		}
	}
	return status
}

// modprobeRun is what one modprobe invocation does with each module it is
// given.
type modprobeRun struct {
	remove      bool
	quiet       bool
	verbose     bool
	dryRun      bool
	force       bool
	showDepends bool
	firstTime   bool
}

func modprobeOne(database *moduleDatabase, root, operand, parameters string, run modprobeRun) int {
	quiet, verbose, remove := run.quiet, run.verbose, run.remove
	target := database.resolve(operand)
	if target == "" {
		if !quiet {
			// kmod words this one as a fatal error naming the directory it
			// searched.
			fatalf("modprobe", "FATAL: Module %s not found in directory %s", operand, root)
		}
		return 1
	}
	if remove {
		// A module anything still refers to cannot go, and kmod says so before
		// it touches anything. The count in /proc/modules covers both the
		// modules holding it and every other user.
		if moduleRefcount(target) > 0 {
			if !quiet {
				fatalf("modprobe", "FATAL: Module %s is in use.", target)
			}
			return 1
		}
		order := database.loadOrder(target)
		for i := len(order) - 1; i >= 0; i-- {
			// What is not loaded is nothing to remove, which is why a dry run
			// over an unloaded tree prints nothing.
			if !moduleLoaded(order[i]) {
				continue
			}
			if verbose {
				fmt.Printf("rmmod %s\n", order[i])
			}
			if run.dryRun {
				continue
			}
			err := removeModule(order[i], run.force)
			switch {
			case err == nil:
			case errors.Is(err, syscall.ENOENT) && !run.firstTime:
				// Already gone, which is only an error under --first-time.
			case !quiet:
				fatalf("modprobe", "FATAL: Module %s could not be removed: %s", order[i], errText(err))
				return 1
			default:
				return 1
			}
		}
		return 0
	}
	for _, name := range database.loadOrder(target) {
		if database.builtins[name] {
			continue
		}
		path := database.paths[name]
		if path == "" {
			continue
		}
		// A module that is already loaded is nothing to do, which is why a dry
		// run over a loaded tree prints nothing at all.
		if moduleLoaded(name) && !run.showDepends {
			continue
		}
		// The dry run alone says nothing; it is -v, or -D, that asks for the
		// listing.
		if run.showDepends || verbose {
			// kmod leaves the parameter field in place even when it is empty,
			// so the line ends in a space.
			fmt.Printf("insmod %s %s\n", path, strings.TrimSpace(moduleParametersFor(name, target, parameters)))
		}
		if run.dryRun {
			continue
		}
		err := insertModule(path, moduleParametersFor(name, target, parameters), run.force)
		switch {
		case err == nil:
		case errors.Is(err, syscall.EEXIST) && !run.firstTime:
			// Already loaded, which is only an error under --first-time.
		case !quiet:
			fatalf("modprobe", "ERROR: could not insert '%s': %s", name, errText(err))
			return 1
		default:
			return 1
		}
	}
	return 0
}

// moduleRefcount is how many users the kernel counts for a module, which is
// -1 when it is not loaded at all.
func moduleRefcount(name string) int {
	data, err := os.ReadFile("/proc/modules")
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == name {
			if count, convErr := strconv.Atoi(fields[2]); convErr == nil {
				return count
			}
		}
	}
	return -1
}

// moduleLoaded reports whether the kernel already holds this module.
func moduleLoaded(name string) bool {
	data, err := os.ReadFile("/proc/modules")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if field, _, _ := strings.Cut(line, " "); field == name {
			return true
		}
	}
	return false
}

// moduleParametersFor gives the parameters only to the module they were meant
// for, which is the one named on the command line.
func moduleParametersFor(name, target, parameters string) string {
	if name == target {
		return parameters
	}
	return ""
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

// loadOrder is the order the modules have to be inserted in. modules.dep
// already lists a module's whole dependency closure, nearest first, so the
// order is that list reversed with the module itself last — which is the order
// kmod loads them in.
func (database *moduleDatabase) loadOrder(target string) []string {
	dependencies := database.deps[target]
	order := make([]string, 0, len(dependencies)+1)
	seen := map[string]bool{}
	for i := len(dependencies) - 1; i >= 0; i-- {
		if seen[dependencies[i]] {
			continue
		}
		seen[dependencies[i]] = true
		order = append(order, dependencies[i])
	}
	if !seen[target] {
		order = append(order, target)
	}
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
