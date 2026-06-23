package files

import (
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"github.com/profullstack/agentbbs/internal/store"
)

// area names exposed at the virtual root.
const (
	areaMe     = "me"
	areaPublic = "public"
)

// errEscape is returned when a resolved path would leave its area root. It maps
// to an SFTP permission-denied; it must never reach the client as a real path.
var errEscape = errors.New("files: path escapes its area")

// session is one member's view of the filesystem for the life of an SFTP
// connection. It implements the pkg/sftp request handlers and enforces area
// confinement, the public-area ACL, and the per-user quota.
type session struct {
	svc      *Service
	user     store.User
	pubWrite bool
	quota    int64
	used     atomic.Int64 // live private-workspace usage, for quota checks
}

func (s *Service) newSession(u store.User) (*session, error) {
	if err := s.ensureWorkspace(u.Name); err != nil {
		return nil, err
	}
	used, err := dirSize(s.privRoot(u.Name))
	if err != nil {
		return nil, err
	}
	sess := &session{svc: s, user: u, pubWrite: s.publicWritable(), quota: s.quotaFor(u.ID)}
	sess.used.Store(used)
	return sess, nil
}

// resolved is the outcome of mapping a client (virtual) path to disk.
type resolved struct {
	real     string // absolute on-disk path ("" for the synthetic root)
	area     string // "", areaMe, or areaPublic
	root     bool   // the synthetic "/" listing me + public
	writable bool   // whether this area accepts writes for this member
}

// resolve maps a client path to disk, confined to its area root. It rejects any
// path that escapes (lexically or via an existing symlink). This is the single
// security chokepoint — every handler goes through it.
func (s *session) resolve(p string) (resolved, error) {
	clean := path.Clean("/" + strings.TrimSpace(p))
	if clean == "/" || clean == "." {
		return resolved{root: true}, nil
	}
	seg := strings.SplitN(strings.TrimPrefix(clean, "/"), "/", 2)
	rest := ""
	if len(seg) == 2 {
		rest = seg[1]
	}
	var areaRoot, area string
	writable := false
	switch seg[0] {
	case areaMe:
		areaRoot, area, writable = s.svc.privRoot(s.user.Name), areaMe, true
	case areaPublic:
		areaRoot, area, writable = s.svc.pubRoot(), areaPublic, s.pubWrite
	default:
		return resolved{}, os.ErrNotExist
	}
	real, err := safeJoin(areaRoot, rest)
	if err != nil {
		return resolved{}, err
	}
	return resolved{real: real, area: area, writable: writable}, nil
}

// safeJoin joins rel onto root and verifies the result stays within root, both
// lexically and after resolving any symlinks that already exist on the path.
func safeJoin(root, rel string) (string, error) {
	full := filepath.Join(root, filepath.FromSlash(rel))
	full = filepath.Clean(full)
	if !within(root, full) {
		return "", errEscape
	}
	// Symlink guard: resolve the longest existing prefix and re-check. This
	// catches a symlink (created out-of-band) that points outside the area.
	probe := full
	for {
		if resolvedPath, err := filepath.EvalSymlinks(probe); err == nil {
			if !within(root, resolvedPath) {
				return "", errEscape
			}
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	return full, nil
}

// within reports whether p is root itself or lives under it.
func within(root, p string) bool {
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(os.PathSeparator))
}

// OpenFor builds a member session by account name, for the in-BBS browser. It
// returns the resolved store user too.
func (s *Service) OpenFor(name string) (*session, store.User, error) {
	u, ok, err := s.st.UserByName(name)
	if err != nil {
		return nil, store.User{}, err
	}
	if !ok {
		return nil, store.User{}, os.ErrNotExist
	}
	sess, err := s.newSession(u)
	return sess, u, err
}

// Entry is a directory entry for the in-BBS browser.
type Entry struct {
	Name    string
	IsDir   bool
	Size    int64
	ModTime time.Time
}

// entries lists a virtual directory for the browser, with the synthetic root
// showing the two areas. Directories sort before files, then by name.
func (s *session) entries(vpath string) ([]Entry, error) {
	res, err := s.resolve(vpath)
	if err != nil {
		return nil, err
	}
	if res.root {
		return []Entry{{Name: areaMe, IsDir: true}, {Name: areaPublic, IsDir: true}}, nil
	}
	des, err := os.ReadDir(res.real)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(des))
	for _, de := range des {
		fi, err := de.Info()
		if err != nil {
			continue
		}
		out = append(out, Entry{Name: de.Name(), IsDir: de.IsDir(), Size: fi.Size(), ModTime: fi.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// canWrite reports whether the area containing vpath accepts writes.
func (s *session) canWrite(vpath string) bool {
	res, err := s.resolve(vpath)
	return err == nil && !res.root && res.writable
}

func (s *session) mkdir(vpath string) error {
	res, err := s.resolve(vpath)
	if err != nil {
		return err
	}
	if res.root || !res.writable {
		return os.ErrPermission
	}
	return os.Mkdir(res.real, 0o755)
}

// remove deletes a file or directory (recursively) within a writable area.
func (s *session) remove(vpath string) error {
	res, err := s.resolve(vpath)
	if err != nil {
		return err
	}
	if res.root || !res.writable {
		return os.ErrPermission
	}
	return os.RemoveAll(res.real)
}

// rename moves within a single writable area (no cross-area moves).
func (s *session) rename(oldv, newv string) error {
	src, err := s.resolve(oldv)
	if err != nil {
		return err
	}
	dst, err := s.resolve(newv)
	if err != nil {
		return err
	}
	if src.root || dst.root || !src.writable || !dst.writable || src.area != dst.area {
		return os.ErrPermission
	}
	return os.Rename(src.real, dst.real)
}

// readFile returns up to max bytes of a file for in-TUI viewing; truncated is
// true if the file was longer.
func (s *session) readFile(vpath string, max int64) (data []byte, truncated bool, err error) {
	res, err := s.resolve(vpath)
	if err != nil {
		return nil, false, err
	}
	if res.root {
		return nil, false, os.ErrInvalid
	}
	f, err := os.Open(res.real)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	buf := make([]byte, max+1)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, false, err
	}
	if int64(n) > max {
		return buf[:max], true, nil
	}
	return buf[:n], false, nil
}

// errQuota means a write would push the member's private workspace over quota.
var errQuota = errors.New("files: quota exceeded")

// webOpen opens a file for HTTP download. It rejects the synthetic root and
// directories; the caller closes the returned file.
func (s *session) webOpen(vpath string) (*os.File, os.FileInfo, error) {
	res, err := s.resolve(vpath)
	if err != nil {
		return nil, nil, err
	}
	if res.root {
		return nil, nil, os.ErrInvalid
	}
	f, err := os.Open(res.real)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if fi.IsDir() {
		_ = f.Close()
		return nil, nil, os.ErrInvalid
	}
	return f, fi, nil
}

// webSave stores an uploaded file at the destination vpath, enforcing the
// private-area quota. It returns errQuota when the upload would exceed it.
func (s *session) webSave(vpath string, r io.Reader) (int64, error) {
	res, err := s.resolve(vpath)
	if err != nil {
		return 0, err
	}
	if res.root || !res.writable {
		return 0, os.ErrPermission
	}
	var existing int64
	if fi, e := os.Stat(res.real); e == nil {
		if fi.IsDir() {
			return 0, os.ErrPermission
		}
		existing = fi.Size()
	}
	f, err := os.OpenFile(res.real, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	limit := int64(-1) // public area is operator-managed, unmetered
	if res.area == areaMe {
		if limit = s.quota - (s.used.Load() - existing); limit < 0 {
			limit = 0
		}
	}
	n, werr := copyLimited(f, r, limit)
	cerr := f.Close()
	if werr != nil {
		_ = os.Remove(res.real)
		return 0, werr
	}
	if cerr != nil {
		return 0, cerr
	}
	if res.area == areaMe {
		s.used.Add(n - existing)
	}
	return n, nil
}

// webMkdir and webRemove reuse the SFTP-side guards (root/writable checks).
func (s *session) webMkdir(vpath string) error  { return s.mkdir(vpath) }
func (s *session) webRemove(vpath string) error { return s.remove(vpath) }

// copyLimited copies r into w. When limit >= 0 it returns errQuota if the source
// has more than limit bytes (after writing exactly limit). limit < 0 is unbounded.
func copyLimited(w io.Writer, r io.Reader, limit int64) (int64, error) {
	if limit < 0 {
		return io.Copy(w, r)
	}
	n, err := io.Copy(w, io.LimitReader(r, limit))
	if err != nil {
		return n, err
	}
	var probe [1]byte
	if m, _ := r.Read(probe[:]); m > 0 {
		return n, errQuota
	}
	return n, nil
}

// --- pkg/sftp request handlers ----------------------------------------------

// Fileread serves downloads.
func (s *session) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	res, err := s.resolve(r.Filepath)
	if err != nil {
		return nil, sftpErr(err)
	}
	if res.root {
		return nil, sftp.ErrSSHFxOpUnsupported
	}
	f, err := os.Open(res.real)
	if err != nil {
		return nil, sftpErr(err)
	}
	return f, nil
}

// Filewrite serves uploads, enforcing the per-user quota in the private area.
func (s *session) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	res, err := s.resolve(r.Filepath)
	if err != nil {
		return nil, sftpErr(err)
	}
	if res.root || !res.writable {
		return nil, sftp.ErrSSHFxPermissionDenied
	}
	var startSize int64
	if fi, err := os.Stat(res.real); err == nil {
		startSize = fi.Size()
	}
	f, err := os.OpenFile(res.real, fileFlags(r.Pflags()), 0o644)
	if err != nil {
		return nil, sftpErr(err)
	}
	// The public area is operator-managed (no per-user quota); the private
	// workspace is metered.
	if res.area != areaMe {
		return f, nil
	}
	return &quotaWriter{f: f, sess: s, tracked: startSize}, nil
}

// Filecmd handles mutating operations other than read/write.
func (s *session) Filecmd(r *sftp.Request) error {
	res, err := s.resolve(r.Filepath)
	if err != nil {
		return sftpErr(err)
	}
	if res.root || !res.writable {
		return sftp.ErrSSHFxPermissionDenied
	}
	switch r.Method {
	case "Mkdir":
		return sftpErr(os.Mkdir(res.real, 0o755))
	case "Rmdir":
		return sftpErr(os.Remove(res.real))
	case "Remove":
		return sftpErr(os.Remove(res.real))
	case "Setstat":
		return s.setstat(res.real, r)
	case "Rename":
		dst, err := s.resolve(r.Target)
		if err != nil {
			return sftpErr(err)
		}
		if dst.root || !dst.writable || dst.area != res.area {
			// Renames stay within one writable area — no cross-area (and so no
			// workspace-to-workspace) moves.
			return sftp.ErrSSHFxPermissionDenied
		}
		return sftpErr(os.Rename(res.real, dst.real))
	case "Symlink", "Link":
		// No symlinks/hardlinks: they are an escape vector and have no place in
		// a metered virtual workspace.
		return sftp.ErrSSHFxOpUnsupported
	default:
		return sftp.ErrSSHFxOpUnsupported
	}
}

func (s *session) setstat(real string, r *sftp.Request) error {
	a := r.Attributes()
	if a.Size != 0 || r.AttrFlags().Size {
		if err := os.Truncate(real, int64(a.Size)); err != nil {
			return sftpErr(err)
		}
	}
	if r.AttrFlags().Permissions {
		if err := os.Chmod(real, a.FileMode().Perm()); err != nil {
			return sftpErr(err)
		}
	}
	return nil
}

// Filelist handles List (readdir), Stat, and Readlink.
func (s *session) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	res, err := s.resolve(r.Filepath)
	if err != nil {
		return nil, sftpErr(err)
	}
	switch r.Method {
	case "List":
		if res.root {
			return listerAt{dirInfo(areaMe), dirInfo(areaPublic)}, nil
		}
		entries, err := os.ReadDir(res.real)
		if err != nil {
			return nil, sftpErr(err)
		}
		infos := make([]os.FileInfo, 0, len(entries))
		for _, e := range entries {
			fi, err := e.Info()
			if err != nil {
				continue
			}
			infos = append(infos, fi)
		}
		return listerAt(infos), nil
	case "Stat":
		if res.root {
			return listerAt{dirInfo("/")}, nil
		}
		fi, err := os.Stat(res.real)
		if err != nil {
			return nil, sftpErr(err)
		}
		return listerAt{fi}, nil
	case "Readlink":
		return nil, sftp.ErrSSHFxOpUnsupported
	default:
		return nil, sftp.ErrSSHFxOpUnsupported
	}
}

// --- helpers ----------------------------------------------------------------

// quotaWriter wraps an *os.File and fails writes that would push the member's
// private workspace over quota. It is conservative: it counts the high-water
// mark of each file and never decrements live usage (recomputed next session).
type quotaWriter struct {
	f       *os.File
	sess    *session
	tracked int64 // current accounted size of this file
}

func (w *quotaWriter) WriteAt(p []byte, off int64) (int, error) {
	end := off + int64(len(p))
	if end > w.tracked {
		delta := end - w.tracked
		if w.sess.used.Add(delta) > w.sess.quota {
			w.sess.used.Add(-delta)
			return 0, sftp.ErrSSHFxFailure // "quota exceeded"
		}
		w.tracked = end
	}
	return w.f.WriteAt(p, off)
}

func (w *quotaWriter) Close() error { return w.f.Close() }

// fileFlags maps SFTP open flags to os.OpenFile flags for writes.
func fileFlags(f sftp.FileOpenFlags) int {
	flags := os.O_WRONLY
	if f.Creat {
		flags |= os.O_CREATE
	}
	if f.Trunc {
		flags |= os.O_TRUNC
	}
	if f.Append {
		flags |= os.O_APPEND
	}
	if f.Excl {
		flags |= os.O_EXCL
	}
	return flags
}

// sftpErr translates an OS/internal error into the SFTP status a client should
// see, without leaking real paths. nil passes through.
func sftpErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errEscape):
		return sftp.ErrSSHFxPermissionDenied
	case errors.Is(err, os.ErrNotExist):
		return sftp.ErrSSHFxNoSuchFile
	case errors.Is(err, os.ErrPermission):
		return sftp.ErrSSHFxPermissionDenied
	case errors.Is(err, os.ErrExist):
		return sftp.ErrSSHFxFailure
	default:
		return sftp.ErrSSHFxFailure
	}
}

// listerAt adapts a slice of FileInfo to sftp.ListerAt.
type listerAt []os.FileInfo

func (l listerAt) ListAt(dst []os.FileInfo, off int64) (int, error) {
	if off >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(dst, l[off:])
	if int(off)+n >= len(l) {
		return n, io.EOF
	}
	return n, nil
}

// dirInfo is a synthetic directory FileInfo for the virtual-root entries.
type dirInfo string

func (d dirInfo) Name() string     { return string(d) }
func (dirInfo) Size() int64        { return 0 }
func (dirInfo) Mode() os.FileMode  { return os.ModeDir | 0o555 }
func (dirInfo) ModTime() time.Time { return time.Time{} }
func (dirInfo) IsDir() bool        { return true }
func (dirInfo) Sys() any           { return nil }
