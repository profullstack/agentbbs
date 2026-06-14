package store

import (
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestNewsGroupsAndArticles(t *testing.T) {
	st := openTestStore(t)

	// EnsureNewsGroup is idempotent; the description sticks from first creation.
	if err := st.EnsureNewsGroup("pfs.general", "General discussion"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := st.EnsureNewsGroup("pfs.general", "ignored on re-create"); err != nil {
		t.Fatalf("ensure 2: %v", err)
	}
	if err := st.EnsureNewsGroup("pfs.agents", "Agents"); err != nil {
		t.Fatalf("ensure agents: %v", err)
	}

	gs, err := st.NewsGroups()
	if err != nil {
		t.Fatalf("groups: %v", err)
	}
	if len(gs) != 2 || gs[0].Name != "pfs.agents" || gs[1].Name != "pfs.general" {
		t.Fatalf("groups sorted/listed wrong: %+v", gs)
	}
	// An empty group reports Low=1, High=0, Count=0 (RFC 3977 convention).
	if gs[1].Description != "General discussion" || gs[1].Count != 0 || gs[1].Low != 1 || gs[1].High != 0 {
		t.Fatalf("empty group bounds wrong: %+v", gs[1])
	}

	// Inserting assigns sequential per-group numbers starting at 1.
	a1, err := st.InsertNewsArticle(NewsArticle{Group: "pfs.general", MsgID: "<1@h>", Subject: "Hello", From: "alice <alice@h>", Body: "hi\n", Lines: 1, Bytes: 3})
	if err != nil {
		t.Fatalf("insert 1: %v", err)
	}
	a2, err := st.InsertNewsArticle(NewsArticle{Group: "pfs.general", MsgID: "<2@h>", Subject: "Re: Hello", From: "bob <bob@h>", Refs: "<1@h>", Body: "yo\n", Lines: 1, Bytes: 3})
	if err != nil {
		t.Fatalf("insert 2: %v", err)
	}
	if a1.Num != 1 || a2.Num != 2 {
		t.Fatalf("numbering wrong: a1=%d a2=%d", a1.Num, a2.Num)
	}

	// Group counts now reflect the two articles.
	g, ok, err := st.NewsGroup("pfs.general")
	if err != nil || !ok {
		t.Fatalf("group: ok=%v err=%v", ok, err)
	}
	if g.Count != 2 || g.Low != 1 || g.High != 2 {
		t.Fatalf("counts wrong: %+v", g)
	}

	// Fetch by number and by message-id.
	got, ok, err := st.NewsArticleByNum("pfs.general", 2)
	if err != nil || !ok || got.Subject != "Re: Hello" || got.Refs != "<1@h>" {
		t.Fatalf("by num: ok=%v err=%v got=%+v", ok, err, got)
	}
	got, ok, err = st.NewsArticleByMsgID("<1@h>")
	if err != nil || !ok || got.Num != 1 {
		t.Fatalf("by msgid: ok=%v err=%v got=%+v", ok, err, got)
	}

	// Range query for OVER/XOVER.
	rng, err := st.NewsArticlesRange("pfs.general", 1, 100)
	if err != nil || len(rng) != 2 || rng[0].Num != 1 || rng[1].Num != 2 {
		t.Fatalf("range: err=%v got=%+v", err, rng)
	}

	// Numbering is independent per group.
	b1, err := st.InsertNewsArticle(NewsArticle{Group: "pfs.agents", MsgID: "<3@h>", Subject: "bot", From: "bot <bot@h>", Body: "beep\n"})
	if err != nil || b1.Num != 1 {
		t.Fatalf("agents numbering: num=%d err=%v", b1.Num, err)
	}

	// Misses are clean.
	if _, ok, _ := st.NewsArticleByNum("pfs.general", 99); ok {
		t.Fatal("missing num should not be found")
	}
	if _, ok, _ := st.NewsGroup("nope"); ok {
		t.Fatal("missing group should not be found")
	}
}
