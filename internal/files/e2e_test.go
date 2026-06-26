package files

import (
	"io"
	"net"
	"sort"
	"testing"

	"github.com/pkg/sftp"
)

// startE2E wires a real sftp client to the request server for one member, over
// an in-memory pipe (no SSH transport).
func startE2E(t *testing.T) *sftp.Client {
	t.Helper()
	svc, _, u := newTestService(t)
	sess, err := svc.newSession(u)
	if err != nil {
		t.Fatal(err)
	}
	srvConn, cliConn := net.Pipe()
	handlers := sftp.Handlers{FileGet: sess, FilePut: sess, FileCmd: sess, FileList: sess}
	server := sftp.NewRequestServer(srvConn, handlers)
	go func() { _ = server.Serve() }()
	client, err := sftp.NewClientPipe(cliConn, cliConn)
	if err != nil {
		t.Fatalf("NewClientPipe: %v", err)
	}
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	return client
}

func TestE2E_RootListing(t *testing.T) {
	client := startE2E(t)
	infos, err := client.ReadDir("/")
	if err != nil {
		t.Fatalf("ReadDir /: %v", err)
	}
	var names []string
	for _, fi := range infos {
		names = append(names, fi.Name())
	}
	sort.Strings(names)
	if len(names) != 3 || names[0] != "me" || names[1] != "public" || names[2] != "site" {
		t.Errorf("root listing = %v, want [me public site]", names)
	}
}

func TestE2E_UploadDownloadPrivate(t *testing.T) {
	client := startE2E(t)
	f, err := client.Create("/me/hello.txt")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.Write([]byte("hello bbs")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rf, err := client.Open("/me/hello.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rf.Close()
	got, err := io.ReadAll(rf)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "hello bbs" {
		t.Errorf("read = %q, want %q", got, "hello bbs")
	}
}

func TestE2E_TraversalDenied(t *testing.T) {
	client := startE2E(t)
	// A normalizing client may collapse "/me/../.." to "/" before sending, so
	// the guarantee we assert is the negative one: no escape ever yields a
	// handle onto a system path.
	if _, err := client.Open("/me/../../../../etc/passwd"); err == nil {
		t.Error("opening an escaping path should fail")
	}
	if _, err := client.Open("/etc/passwd"); err == nil {
		t.Error("opening an unknown area should fail")
	}
}
