package trust

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/lang"
)

func agentsFrom(t *testing.T, source string) []*lang.AgentDef {
	t.Helper()
	agents, err := lang.ParseFile("a.ag", "", source)
	if err != nil {
		t.Fatalf("the source does not parse: %v", err)
	}
	return agents
}

func problemText(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected a problem")
	}
	d, ok := diag.From(err)
	if !ok {
		t.Fatalf("the error is not a diagnostic: %v", err)
	}
	return d.Render()
}

func newRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	root := t.TempDir()
	return NewRegistry(filepath.Join(t.TempDir(), "config")), root
}

// req: T1
func TestNeedsListTheProgramsAndAddressesOnceInAStableOrder(t *testing.T) {
	agents := agentsFrom(t, `agent A
  goal "a"
  tool weather from mcp "npx -y weather-mcp@1.0.0" env "PROXY" "ALSO"
  tool fetch from mcp "https://tools.example/mcp"
  tool file
  remote Bob at "https://bob.example"
agent B
  goal "b"
  tool weather from mcp "npx -y weather-mcp@1.0.0" env "ALSO" "PROXY"
  remote Bob at "https://bob.example"
`)
	needs := NeedsOf(agents)
	var got []string
	for _, item := range needs {
		got = append(got, item.Describe())
	}
	// Programs come first, then addresses, each group in alphabetical order.
	want := []string{
		"starts the program: npx -y weather-mcp@1.0.0 (it receives the variables ALSO, PROXY)",
		"connects to: https://bob.example",
		"connects to: https://tools.example/mcp",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("needs:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestTheVariablesPassedOnArePartOfWhatIsApproved(t *testing.T) {
	plain := CommandItem("server --stdio", nil)
	withVariable := CommandItem("server --stdio", []string{"TOKEN"})
	if plain.Key() == withVariable.Key() {
		t.Error("the same program with one more variable must need its own approval")
	}
	if CommandItem("server", []string{"B", "A", "B"}).Key() != CommandItem("server", []string{"A", "B"}).Key() {
		t.Error("the order and repeats of the variables must not matter")
	}
	if CommandItem("  server  ", nil).Key() != CommandItem("server", nil).Key() {
		t.Error("spaces around the command must not matter")
	}
}

func TestTheHashOfASetDoesNotDependOnItsOrder(t *testing.T) {
	a, b := CommandItem("one", nil), RemoteItem("https://two.example")
	if SetHash([]Item{a, b}) != SetHash([]Item{b, a}) {
		t.Error("the hash changed with the order")
	}
	if SetHash([]Item{a}) == SetHash([]Item{a, b}) {
		t.Error("different sets have the same hash")
	}
	if len(SetHash(nil)) != 64 {
		t.Errorf("a SHA-256 in hex has 64 characters, got %q", SetHash(nil))
	}
}

// req: T1
func TestOnlyWhatIsNewNeedsApproval(t *testing.T) {
	registry, root := newRegistry(t)
	a, b := CommandItem("one", nil), CommandItem("two", nil)

	missing, err := registry.Missing(root, []Item{a, b})
	if err != nil || len(missing) != 2 {
		t.Fatalf("nothing is approved yet: missing %v, error %v", missing, err)
	}
	if err := registry.Approve(root, []Item{a}); err != nil {
		t.Fatal(err)
	}
	missing, _ = registry.Missing(root, []Item{a, b})
	if len(missing) != 1 || missing[0].Key() != b.Key() {
		t.Errorf("only `two` should be missing, got %v", missing)
	}
	// Needing less than what was approved is fine.
	if missing, _ := registry.Missing(root, []Item{a}); len(missing) != 0 {
		t.Errorf("a subset of the approved items needs nothing, got %v", missing)
	}
	// Approving adds to what was there.
	if err := registry.Approve(root, []Item{b}); err != nil {
		t.Fatal(err)
	}
	if missing, _ := registry.Missing(root, []Item{a, b}); len(missing) != 0 {
		t.Errorf("both should be approved now, got %v", missing)
	}
}

func TestApprovalsBelongToTheProjectTheyWereGivenFor(t *testing.T) {
	registry, root := newRegistry(t)
	other := t.TempDir()
	item := CommandItem("one", nil)
	if err := registry.Approve(root, []Item{item}); err != nil {
		t.Fatal(err)
	}
	if ok, err := registry.Allows(root, item); err != nil || !ok {
		t.Errorf("approved for its own project: %v %v", ok, err)
	}
	if ok, _ := registry.Allows(other, item); ok {
		t.Error("an approval leaked to another project")
	}
}

func TestAnApprovalOfNothingToDoDoesNotTouchTheDisk(t *testing.T) {
	registry, root := newRegistry(t)
	if missing, err := registry.Missing(root, nil); err != nil || missing != nil {
		t.Errorf("got %v, %v", missing, err)
	}
	if _, err := os.Stat(registry.Path()); err == nil {
		t.Error("the file of approvals was created for nothing")
	}
}

// req: T2
func TestTheFileOfApprovalsIsPrivateAndLeavesNoTemporaryFile(t *testing.T) {
	registry, root := newRegistry(t)
	for _, name := range []string{"one", "two"} {
		if err := registry.Approve(root, []Item{CommandItem(name, nil)}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(registry.Path()))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "trust.json" {
		t.Errorf("the folder should hold only trust.json, it holds %v", entries)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if info, _ := os.Stat(registry.Path()); info.Mode().Perm() != 0o600 {
		t.Errorf("the file has permissions %v, want 0600", info.Mode().Perm())
	}
	if info, _ := os.Stat(filepath.Dir(registry.Path())); info.Mode().Perm() != 0o700 {
		t.Errorf("the folder has permissions %v, want 0700", info.Mode().Perm())
	}
}

// req: T2
func TestApprovalsAreNeverKeptInsideTheProject(t *testing.T) {
	root := t.TempDir()
	registry := NewRegistry(filepath.Join(root, ".config", "metagente"))
	err := registry.Approve(root, []Item{CommandItem("one", nil)})
	if text := problemText(t, err); !strings.Contains(text, "inside the project") {
		t.Errorf("unexpected message:\n%s", text)
	}
	if _, statErr := os.Stat(registry.Path()); statErr == nil {
		t.Error("the file was written inside the project")
	}
	if _, err := registry.Missing(root, []Item{CommandItem("one", nil)}); err == nil {
		t.Error("reading approvals from inside the project must be refused too")
	}
}

func TestWithoutAFolderOfOneSOwnNothingCanBeApproved(t *testing.T) {
	err := NewRegistry("").Approve(t.TempDir(), []Item{CommandItem("one", nil)})
	problemText(t, err)
}

// req: T1
func TestApprovalsChangedByHandAreNotTrusted(t *testing.T) {
	registry, root := newRegistry(t)
	if err := registry.Approve(root, []Item{CommandItem("one", nil)}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(registry.Path())
	if err != nil {
		t.Fatal(err)
	}
	// Add an approval without updating the hash that ties the set together.
	edited := strings.Replace(string(raw), `"items": [`, `"items": [
        {"kind": "command", "target": "evil --steal"},`, 1)
	if edited == string(raw) {
		t.Fatalf("the test could not edit the file:\n%s", raw)
	}
	if err := os.WriteFile(registry.Path(), []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = registry.Missing(root, []Item{CommandItem("evil --steal", nil)})
	if text := problemText(t, err); !strings.Contains(text, "changed by hand") {
		t.Errorf("unexpected message:\n%s", text)
	}
}

func TestAFileOthersCanWriteIsNotTrusted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits mean something else on Windows")
	}
	registry, root := newRegistry(t)
	if err := registry.Approve(root, []Item{CommandItem("one", nil)}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(registry.Path(), 0o666); err != nil {
		t.Fatal(err)
	}
	_, err := registry.Missing(root, []Item{CommandItem("one", nil)})
	if text := problemText(t, err); !strings.Contains(text, "chmod 600") {
		t.Errorf("unexpected message:\n%s", text)
	}
}

func TestADamagedFileIsReportedNotIgnored(t *testing.T) {
	registry, root := newRegistry(t)
	if err := os.MkdirAll(filepath.Dir(registry.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := registry.Missing(root, []Item{CommandItem("one", nil)})
	if text := problemText(t, err); !strings.Contains(text, "damaged") {
		t.Errorf("unexpected message:\n%s", text)
	}
}

func TestRevokingRemovesEveryApprovalOfTheProject(t *testing.T) {
	registry, root := newRegistry(t)
	other := t.TempDir()
	items := []Item{CommandItem("one", nil), RemoteItem("https://two.example")}
	if err := registry.Approve(root, items); err != nil {
		t.Fatal(err)
	}
	if err := registry.Approve(other, items[:1]); err != nil {
		t.Fatal(err)
	}
	removed, err := registry.Revoke(root)
	if err != nil || removed != 2 {
		t.Fatalf("removed %d, error %v", removed, err)
	}
	if missing, _ := registry.Missing(root, items); len(missing) != 2 {
		t.Errorf("the approvals are still there: missing %v", missing)
	}
	if ok, _ := registry.Allows(other, items[0]); !ok {
		t.Error("revoking one project removed the approvals of another")
	}
	if removed, err := registry.Revoke(root); err != nil || removed != 0 {
		t.Errorf("revoking twice: removed %d, error %v", removed, err)
	}
}

func TestTheListShowsEveryProjectWithItsHash(t *testing.T) {
	registry, root := newRegistry(t)
	items := []Item{CommandItem("one", nil)}
	if err := registry.Approve(root, items); err != nil {
		t.Fatal(err)
	}
	list, err := registry.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list %v, error %v", list, err)
	}
	if list[0].SHA256 != SetHash(items) || len(list[0].Items) != 1 || list[0].At == "" {
		t.Errorf("unexpected entry: %+v", list[0])
	}
	if len(Short(list[0].SHA256)) != 12 {
		t.Errorf("Short gave %q", Short(list[0].SHA256))
	}
}

func TestDefaultDirFollowsTheEnvironmentVariable(t *testing.T) {
	t.Setenv("METAGENTE_CONFIG_DIR", "/somewhere/private")
	if got := DefaultDir(); got != "/somewhere/private" {
		t.Errorf("DefaultDir() = %q", got)
	}
}

// The folder of the approvals may not exist yet, and may be reached through a
// link. Neither must hide that it is inside the project.
func TestAPrefixThatIsALinkDoesNotHideAFolderInsideTheProject(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	symlink(t, root, alias)
	registry := NewRegistry(filepath.Join(alias, "config")) // does not exist yet
	err := registry.Approve(root, []Item{CommandItem("one", nil)})
	if text := problemText(t, err); !strings.Contains(text, "inside the project") {
		t.Errorf("unexpected message:\n%s", text)
	}
}

// The approvals made before credentials existed must stay valid.
func TestTheKeyOfAnItemWithoutACredentialIsTheOneItAlwaysWas(t *testing.T) {
	if got := CommandItem("server --stdio", []string{"A", "B"}).Key(); got != "command\x00server --stdio\x00A,B" {
		t.Errorf("key = %q", got)
	}
	if got := RemoteItem("https://bob.example").Key(); got != "remote\x00https://bob.example\x00" {
		t.Errorf("key = %q", got)
	}
}

// req: E5
func TestACredentialIsPartOfWhatIsApprovedBecauseTheTokenGoesThere(t *testing.T) {
	plain := RemoteItem("https://bob.example")
	with := plain.WithCredential("BOB_TOKEN")
	if plain.Key() == with.Key() {
		t.Error("the same address with a token needs its own approval")
	}
	if with.WithCredential("OTHER_TOKEN").Key() == with.Key() {
		t.Error("another token for the same address needs its own approval")
	}
	mustHave := "connects to: https://bob.example (and sends it the token held in BOB_TOKEN)"
	if got := with.Describe(); got != mustHave {
		t.Errorf("describe = %q", got)
	}
	if got := plain.Describe(); got != "connects to: https://bob.example" {
		t.Errorf("describe = %q", got)
	}
}

// req: E5
func TestOnlyAnAddressGetsATokenNeverAProgram(t *testing.T) {
	agents := agentsFrom(t, `agent A
  goal "a"
  tool weather from mcp "npx -y weather-mcp@1.0.0"
  tool search from mcp "https://tools.example/mcp"
  remote Bob at "https://bob.example"
  remote Carol at "https://carol.example"
`)
	needs := NeedsOfWith(agents, map[string]string{"weather": "W_TOKEN", "search": "S_TOKEN", "Bob": "BOB_TOKEN"})
	var got []string
	for _, item := range needs {
		got = append(got, item.Describe())
	}
	want := []string{
		"starts the program: npx -y weather-mcp@1.0.0", // no token: a program gets variables by `env`
		"connects to: https://bob.example (and sends it the token held in BOB_TOKEN)",
		"connects to: https://carol.example",
		"connects to: https://tools.example/mcp (and sends it the token held in S_TOKEN)",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("needs:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// req: T3
func TestAModelThatGetsNoKeyIsDescribedSoAndIsTheSameApproval(t *testing.T) {
	address := "http://localhost:11434/v1"
	plain, keyless := ModelItem(address), ModelItem(address).WithoutKey()

	if !strings.Contains(plain.Describe(), "sends your key") {
		t.Errorf("a model that gets a key: %q", plain.Describe())
	}
	if d := keyless.Describe(); !strings.Contains(d, "with no key") || strings.Contains(d, "sends your key") || !strings.HasSuffix(d, address) {
		t.Errorf("a model that gets no key: %q", d)
	}
	// What was approved before this wording existed stays approved: it is the same approval.
	if plain.Key() != keyless.Key() {
		t.Error("the wording changed what is approved")
	}
	// ...and the file of approvals does not change either.
	raw, err := json.Marshal(keyless)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "keyless") {
		t.Errorf("the wording is written in the approvals: %s", raw)
	}
}

// symlink makes a symbolic link. Where the one who runs the tests may not make one (on Windows that
// takes the privilege to create symbolic links), the test is skipped; anywhere else, it fails.
func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("this computer does not let the test make symbolic links: %v", err)
		}
		t.Fatal(err)
	}
}

func TestADatabaseIsKeyedByItsStatementsAndTheOldKeysDoNotChange(t *testing.T) {
	a := SQLItem("sqlite", "orders.db", "orders", []string{"next_page"}, "aaa")
	b := SQLItem("sqlite", "orders.db", "orders", []string{"next_page"}, "bbb")
	if a.Key() == b.Key() {
		t.Error("another statement text must need another approval")
	}
	if a.Kind != KindSQL || !strings.Contains(a.Describe(), "reads the database: sqlite orders.db") {
		t.Errorf("describe = %q", a.Describe())
	}
	plain := Item{Kind: KindRemote, Target: "x"}
	if plain.Key() != "remote\x00x\x00" {
		t.Errorf("an item without detail changed its key: %q", plain.Key())
	}
}

func TestABrokerIsKeyedByItsSubjectsAndSaysWhereItPublishes(t *testing.T) {
	a := BrokerItem("jetstream", "tls://b:4222 (tls verify)", "main", []string{"etl.>"}, "aaa")
	b := BrokerItem("jetstream", "tls://b:4222 (tls verify)", "main", []string{"etl.>"}, "bbb")
	if a.Key() == b.Key() {
		t.Error("other subjects must need another approval")
	}
	if a.Kind != KindBroker || !strings.Contains(a.Describe(), "publishes to the broker: jetstream tls://b:4222") ||
		!strings.Contains(a.Describe(), "subjects etl.>") {
		t.Errorf("describe = %q", a.Describe())
	}
	if a.WithCredential("PW").Key() == a.Key() {
		t.Error("the variable of the password is part of what is approved")
	}
}
