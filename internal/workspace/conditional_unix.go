//go:build linux || darwin

package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/GoGoslingCA/gogosling/internal/model"
	"golang.org/x/sys/unix"
)

type conditionalParent struct {
	fd                          int
	rootID, parentID, rel, base string
}
type conditionalIdentity struct {
	Path, Parent        string
	Device, Inode       uint64
	Mode                uint32
	UID, GID            uint32
	MtimeSec, MtimeNano int64
}
type reservationJournal struct {
	Version     int      `json:"version"`
	OperationID string   `json:"operation_id"`
	Path        string   `json:"path"`
	Before      Snapshot `json:"before"`
	After       Snapshot `json:"after"`
	Delete      bool     `json:"delete"`
	Stage       string   `json:"stage"`
}

func statID(st unix.Stat_t) string { return fmt.Sprintf("%d:%d", st.Dev, st.Ino) }

func (w *Workspace) openConditionalParent(ctx context.Context, rel string) (conditionalParent, error) {
	var out conditionalParent
	if err := checkCtx(ctx); err != nil {
		return out, err
	}
	fd, err := unix.Open(w.realRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return out, conditionalError(model.CodeForbidden, "unsafe_root", err)
	}
	var root unix.Stat_t
	if err = unix.Fstat(fd, &root); err != nil {
		_ = unix.Close(fd)
		return out, conditionalError(model.CodeInternal, "stat_failed", err)
	}
	// 配置根本身可以是符号链接，但必须仍指向本次 Workspace 打开时的真实根。
	var configuredRoot unix.Stat_t
	if err := unix.Stat(w.root, &configuredRoot); err != nil || statID(configuredRoot) != statID(root) {
		_ = unix.Close(fd)
		return out, conditionalError(model.CodeConflict, "unsafe_root", err)
	}
	parent := path.Dir(rel)
	if parent != "." {
		for _, part := range strings.Split(parent, "/") {
			if err := checkCtx(ctx); err != nil {
				_ = unix.Close(fd)
				return out, err
			}
			next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			_ = unix.Close(fd)
			if err != nil {
				return out, conditionalError(model.CodeForbidden, "unsafe_parent", err)
			}
			fd = next
			var st unix.Stat_t
			if err := unix.Fstat(fd, &st); err != nil {
				_ = unix.Close(fd)
				return out, conditionalError(model.CodeInternal, "stat_failed", err)
			}
			if st.Dev != root.Dev {
				_ = unix.Close(fd)
				return out, conditionalError(model.CodeUnavailable, "cross_filesystem", nil)
			}
		}
	}
	var parentStat unix.Stat_t
	if err = unix.Fstat(fd, &parentStat); err != nil {
		_ = unix.Close(fd)
		return out, conditionalError(model.CodeInternal, "stat_failed", err)
	}
	if err := rejectUnsupportedMetadata(fd, parentStat); err != nil {
		_ = unix.Close(fd)
		return out, err
	}
	return conditionalParent{fd: fd, rootID: statID(root), parentID: statID(parentStat), rel: rel, base: path.Base(rel)}, nil
}

func (w *Workspace) verifyConditionalParent(ctx context.Context, p conditionalParent) error {
	current, err := w.openConditionalParent(ctx, p.rel)
	if err != nil {
		return err
	}
	defer unix.Close(current.fd)
	if current.rootID != p.rootID || current.parentID != p.parentID {
		return conditionalError(model.CodeConflict, "parent_changed", nil)
	}
	return nil
}

func missingSnapshot(p conditionalParent) Snapshot {
	identity, _ := json.Marshal(conditionalIdentity{Path: p.rel, Parent: p.parentID})
	return Snapshot{Identity: string(identity), RootIdentity: p.rootID}
}

func readConditionalAt(ctx context.Context, fd int, name string, p conditionalParent, max int64) (Snapshot, error) {
	if err := checkCtx(ctx); err != nil {
		return Snapshot{}, err
	}
	opened, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return missingSnapshot(p), nil
	}
	if err != nil {
		return Snapshot{}, conditionalError(model.CodeForbidden, "unsafe_file", err)
	}
	file := os.NewFile(uintptr(opened), name)
	defer file.Close()
	var before, after unix.Stat_t
	if err := unix.Fstat(opened, &before); err != nil {
		return Snapshot{}, conditionalError(model.CodeInternal, "stat_failed", err)
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Mode&0o7000 != 0 || before.Uid != uint32(os.Geteuid()) {
		return Snapshot{}, conditionalError(model.CodeForbidden, "unsafe_file", nil)
	}
	if err := rejectUnsupportedMetadata(opened, before); err != nil {
		return Snapshot{}, err
	}
	if before.Size > max {
		return Snapshot{}, conditionalError(model.CodeInvalid, "text_too_large", nil)
	}
	body, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil {
		return Snapshot{}, conditionalError(model.CodeInternal, "read_failed", err)
	}
	if int64(len(body)) > max || !utf8.Valid(body) {
		return Snapshot{}, conditionalError(model.CodeInvalid, "invalid_text", nil)
	}
	if err := checkCtx(ctx); err != nil {
		return Snapshot{}, err
	}
	if err := unix.Fstat(opened, &after); err != nil {
		return Snapshot{}, conditionalError(model.CodeInternal, "stat_failed", err)
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size || before.Mode != after.Mode || before.Nlink != after.Nlink || before.Uid != after.Uid || before.Gid != after.Gid || before.Mtim != after.Mtim || before.Ctim != after.Ctim {
		return Snapshot{}, conditionalError(model.CodeConflict, "file_changed", nil)
	}
	identity, _ := json.Marshal(conditionalIdentity{Path: p.rel, Parent: p.parentID, Device: uint64(after.Dev), Inode: after.Ino, Mode: uint32(after.Mode), UID: after.Uid, GID: after.Gid, MtimeSec: after.Mtim.Sec, MtimeNano: after.Mtim.Nsec})
	return Snapshot{Exists: true, SHA256: digestText(string(body)), Identity: string(identity), RootIdentity: p.rootID, Content: string(body), Size: int64(len(body))}, nil
}

func sameSnapshot(a, b Snapshot) bool {
	return a.Exists == b.Exists && a.SHA256 == b.SHA256 && a.Size == b.Size && a.Identity == b.Identity && a.RootIdentity == b.RootIdentity
}

func snapshotContextMatches(snapshot Snapshot, p conditionalParent) bool {
	var identity conditionalIdentity
	return snapshot.RootIdentity == p.rootID && json.Unmarshal([]byte(snapshot.Identity), &identity) == nil && identity.Path == p.rel && identity.Parent == p.parentID
}

func (w *Workspace) snapshotConditional(ctx context.Context, rel string, max int64) (Snapshot, error) {
	p, err := w.openConditionalParent(ctx, rel)
	if err != nil {
		return Snapshot{}, err
	}
	defer unix.Close(p.fd)
	snapshot, err := readConditionalAt(ctx, p.fd, p.base, p, max)
	if err != nil {
		return Snapshot{}, err
	}
	if err := w.verifyConditionalParent(ctx, p); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func openReservation(p conditionalParent, id string, create bool) (int, error) {
	name := reservationName(id)
	if create {
		if err := unix.Mkdirat(p.fd, name, 0o700); err != nil {
			if errors.Is(err, unix.EEXIST) {
				return -1, conditionalError(model.CodeConflict, "operation_exists", err)
			}
			return -1, conditionalError(model.CodeInternal, "reservation_failed", err)
		}
	}
	fd, err := unix.Openat(p.fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return -1, conditionalError(model.CodeNotFound, "reservation_missing", err)
		}
		return -1, conditionalError(model.CodeForbidden, "unsafe_reservation", err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return -1, conditionalError(model.CodeInternal, "stat_failed", err)
	}
	if st.Mode&0o777 != 0o700 || st.Uid != uint32(os.Geteuid()) {
		_ = unix.Close(fd)
		return -1, conditionalError(model.CodeForbidden, "unsafe_reservation", nil)
	}
	return fd, nil
}

func writePrivateAt(fd int, name string, body []byte) error {
	opened, err := unix.Openat(fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(opened), name)
	if err := unix.Fchmod(opened, 0o600); err != nil {
		_ = f.Close()
		_ = unix.Unlinkat(fd, name, 0)
		return err
	}
	_, err = f.Write(body)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		_ = unix.Unlinkat(fd, name, 0)
		return err
	}
	if closeErr != nil {
		_ = unix.Unlinkat(fd, name, 0)
	}
	return closeErr
}

func writeJournal(fd int, j reservationJournal) error {
	body, err := json.Marshal(j)
	if err != nil {
		return err
	}
	temp := "journal-" + model.NewID() + ".tmp"
	if err := writePrivateAt(fd, temp, body); err != nil {
		return err
	}
	if err := unix.Renameat(fd, temp, fd, "journal.json"); err != nil {
		_ = unix.Unlinkat(fd, temp, 0)
		return err
	}
	return unix.Fsync(fd)
}

func readJournal(fd int) (reservationJournal, error) {
	var j reservationJournal
	opened, err := unix.Openat(fd, "journal.json", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return j, err
	}
	f := os.NewFile(uintptr(opened), "journal.json")
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(opened, &st); err != nil {
		return j, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || st.Size > 32<<10 || st.Mode&0o777 != 0o600 || st.Uid != uint32(os.Geteuid()) {
		return j, conditionalError(model.CodeForbidden, "unsafe_reservation", nil)
	}
	body, err := io.ReadAll(io.LimitReader(f, 32<<10))
	if err != nil {
		return j, err
	}
	err = json.Unmarshal(body, &j)
	if err == nil && j.Version != 1 {
		err = conditionalError(model.CodeConflict, "invalid_journal", nil)
	}
	return j, err
}

func journalMatches(j reservationJournal, r Receipt, proposed *string) bool {
	if j.OperationID != r.OperationID || j.Path != r.Path || !sameSnapshot(j.Before, r.Before) || j.Delete != (proposed == nil) {
		return false
	}
	return proposed == nil || j.After.SHA256 == digestText(*proposed)
}

func recoverRequired(r Receipt, reason string, err error) (Receipt, error) {
	r.State = "recovery_required"
	r.After = Snapshot{}
	r.Reason = reason
	return r, conditionalError(model.CodeConflict, reason, err)
}

func platformRenameError(err error) error {
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) {
		return conditionalError(model.CodeUnavailable, "exclusive_rename_unavailable", err)
	}
	return conditionalError(model.CodeConflict, "file_changed", err)
}

func (w *Workspace) applyConditional(ctx context.Context, r Receipt, proposed *string) (Receipt, error) {
	p, err := w.openConditionalParent(ctx, r.Path)
	if err != nil {
		return r, err
	}
	defer unix.Close(p.fd)
	actual, err := readConditionalAt(ctx, p.fd, p.base, p, ConditionalTextLimit)
	if err != nil {
		return r, err
	}
	r.After = actual
	if !sameSnapshot(actual, r.Before) {
		r.State = "conflict"
		r.Reason = "file_changed"
		return r, conditionalError(model.CodeConflict, "file_changed", nil)
	}
	fd, err := openReservation(p, r.OperationID, true)
	if err != nil {
		return r, err
	}
	defer unix.Close(fd)
	j := reservationJournal{Version: 1, OperationID: r.OperationID, Path: r.Path, Before: r.Before, Delete: proposed == nil, Stage: "prepared", After: missingSnapshot(p)}
	if proposed != nil {
		if err := writePrivateAt(fd, "proposed", []byte(*proposed)); err != nil {
			return recoverRequired(r, "reservation_io_failed", err)
		}
		if r.Before.Exists {
			if err := preserveProposalMode(fd, r.Before); err != nil {
				return recoverRequired(r, "unsupported_metadata", err)
			}
		}
		j.After, err = readConditionalAt(ctx, fd, "proposed", p, ConditionalTextLimit)
		if err != nil {
			return recoverRequired(r, "reservation_io_failed", err)
		}
	}
	if err := writeJournal(fd, j); err != nil {
		return recoverRequired(r, "reservation_io_failed", err)
	}
	if err := unix.Fsync(p.fd); err != nil {
		return recoverRequired(r, "reservation_io_failed", err)
	}
	if err := w.verifyConditionalParent(ctx, p); err != nil {
		return recoverRequired(r, "parent_changed", err)
	}
	if w.conditionalHooks != nil && w.conditionalHooks.beforeMove != nil {
		w.conditionalHooks.beforeMove()
	}
	if err := checkCtx(ctx); err != nil {
		j.Stage = "restored"
		if saveErr := writeJournal(fd, j); saveErr != nil {
			return recoverRequired(r, "reservation_io_failed", saveErr)
		}
		r.State = "unchanged"
		r.Reason = "cancelled"
		return r, err
	}
	if r.Before.Exists {
		if err := renameExclusive(p.fd, p.base, fd, "original"); err != nil {
			j.Stage = "restored"
			_ = writeJournal(fd, j)
			r.State = "conflict"
			r.Reason = "file_changed"
			return r, platformRenameError(err)
		}
		if err := unix.Fsync(p.fd); err != nil {
			return recoverRequired(r, "reservation_io_failed", err)
		}
		if err := unix.Fsync(fd); err != nil {
			return recoverRequired(r, "reservation_io_failed", err)
		}
		moved, err := readConditionalAt(ctx, fd, "original", p, ConditionalTextLimit)
		if err != nil || !sameSnapshot(moved, r.Before) {
			return w.restoreConditional(ctx, p, fd, j, r, "file_changed", err)
		}
		j.Stage = "moved"
		if err := writeJournal(fd, j); err != nil {
			return recoverRequired(r, "reservation_io_failed", err)
		}
	}
	if err := checkCtx(ctx); err != nil {
		return w.restoreConditional(context.WithoutCancel(ctx), p, fd, j, r, "cancelled", err)
	}
	if w.conditionalHooks != nil && w.conditionalHooks.beforeInstall != nil {
		w.conditionalHooks.beforeInstall()
	}
	if err := w.verifyConditionalParent(ctx, p); err != nil {
		return recoverRequired(r, "parent_changed", err)
	}
	if proposed != nil {
		staged, err := readConditionalAt(ctx, fd, "proposed", p, ConditionalTextLimit)
		if err != nil || !sameSnapshot(staged, j.After) {
			return w.restoreConditional(ctx, p, fd, j, r, "proposed_changed", err)
		}
		if err := renameExclusive(fd, "proposed", p.fd, p.base); err != nil {
			return w.restoreConditional(ctx, p, fd, j, r, "file_changed", platformRenameError(err))
		}
	}
	if w.conditionalHooks != nil && w.conditionalHooks.afterInstall != nil {
		w.conditionalHooks.afterInstall()
	}
	if err := unix.Fsync(p.fd); err != nil {
		return recoverRequired(r, "reservation_io_failed", err)
	}
	if err := unix.Fsync(fd); err != nil {
		return recoverRequired(r, "reservation_io_failed", err)
	}
	actual, err = readConditionalAt(ctx, p.fd, p.base, p, ConditionalTextLimit)
	if err != nil || !sameSnapshot(actual, j.After) {
		return recoverRequired(r, "file_changed_after_install", err)
	}
	if err := w.verifyConditionalParent(ctx, p); err != nil {
		return recoverRequired(r, "parent_changed", err)
	}
	if j.Before.Exists {
		original, err := readConditionalAt(ctx, fd, "original", p, ConditionalTextLimit)
		if err != nil || !sameSnapshot(original, j.Before) {
			return recoverRequired(r, "original_changed", err)
		}
	}
	j.Stage = "applied"
	if err := writeJournal(fd, j); err != nil {
		return recoverRequired(r, "reservation_io_failed", err)
	}
	r.State = "applied"
	r.After = actual
	return r, nil
}

// preserveProposalMode 只从服务端冻结的文件身份恢复权限，不接受外部 mode 参数。
// owner 必须仍为当前用户；group 无法保持时拒绝，不能默默改变访问对象。
func preserveProposalMode(fd int, expected Snapshot) error {
	var identity conditionalIdentity
	if err := json.Unmarshal([]byte(expected.Identity), &identity); err != nil {
		return conditionalError(model.CodeInvalid, "invalid_snapshot", err)
	}
	if identity.Mode&0o7000 != 0 || identity.UID != uint32(os.Geteuid()) {
		return conditionalError(model.CodeForbidden, "unsupported_metadata", nil)
	}
	opened, err := unix.Openat(fd, "proposed", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(opened)
	var st unix.Stat_t
	if err := unix.Fstat(opened, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || st.Uid != identity.UID {
		return conditionalError(model.CodeForbidden, "unsafe_file", nil)
	}
	if err := rejectUnsupportedMetadata(opened, st); err != nil {
		return err
	}
	if st.Gid != identity.GID {
		if err := unix.Fchown(opened, -1, int(identity.GID)); err != nil {
			return err
		}
	}
	if err := unix.Fchmod(opened, identity.Mode&0o777); err != nil {
		return err
	}
	return unix.Fsync(opened)
}

func (w *Workspace) restoreConditional(ctx context.Context, p conditionalParent, fd int, j reservationJournal, r Receipt, reason string, cause error) (Receipt, error) {
	// 一旦原件已移动，取消请求不能跳过最小的无覆盖恢复尝试。
	ctx = context.WithoutCancel(ctx)
	if err := w.verifyConditionalParent(ctx, p); err != nil {
		return recoverRequired(r, "parent_changed", err)
	}
	if j.Before.Exists {
		if err := renameExclusive(fd, "original", p.fd, p.base); err != nil {
			reason := "restore_failed"
			if errors.Is(err, unix.EEXIST) {
				reason = "restore_target_occupied"
			}
			return recoverRequired(r, reason, err)
		}
	}
	if err := unix.Fsync(p.fd); err != nil {
		return recoverRequired(r, "reservation_io_failed", err)
	}
	j.Stage = "restored"
	if err := writeJournal(fd, j); err != nil {
		return recoverRequired(r, "reservation_io_failed", err)
	}
	r.State = "restored"
	r.Reason = reason
	r.After, _ = readConditionalAt(ctx, p.fd, p.base, p, ConditionalTextLimit)
	if model.IsCode(cause, model.CodeUnavailable) {
		r.Reason = "exclusive_rename_unavailable"
		return r, conditionalError(model.CodeUnavailable, r.Reason, cause)
	}
	return r, conditionalError(model.CodeConflict, reason, cause)
}

func (w *Workspace) recoverConditional(ctx context.Context, r Receipt, proposed *string) (Receipt, error) {
	p, err := w.openConditionalParent(ctx, r.Path)
	if err != nil {
		return r, err
	}
	defer unix.Close(p.fd)
	fd, err := openReservation(p, r.OperationID, false)
	if err != nil {
		return r, err
	}
	defer unix.Close(fd)
	j, err := readJournal(fd)
	if errors.Is(err, unix.ENOENT) {
		// 崩溃可发生在创建保留目录后、第一份日志落盘前；只有目标仍匹配且没有原件被移动时才确认未改动。
		actual, readErr := readConditionalAt(ctx, p.fd, p.base, p, ConditionalTextLimit)
		var original unix.Stat_t
		originalErr := unix.Fstatat(fd, "original", &original, unix.AT_SYMLINK_NOFOLLOW)
		if readErr == nil && errors.Is(originalErr, unix.ENOENT) && sameSnapshot(actual, r.Before) {
			j = reservationJournal{Version: 1, OperationID: r.OperationID, Path: r.Path, Before: r.Before, After: missingSnapshot(p), Delete: proposed == nil, Stage: "restored"}
			if proposed != nil {
				j.After.SHA256 = digestText(*proposed)
				j.After.Size = int64(len(*proposed))
				j.After.Exists = true
			}
			if err := writeJournal(fd, j); err != nil {
				return recoverRequired(r, "reservation_io_failed", err)
			}
			r.State = "unchanged"
			r.After = actual
			return r, nil
		}
	}
	if err != nil || !journalMatches(j, r, proposed) {
		return recoverRequired(r, "invalid_journal", err)
	}
	if !snapshotContextMatches(j.Before, p) {
		return recoverRequired(r, "parent_changed", nil)
	}
	if err := w.verifyConditionalParent(ctx, p); err != nil {
		return recoverRequired(r, "parent_changed", err)
	}
	actual, err := readConditionalAt(ctx, p.fd, p.base, p, ConditionalTextLimit)
	if err != nil {
		return recoverRequired(r, "file_changed", err)
	}
	r.After = actual
	var original unix.Stat_t
	originalErr := unix.Fstatat(fd, "original", &original, unix.AT_SYMLINK_NOFOLLOW)
	if originalErr != nil && !errors.Is(originalErr, unix.ENOENT) {
		return recoverRequired(r, "reservation_io_failed", originalErr)
	}
	if j.Stage == "restored" {
		if originalErr == nil {
			return recoverRequired(r, "invalid_journal", nil)
		}
		r.State = "restored"
		return r, nil
	}
	if sameSnapshot(actual, j.After) {
		if j.Before.Exists {
			old, err := readConditionalAt(ctx, fd, "original", p, ConditionalTextLimit)
			if err != nil || !sameSnapshot(old, j.Before) {
				return recoverRequired(r, "original_changed", err)
			}
		}
		j.Stage = "applied"
		if err := writeJournal(fd, j); err != nil {
			return recoverRequired(r, "reservation_io_failed", err)
		}
		r.State = "applied"
		return r, nil
	}
	if originalErr == nil && !actual.Exists {
		restored, err := w.restoreConditional(ctx, p, fd, j, r, "interrupted", nil)
		if restored.State == "restored" {
			return restored, nil
		}
		return restored, err
	}
	if originalErr != nil && sameSnapshot(actual, j.Before) {
		j.Stage = "restored"
		if err := writeJournal(fd, j); err != nil {
			return recoverRequired(r, "reservation_io_failed", err)
		}
		r.State = "unchanged"
		return r, nil
	}
	return recoverRequired(r, "recovery_required", nil)
}

func (w *Workspace) finalizeConditional(ctx context.Context, id, rel string) error {
	p, err := w.openConditionalParent(ctx, rel)
	if err != nil {
		return err
	}
	defer unix.Close(p.fd)
	fd, err := openReservation(p, id, false)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	j, err := readJournal(fd)
	if err != nil {
		return conditionalError(model.CodeConflict, "invalid_journal", err)
	}
	if j.OperationID != id || j.Path != rel || (j.Stage != "applied" && j.Stage != "restored") {
		return conditionalError(model.CodeConflict, "recovery_required", nil)
	}
	if !snapshotContextMatches(j.Before, p) {
		return conditionalError(model.CodeConflict, "parent_changed", nil)
	}
	if err := w.verifyConditionalParent(ctx, p); err != nil {
		return err
	}
	// 完成前先检查目录内容，防止删完主日志才发现遗留临时文件或陌生条目。
	copyFD, err := unix.Dup(fd)
	if err != nil {
		return conditionalError(model.CodeInternal, "cleanup_failed", err)
	}
	directory := os.NewFile(uintptr(copyFD), "reservation")
	names, readErr := directory.Readdirnames(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return conditionalError(model.CodeInternal, "cleanup_failed", readErr)
	}
	if closeErr != nil {
		return conditionalError(model.CodeInternal, "cleanup_failed", closeErr)
	}
	for _, name := range names {
		known := name == "original" || name == "proposed" || name == "journal.json" || (strings.HasPrefix(name, "journal-") && strings.HasSuffix(name, ".tmp"))
		var st unix.Stat_t
		if !known {
			return conditionalError(model.CodeConflict, "unsafe_reservation", nil)
		}
		if err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
			return conditionalError(model.CodeConflict, "unsafe_reservation", err)
		}
	}
	var original unix.Stat_t
	err = unix.Fstatat(fd, "original", &original, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		old, err := readConditionalAt(ctx, fd, "original", p, ConditionalTextLimit)
		if err != nil || !sameSnapshot(old, j.Before) {
			return conditionalError(model.CodeConflict, "original_changed", err)
		}
		if err := unix.Unlinkat(fd, "original", 0); err != nil {
			return conditionalError(model.CodeInternal, "cleanup_failed", err)
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return conditionalError(model.CodeInternal, "cleanup_failed", err)
	}
	if err := unix.Unlinkat(fd, "proposed", 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return conditionalError(model.CodeInternal, "cleanup_failed", err)
	}
	for _, name := range names {
		if strings.HasPrefix(name, "journal-") && strings.HasSuffix(name, ".tmp") {
			if err := unix.Unlinkat(fd, name, 0); err != nil {
				return conditionalError(model.CodeInternal, "cleanup_failed", err)
			}
		}
	}
	if err := unix.Unlinkat(fd, "journal.json", 0); err != nil {
		return conditionalError(model.CodeInternal, "cleanup_failed", err)
	}
	if err := unix.Unlinkat(p.fd, reservationName(id), unix.AT_REMOVEDIR); err != nil {
		return conditionalError(model.CodeInternal, "cleanup_failed", err)
	}
	return unix.Fsync(p.fd)
}
