// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

//go:build linux

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const maxAccountDatabaseBytes = 16 << 20

// These paths are variables so the database transaction can be exercised with
// disposable fixtures. They are never selected from command-line input.
var (
	accountPasswdPath = loginPasswdPath
	accountShadowPath = loginShadowPath
	accountGroupPath  = loginGroupPath
	accountHomeRoot   = "/home"
)

type accountPaths struct {
	passwd, shadow, group, homeRoot string
}

func currentAccountPaths() accountPaths {
	return accountPaths{
		passwd: accountPasswdPath, shadow: accountShadowPath,
		group: accountGroupPath, homeRoot: accountHomeRoot,
	}
}

type accountFile struct {
	path     string
	data     []byte
	original []byte
	info     os.FileInfo
}

type accountUser struct {
	name     string
	uid, gid int
}

type accountGroup struct {
	name    string
	gid     int
	members []string
	line    int
}

type accountDatabase struct {
	passwd, shadow, group accountFile
	users                 map[string]accountUser
	uids                  map[int]bool
	shadowNames           map[string]bool
	groups                map[string]accountGroup
	gids                  map[int]bool
}

// The account tools report failures through the exit statuses shadow-utils
// documents, because scripts branch on them; see useradd(8) EXIT VALUES. The
// default of 1 is its "can't update password file" case.
const (
	accountStatusUsage     = 2
	accountStatusBadArg    = 3
	accountStatusIDInUse   = 4
	accountStatusNotFound  = 6
	accountStatusNameInUse = 9
	accountStatusHomedir   = 12
	accountStatusBadName   = 19
)

// accountError pairs a failure with the status the originals exit with.
type accountError struct {
	status int
	err    error
}

func (e accountError) Error() string { return e.err.Error() }
func (e accountError) Unwrap() error { return e.err }

func accountFail(status int, format string, args ...any) error {
	return accountError{status: status, err: fmt.Errorf(format, args...)}
}

func accountStatus(err error) int {
	var failure accountError
	if errors.As(err, &failure) {
		return failure.status
	}
	return 1
}

// accountIDError labels a clash on an explicitly requested UID or GID with the
// caller's wording and the originals' "already in use" status. Exhaustion of
// the identifier space is a different failure and keeps the generic status.
func accountIDError(err error, format string, requested *int) error {
	if errors.Is(err, errAccountIDInUse) && requested != nil {
		return accountFail(accountStatusIDInUse, format, *requested)
	}
	return err
}

func cmdGroupadd(args []string) int {
	if !accountAdministrator("groupadd") {
		return 1
	}
	name, gid, err := parseGroupaddArgs(args)
	if err != nil {
		fatalf("groupadd", "%v", err)
		return accountStatus(err)
	}
	if err := createAccountGroup(currentAccountPaths(), name, gid); err != nil {
		fatalf("groupadd", "%v", err)
		return accountStatus(err)
	}
	return 0
}

func cmdUseradd(args []string) int {
	if !accountAdministrator("useradd") {
		return 1
	}
	spec, err := parseUseraddArgs(args, false)
	if err != nil {
		fatalf("useradd", "%v", err)
		return accountStatus(err)
	}
	if err := createAccountUser(currentAccountPaths(), spec, time.Now()); err != nil {
		fatalf("useradd", "%v", err)
		return accountStatus(err)
	}
	return 0
}

func cmdAdduser(args []string) int {
	if !accountAdministrator("adduser") {
		return 1
	}
	if len(args) == 2 && !strings.HasPrefix(args[0], "-") && !strings.HasPrefix(args[1], "-") {
		if err := addAccountUserToGroup(currentAccountPaths(), args[0], args[1]); err != nil {
			fatalf("adduser", "%v", err)
			return accountStatus(err)
		}
		return 0
	}
	spec, err := parseUseraddArgs(args, true)
	if err != nil {
		fatalf("adduser", "%v", err)
		return accountStatus(err)
	}
	if err := createAccountUser(currentAccountPaths(), spec, time.Now()); err != nil {
		fatalf("adduser", "%v", err)
		return accountStatus(err)
	}
	return 0
}

func accountAdministrator(prog string) bool {
	if os.Geteuid() == 0 {
		return true
	}
	fatalf(prog, "must be run as root")
	return false
}

func parseGroupaddArgs(args []string) (string, *int, error) {
	args = expandShortOptions(args, "g")
	var gid *int
	var operands []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			operands = append(operands, args[i+1:]...)
			break
		}
		switch {
		case arg == "-g" || arg == "--gid":
			i++
			if i >= len(args) {
				return "", nil, fmt.Errorf("option %s requires an argument", arg)
			}
			value, err := parseAccountID(args[i])
			if err != nil {
				return "", nil, fmt.Errorf("invalid gid %q", args[i])
			}
			gid = &value
		case strings.HasPrefix(arg, "--gid="):
			value, err := parseAccountID(strings.TrimPrefix(arg, "--gid="))
			if err != nil {
				return "", nil, fmt.Errorf("invalid gid %q", strings.TrimPrefix(arg, "--gid="))
			}
			gid = &value
		case strings.HasPrefix(arg, "-"):
			return "", nil, fmt.Errorf("unsupported option %q", arg)
		default:
			operands = append(operands, arg)
		}
	}
	if len(operands) != 1 {
		return "", nil, accountFail(accountStatusUsage, "expected one group name")
	}
	if !validAccountName(operands[0]) {
		return "", nil, accountFail(accountStatusBadArg, "'%s' is not a valid group name", operands[0])
	}
	return operands[0], gid, nil
}

type useraddSpec struct {
	name                string
	uid                 *int
	primaryGroup        string
	supplementaryGroups []string
	home, shell, gecos  string
	createHome          bool
}

func parseUseraddArgs(args []string, createHome bool) (useraddSpec, error) {
	spec := useraddSpec{createHome: createHome, shell: "/bin/sh"}
	args = expandShortOptions(args, "ugGdsc")
	var operands []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			operands = append(operands, args[i+1:]...)
			break
		}
		switch {
		case arg == "-m" || arg == "--create-home":
			spec.createHome = true
		case arg == "-M" || arg == "--no-create-home":
			spec.createHome = false
		case arg == "-u" || arg == "--uid":
			i++
			if i >= len(args) {
				return spec, fmt.Errorf("option %s requires an argument", arg)
			}
			value, err := parseAccountID(args[i])
			if err != nil {
				return spec, fmt.Errorf("invalid uid %q", args[i])
			}
			spec.uid = &value
		case strings.HasPrefix(arg, "--uid="):
			value, err := parseAccountID(strings.TrimPrefix(arg, "--uid="))
			if err != nil {
				return spec, fmt.Errorf("invalid uid %q", strings.TrimPrefix(arg, "--uid="))
			}
			spec.uid = &value
		case arg == "-g" || arg == "--gid":
			i++
			if i >= len(args) {
				return spec, fmt.Errorf("option %s requires an argument", arg)
			}
			spec.primaryGroup = args[i]
		case strings.HasPrefix(arg, "--gid="):
			spec.primaryGroup = strings.TrimPrefix(arg, "--gid=")
		case arg == "-G" || arg == "--groups":
			i++
			if i >= len(args) {
				return spec, fmt.Errorf("option %s requires an argument", arg)
			}
			spec.supplementaryGroups = append(spec.supplementaryGroups, strings.Split(args[i], ",")...)
		case strings.HasPrefix(arg, "--groups="):
			spec.supplementaryGroups = append(spec.supplementaryGroups, strings.Split(strings.TrimPrefix(arg, "--groups="), ",")...)
		case arg == "-d" || arg == "--home-dir":
			i++
			if i >= len(args) {
				return spec, fmt.Errorf("option %s requires an argument", arg)
			}
			spec.home = args[i]
		case strings.HasPrefix(arg, "--home-dir="):
			spec.home = strings.TrimPrefix(arg, "--home-dir=")
		case arg == "-s" || arg == "--shell":
			i++
			if i >= len(args) {
				return spec, fmt.Errorf("option %s requires an argument", arg)
			}
			spec.shell = args[i]
		case strings.HasPrefix(arg, "--shell="):
			spec.shell = strings.TrimPrefix(arg, "--shell=")
		case arg == "-c" || arg == "--comment":
			i++
			if i >= len(args) {
				return spec, fmt.Errorf("option %s requires an argument", arg)
			}
			spec.gecos = args[i]
		case strings.HasPrefix(arg, "--comment="):
			spec.gecos = strings.TrimPrefix(arg, "--comment=")
		case strings.HasPrefix(arg, "-"):
			return spec, fmt.Errorf("unsupported option %q", arg)
		default:
			operands = append(operands, arg)
		}
	}
	if len(operands) != 1 {
		return spec, accountFail(accountStatusUsage, "expected one user name")
	}
	if !validAccountName(operands[0]) {
		return spec, accountFail(accountStatusBadName, "invalid user name '%s'", operands[0])
	}
	if strings.ContainsAny(spec.gecos, ":\r\n") || strings.ContainsAny(spec.home, ":\r\n") ||
		strings.ContainsAny(spec.shell, ":\r\n") {
		return spec, fmt.Errorf("invalid account field")
	}
	spec.name = operands[0]
	return spec, nil
}

func parseAccountID(value string) (int, error) {
	parsed, err := strconv.ParseUint(value, 10, 31)
	if err != nil {
		return 0, err
	}
	return int(parsed), nil
}

func validAccountName(value string) bool {
	if len(value) == 0 || len(value) > 32 {
		return false
	}
	for index, character := range value {
		if index == 0 {
			if character >= 'a' && character <= 'z' || character == '_' {
				continue
			}
			return false
		}
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			character == '_' || character == '-' || character == '$' && index == len(value)-1 {
			continue
		}
		return false
	}
	return true
}

func createAccountGroup(paths accountPaths, name string, wantedGID *int) error {
	return withAccountDatabase(paths, func(database *accountDatabase) error {
		if !validAccountName(name) {
			return accountFail(accountStatusBadArg, "'%s' is not a valid group name", name)
		}
		if _, found := database.groups[name]; found {
			return accountFail(accountStatusNameInUse, "group '%s' already exists", name)
		}
		gid, err := chooseAccountID(wantedGID, database.gids, nil)
		if err != nil {
			return accountIDError(err, "GID '%d' already exists", wantedGID)
		}
		database.appendGroup(name, gid)
		return nil
	})
}

func createAccountUser(paths accountPaths, spec useraddSpec, now time.Time) error {
	createdHome := ""
	err := withAccountDatabase(paths, func(database *accountDatabase) error {
		account, err := database.planUser(spec, paths.homeRoot, now)
		if err != nil {
			return err
		}
		if spec.createHome {
			if err := createAccountHome(account.home, account.uid, account.gid); err != nil {
				return err
			}
			createdHome = account.home
		}
		return nil
	})
	if err != nil && createdHome != "" {
		_ = os.Remove(createdHome)
	}
	return err
}

type createdAccount struct {
	name, home string
	uid, gid   int
}

func (database *accountDatabase) planUser(spec useraddSpec, homeRoot string, now time.Time) (createdAccount, error) {
	if !validAccountName(spec.name) {
		return createdAccount{}, accountFail(accountStatusBadName, "invalid user name '%s'", spec.name)
	}
	if _, found := database.users[spec.name]; found {
		return createdAccount{}, accountFail(accountStatusNameInUse, "user '%s' already exists", spec.name)
	}
	if database.shadowNames[spec.name] {
		return createdAccount{}, accountFail(accountStatusNameInUse, "shadow entry for '%s' already exists", spec.name)
	}
	usedForPrivateGroup := map[int]bool(nil)
	if spec.primaryGroup == "" {
		usedForPrivateGroup = database.gids
	}
	uid, err := chooseAccountID(spec.uid, database.uids, usedForPrivateGroup)
	if err != nil {
		return createdAccount{}, accountIDError(err, "UID %d is not unique", spec.uid)
	}
	gid := uid
	if spec.primaryGroup == "" {
		if _, found := database.groups[spec.name]; found {
			return createdAccount{}, accountFail(accountStatusNameInUse, "group '%s' already exists", spec.name)
		}
		database.appendGroup(spec.name, gid)
	} else {
		resolved, err := database.resolveGroup(spec.primaryGroup)
		if err != nil {
			return createdAccount{}, err
		}
		gid = resolved.gid
	}
	for _, groupName := range spec.supplementaryGroups {
		if groupName == "" {
			return createdAccount{}, fmt.Errorf("empty supplementary group")
		}
		group, err := database.resolveGroup(groupName)
		if err != nil {
			return createdAccount{}, err
		}
		if group.gid != gid {
			if err := database.addUserToGroup(spec.name, group.name); err != nil {
				return createdAccount{}, err
			}
		}
	}
	home := spec.home
	if home == "" {
		home = filepath.Join(homeRoot, spec.name)
	}
	if !filepath.IsAbs(home) {
		return createdAccount{}, fmt.Errorf("home directory must be absolute")
	}
	if spec.shell == "" || !filepath.IsAbs(spec.shell) {
		return createdAccount{}, fmt.Errorf("shell must be an absolute path")
	}
	days := now.Unix() / 86400
	database.passwd.data = appendAccountLine(database.passwd.data,
		fmt.Sprintf("%s:x:%d:%d:%s:%s:%s", spec.name, uid, gid, spec.gecos, home, spec.shell))
	database.shadow.data = appendAccountLine(database.shadow.data,
		fmt.Sprintf("%s:!:%d:0:99999:7:::", spec.name, days))
	database.users[spec.name] = accountUser{name: spec.name, uid: uid, gid: gid}
	database.uids[uid] = true
	database.shadowNames[spec.name] = true
	return createdAccount{name: spec.name, home: home, uid: uid, gid: gid}, nil
}

func createAccountHome(path string, uid, gid int) error {
	if info, err := os.Lstat(path); err == nil {
		if info.IsDir() {
			return accountFail(accountStatusHomedir, "home directory '%s' already exists", path)
		}
		return accountFail(accountStatusHomedir, "home path '%s' already exists", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return err
	}
	if err := os.Chown(path, uid, gid); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func addAccountUserToGroup(paths accountPaths, username, groupName string) error {
	return withAccountDatabase(paths, func(database *accountDatabase) error {
		if _, found := database.users[username]; !found {
			return fmt.Errorf("unknown user %q", username)
		}
		group, err := database.resolveGroup(groupName)
		if err != nil {
			return err
		}
		return database.addUserToGroup(username, group.name)
	})
}

// errAccountIDInUse marks a requested identifier that is already taken, so the
// caller can attach the wording and status its own tool uses.
var errAccountIDInUse = errors.New("identifier is already in use")

func chooseAccountID(requested *int, used, alsoUsed map[int]bool) (int, error) {
	if requested != nil {
		if used[*requested] || alsoUsed != nil && alsoUsed[*requested] {
			return 0, errAccountIDInUse
		}
		return *requested, nil
	}
	for value := 1000; value <= 1<<31-1; value++ {
		if !used[value] && (alsoUsed == nil || !alsoUsed[value]) {
			return value, nil
		}
	}
	return 0, fmt.Errorf("no available identifiers")
}

func (database *accountDatabase) resolveGroup(nameOrID string) (accountGroup, error) {
	if group, found := database.groups[nameOrID]; found {
		return group, nil
	}
	id, err := parseAccountID(nameOrID)
	if err != nil {
		return accountGroup{}, accountFail(accountStatusNotFound, "group '%s' does not exist", nameOrID)
	}
	for _, group := range database.groups {
		if group.gid == id {
			return group, nil
		}
	}
	return accountGroup{}, accountFail(accountStatusNotFound, "group '%s' does not exist", nameOrID)
}

func (database *accountDatabase) appendGroup(name string, gid int) {
	database.group.data = appendAccountLine(database.group.data, fmt.Sprintf("%s:x:%d:", name, gid))
	lines, _ := accountLines(database.group.data)
	database.groups[name] = accountGroup{name: name, gid: gid, line: len(lines) - 1}
	database.gids[gid] = true
}

func (database *accountDatabase) addUserToGroup(username, groupName string) error {
	group, found := database.groups[groupName]
	if !found {
		return accountFail(accountStatusNotFound, "group '%s' does not exist", groupName)
	}
	for _, member := range group.members {
		if member == username {
			return nil
		}
	}
	lines, finalNewline := accountLines(database.group.data)
	if group.line >= len(lines) {
		return fmt.Errorf("invalid group database")
	}
	fields := strings.Split(lines[group.line], ":")
	if len(fields) != 4 {
		return fmt.Errorf("invalid group entry for %q", groupName)
	}
	group.members = append(group.members, username)
	fields[3] = strings.Join(group.members, ",")
	lines[group.line] = strings.Join(fields, ":")
	database.group.data = joinAccountLines(lines, finalNewline)
	database.groups[groupName] = group
	return nil
}

func withAccountDatabase(paths accountPaths, mutate func(*accountDatabase) error) error {
	lock, err := lockPasswdDatabase(paths.passwd)
	if err != nil {
		return err
	}
	defer lock.Close()
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck // Best-effort unlock before close.
	database, err := loadAccountDatabase(paths)
	if err != nil {
		return err
	}
	if err := mutate(&database); err != nil {
		return err
	}
	return database.commit()
}

func loadAccountDatabase(paths accountPaths) (accountDatabase, error) {
	passwd, err := readAccountFile(paths.passwd)
	if err != nil {
		return accountDatabase{}, fmt.Errorf("read passwd: %w", err)
	}
	shadow, err := readAccountFile(paths.shadow)
	if err != nil {
		return accountDatabase{}, fmt.Errorf("read shadow: %w", err)
	}
	group, err := readAccountFile(paths.group)
	if err != nil {
		return accountDatabase{}, fmt.Errorf("read group: %w", err)
	}
	database := accountDatabase{
		passwd: passwd, shadow: shadow, group: group,
		users: map[string]accountUser{}, uids: map[int]bool{}, shadowNames: map[string]bool{},
		groups: map[string]accountGroup{}, gids: map[int]bool{},
	}
	if err := database.parsePasswd(); err != nil {
		return accountDatabase{}, err
	}
	if err := database.parseShadow(); err != nil {
		return accountDatabase{}, err
	}
	if err := database.parseGroup(); err != nil {
		return accountDatabase{}, err
	}
	return database, nil
}

func readAccountFile(path string) (accountFile, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return accountFile{}, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return accountFile{}, err
	}
	if !info.Mode().IsRegular() {
		return accountFile{}, fmt.Errorf("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxAccountDatabaseBytes+1))
	if err != nil {
		return accountFile{}, err
	}
	if len(data) > maxAccountDatabaseBytes {
		return accountFile{}, fmt.Errorf("file is too large")
	}
	return accountFile{path: path, data: data, original: append([]byte(nil), data...), info: info}, nil
}

func (database *accountDatabase) parsePasswd() error {
	lines, _ := accountLines(database.passwd.data)
	for _, line := range lines {
		if ignorableAccountLine(line) {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) != 7 || fields[0] == "" {
			return fmt.Errorf("invalid passwd database")
		}
		uid, uidErr := parseAccountID(fields[2])
		gid, gidErr := parseAccountID(fields[3])
		if uidErr != nil || gidErr != nil {
			return fmt.Errorf("invalid passwd database")
		}
		if _, duplicate := database.users[fields[0]]; duplicate {
			return fmt.Errorf("duplicate passwd entry for %q", fields[0])
		}
		database.users[fields[0]] = accountUser{name: fields[0], uid: uid, gid: gid}
		database.uids[uid] = true
	}
	return nil
}

func (database *accountDatabase) parseShadow() error {
	lines, _ := accountLines(database.shadow.data)
	for _, line := range lines {
		if ignorableAccountLine(line) {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) != 9 || fields[0] == "" {
			return fmt.Errorf("invalid shadow database")
		}
		if database.shadowNames[fields[0]] {
			return fmt.Errorf("duplicate shadow entry for %q", fields[0])
		}
		database.shadowNames[fields[0]] = true
	}
	return nil
}

func (database *accountDatabase) parseGroup() error {
	lines, _ := accountLines(database.group.data)
	for index, line := range lines {
		if ignorableAccountLine(line) {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) != 4 || fields[0] == "" {
			return fmt.Errorf("invalid group database")
		}
		gid, err := parseAccountID(fields[2])
		if err != nil {
			return fmt.Errorf("invalid group database")
		}
		if _, duplicate := database.groups[fields[0]]; duplicate {
			return fmt.Errorf("duplicate group entry for %q", fields[0])
		}
		members := []string{}
		if fields[3] != "" {
			members = strings.Split(fields[3], ",")
		}
		database.groups[fields[0]] = accountGroup{name: fields[0], gid: gid, members: members, line: index}
		database.gids[gid] = true
	}
	return nil
}

func (database *accountDatabase) commit() error {
	if !bytes.Equal(database.shadow.data, database.shadow.original) {
		if err := atomicReplacePasswdFile(database.shadow.path, database.shadow.data, database.shadow.info); err != nil {
			return err
		}
	}
	if !bytes.Equal(database.group.data, database.group.original) {
		if err := atomicReplacePasswdFile(database.group.path, database.group.data, database.group.info); err != nil {
			return err
		}
	}
	if !bytes.Equal(database.passwd.data, database.passwd.original) {
		if err := atomicReplacePasswdFile(database.passwd.path, database.passwd.data, database.passwd.info); err != nil {
			return err
		}
	}
	return nil
}

func accountLines(data []byte) ([]string, bool) {
	finalNewline := len(data) > 0 && data[len(data)-1] == '\n'
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return nil, finalNewline
	}
	return strings.Split(text, "\n"), finalNewline
}

func joinAccountLines(lines []string, finalNewline bool) []byte {
	if len(lines) == 0 {
		return nil
	}
	text := strings.Join(lines, "\n")
	if finalNewline {
		text += "\n"
	}
	return []byte(text)
}

func appendAccountLine(data []byte, line string) []byte {
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	data = append(data, line...)
	return append(data, '\n')
}

func ignorableAccountLine(line string) bool {
	return line == "" || strings.HasPrefix(line, "#")
}
