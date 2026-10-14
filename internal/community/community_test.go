package community

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func digest(c string) string { return "sha256:" + strings.Repeat(c, 64) }

func entry(id, d string) map[string]any {
	return map[string]any{"id": id, "name": map[string]string{"en": "Hello", "ru": "Привет"}, "description": map[string]string{"en": "says hello"},
		"version": "1.0.0", "protocol": 2, "kind": "generic", "mode": "client", "permissions": []string{"kv"},
		"image": "ghcr.io/author/hello", "digest": digest(d), "homepage": "https://example.com"}
}

func newKey(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv, base64.StdEncoding.EncodeToString(pub)
}

func signed(t *testing.T, doc any, priv ed25519.PrivateKey) ([]byte, []byte) {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return data, []byte(Sign(data, priv) + "\n")
}

func TestParseDocUnsignedEntry(t *testing.T) {
	data, _ := json.Marshal(entry("hello", "a"))
	doc, err := ParseDoc(data, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Key != "" || len(doc.Entries) != 1 || doc.Entries[0].Mode != "client" || doc.Entries[0].Name["ru"] != "Привет" {
		t.Fatalf("doc %+v", doc)
	}
}

func TestParseDocSignatures(t *testing.T) {
	priv, pub := newKey(t)
	other, otherPub := newKey(t)
	file := map[string]any{"title": "Author", "key": pub, "adapters": []any{entry("hello", "a")}}
	data, sig := signed(t, file, priv)
	doc, err := ParseDoc(data, sig, "")
	if err != nil || doc.Key != pub {
		t.Fatalf("signed by the key it names: %v %q", err, doc.Key)
	}
	// The registry holds another key: the file names its own, which verifies, and comes out
	// as it is for the sync to call it a change.
	if doc, err := ParseDoc(data, sig, otherPub); err != nil || doc.Key != pub {
		t.Fatalf("pinned another key: %v %q", err, doc.Key)
	}
	bad, _ := signed(t, file, other)
	_, badSig := signed(t, map[string]any{"x": 1}, other)
	if _, err := ParseDoc(bad, badSig, ""); !errors.Is(err, ErrSignature) {
		t.Fatalf("a signature over other bytes: %v", err)
	}
	noKey := map[string]any{"adapters": []any{entry("hello", "a")}}
	data, sig = signed(t, noKey, priv)
	if _, err := ParseDoc(data, sig, ""); !errors.Is(err, ErrKeyNeeded) {
		t.Fatalf("signed with no key named: %v", err)
	}
	if doc, err := ParseDoc(data, sig, pub); err != nil || doc.Key != pub {
		t.Fatalf("the pinned key verifies a file that names none: %v", err)
	}
	if _, err := ParseDoc(data, []byte("not base64!"), pub); !errors.Is(err, ErrBadSigFile) {
		t.Fatalf("garbage signature: %v", err)
	}
}

func TestParseDocLeavesOut(t *testing.T) {
	pay := entry("pay", "b")
	pay["protocol"] = 1
	local := entry("local", "c")
	local["image"] = "127.0.0.1:5000/x"
	lan := entry("lan", "c")
	lan["image"] = "10.0.0.1/x"
	tag := entry("tag", "d")
	tag["digest"] = "latest"
	perm := entry("perm", "e")
	perm["permissions"] = []string{"root"}
	tg := entry("telegram", "f")
	file := map[string]any{"adapters": []any{entry("hello", "a"), entry("hello", "b"), pay, local, lan, tag, perm, tg}}
	data, _ := json.Marshal(file)
	doc, err := ParseDoc(data, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Entries) != 1 || doc.Entries[0].Digest != digest("a") {
		t.Fatalf("entries %+v", doc.Entries)
	}
	want := map[string]string{"hello": "duplicate", "pay": "payment", "local": "image_invalid", "lan": "image_invalid", "tag": "digest_invalid", "perm": "invalid", "telegram": "id_taken"}
	for _, s := range doc.Skipped {
		if want[s.ID] != s.Reason {
			t.Errorf("%s: %s, want %s", s.ID, s.Reason, want[s.ID])
		}
		delete(want, s.ID)
	}
	if len(want) != 0 {
		t.Errorf("not left out: %v", want)
	}
	only, _ := json.Marshal(map[string]any{"adapters": []any{pay}})
	if _, err := ParseDoc(only, nil, ""); !errors.Is(err, ErrNoEntries) {
		t.Fatalf("nothing installable: %v", err)
	}
	if _, err := ParseDoc([]byte("<html>"), nil, ""); !errors.Is(err, ErrBadFile) {
		t.Fatalf("a page: %v", err)
	}
}

func addon(id, repo string, repoID int64, d string) Addon {
	data, _ := json.Marshal(entry(id, d))
	doc, err := ParseDoc(data, nil, "")
	if err != nil {
		panic(err)
	}
	return Addon{Entry: doc.Entries[0], Repo: repo, RepoID: repoID, Stars: 1, AddedAt: 100, UpdatedAt: 100}
}

func TestBuildSignVerify(t *testing.T) {
	priv, _ := newKey(t)
	a := addon("hello", "github.com/Author/hello", 7, "a")
	b := addon("bye", "github.com/Other/bye", 8, "b")
	idx, err := Build([]Addon{a, b}, []Revoked{{Repo: "github.com/other/bye", Reason: "stolen code", At: "2026-10-14"}}, []string{"yookassa"}, time.Unix(1000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Addons) != 1 || idx.Addons[0].ID != "hello" || len(idx.Revoked) != 1 {
		t.Fatalf("a revoked addon stays out: %+v", idx)
	}
	data, _ := json.Marshal(idx)
	got, err := Verify(data, Sign(data, priv), priv.Public().(ed25519.PublicKey), nil)
	if err != nil || len(got.Addons) != 1 {
		t.Fatalf("verify: %v", err)
	}
	other, _ := newKey(t)
	if _, err := Verify(data, Sign(data, other), priv.Public().(ed25519.PublicKey), nil); !errors.Is(err, ErrIndexSignature) {
		t.Fatalf("another key: %v", err)
	}
	if _, err := Build([]Addon{addon("yookassa", "github.com/A/y", 9, "c")}, nil, []string{"yookassa"}, time.Now()); err == nil {
		t.Fatal("an id of the catalog is taken")
	}
	if _, err := Build([]Addon{a, addon("hello", "github.com/B/h", 10, "d")}, nil, nil, time.Now()); err == nil {
		t.Fatal("an id twice")
	}
	if _, err := Build(nil, []Revoked{{ID: "x", Reason: "", At: "2026-10-14"}}, nil, time.Now()); err == nil {
		t.Fatal("a revocation needs a reason")
	}
	later, _ := Build([]Addon{a, b}, idx.Revoked, nil, time.Unix(5000, 0))
	if !SameContent(idx, later) {
		t.Fatal("only the time differs")
	}
}

func TestLoad(t *testing.T) {
	root := t.TempDir()
	a := addon("hello", "github.com/Author/hello", 7, "a")
	write := func(p string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got, rev, err := Load(root); err != nil || len(got) != 0 || len(rev) != 0 {
		t.Fatalf("an empty registry: %v", err)
	}
	write("addons/author/hello.json", Encode(a))
	write("revoked.json", []byte(`[{"id":"bad","reason":"malware","at":"2026-10-14"}]`))
	got, rev, err := Load(root)
	if err != nil || len(got) != 1 || got[0].ID != "hello" || len(rev) != 1 {
		t.Fatalf("load: %v %+v", err, got)
	}
	write("addons/someone/hello.json", Encode(a))
	if _, _, err := Load(root); !errors.Is(err, ErrLayout) {
		t.Fatalf("out of place: %v", err)
	}
}
