package isolation

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

const uidBase uint64 = 10000

type Identity struct {
	Name     string
	UID, GID int
}

func ForService(id string) (Identity, error) {
	value, ok := strings.CutPrefix(id, "whmcs-")
	if !ok || value == "" || value[0] == '0' {
		return Identity{}, fmt.Errorf("invalid service id %q", id)
	}
	n, err := strconv.ParseUint(value, 10, 32)
	if err != nil || n == 0 || n > uint64(^uint32(0))-1-uidBase {
		return Identity{}, fmt.Errorf("service id %q cannot map to a Unix uid", id)
	}
	uid := int(uidBase + n)
	return Identity{Name: id, UID: uid, GID: uid}, nil
}

func EnsureAccount(ctx context.Context, identity Identity, home string) error {
	gid := strconv.Itoa(identity.GID)
	group, err := user.LookupGroup(identity.Name)
	if err == nil {
		if group.Gid != gid {
			return fmt.Errorf("group %s has gid %s, expected %s", identity.Name, group.Gid, gid)
		}
	} else if !unknownGroup(err) {
		return err
	} else if group, err = user.LookupGroupId(gid); err == nil {
		return fmt.Errorf("gid %s belongs to group %s", gid, group.Name)
	} else if !unknownGroup(err) {
		return err
	} else if err := run(ctx, "groupadd", "--gid", gid, identity.Name); err != nil {
		return err
	}

	uid := strconv.Itoa(identity.UID)
	account, err := user.Lookup(identity.Name)
	if err == nil {
		if account.Uid != uid || account.Gid != gid {
			return fmt.Errorf("user %s has uid:gid %s:%s, expected %s:%s", identity.Name, account.Uid, account.Gid, uid, gid)
		}
		return nil
	}
	if !unknownUser(err) {
		return err
	}
	if account, err = user.LookupId(uid); err == nil {
		return fmt.Errorf("uid %s belongs to user %s", uid, account.Username)
	} else if !unknownUser(err) {
		return err
	}
	return run(ctx, "useradd", "--uid", uid, "--gid", gid, "--no-user-group", "--no-create-home", "--home-dir", home,
		"--shell", "/usr/sbin/nologin", identity.Name)
}

func ChownTree(root string, identity Identity) error {
	return filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(path, identity.UID, identity.GID)
	})
}

func RemoveAccount(ctx context.Context, identity Identity) error {
	account, err := user.Lookup(identity.Name)
	if err == nil {
		if account.Uid != strconv.Itoa(identity.UID) || account.Gid != strconv.Itoa(identity.GID) {
			return fmt.Errorf("refusing to remove user %s with unexpected uid:gid %s:%s", identity.Name, account.Uid, account.Gid)
		}
		if err := run(ctx, "userdel", identity.Name); err != nil {
			return err
		}
	} else if !unknownUser(err) {
		return err
	}
	group, err := user.LookupGroup(identity.Name)
	if err == nil {
		if group.Gid != strconv.Itoa(identity.GID) {
			return fmt.Errorf("refusing to remove group %s with unexpected gid %s", identity.Name, group.Gid)
		}
		return run(ctx, "groupdel", identity.Name)
	}
	if unknownGroup(err) {
		return nil
	}
	return err
}

func unknownUser(err error) bool {
	var byName user.UnknownUserError
	var byID user.UnknownUserIdError
	return errors.As(err, &byName) || errors.As(err, &byID)
}

func unknownGroup(err error) bool {
	var byName user.UnknownGroupError
	var byID user.UnknownGroupIdError
	return errors.As(err, &byName) || errors.As(err, &byID)
}

func run(ctx context.Context, name string, args ...string) error {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err == nil {
		return nil
	}
	if len(output) > 4096 {
		output = output[:4096]
	}
	return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(output)))
}
